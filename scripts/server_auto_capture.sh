#!/bin/sh
# Reproducible server-side capture. The caller may set OUT to an explicit
# new directory; the default is timestamped so previous evidence is never
# overwritten.
set -u

ROOT=${ROOT:-/opt/cann-ebpf/m4-20261126}
DURATION=${DURATION:-5s}
METRICS=${METRICS:-auto}
METRICS_NPU_BACKEND=${METRICS_NPU_BACKEND:-auto}
PROMETHEUS_URL=${PROMETHEUS_URL:-}
PROMETHEUS_INCLUDE=${PROMETHEUS_INCLUDE:-}
REPORT=${REPORT:-auto}
HEALTH=${VLLM_HEALTH:-http://127.0.0.1:8905/health}
STAMP=$(date -u +%Y%m%dT%H%M%SZ)
OUT=${OUT:-"$ROOT/runs/ebpf-auto-$DURATION-$STAMP"}

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

mkdir -p "$OUT"
cd "$ROOT" || exit 4

date -u +%FT%TZ > "$OUT/started_at.txt"
curl -sS -o /dev/null -w '%{http_code}\n' -m 5 "$HEALTH" > "$OUT/health-before.txt" || true
systemctl is-active vllm-ds4-8905.service > "$OUT/service-before.txt" || true
start_epoch=$(date +%s)

set -- attach \
    --pid auto \
    --profile vllm-ascend \
    --profiles-dir profiles \
    --duration "$DURATION" \
    --output "$OUT" \
    --metrics "$METRICS" \
    --metrics-npu-backend "$METRICS_NPU_BACKEND" \
    --report "$REPORT"
if [ -n "$PROMETHEUS_URL" ]; then
    set -- "$@" --prometheus "$PROMETHEUS_URL"
fi
if [ -n "$PROMETHEUS_INCLUDE" ]; then
    set -- "$@" --prometheus-include "$PROMETHEUS_INCLUDE"
fi
timeout 180 ./cann-ebpf-linux-arm64 "$@" > "$OUT/collector.log" 2>&1
rc=$?

end_epoch=$(date +%s)
date -u +%FT%TZ > "$OUT/ended_at.txt"
echo "$rc" > "$OUT/exit-code.txt"
echo "$((end_epoch - start_epoch))" > "$OUT/wall-seconds.txt"
curl -sS -o /dev/null -w '%{http_code}\n' -m 5 "$HEALTH" > "$OUT/health-after.txt" || true
systemctl is-active vllm-ds4-8905.service > "$OUT/service-after.txt" || true
if grep -q 'cann_' /sys/kernel/tracing/uprobe_events 2>/dev/null; then
    grep 'cann_' /sys/kernel/tracing/uprobe_events > "$OUT/uprobe-residual.txt" || true
else
    : > "$OUT/uprobe-residual.txt"
fi

echo "output=$OUT"
echo "exit=$rc"
echo "duration=$DURATION"
exit "$rc"
