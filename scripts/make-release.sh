#!/bin/sh
# Build a release tarball with binary, profiles, configs, deploy and docs.
# Dry-run by default; --apply writes dist/<name>.tar.gz and SHA256SUMS.
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
NAME=${NAME:-cann-ebpf-0.6.0-m2-m3}
BINARY=${BINARY:-$ROOT/dist/cann-ebpf-linux-arm64-m3}
OUT=${OUT:-$ROOT/dist/$NAME.tar.gz}
DRY_RUN=1

while [ $# -gt 0 ]; do
  case "$1" in
    --apply) DRY_RUN=0 ;;
    --dry-run) DRY_RUN=1 ;;
    --name) NAME=$2; shift ;;
    --binary) BINARY=$2; shift ;;
    --out) OUT=$2; shift ;;
    *) echo "usage: make-release.sh [--apply] [--name NAME] [--binary PATH] [--out TAR]" >&2; exit 2 ;;
  esac
  shift
done

[ -f "$BINARY" ] || { echo "binary not found: $BINARY" >&2; exit 4; }

STAGE=$(mktemp -d)
trap 'rm -rf "$STAGE"' EXIT
PKG="$STAGE/$NAME"
mkdir -p "$PKG/bin" "$PKG/profiles" "$PKG/configs" "$PKG/deploy" "$PKG/scripts" "$PKG/docs" "$PKG/adapters" "$PKG/m3"
cp "$BINARY" "$PKG/bin/cann-ebpf"
cp "$ROOT"/profiles/*.yaml "$PKG/profiles/"
cp "$ROOT/configs/base.yaml" "$PKG/configs/"
cp "$ROOT"/deploy/*.sh "$ROOT"/deploy/*.service "$ROOT"/deploy/*.timer "$PKG/deploy/"
cp "$ROOT"/scripts/*.py "$ROOT"/scripts/*.sh "$PKG/scripts/" 2>/dev/null || true
cp "$ROOT"/docs/*.md "$PKG/docs/"
cp -R "$ROOT/adapters/vllm_ascend" "$PKG/adapters/" 2>/dev/null || true
cp -R "$ROOT/m3/mspti_collector" "$PKG/m3/" 2>/dev/null || true
rm -rf "$PKG/m3/mspti_collector/build"
cp "$ROOT/README.md" "$PKG/"

(
  cd "$PKG"
  find bin profiles configs deploy scripts docs adapters m3 -type f -print | sort | while read -r file; do
    sha256sum "$file"
  done > SHA256SUMS.txt
  sha256sum README.md >> SHA256SUMS.txt
)

echo "release name=$NAME binary=$BINARY out=$OUT"
find "$PKG" -type f | sed "s|$PKG/||" | sort | head -40
if [ "$DRY_RUN" -eq 1 ]; then
  echo "dry-run: staging ready; pass --apply to write $OUT"
  exit 0
fi
mkdir -p "$(dirname "$OUT")"
tar -C "$STAGE" -czf "$OUT" "$NAME"
echo "written $OUT"
sha256sum "$OUT"
