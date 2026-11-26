#!/bin/sh
# Prepare (and optionally activate) the V4 msPTI collector via LD_PRELOAD.
#
# Default is a dry-run. --stage only copies the library and writes the env
# fragment; --apply also appends a marked block to /etc/vllm-ds4.env. The
# change takes effect on the next V4 restart; this script never restarts it.
set -eu

CACHE=${CACHE:-/data/cache/cann-ebpf}
LIB=${LIB:-/tmp/cann-mspti-final-src/build/libcannmspti.so}
ENV_FILE=${ENV_FILE:-/etc/vllm-ds4.env}
FRAGMENT=${FRAGMENT:-/etc/cann-ebpf/collector.env}
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
[ "$MODE" = dry-run ] || [ -f "$LIB" ] || { echo "collector library not found: $LIB" >&2; exit 4; }

BLOCK='# BEGIN cann-ebpf collector
LD_PRELOAD=/cache/cann-ebpf/libcannmspti.so
CANN_MSPTI_ACTIVITIES=api,acl_api,runtime_api,kernel,memcpy,hccl
CANN_MSPTI_OUTPUT=/cache/cann-ebpf/device-{pid}.jsonl
CANN_MSPTI_RUN_ID=vllm-collector
CANN_MSPTI_FLUSH_INTERVAL_MS=1000
# END cann-ebpf collector'

if [ "$MODE" = dry-run ]; then
  echo "would copy $LIB -> $CACHE/libcannmspti.so"
  echo "would write $FRAGMENT with:"
  echo "$BLOCK"
  echo "would append the block to $ENV_FILE (backup first)"
  exit 0
fi

mkdir -p "$CACHE"
cp -f "$LIB" "$CACHE/libcannmspti.so"
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
