#!/bin/sh
# Run one metrics-only window against the local vLLM service. The caller must
# provide a new OUT directory so previous evidence is never overwritten.
set -eu

ROOT=${ROOT:-/root/cann-ebpf}
BIN=${BIN:-$ROOT/cann-ebpf-review}
DURATION=${DURATION:-360s}
OUT=${OUT:?set OUT to a new run directory}
PROMETHEUS_URL=${PROMETHEUS_URL:-http://127.0.0.1:8905/metrics}
HEALTH_URL=${HEALTH_URL:-http://127.0.0.1:8905/health}

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
cd "$ROOT"
date -u +%FT%TZ > "$OUT/started_at.txt"
curl -sS -o /dev/null -w '%{http_code}\n' -m 5 "$HEALTH_URL" > "$OUT/health-before.txt" || true

MAIN_PID=${VLLM_MAIN_PID:-$(pgrep -f '/bin/vllm serve' | head -n 1)}
if [ -z "$MAIN_PID" ]; then
    echo "cannot find vLLM main pid" >&2
    exit 3
fi
ENGINE_PID=$(pgrep -P "$MAIN_PID" -f 'VLLM::EngineCore' | head -n 1 || true)
PIDS="$MAIN_PID"
if [ -n "$ENGINE_PID" ]; then
    PIDS="$PIDS $ENGINE_PID"
    for pid in $(pgrep -P "$ENGINE_PID" 2>/dev/null || true); do
        PIDS="$PIDS $pid"
    done
fi
echo "pids=$PIDS" | tee "$OUT/pids.txt"
echo "main_pid=$MAIN_PID" > "$OUT/pid-sources.txt"
echo "engine_pid=$ENGINE_PID" >> "$OUT/pid-sources.txt"

set -- sample \
    --out "$OUT" \
    --duration "$DURATION" \
    --interval 1s \
    --prometheus "vllm=$PROMETHEUS_URL" \
    --system \
    --npu-smi
for pid in $PIDS; do
    set -- "$@" --pid "$pid"
done

"$BIN" "$@" > "$OUT/sample.log" 2>&1
rc=$?
date -u +%FT%TZ > "$OUT/ended_at.txt"
echo "$rc" > "$OUT/exit-code.txt"
curl -sS -o /dev/null -w '%{http_code}\n' -m 5 "$HEALTH_URL" > "$OUT/health-after.txt" || true
exit "$rc"
