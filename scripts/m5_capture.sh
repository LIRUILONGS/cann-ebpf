#!/bin/sh
# M5 capture wrapper: one labeled run with health/service checks and no overwrite.
# Usage: PROFILE=orinfer LABEL=orinfer-01 DURATION=30s sh scripts/m5_capture.sh
set -eu

PROFILE=${PROFILE:-vllm-ascend}
LABEL=${LABEL:-run}
DURATION=${DURATION:-30s}
OUTROOT=${OUTROOT:-runs/m5}
BINARY=${BINARY:-./cann-ebpf-linux-arm64-m3}
PROFILES_DIR=${PROFILES_DIR:-profiles}
PID=${PID:-auto}
BLOCKING=${BLOCKING:-on}
HARDWARE=${HARDWARE:-on}
HEALTH_URL=${HEALTH_URL:-http://127.0.0.1:8905/health}
SERVICE=${SERVICE:-vllm-ds4-8905.service}
DRY_RUN=0

[ "${1:-}" = "--dry-run" ] && DRY_RUN=1

STAMP=$(date -u +%Y%m%dT%H%M%SZ)
OUT="$OUTROOT/$LABEL-$STAMP"
CMD="$BINARY attach --pid $PID --profile $PROFILE --profiles-dir $PROFILES_DIR --duration $DURATION --blocking $BLOCKING --hardware $HARDWARE --no-context --output $OUT"

echo "plan: $CMD"
if [ "$DRY_RUN" -eq 1 ]; then
  echo "dry-run: no capture performed"
  exit 0
fi
[ -x "$BINARY" ] || { echo "binary not executable: $BINARY" >&2; exit 4; }
[ -e "$OUT" ] && { echo "refusing to overwrite $OUT" >&2; exit 2; }
mkdir -p "$OUT"
"$BINARY" preflight >/dev/null || exit 4

date -u +%FT%TZ > "$OUT/started_at.txt"
curl -s -o /dev/null -w '%{http_code}\n' -m 5 "$HEALTH_URL" > "$OUT/health-before.txt" 2>/dev/null || true
systemctl is-active "$SERVICE" > "$OUT/service-before.txt" 2>/dev/null || true

rc=0
timeout 240 $CMD > "$OUT/collector.log" 2>&1 || rc=$?
echo "$rc" > "$OUT/exit-code.txt"

curl -s -o /dev/null -w '%{http_code}\n' -m 5 "$HEALTH_URL" > "$OUT/health-after.txt" 2>/dev/null || true
systemctl is-active "$SERVICE" > "$OUT/service-after.txt" 2>/dev/null || true
date -u +%FT%TZ > "$OUT/ended_at.txt"

echo "output=$OUT"
echo "rc=$rc health=$(cat "$OUT/health-before.txt")->$(cat "$OUT/health-after.txt") service=$(cat "$OUT/service-before.txt")->$(cat "$OUT/service-after.txt")"
exit "$rc"
