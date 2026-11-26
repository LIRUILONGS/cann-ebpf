#!/bin/sh
# Install cann-ebpf (binary, profiles, configs, docs, systemd timer).
# Default is a dry-run plan only; pass --apply to modify the host.
set -eu

PREFIX=${PREFIX:-/opt/cann-ebpf}
UNITDIR=${UNITDIR:-/etc/systemd/system}
SOURCE_DIR=${SOURCE_DIR:-$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)}
BINARY=${BINARY:-$SOURCE_DIR/dist/cann-ebpf-linux-arm64-m3}
if [ ! -f "$BINARY" ] && [ -f "$SOURCE_DIR/bin/cann-ebpf" ]; then
  BINARY=$SOURCE_DIR/bin/cann-ebpf
fi
CHECKSUMS=${CHECKSUMS:-$SOURCE_DIR/dist/SHA256SUMS.txt}
if [ ! -f "$CHECKSUMS" ] && [ -f "$SOURCE_DIR/SHA256SUMS.txt" ]; then
  CHECKSUMS=$SOURCE_DIR/SHA256SUMS.txt
fi
DRY_RUN=1
ENABLE_TIMER=1

usage() {
  echo "usage: install.sh [--apply] [--prefix DIR] [--unit-dir DIR] [--binary PATH]" >&2
  exit 2
}

while [ $# -gt 0 ]; do
  case "$1" in
    --apply) DRY_RUN=0 ;;
    --dry-run) DRY_RUN=1 ;;
    --no-enable) ENABLE_TIMER=0 ;;
    --prefix) PREFIX=$2; shift ;;
    --unit-dir) UNITDIR=$2; shift ;;
    --binary) BINARY=$2; shift ;;
    --checksums) CHECKSUMS=$2; shift ;;
    *) usage ;;
  esac
  shift
done

run() {
  if [ "$DRY_RUN" -eq 1 ]; then
    echo "DRY-RUN: $*"
  else
    "$@"
  fi
}

[ -f "$BINARY" ] || { echo "binary not found: $BINARY" >&2; exit 4; }
NAME=$(basename "$BINARY")
if [ -f "$CHECKSUMS" ]; then
  EXPECTED=$(awk -v name="$NAME" '$2 == name || $2 ~ ("(^|/)" name "$") { print $1; exit }' "$CHECKSUMS")
  if [ -n "$EXPECTED" ]; then
    ACTUAL=$(sha256sum "$BINARY" | awk '{ print $1 }')
    [ "$EXPECTED" = "$ACTUAL" ] || { echo "SHA-256 mismatch for $NAME" >&2; exit 7; }
    echo "SHA-256 OK: $NAME $ACTUAL"
  else
    echo "WARN: $NAME not listed in $CHECKSUMS" >&2
  fi
else
  echo "WARN: checksums file not found: $CHECKSUMS" >&2
fi

if [ "$DRY_RUN" -eq 0 ] && [ "$(id -u)" -ne 0 ]; then
  echo "install requires root (or use --dry-run)" >&2
  exit 4
fi

run install -d -m 0755 "$PREFIX/bin" "$PREFIX/profiles" "$PREFIX/configs" "$PREFIX/docs"
run install -m 0755 "$BINARY" "$PREFIX/bin/cann-ebpf"
run install -m 0755 "$SOURCE_DIR/deploy/capture-once.sh" "$PREFIX/capture-once.sh"
for profile in "$SOURCE_DIR"/profiles/*.yaml; do
  run install -m 0644 "$profile" "$PREFIX/profiles/$(basename "$profile")"
done
run install -m 0644 "$SOURCE_DIR/configs/base.yaml" "$PREFIX/configs/base.yaml"
for doc in "$SOURCE_DIR"/docs/*.md; do
  run install -m 0644 "$doc" "$PREFIX/docs/$(basename "$doc")"
done
run install -m 0644 "$SOURCE_DIR/deploy/cann-ebpf.service" "$UNITDIR/cann-ebpf.service"
run install -m 0644 "$SOURCE_DIR/deploy/cann-ebpf.timer" "$UNITDIR/cann-ebpf.timer"

if [ "$ENABLE_TIMER" -eq 1 ]; then
  run systemctl daemon-reload
  run systemctl enable --now cann-ebpf.timer
fi

echo "installed prefix=$PREFIX unit_dir=$UNITDIR enable_timer=$ENABLE_TIMER dry_run=$DRY_RUN"
