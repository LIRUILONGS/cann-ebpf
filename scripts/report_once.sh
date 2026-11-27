#!/bin/sh
# One command: host attach + kernel stacks, then render report.
# Read-only: never restarts or modifies the target process.
set -eu

BIN=${BIN:-cann-ebpf}
PROFILE=${PROFILE:-vllm-ascend}
PROFILES_DIR=${PROFILES_DIR:-profiles}
DURATION=${DURATION:-10m}
FREQ=${FREQ:-99}
METRICS=${METRICS:-auto}
OUT=${OUT:-}
PIDS=""
PID_AUTO=0
WITH_STACKS=${WITH_STACKS:-1}

usage() {
  cat <<'EOF'
usage: report_once.sh --pid PID [--pid PID ...] --out DIR [options]
       report_once.sh --pid auto --out DIR [options]

  --pid PID            target host PID; repeat for vLLM (frontend/EngineCore/Workers)
                       or pass "auto" to use profile discovery
  --out DIR            new output directory (must not exist)
  --duration DUR       collection duration (default 10m)
  --freq HZ            stack sampling frequency (default 99)
  --profile NAME       discovery profile (default vllm-ascend; V4.1 use orinfer)
  --profiles-dir DIR   profile directory (default profiles)
  --bin PATH           cann-ebpf binary (default cann-ebpf)
  --metrics MODE       auto|on|off (default auto)
  --no-stacks          skip kernel stack sampling / flamegraph
  --yes                required only for --metrics on against a live service

Outputs:
  DIR/attach/{spans.jsonl,metrics.jsonl,...}
  DIR/stacks/{stacks.folded,stacks.meta.json}
  DIR/report.html

This command only reads evidence; it does not send load requests.
For a pressure/sweep run, use scripts/run_sweep.sh separately.
EOF
}

YES=0
while [ $# -gt 0 ]; do
  case "$1" in
    --pid)
      value=$2
      shift
      if [ "$value" = "auto" ]; then
        PID_AUTO=1
      else
        for p in $(printf '%s' "$value" | tr ',' ' '); do
          case "$p" in ''|*[!0-9]*) echo "bad pid: $p" >&2; exit 2 ;; esac
          PIDS="$PIDS $p"
        done
      fi
      ;;
    --out) OUT=$2; shift ;;
    --duration) DURATION=$2; shift ;;
    --freq) FREQ=$2; shift ;;
    --profile) PROFILE=$2; shift ;;
    --profiles-dir) PROFILES_DIR=$2; shift ;;
    --bin) BIN=$2; shift ;;
    --metrics) METRICS=$2; shift ;;
    --no-stacks) WITH_STACKS=0 ;;
    --yes) YES=1 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown option: $1" >&2; usage >&2; exit 2 ;;
  esac
  shift
done

if [ "$PID_AUTO" -eq 0 ] && [ -z "$(printf '%s' "$PIDS" | tr -d ' ')" ]; then
  echo "--pid is required (repeat for vLLM multi-process, or use --pid auto)" >&2
  exit 2
fi
[ -n "$OUT" ] || { echo "--out is required" >&2; exit 2; }
[ ! -e "$OUT" ] || { echo "refusing to overwrite: $OUT" >&2; exit 2; }
[ "$METRICS" = "on" ] && [ "$YES" -ne 1 ] && { echo "--metrics on requires --yes" >&2; exit 2; }

if [ "$PID_AUTO" -eq 1 ]; then
  PID_ARGS="--pid auto"
else
  PID_ARGS=""
  for p in $PIDS; do PID_ARGS="$PID_ARGS --pid $p"; done
fi

mkdir -p "$OUT/attach"
echo "pids=${PID_ARGS} duration=$DURATION out=$OUT"
# shellcheck disable=SC2086
"$BIN" attach \
  $PID_ARGS \
  --profile "$PROFILE" \
  --profiles-dir "$PROFILES_DIR" \
  --duration "$DURATION" \
  --blocking on \
  --hardware on \
  --metrics "$METRICS" \
  --output "$OUT/attach" \
  --json > "$OUT/attach.json" 2> "$OUT/attach.log" &
APID=$!

SPID=""
if [ "$WITH_STACKS" = 1 ]; then
  # shellcheck disable=SC2086
  "$BIN" stacks $PID_ARGS --duration "$DURATION" --freq "$FREQ" \
    --out "$OUT/stacks" > "$OUT/stacks.log" 2>&1 &
  SPID=$!
fi

wait "$APID"; ARC=$?
if [ -n "$SPID" ]; then wait "$SPID"; SRC=$?; else SRC=0; fi
echo "attach_rc=$ARC stacks_rc=$SRC"

if [ "$ARC" -ne 0 ] || [ ! -s "$OUT/attach/spans.jsonl" ]; then
  echo "attach produced no spans; report not generated" >&2
  exit 3
fi

set -- --input "$OUT/attach" --out "$OUT/report.html"
[ -s "$OUT/attach/metrics.jsonl" ] && set -- "$@" --metrics "$OUT/attach/metrics.jsonl"
[ -s "$OUT/stacks/stacks.folded" ] && set -- "$@" --stacks "$OUT/stacks"

"$BIN" report "$@"
echo "OK report=$OUT/report.html"
