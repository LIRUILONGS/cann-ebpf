#!/bin/sh
# apply.sh - install the vllm-ascend profiler patch for the V4 maintenance window.
#
# It ONLY touches files; it NEVER stops/starts V4 or rewrites the systemd unit.
# The restart is the operator's single stop/start in the window plan
# (docs/V4-SINGLE-WINDOW-PLAN.md Phase 4-5).
#
# Default = dry-run. Pass --apply to change anything.
#
# What it does (--apply):
#   1. preflight: verify staged files, hashes, that the live launcher is still the
#      pristine one, and that the patch is not already installed;
#   2. backup:   copy the live launcher (+ recorded hashes / image id) to
#                <patch-dir>/backup/<UTC>/ ;
#   3. install:  drop torch_npu_profiler.py next to this script (bind-mount source)
#                and atomically replace /usr/local/libexec/run_vllm_ds4_8905.sh
#                with the patched launcher;
#   4. verify:   bash -n, grep --profiler-config, write the .applied marker.
#
# Undo with ./rollback.sh (also dry-run by default).
set -eu

PATCH_DIR="${PATCH_DIR:-/opt/cann-ebpf/patches/vllm-ascend-profiler}"
RUN_SCRIPT="${RUN_SCRIPT:-/usr/local/libexec/run_vllm_ds4_8905.sh}"
MARKER="${MARKER:-/opt/cann-ebpf/patches/vllm-ascend-profiler.applied}"
CONTAINER="${CONTAINER:-deepseek-v4-flash-8905}"

# Pristine launcher sha256 (must match the live file before we patch).
ORIG_RUN_SHA="2a0f71cf7832d50da306c2282047ea447bd68f764dd47dcacb3b9fd4d03aebfd"
# Staged artefacts we are about to install (regenerate via tools/gen_diff.py only).
STAGE_PY_SHA="971ac14c7a1e6fe03d345218558a31ec02fd67915b14d2d19aa0dffa1abdf612"
STAGE_RUN_SHA="b662ccb698d0905d99b388ddb3b633e910ca7179656b6e35545fa0070fc08ee5"

APPLY=0
FORCE=0
for a in "$@"; do
  case "$a" in
    --apply) APPLY=1 ;;
    --force) FORCE=1 ;;
    -h|--help) sed -n '2,26p' "$0"; exit 0 ;;
    *) echo "unknown arg: $a" >&2; exit 2 ;;
  esac
done

TS="$(date -u +%Y%m%dT%H%M%SZ)"
say() { printf '%s\n' "$*"; }
die() { printf 'ERROR: %s\n' "$*" >&2; exit 4; }

mode="dry-run"; [ "$APPLY" -eq 1 ] && mode="apply"
say "mode=$mode patch_dir=$PATCH_DIR"
say "run_script=$RUN_SCRIPT"
say "marker=$MARKER"

# ---------------------------------------------------------------- preflight
fail=0
chk() { # chk <label> <cmd...>
  label="$1"; shift
  if "$@" >/dev/null 2>&1; then say "PASS  $label"; else say "FAIL  $label"; fail=$((fail+1)); fi
}

[ -f "$PATCH_DIR/files/torch_npu_profiler.py" ] && say "PASS  staged torch_npu_profiler.py present" || { say "FAIL  staged torch_npu_profiler.py present"; fail=$((fail+1)); }
[ -f "$PATCH_DIR/files/run_vllm_ds4_8905.sh" ] && say "PASS  staged run script present" || { say "FAIL  staged run script present"; fail=$((fail+1)); }
[ "$(sha256sum "$PATCH_DIR/files/torch_npu_profiler.py" 2>/dev/null | cut -d' ' -f1)" = "$STAGE_PY_SHA" ] \
  && say "PASS  staged py sha256" || { say "FAIL  staged py sha256"; fail=$((fail+1)); }
[ "$(sha256sum "$PATCH_DIR/files/run_vllm_ds4_8905.sh" 2>/dev/null | cut -d' ' -f1)" = "$STAGE_RUN_SHA" ] \
  && say "PASS  staged run sha256" || { say "FAIL  staged run sha256"; fail=$((fail+1)); }
[ -f "$RUN_SCRIPT" ] && say "PASS  live launcher present" || { say "FAIL  live launcher present"; fail=$((fail+1)); }
[ "$(sha256sum "$RUN_SCRIPT" 2>/dev/null | cut -d' ' -f1)" = "$ORIG_RUN_SHA" ] \
  && say "PASS  live launcher is pristine (sha matches orig)" \
  || { say "FAIL  live launcher is pristine (drift or already patched)"; fail=$((fail+1)); }
[ ! -f "$MARKER" ] && say "PASS  patch marker absent" || { say "WARN  patch marker already present ($MARKER)"; [ "$FORCE" -eq 1 ] || fail=$((fail+1)); }
chk "staged py compiles" sh -c "PYTHONPYCACHEPREFIX=/tmp/pyc-validate python3 -m py_compile '$PATCH_DIR/files/torch_npu_profiler.py'"
chk "staged run script bash -n" sh -c "bash -n '$PATCH_DIR/files/run_vllm_ds4_8905.sh'"
chk "profiler-config.json parses" sh -c "python3 -c 'import json,sys; json.load(open(\"$PATCH_DIR/profiler-config.json\"))'"
if [ -f "$PATCH_DIR/tools/validate_patch.py" ]; then
  chk "hermetic validate_patch.py" sh -c "python3 -B '$PATCH_DIR/tools/validate_patch.py'"
else
  say "SKIP  tools/validate_patch.py not staged"
fi

if [ "$fail" -ne 0 ]; then
  say "preflight blockers: $fail"
  [ "$APPLY" -eq 1 ] && die "preflight failed; nothing changed"
  say "(dry-run: blockers do not abort)"
else
  say "preflight blockers: 0"
fi

say ""
say "================= PLAN ================="
say "1. backup $RUN_SCRIPT -> $PATCH_DIR/backup/$TS/"
say "2. install $PATCH_DIR/files/torch_npu_profiler.py -> $PATCH_DIR/torch_npu_profiler.py (bind-mount source, 0644)"
say "3. replace $RUN_SCRIPT with patched launcher (adds a read-only --mount + --profiler-config)"
say "4. verify bash -n / grep --profiler-config, write $MARKER"
say "NO restart, NO unit edit, NO container action. Restart is the window's single stop/start."
say "========================================"

if [ "$APPLY" -ne 1 ]; then
  say "dry-run complete: no changes made."
  exit 0
fi

[ "$(id -u)" = "0" ] || die "--apply needs root"

# ---------------------------------------------------------------- backup
BK="$PATCH_DIR/backup/$TS"
mkdir -p "$BK"
cp -a "$RUN_SCRIPT" "$BK/run_vllm_ds4_8905.sh"
sha256sum "$RUN_SCRIPT" > "$BK/run_vllm_ds4_8905.sh.sha256"
if command -v docker >/dev/null 2>&1; then
  docker image inspect --format '{{.Id}}' "ds4/vllm-ascend:v0.23.0rc1-nonroot" > "$BK/image-id.txt" 2>/dev/null || true
  docker inspect -f '{{.Id}}' "$CONTAINER" > "$BK/container-id.txt" 2>/dev/null || true
fi
say "backup -> $BK"

# ---------------------------------------------------------------- install
install -m 0644 "$PATCH_DIR/files/torch_npu_profiler.py" "$PATCH_DIR/torch_npu_profiler.py"
install -m 0755 "$PATCH_DIR/files/run_vllm_ds4_8905.sh" "$RUN_SCRIPT.new"
mv -f "$RUN_SCRIPT.new" "$RUN_SCRIPT"
say "installed patched module + launcher"

# ---------------------------------------------------------------- verify
bash -n "$RUN_SCRIPT" || die "installed launcher failed bash -n (restore from $BK)"
grep -q -- "--profiler-config" "$RUN_SCRIPT" || die "installed launcher missing --profiler-config"
grep -q "cann-ebpf-patch v1" "$PATCH_DIR/torch_npu_profiler.py" || die "installed module missing patch marker"

{
  echo "patch=vllm-ascend-profiler v1"
  echo "applied_utc=$TS"
  echo "run_script=$RUN_SCRIPT"
  echo "run_script_sha256=$(sha256sum "$RUN_SCRIPT" | cut -d' ' -f1)"
  echo "module_source=$PATCH_DIR/torch_npu_profiler.py"
  echo "module_sha256=$(sha256sum "$PATCH_DIR/torch_npu_profiler.py" | cut -d' ' -f1)"
  echo "backup=$BK"
} > "$MARKER"
say "wrote marker $MARKER"
say "APPLIED (not restarted). Next: window Phase 4-5 (single stop/start), then POST /start_profile."
