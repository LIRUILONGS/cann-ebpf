#!/bin/sh
# rollback.sh - undo the vllm-ascend profiler patch (files only, no restart).
#
# Default = dry-run. Restores the pristine launcher from the newest backup under
# <patch-dir>/backup/, removes the .applied marker, and (with --purge) removes
# the staged torch_npu_profiler.py that the bind mount referenced.
#
# It NEVER stops/starts V4. If the patched V4 already ran, restoring files only
# takes effect on the next scheduled restart; that is intentional.
#
# Usage:
#   ./rollback.sh                 # dry-run, newest backup
#   ./rollback.sh --apply         # do it
#   ./rollback.sh --apply --purge # also delete the bind-mount source file
#   ./rollback.sh --backup <dir> --apply
set -eu

PATCH_DIR="${PATCH_DIR:-/opt/cann-ebpf/patches/vllm-ascend-profiler}"
RUN_SCRIPT="${RUN_SCRIPT:-/usr/local/libexec/run_vllm_ds4_8905.sh}"
MARKER="${MARKER:-/opt/cann-ebpf/patches/vllm-ascend-profiler.applied}"

ORIG_RUN_SHA="2a0f71cf7832d50da306c2282047ea447bd68f764dd47dcacb3b9fd4d03aebfd"

APPLY=0
PURGE=0
BK=""
while [ $# -gt 0 ]; do
  case "$1" in
    --apply) APPLY=1 ;;
    --purge) PURGE=1 ;;
    --backup) shift; BK="${1:-}" ;;
    -h|--help) sed -n '2,20p' "$0"; exit 0 ;;
    *) echo "unknown arg: $1" >&2; exit 2 ;;
  esac
  shift
done

say() { printf '%s\n' "$*"; }
die() { printf 'ERROR: %s\n' "$*" >&2; exit 4; }

if [ -z "$BK" ]; then
  BK="$(ls -1d "$PATCH_DIR"/backup/*/ 2>/dev/null | sort | tail -n 1 || true)"
fi
[ -n "$BK" ] || die "no backup dir under $PATCH_DIR/backup; pass --backup <dir>"
BK="${BK%/}"
[ -f "$BK/run_vllm_ds4_8905.sh" ] || die "backup missing run_vllm_ds4_8905.sh: $BK"

mode="dry-run"; [ "$APPLY" -eq 1 ] && mode="apply"
say "mode=$mode backup=$BK"
say "restore: $BK/run_vllm_ds4_8905.sh -> $RUN_SCRIPT"
say "remove marker: $MARKER"
[ "$PURGE" -eq 1 ] && say "purge bind-mount source: $PATCH_DIR/torch_npu_profiler.py"

if [ "$APPLY" -ne 1 ]; then
  say "dry-run complete: no changes made."
  exit 0
fi

[ "$(id -u)" = "0" ] || die "--apply needs root"

install -m 0755 "$BK/run_vllm_ds4_8905.sh" "$RUN_SCRIPT.new"
mv -f "$RUN_SCRIPT.new" "$RUN_SCRIPT"
bash -n "$RUN_SCRIPT" || die "restored launcher failed bash -n"

cur="$(sha256sum "$RUN_SCRIPT" | cut -d' ' -f1)"
say "restored launcher sha256=$cur"
[ "$cur" = "$ORIG_RUN_SHA" ] && say "PASS  restored launcher equals pristine" \
  || say "WARN  restored launcher differs from known pristine sha (check backup source)"

rm -f "$MARKER" && say "removed marker"
if [ "$PURGE" -eq 1 ]; then
  rm -f "$PATCH_DIR/torch_npu_profiler.py" && say "purged bind-mount source"
fi

say "ROLLED BACK (files only, not restarted)."
