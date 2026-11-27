#!/bin/sh
# Remove the cann-ebpf collector block from the V4 env file. Dry-run by default.
# The change takes effect on the next V4 restart; this script never restarts it.
set -eu

ENV_FILE=${ENV_FILE:-/etc/vllm-ds4.env}
FRAGMENT=${FRAGMENT:-/etc/cann-ebpf/collector.env}
CACHE=${CACHE:-/data/cache/cann-ebpf}
MODE=dry-run
PURGE=0

while [ $# -gt 0 ]; do
  case "$1" in
    --apply) MODE=apply ;;
    --dry-run) MODE=dry-run ;;
    --purge-cache) PURGE=1 ;;
    --env-file) ENV_FILE=$2; shift ;;
    *) echo "usage: rollback-collector.sh [--apply] [--purge-cache]" >&2; exit 2 ;;
  esac
  shift
done

echo "mode=$MODE env_file=$ENV_FILE fragment=$FRAGMENT purge_cache=$PURGE"
if [ "$MODE" = dry-run ]; then
  echo "would remove '# BEGIN cann-ebpf collector' block from $ENV_FILE"
  echo "would remove $FRAGMENT"
  [ "$PURGE" -eq 1 ] && echo "would remove $CACHE/libcannmspti.so"
  exit 0
fi

[ -f "$ENV_FILE" ] || { echo "env file not found: $ENV_FILE" >&2; exit 4; }
cp -a "$ENV_FILE" "$ENV_FILE.cann-ebpf.rollback.$(date -u +%Y%m%dT%H%M%SZ)"
TMP=$(mktemp)
awk '
  /^# BEGIN cann-ebpf collector/ { skip=1; next }
  /^# END cann-ebpf collector/ { skip=0; next }
  !skip { print }
' "$ENV_FILE" > "$TMP"
cat "$TMP" > "$ENV_FILE"
rm -f "$TMP"
rm -f "$FRAGMENT"
[ "$PURGE" -eq 1 ] && rm -f "$CACHE/libcannmspti.so"
echo "collector block removed; activate with: systemctl restart vllm-ds4-8905.service"
