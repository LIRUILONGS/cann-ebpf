#!/bin/sh
# Upgrade the cann-ebpf binary with backup + rollback. Dry-run by default.
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

while [ $# -gt 0 ]; do
  case "$1" in
    --apply) DRY_RUN=0 ;;
    --dry-run) DRY_RUN=1 ;;
    --prefix) PREFIX=$2; shift ;;
    --binary) BINARY=$2; shift ;;
    --checksums) CHECKSUMS=$2; shift ;;
    *) echo "usage: upgrade.sh [--apply] [--prefix DIR] [--binary PATH]" >&2; exit 2 ;;
  esac
  shift
done

run() {
  if [ "$DRY_RUN" -eq 1 ]; then echo "DRY-RUN: $*"; else "$@"; fi
}

[ -f "$BINARY" ] || { echo "binary not found: $BINARY" >&2; exit 4; }
NAME=$(basename "$BINARY")
if [ -f "$CHECKSUMS" ]; then
  EXPECTED=$(awk -v name="$NAME" '$2 == name || $2 ~ ("(^|/)" name "$") { print $1; exit }' "$CHECKSUMS")
  if [ -n "$EXPECTED" ]; then
    ACTUAL=$(sha256sum "$BINARY" | awk '{ print $1 }')
    [ "$EXPECTED" = "$ACTUAL" ] || { echo "SHA-256 mismatch for $NAME" >&2; exit 7; }
    echo "SHA-256 OK: $NAME $ACTUAL"
  fi
fi

if [ "$DRY_RUN" -eq 0 ] && [ "$(id -u)" -ne 0 ]; then
  echo "upgrade requires root (or use --dry-run)" >&2
  exit 4
fi

BACKUP="$PREFIX.bak.$(date -u +%Y%m%dT%H%M%SZ)"
echo "backup plan: $PREFIX -> $BACKUP"
run systemctl stop cann-ebpf.timer
run cp -a "$PREFIX" "$BACKUP"
run install -m 0755 "$BINARY" "$PREFIX/bin/cann-ebpf"
# Refresh profiles/configs/docs from the release tree.
for profile in "$SOURCE_DIR"/profiles/*.yaml; do
  run install -m 0644 "$profile" "$PREFIX/profiles/$(basename "$profile")"
done
run install -m 0644 "$SOURCE_DIR/configs/base.yaml" "$PREFIX/configs/base.yaml"
for doc in "$SOURCE_DIR"/docs/*.md; do
  run install -m 0644 "$doc" "$PREFIX/docs/$(basename "$doc")"
done
run systemctl start cann-ebpf.timer

if [ "$DRY_RUN" -eq 0 ]; then
  if "$PREFIX/bin/cann-ebpf" version >/dev/null 2>&1; then
    echo "upgrade OK: $BACKUP retained for rollback"
  else
    echo "upgrade failed; restoring $BACKUP" >&2
    rm -rf "$PREFIX"
    cp -a "$BACKUP" "$PREFIX"
    systemctl start cann-ebpf.timer || true
    exit 6
  fi
fi
