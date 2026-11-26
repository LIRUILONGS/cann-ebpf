#!/bin/sh
# One capture cycle used by cann-ebpf.timer. Read-only attach with bounded
# duration; every run gets a new directory and never overwrites evidence.
set -eu

PREFIX=${PREFIX:-/opt/cann-ebpf}
OUTROOT=${OUTROOT:-/var/log/cann-ebpf/runs}
DURATION=${DURATION:-10s}
PROFILE=${PROFILE:-vllm-ascend}
BLOCKING=${BLOCKING:-on}
HARDWARE=${HARDWARE:-on}

BIN="$PREFIX/bin/cann-ebpf"
[ -x "$BIN" ] || { echo "cann-ebpf binary missing: $BIN" >&2; exit 4; }

mkdir -p "$OUTROOT"
OUT="$OUTROOT/ebpf-$(date -u +%Y%m%dT%H%M%SZ)"
[ -e "$OUT" ] && { echo "refusing to overwrite $OUT" >&2; exit 2; }

# Fatal environment problems should fail the unit before touching the target.
"$BIN" preflight >/dev/null || exit 4

exec "$BIN" attach \
  --pid auto \
  --profile "$PROFILE" \
  --profiles-dir "$PREFIX/profiles" \
  --duration "$DURATION" \
  --blocking "$BLOCKING" \
  --hardware "$HARDWARE" \
  --no-context \
  --output "$OUT"
