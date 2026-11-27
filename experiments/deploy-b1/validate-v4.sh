#!/bin/sh
# Validate real V4 host+device timeline with the fixed collector.
# Default is a dry-run. --apply requires V4.1 to be stopped (8 cards are exclusive).
set -eu

PREFIX=${PREFIX:-/opt/cann-ebpf}
COLLECTOR=${COLLECTOR:-$PREFIX/collectors/9.0.1/libcannmspti.so}
ENV_FILE=${ENV_FILE:-/etc/vllm-ds4.env}
PROFILES=${PROFILES:-/opt/cann-ebpf/profiles}
BIN=${BIN:-/opt/cann-ebpf/bin/cann-ebpf}
OUTROOT=${OUTROOT:-/var/log/cann-ebpf/runs/b1-real}
MERGE=${MERGE:-/opt/cann-ebpf/b1_merge_device.py}
DURATION=${DURATION:-30s}
MODE=dry-run

while [ $# -gt 0 ]; do
  case "$1" in
    --apply) MODE=apply ;;
    --dry-run) MODE=dry-run ;;
    --collector) COLLECTOR=$2; shift ;;
    --duration) DURATION=$2; shift ;;
    *) echo "usage: validate-v4.sh [--apply] [--collector PATH] [--duration 30s]" >&2; exit 2 ;;
  esac
  shift
done

echo "mode=$MODE collector=$COLLECTOR outroot=$OUTROOT"
if [ "$MODE" = dry-run ]; then
  echo "would require: orinfer-v41 inactive, vllm-ds4 inactive"
  echo "would stage $COLLECTOR -> /data/cache/cann-ebpf/libcannmspti.so"
  echo "would append collector block to $ENV_FILE, start vllm-ds4, wait health 200 + device files"
  echo "would merge device files and run attach --device-jsonl + diagnose"
  exit 0
fi

[ "$(id -u)" -eq 0 ] || { echo "requires root" >&2; exit 4; }
[ -f "$COLLECTOR" ] || { echo "collector not found: $COLLECTOR" >&2; exit 4; }
[ -x "$BIN" ] || { echo "binary not found: $BIN" >&2; exit 4; }
if systemctl is-active --quiet orinfer-v41.service; then
  echo "orinfer-v41 is active; stop it first (8 cards are exclusive)" >&2
  exit 3
fi
if systemctl is-active --quiet vllm-ds4-8905.service; then
  echo "vllm-ds4 already active; stop it first" >&2
  exit 3
fi

cp -f "$COLLECTOR" /data/cache/cann-ebpf/libcannmspti.so
chmod 0755 /data/cache/cann-ebpf/libcannmspti.so
chmod 0777 /data/cache/cann-ebpf
cp -a "$ENV_FILE" "$ENV_FILE.cann-ebpf.validate.$(date -u +%Y%m%dT%H%M%SZ)"
if ! grep -q '^# BEGIN cann-ebpf collector' "$ENV_FILE"; then
  printf '\n%s\n' '# BEGIN cann-ebpf collector
LD_PRELOAD=/usr/lib/aarch64-linux-gnu/libjemalloc.so.2:/usr/local/Ascend/cann-9.0.1/tools/mspti/lib64/libmspti.so:/cache/cann-ebpf/libcannmspti.so
CANN_MSPTI_ACTIVITIES=api,acl_api,runtime_api,kernel,memcpy,hccl
CANN_MSPTI_ONLY_COMM=vllm,python
CANN_MSPTI_OUTPUT=/cache/cann-ebpf/device-{pid}.jsonl
CANN_MSPTI_RUN_ID=vllm-collector
CANN_MSPTI_FLUSH_INTERVAL_MS=1000
# END cann-ebpf collector' >> "$ENV_FILE"
fi
systemctl reset-failed vllm-ds4-8905.service 2>/dev/null || true
systemctl start vllm-ds4-8905.service
for i in $(seq 1 120); do
  code=$(curl -s -o /dev/null -w '%{http_code}' -m 3 http://127.0.0.1:8905/health 2>/dev/null || echo 000)
  files=$(ls /data/cache/cann-ebpf/device-*.jsonl 2>/dev/null | wc -l)
  echo "wait loop=$i health=$code device_files=$files"
  [ "$code" = 200 ] && [ "$files" -gt 0 ] && break
  sleep 10
done
[ "$(curl -s -o /dev/null -w '%{http_code}' -m 3 http://127.0.0.1:8905/health)" = 200 ] || {
  echo "V4 health not 200; run deploy/b1/rollback-collector.sh --apply" >&2
  exit 5
}
STAMP=$(date -u +%Y%m%dT%H%M%SZ)
MERGED="$OUTROOT/merged-$STAMP"
OUT="$OUTROOT/run-$STAMP"
mkdir -p "$OUT"
python3 "$MERGE" --input-dir /data/cache/cann-ebpf --out "$MERGED"
"$BIN" attach --pid auto --profile vllm-ascend --profiles-dir "$PROFILES" \
  --duration "$DURATION" --blocking on --hardware on \
  --device-jsonl "$MERGED/device.jsonl" --output "$OUT" --json > "$OUT/collector.json" 2> "$OUT/collector.log"
"$BIN" diagnose --input "$OUT" --json --out "$OUT/diagnose.json"
echo "VALIDATED out=$OUT"
echo "rollback: deploy/b1/rollback-collector.sh --apply; systemctl stop vllm-ds4-8905.service"
