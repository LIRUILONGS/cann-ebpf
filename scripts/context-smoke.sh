#!/bin/sh
set -eu

ROOT=${ROOT:-/opt/cann-ebpf/m4-20261126}
SOCKET=${CONTEXT_SOCKET:-/tmp/cann-ebpf-smoke.sock}
HEALTH=${VLLM_HEALTH:-http://127.0.0.1:8905/health}
STAMP=$(date -u +%Y%m%dT%H%M%SZ)
OUT=${OUT:-"$ROOT/runs/context-test-$STAMP"}

cd "$ROOT"

TARGET_PID=${TARGET_PID:-$(ps -eo pid=,comm= | awk '$2=="VLLM::Worker_TP" {print $1; exit}')}
if [ -z "$TARGET_PID" ]; then
    echo "context smoke: no VLLM::Worker_TP process found" >&2
    exit 3
fi
TARGET_UID=$(awk '/^Uid:/ {print $2; exit}' "/proc/$TARGET_PID/status")
TARGET_GID=$(awk '/^Gid:/ {print $2; exit}' "/proc/$TARGET_PID/status")

case "$OUT" in
    "$ROOT"/runs/*) ;;
    *)
        echo "refusing output outside $ROOT/runs: $OUT" >&2
        exit 2
        ;;
esac
if [ -e "$OUT" ]; then
    echo "refusing to overwrite existing evidence directory: $OUT" >&2
    exit 2
fi
rm -f "$SOCKET"
mkdir -p "$OUT"
printf 'target_pid=%s\ntarget_uid=%s\ntarget_gid=%s\n' "$TARGET_PID" "$TARGET_UID" "$TARGET_GID" > "$OUT/target-identity.txt"
curl -fsS -m 5 "$HEALTH" >/dev/null
printf '%s\n' "$(curl -sS -o /dev/null -w '%{http_code}' -m 5 "$HEALTH" || true)" > "$OUT/health-before.txt"
systemctl is-active vllm-ds4-8905.service > "$OUT/service-before.txt"

./cann-ebpf-linux-arm64 attach \
    --pid "$TARGET_PID" \
    --profile vllm-ascend \
    --profiles-dir profiles \
    --context-socket "$SOCKET" \
    --duration 3s \
    --output "$OUT" > "$OUT/collector.log" 2>&1 &
collector=$!
trap 'kill "$collector" 2>/dev/null || true' EXIT INT TERM

i=0
while [ ! -S "$SOCKET" ] && [ "$i" -lt 80 ]; do
    sleep 0.1
    i=$((i + 1))
done
if [ ! -S "$SOCKET" ]; then
    echo "context smoke: socket was not created" >&2
    kill "$collector" 2>/dev/null || true
    wait "$collector" || true
    tail -40 "$OUT/collector.log" >&2 || true
    exit 4
fi

python3 "$ROOT/scripts/context_smoke_client.py" --socket "$SOCKET" --pid "$TARGET_PID" --uid "$TARGET_UID" --gid "$TARGET_GID"
rc=0
wait "$collector" || rc=$?
trap - EXIT INT TERM

printf '%s\n' "$(curl -sS -o /dev/null -w '%{http_code}' -m 5 "$HEALTH" || true)" > "$OUT/health-after.txt"
systemctl is-active vllm-ds4-8905.service > "$OUT/service-after.txt"
if grep -q "cann_" /sys/kernel/tracing/uprobe_events 2>/dev/null; then
    grep "cann_" /sys/kernel/tracing/uprobe_events > "$OUT/uprobe-residual.txt" || true
else
    : > "$OUT/uprobe-residual.txt"
fi

tail -40 "$OUT/collector.log"
if [ "$rc" -ne 0 ]; then
    echo "context smoke: collector exit=$rc" >&2
    exit "$rc"
fi
python3 "$ROOT/scripts/verify_context_smoke.py" --output "$OUT" --batch-id ctx-smoke-1 --pid "$TARGET_PID"
echo "output=$OUT"
