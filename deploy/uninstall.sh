#!/bin/sh
# Uninstall cann-ebpf. Dry-run by default; --apply removes files.
set -eu

PREFIX=${PREFIX:-/opt/cann-ebpf}
UNITDIR=${UNITDIR:-/etc/systemd/system}
DRY_RUN=1
KEEP_DATA=0

while [ $# -gt 0 ]; do
  case "$1" in
    --apply) DRY_RUN=0 ;;
    --dry-run) DRY_RUN=1 ;;
    --keep-data) KEEP_DATA=1 ;;
    --prefix) PREFIX=$2; shift ;;
    --unit-dir) UNITDIR=$2; shift ;;
    *) echo "usage: uninstall.sh [--apply] [--keep-data] [--prefix DIR]" >&2; exit 2 ;;
  esac
  shift
done

run() {
  if [ "$DRY_RUN" -eq 1 ]; then echo "DRY-RUN: $*"; else "$@"; fi
}

if [ "$DRY_RUN" -eq 0 ] && [ "$(id -u)" -ne 0 ]; then
  echo "uninstall requires root (or use --dry-run)" >&2
  exit 4
fi

run systemctl disable --now cann-ebpf.timer
run systemctl stop cann-ebpf.service
run rm -f "$UNITDIR/cann-ebpf.service" "$UNITDIR/cann-ebpf.timer"
run systemctl daemon-reload
run rm -rf "$PREFIX"
if [ "$KEEP_DATA" -eq 0 ]; then
  run rm -rf /var/log/cann-ebpf
else
  echo "keeping /var/log/cann-ebpf (--keep-data)"
fi
echo "uninstalled prefix=$PREFIX dry_run=$DRY_RUN keep_data=$KEEP_DATA"
