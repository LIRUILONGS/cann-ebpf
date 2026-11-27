#!/bin/sh
# Prepare (and optionally activate) the V4 msPTI collector via LD_PRELOAD.
#
# Default is a dry-run. --stage only copies the library and writes the env
# fragment; --apply also appends a marked block to /etc/vllm-ds4.env. The
# change takes effect on the next V4 restart; this script never restarts it.
#
# Worker-only collection: vLLM rewrites Worker argv after exec, so
# CANN_MSPTI_ONLY_CMDLINE cannot select Workers (it silently matches 0
# processes). The env block therefore leaves ONLY_CMDLINE unset. To collect
# only from the 8 V4 Workers, stage with the parent-comm filter enabled:
#   PARENT_COMM=VLLM::EngineCor sh stage-collector.sh --stage
# For a one-shot window (stop without a second restart), add DURATION:
#   PARENT_COMM=VLLM::EngineCor DURATION=60s sh stage-collector.sh --stage
set -eu

CACHE=${CACHE:-/data/cache/cann-ebpf}
LIB=${LIB:-/tmp/cann-mspti-final-src/build/libcannmspti.so}
ENV_FILE=${ENV_FILE:-/etc/vllm-ds4.env}
FRAGMENT=${FRAGMENT:-/etc/cann-ebpf/collector.env}
# Optional: set to VLLM::EngineCor to emit CANN_MSPTI_ONLY_PARENT_COMM and
# restrict collection to the vLLM Worker processes (children of EngineCore).
PARENT_COMM=${PARENT_COMM:-}
# Optional: seconds or Go duration (60, 60s, 1m) for a one-shot window.
DURATION=${DURATION:-}
MODE=dry-run

while [ $# -gt 0 ]; do
  case "$1" in
    --stage) MODE=stage ;;
    --apply) MODE=apply ;;
    --dry-run) MODE=dry-run ;;
    --lib) LIB=$2; shift ;;
    --cache) CACHE=$2; shift ;;
    --env-file) ENV_FILE=$2; shift ;;
    *) echo "usage: stage-collector.sh [--stage|--apply|--dry-run] [--lib PATH] [--cache DIR]" >&2; exit 2 ;;
  esac
  shift
done

echo "mode=$MODE lib=$LIB cache=$CACHE env_file=$ENV_FILE fragment=$FRAGMENT"
if [ "$MODE" != dry-run ] && [ ! -f "$LIB" ] && [ -f "$CACHE/libcannmspti.so" ]; then
  LIB="$CACHE/libcannmspti.so"
  echo "fallback: using already staged $LIB"
fi
[ "$MODE" = dry-run ] || [ -f "$LIB" ] || { echo "collector library not found: $LIB" >&2; exit 4; }

BASE_LD=""
if [ -f "$ENV_FILE" ]; then
  BASE_LD=$(grep -E '^LD_PRELOAD=' "$ENV_FILE" | tail -1 | cut -d= -f2- || true)
fi
MS_PRELOAD=/usr/local/Ascend/cann-9.0.1/tools/mspti/lib64/libmspti.so
COLLECTOR_PRELOAD=/cache/cann-ebpf/libcannmspti.so
if [ -n "$BASE_LD" ]; then
  PRELOAD_VALUE="$BASE_LD:$MS_PRELOAD:$COLLECTOR_PRELOAD"
else
  PRELOAD_VALUE="$MS_PRELOAD:$COLLECTOR_PRELOAD"
fi

BLOCK="# BEGIN cann-ebpf collector
LD_PRELOAD=$PRELOAD_VALUE
CANN_MSPTI_ACTIVITIES=api,acl_api,runtime_api,kernel,memcpy,hccl
CANN_MSPTI_ONLY_COMM=vllm,python
# CANN_MSPTI_ONLY_CMDLINE is intentionally unset: vLLM rewrites Worker argv
# after exec, so Worker titles match 0 processes (silent no-data). Use
# CANN_MSPTI_ONLY_PARENT_COMM instead to narrow collection to Workers.
# Optional Worker-only filter (uncomment, or stage with PARENT_COMM=VLLM::EngineCor):
# CANN_MSPTI_ONLY_PARENT_COMM=VLLM::EngineCor
CANN_MSPTI_OUTPUT=/cache/cann-ebpf/device-{pid}.jsonl
CANN_MSPTI_RUN_ID=vllm-collector
CANN_MSPTI_MAX_BYTES=67108864
CANN_MSPTI_AUTO_STOP=0
# One-shot window: stop collecting N seconds after start, without restarting V4.
# Recommended B1 window: 60s (accepts 60, 60s, 1m). Uncomment, or stage with
# DURATION=60s. AUTO_STOP=0 still means: do not stop when the process exits.
# CANN_MSPTI_DURATION=60s
CANN_MSPTI_FLUSH_INTERVAL_MS=1000
# END cann-ebpf collector"

if [ -n "$PARENT_COMM" ]; then
  BLOCK=$(printf '%s\n' "$BLOCK" | sed "s|^# CANN_MSPTI_ONLY_PARENT_COMM=.*|CANN_MSPTI_ONLY_PARENT_COMM=$PARENT_COMM|")
fi
if [ -n "$DURATION" ]; then
  BLOCK=$(printf '%s\n' "$BLOCK" | sed "s|^# CANN_MSPTI_DURATION=.*|CANN_MSPTI_DURATION=$DURATION|")
fi

if [ "$MODE" = dry-run ]; then
  echo "would copy $LIB -> $CACHE/libcannmspti.so"
  echo "would write $FRAGMENT with:"
  echo "$BLOCK"
  echo "would append the block to $ENV_FILE (backup first)"
  exit 0
fi

mkdir -p "$CACHE"
if [ "$LIB" != "$CACHE/libcannmspti.so" ]; then
  cp -f "$LIB" "$CACHE/libcannmspti.so"
fi
chmod 0755 "$CACHE/libcannmspti.so"
echo "staged $CACHE/libcannmspti.so"

mkdir -p "$(dirname "$FRAGMENT")"
printf '%s\n' "$BLOCK" > "$FRAGMENT"
echo "wrote $FRAGMENT"

if [ "$MODE" = stage ]; then
  echo "stage only: $ENV_FILE unchanged; run --apply before the next restart"
  exit 0
fi

if grep -q '^# BEGIN cann-ebpf collector' "$ENV_FILE" 2>/dev/null; then
  echo "collector block already present in $ENV_FILE"
  exit 0
fi
cp -a "$ENV_FILE" "$ENV_FILE.cann-ebpf.bak.$(date -u +%Y%m%dT%H%M%SZ)"
printf '\n%s\n' "$BLOCK" >> "$ENV_FILE"
echo "appended collector block to $ENV_FILE"
echo "activate with: systemctl restart vllm-ds4-8905.service (this script never restarts it)"
