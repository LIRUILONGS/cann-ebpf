#!/bin/sh
# One command: host attach + kernel stacks, then render report.
# Read-only: never restarts or modifies the target process.
#
# PID is explicit on purpose. Auto-detection is intentionally not used here.
set -eu

BIN=${BIN:-./cann-ebpf}
PROFILE=${PROFILE:-vllm-ascend}
PROFILES_DIR=${PROFILES_DIR:-profiles}
DURATION=${DURATION:-10m}
FREQ=${FREQ:-99}
METRICS=${METRICS:-auto}
OUT=${OUT:-}
PIDS=""
WITH_STACKS=${WITH_STACKS:-1}

usage() {
  cat <<'EOF'
usage: report_once.sh --pid PID [--pid PID ...] --out DIR [options]

Required:
  --pid PID            target host PID (repeat for vLLM: frontend/EngineCore/Workers)
  --out DIR            new output directory (must not exist)

Options:
  --profile NAME       vllm-ascend (default) or orinfer
  --profiles-dir DIR   profile directory (default profiles)
  --duration DUR       collection duration (default 10m)
  --freq HZ            stack sampling frequency (default 99)
  --bin PATH           cann-ebpf binary (default ./cann-ebpf, fallback dist/...)
  --metrics MODE       auto|on|off (default auto)
  --no-stacks          skip kernel stack sampling / flamegraph
  --yes                required only for --metrics on against a live service

Examples:
  # Orinfer: one main process
  PID=$(pgrep -f '/opt/orinfer/bin/orinfer serve' | head -1)
  ./scripts/report_once.sh --pid "$PID" --profile orinfer --out runs/report-...

  # vLLM: pass all frontend/EngineCore/Worker PIDs
  PIDS=$(pgrep -f 'vllm serve|VLLM::Worker_TP|VLLM::EngineCore' | sort -u)
  PID_ARGS=$(for p in $PIDS; do echo -n " --pid $p"; done)
  ./scripts/report_once.sh $PID_ARGS --profile vllm-ascend --out runs/report-...

Outputs:
  DIR/attach/{spans.jsonl,metrics.jsonl,...}
  DIR/stacks/{stacks.folded,stacks.meta.json}
  DIR/report.html

Read-only: no load requests, no service restart.
For pressure testing use scripts/run_sweep.sh separately.
EOF
}

while [ $# -gt 0 ]; do
  case "$1" in
    --pid)
      value=$2
      shift
      for p in $(printf '%s' "$value" | tr ',' ' '); do
        case "$p" in
          ''|*[!0-9]*) echo "bad pid: $p (auto-detection is intentionally not used)" >&2; exit 2 ;;
        esac
        PIDS="$PIDS $p"
      done
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

PIDS=$(printf '%s' "$PIDS" | tr -s ' ' | sed 's/^ //; s/ $//')
[ -n "$PIDS" ] || { echo "at least one --pid is required" >&2; exit 2; }
[ -n "$OUT" ] || { echo "--out is required" >&2; exit 2; }
[ ! -e "$OUT" ] || { echo "refusing to overwrite: $OUT" >&2; exit 2; }
[ "$METRICS" = "on" ] && [ "$YES" -ne 1 ] && { echo "--metrics on requires --yes" >&2; exit 2; }

case "$PROFILE" in
  vllm-ascend|orinfer) ;;
  *) echo "--profile must be vllm-ascend or orinfer (got: $PROFILE)" >&2; exit 2 ;;
esac

if [ ! -x "$BIN" ] && [ -x "dist/cann-ebpf-linux-arm64" ]; then
  BIN="dist/cann-ebpf-linux-arm64"
fi
[ -x "$BIN" ] || { echo "binary is not executable: $BIN (pass --bin)" >&2; exit 4; }

PID_ARGS=""
for p in $PIDS; do PID_ARGS="$PID_ARGS --pid $p"; done

mkdir -p "$OUT/attach"
echo "profile=$PROFILE pids=$PIDS duration=$DURATION out=$OUT"
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
