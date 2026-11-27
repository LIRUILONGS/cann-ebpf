#!/bin/sh
# One command: host attach + kernel stacks, then render report.
# Read-only: never restarts or modifies the target process.
set -eu

BIN=${BIN:-./cann-ebpf}
PROFILE=${PROFILE:-auto}
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
usage: report_once.sh --out DIR [options]
       report_once.sh --pid PID [--pid PID ...] --out DIR [options]

  --pid PID            optional host PID; repeat for vLLM (frontend/EngineCore/Workers)
                       omit or "auto" to auto-detect Orinfer or vLLM
  --out DIR            new output directory (must not exist)
  --duration DUR       collection duration (default 10m)
  --freq HZ            stack sampling frequency (default 99)
  --profile NAME       auto|vllm-ascend|orinfer (default auto)
  --profiles-dir DIR   profile directory (default profiles)
  --bin PATH           cann-ebpf binary (default ./cann-ebpf)
  --metrics MODE       auto|on|off (default auto)
  --no-stacks          skip kernel stack sampling / flamegraph
  --yes                required only for --metrics on against a live service

Examples:
  ./scripts/report_once.sh --out runs/report-$(date -u +%Y%m%dT%H%M%SZ)
  ./scripts/report_once.sh --pid 1234 --profile orinfer --out runs/report-...
  PIDS=$(pgrep -f 'VLLM::Worker_TP|VLLM::EngineCore|vllm serve' | sort -u)
  ./scripts/report_once.sh $(for p in $PIDS; do echo --pid $p; done) --out runs/report-...

Outputs:
  DIR/attach/{spans.jsonl,metrics.jsonl,...}
  DIR/stacks/{stacks.folded,stacks.meta.json}
  DIR/report.html

Read-only: no load requests, no service restart.
For pressure testing use scripts/run_sweep.sh separately.
EOF
}

detect_profile() {
  if pgrep -f '/opt/orinfer/bin/orinfer serve' >/dev/null 2>&1; then
    echo orinfer
  elif pgrep -f 'vllm serve|VLLM::Worker_TP|VLLM::EngineCore' >/dev/null 2>&1; then
    echo vllm-ascend
  else
    echo ""
  fi
}

resolve_pids() {
  case "$1" in
    orinfer) pgrep -f '/opt/orinfer/bin/orinfer serve' 2>/dev/null || true ;;
    vllm-ascend) pgrep -f 'vllm serve|VLLM::Worker_TP|VLLM::EngineCore' 2>/dev/null | sort -u ;;
    *) pgrep -f '/opt/orinfer/bin/orinfer serve|vllm serve|VLLM::Worker_TP|VLLM::EngineCore' 2>/dev/null | sort -u ;;
  esac
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

[ -n "$OUT" ] || { echo "--out is required" >&2; exit 2; }
[ ! -e "$OUT" ] || { echo "refusing to overwrite: $OUT" >&2; exit 2; }
[ "$METRICS" = "on" ] && [ "$YES" -ne 1 ] && { echo "--metrics on requires --yes" >&2; exit 2; }
if [ ! -x "$BIN" ] && [ -x "dist/cann-ebpf-linux-arm64" ]; then
  BIN="dist/cann-ebpf-linux-arm64"
fi
[ -x "$BIN" ] || { echo "binary is not executable: $BIN (pass --bin)" >&2; exit 4; }

if [ "$PROFILE" = "auto" ]; then
  PROFILE=$(detect_profile)
  [ -n "$PROFILE" ] || {
    echo "no target process found; use --pid or run:" >&2
    echo "  Orinfer: pgrep -af '/opt/orinfer/bin/orinfer serve'" >&2
    echo "  vLLM:    pgrep -af 'vllm serve|VLLM::Worker_TP|VLLM::EngineCore'" >&2
    exit 4
  }
fi

if [ "$PID_AUTO" -eq 1 ] || [ -z "$(printf '%s' "$PIDS" | tr -d ' ')" ]; then
  RESOLVED=$(resolve_pids "$PROFILE")
  [ -n "$RESOLVED" ] || {
    echo "no PID matched profile $PROFILE; list candidates with:" >&2
    echo "  Orinfer: pgrep -af '/opt/orinfer/bin/orinfer serve'" >&2
    echo "  vLLM:    pgrep -af 'vllm serve|VLLM::Worker_TP|VLLM::EngineCore'" >&2
    exit 4
  }
  PIDS=$(printf '%s\n' "$RESOLVED" | tr '\n' ' ')
fi

PID_ARGS=""
for p in $PIDS; do PID_ARGS="$PID_ARGS --pid $p"; done
[ -n "$PID_ARGS" ] || { echo "no target PID" >&2; exit 4; }

mkdir -p "$OUT/attach"
echo "profile=$PROFILE pids=${PID_ARGS# } duration=$DURATION out=$OUT"
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
