#!/bin/sh
# One-command concurrency sweep + report.
#
# For each tier it starts `cann-ebpf sample` (metrics.jsonl), runs the fixed
# vLLM load (load.json), then assembles sweep.json and renders the report.
# Default is a dry-run; --yes is required to generate real load.
set -eu

BIN=${BIN:-./cann-ebpf}
LOADGEN=${LOADGEN:-scripts/vllm_sweep_load.py}
BASE_URL=${BASE_URL:-http://127.0.0.1:8905}
MODEL=${MODEL:-deepseek-v4-flash}
API_KEY=${API_KEY:-${VLLM_API_KEY:-}}
PROM_INCLUDE=${PROM_INCLUDE:-'^(vllm:|orinfer_)'}
STACKS_DIR=${STACKS_DIR:-}
TIERS=${TIERS:-1,2,4,8}
DURATION=${DURATION:-30}
MAX_TOKENS=${MAX_TOKENS:-64}
RUN_DIR=${RUN_DIR:-}
REPORT=${REPORT:-}
WORKDIR=${WORKDIR:-}
MODEL_SERVED=${MODEL_SERVED:-}
APPLY=0

usage() {
  cat <<'EOF'
usage: run_sweep.sh --workdir <dir> [--report <report.html>] [options]

  --workdir DIR       sweep evidence dir (default runs/sweep-<UTC>)
  --run DIR           existing host run dir (required only with --report)
  --report FILE       optional: after producing sweep evidence, render report
  --stacks DIR        optional: include kernel-stack flamegraph from a stacks run
  --tiers LIST        comma-separated concurrency tiers (>=4, default 1,2,4,8)
  --duration SEC      load duration per tier (default 30)
  --max-tokens N      fixed output length (default 64)
  --base-url URL      vLLM/Orinfer endpoint (default 127.0.0.1:8905)
  --model NAME        served model name
  --api-key KEY       bearer token (default VLLM_API_KEY)
  --bin PATH          cann-ebpf binary
  --yes               actually run the sweep (otherwise dry-run)

Phases:
  1. produce: per tier sample metrics + fixed load -> metrics.jsonl/load.json/sweep.json
  2. consume: only if --report is given, render HTML; never sends load.

EOF
}

while [ $# -gt 0 ]; do
  case "$1" in
    --run) RUN_DIR=$2; shift ;;
    --report) REPORT=$2; shift ;;
    --stacks) STACKS_DIR=$2; shift ;;
    --out) REPORT=$2; shift ;;
    --tiers) TIERS=$2; shift ;;
    --duration) DURATION=$2; shift ;;
    --max-tokens) MAX_TOKENS=$2; shift ;;
    --base-url) BASE_URL=$2; shift ;;
    --model) MODEL=$2; shift ;;
    --api-key) API_KEY=$2; shift ;;
    --workdir) WORKDIR=$2; shift ;;
    --bin) BIN=$2; shift ;;
    --yes) APPLY=1 ;;
    --dry-run) APPLY=0 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown option: $1" >&2; usage >&2; exit 2 ;;
  esac
  shift
done

[ -x "$BIN" ] || { echo "binary not executable: $BIN" >&2; exit 4; }
[ -f "$LOADGEN" ] || { echo "load generator not found: $LOADGEN" >&2; exit 4; }
if [ -n "$REPORT" ]; then
  [ -n "$RUN_DIR" ] || { echo "--run is required with --report" >&2; exit 2; }
  [ -f "$RUN_DIR/metrics.jsonl" ] || { echo "missing $RUN_DIR/metrics.jsonl" >&2; exit 4; }
  [ "$REPORT" = "auto" ] && REPORT="$RUN_DIR/report.html"
fi

STAMP=$(date -u +%Y%m%dT%H%M%SZ)
[ -n "$WORKDIR" ] || WORKDIR="runs/sweep-$STAMP"
WORKLOAD="vllm-sweep-$STAMP"

echo "run=$RUN_DIR report=$REPORT workdir=$WORKDIR tiers=$TIERS duration=${DURATION}s max_tokens=$MAX_TOKENS"
echo "base_url=$BASE_URL model=$MODEL"
echo "phase 1: produce sweep evidence (load + sample)"

if [ "$APPLY" -ne 1 ]; then
  echo "DRY-RUN: no load, no sample, no report"
  echo "would create $WORKDIR/<cN>/{metrics.jsonl,load.json}, $WORKDIR/sweep.json"
  if [ -n "$REPORT" ]; then
    echo "phase 2: consume -> $BIN report --input $RUN_DIR --metrics $RUN_DIR/metrics.jsonl --sweep $WORKDIR/sweep.json --out $REPORT"
  else
    echo "phase 2: skipped (no --report)"
  fi
  exit 0
fi

[ -n "$API_KEY" ] || { echo "--api-key or VLLM_API_KEY is required for real load" >&2; exit 4; }

# normalize and validate tiers
TIERS=$(printf '%s' "$TIERS" | tr -d ' ')
COUNT=$(printf '%s' "$TIERS" | awk -F, '{print NF}')
[ "$COUNT" -ge 4 ] || { echo "need >=4 tiers (got $TIERS)" >&2; exit 2; }
mkdir -p "$WORKDIR"

for C in $(printf '%s' "$TIERS" | tr ',' ' '); do
  case "$C" in ''|*[!0-9]*) echo "bad tier: $C" >&2; exit 2 ;; esac
  T="$WORKDIR/c$C"
  mkdir -p "$T"
  [ -e "$T/metrics.jsonl" ] && { echo "refusing to overwrite $T" >&2; exit 2; }
  echo "=== tier c$C ==="
  "$BIN" sample \
    --prometheus "vllm=$BASE_URL/metrics" \
    --include "$PROM_INCLUDE" \
    --interval 1s \
    --duration "$((DURATION + 10))s" \
    --out "$T" &
  SPID=$!
  sleep 2
  python3 "$LOADGEN" \
    --base-url "$BASE_URL" --model "$MODEL" --api-key "$API_KEY" \
    --concurrency "$C" --duration "$DURATION" --max-tokens "$MAX_TOKENS" \
    --out "$T/load.json" || { kill "$SPID" 2>/dev/null || true; wait "$SPID" 2>/dev/null || true; exit 3; }
  wait "$SPID" || { echo "sample failed for c$C" >&2; exit 3; }
  [ -s "$T/metrics.jsonl" ] || { echo "empty metrics for c$C" >&2; exit 3; }
  [ -s "$T/load.json" ] || { echo "empty load.json for c$C" >&2; exit 3; }
done

python3 - "$WORKDIR" "$WORKLOAD" "$MODEL" "$TIERS" <<'PY'
import json, os, sys
workdir, workload, model, tiers = sys.argv[1], sys.argv[2], sys.argv[3], sys.argv[4].split(',')
runs = []
for c in tiers:
    with open(os.path.join(workdir, 'c'+c, 'load.json'), encoding='utf-8') as f:
        load = json.load(f)
    runs.append({
        'label': 'c'+c, 'dir': 'c'+c, 'concurrency': int(c),
        'warmup_s': 0, 'duration_s': int(load.get('duration_s') or 0),
        'qps': float(load.get('client_rps') or 0),
    })
manifest = {'workload_id': workload, 'engine': 'vllm-ascend', 'model': model, 'runs': runs}
with open(os.path.join(workdir, 'sweep.json'), 'w', encoding='utf-8') as f:
    json.dump(manifest, f, indent=2)
print(os.path.join(workdir, 'sweep.json'))
PY

if [ -n "$REPORT" ]; then
  echo "phase 2: consume -> $REPORT"
  set -- --input "$RUN_DIR" --metrics "$RUN_DIR/metrics.jsonl" --sweep "$WORKDIR/sweep.json" --out "$REPORT"
  if [ -n "$STACKS_DIR" ] && [ -d "$STACKS_DIR" ]; then
    set -- "$@" --stacks "$STACKS_DIR"
  fi
  "$BIN" report "$@"
  echo "OK report=$REPORT sweep=$WORKDIR/sweep.json"
else
  echo "OK sweep=$WORKDIR/sweep.json (no report requested)"
fi
