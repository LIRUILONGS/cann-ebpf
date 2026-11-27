#!/bin/sh
# v4-maintenance-window.sh -- "one and only one" V4 restart window.
#
# DEFAULT IS A DRY-RUN. Nothing on the host is changed unless --apply is given.
#
# Invariant enforced by this script (see docs/V4-SINGLE-WINDOW-PLAN.md §1.2):
#   * primary_stop  : at most ONE `systemctl stop` of the V4 service
#   * primary_start : at most ONE `systemctl start` of the V4 service
#   * no `systemctl restart` anywhere inside the wait loop
#   * rollback      : at most ONE rollback (restore env + one stop + one start)
# A second call to any of those guard-protected actions is refused outright, so
# the script can never get into a "restart until healthy" loop.
#
# It refuses to run --apply unless every blocking preflight check passes.
# Profiler capture is GATED on a not-yet-done vLLM-Ascend patch; when the patch
# marker is absent the script records profiler=skipped(patch_not_ready) and
# continues with host+device only. It never pretends the profiler ran.
#
# FS_ROOT is for OFFLINE dry-run rehearsal only (point the filesystem checks at
# a local fake tree). --apply with FS_ROOT set is refused.
#
# Usage:
#   sh scripts/v4-maintenance-window.sh                 # dry-run (default)
#   sh scripts/v4-maintenance-window.sh --apply         # real window (owner only)
#   sh scripts/v4-maintenance-window.sh --help
set -eu

# ------------------------------------------------------------------ defaults
FS=${FS_ROOT:-}
SERVICE=${SERVICE:-vllm-ds4-8905.service}
ORINFER=${ORINFER:-orinfer-v41.service}
CONTAINER=${CONTAINER:-deepseek-v4-flash-8905}

PREFIX=${PREFIX:-/opt/cann-ebpf}
COLLECTOR=${COLLECTOR:-$PREFIX/collectors/9.0.1/libcannmspti.so}
EXPECT_SHA=${EXPECT_SHA:-bd94de8574dfb949fbabb60e7c254ad05e7dd5817ec90904a8baee798c3fb621}
STAGED=$PREFIX/../data/cache/cann-ebpf/libcannmspti.so   # placeholder, recomputed below
CACHE=${CACHE:-/data/cache/cann-ebpf}
ENV_FILE=${ENV_FILE:-/etc/vllm-ds4.env}
FRAGMENT=${FRAGMENT:-/etc/cann-ebpf/collector.env}
UNIT=${UNIT:-/etc/systemd/system/vllm-ds4-8905.service}
RUN_SCRIPT=${RUN_SCRIPT:-/usr/local/libexec/run_vllm_ds4_8905.sh}
MSPTI_LIB=${MSPTI_LIB:-/usr/local/Ascend/cann-9.0.1/tools/mspti/lib64/libmspti.so}
BIN=${BIN:-$PREFIX/bin/cann-ebpf}
PROFILES=${PROFILES:-$PREFIX/profiles}
MERGE=${MERGE:-$PREFIX/b1_merge_device.py}
OPS_CONVERTER=${OPS_CONVERTER:-/opt/cann-ebpf/ops_jsonl.py}
PATCH_MARKER=${PATCH_MARKER:-$PREFIX/patches/vllm-ascend-profiler.applied}

OUTROOT=${OUTROOT:-/var/log/cann-ebpf/runs/v4-window}
HEALTH_URL=${HEALTH_URL:-http://127.0.0.1:8905/health}
BASE_URL=${BASE_URL:-http://127.0.0.1:8905}
PROFILE_PID_AUTO=${PROFILE_PID_AUTO:-auto}
SPAN_DURATION=${SPAN_DURATION:-30s}
PROF_DURATION=${PROF_DURATION:-8s}
PARENT_COMM=${PARENT_COMM:-VLLM::EngineCor}
COLLECTOR_DURATION=${COLLECTOR_DURATION:-60s}
TIMEOUT_HEALTH=${TIMEOUT_HEALTH:-1800}
ROLLBACK_TIMEOUT=${ROLLBACK_TIMEOUT:-900}
WAIT_INTERVAL=${WAIT_INTERVAL:-10}

MODE=dry-run
WANT_PROFILER=auto        # auto|yes|no
REQUIRE_PROFILER=0
DECOMMISSION_ON_SUCCESS=0
HOST_CAPTURE_OK=0
DIAGNOSE_OK=0
DEVICE_MERGE_OK=0

# runtime state
BLOCKERS=0
PRIMARY_STOP_DONE=0
PRIMARY_START_DONE=0
ROLLBACK_DONE=0
STAMP=$(date -u +%Y%m%dT%H%M%SZ)
EV=""
PROFILER_STATE=unknown

# ------------------------------------------------------------------ helpers
say(){ printf '%s\n' "$*"; }
warn(){ printf 'WARN: %s\n' "$*" >&2; }
die(){ printf 'ERROR: %s\n' "$*" >&2; exit "${2:-2}"; }
have(){ command -v "$1" >/dev/null 2>&1; }
fp(){ printf '%s%s' "$FS" "$1"; }        # filesystem path (FS_ROOT-prefixed)
sha_of(){ sha256sum "$1" 2>/dev/null | awk '{print $1}'; }

usage(){
  cat <<'EOF'
v4-maintenance-window.sh -- single-restart V4 window (default dry-run)

  --apply                 really perform the window (requires root; not with FS_ROOT)
  --dry-run               plan + preflight only (default)
  --service NAME          V4 unit (default vllm-ds4-8905.service)
  --collector PATH        fixed collector .so (default /opt/cann-ebpf/collectors/9.0.1/libcannmspti.so)
  --expect-collector-sha H expected SHA-256 of the collector source
  --profiler MODE         auto|yes|no  (auto = enable iff patch marker + --profiler-config present)
  --require-profiler      make an unavailable profiler a blocking error
  --decommission-on-success
                          on success also do the single rollback restart back to NORMAL V4.
                          This consumes the rollback budget => two total starts; owner must
                          approve first (default OFF; success leaves V4 instrumented).
  --span-duration DUR     host attach duration (default 30s)
  --profiler-duration DUR profiler window, clamped to 5..10s (default 8s)
  --timeout-health SEC    wait budget for health+device (default 1800)
  --outroot DIR           evidence root (default /var/log/cann-ebpf/runs/v4-window)
  -h | --help

Environment: FS_ROOT (offline rehearsal only), PREFIX, ENV_FILE, BIN, PROFILES, ...
EOF
}

# ------------------------------------------------------------------ arg parse
while [ $# -gt 0 ]; do
  case "$1" in
    --apply) MODE=apply ;;
    --dry-run) MODE=dry-run ;;
    --service) SERVICE=$2; shift ;;
    --collector) COLLECTOR=$2; shift ;;
    --expect-collector-sha) EXPECT_SHA=$2; shift ;;
    --profiler) WANT_PROFILER=$2; shift ;;
    --require-profiler) REQUIRE_PROFILER=1 ;;
    --decommission-on-success) DECOMMISSION_ON_SUCCESS=1 ;;
    --span-duration) SPAN_DURATION=$2; shift ;;
    --profiler-duration) PROF_DURATION=$2; shift ;;
    --timeout-health) TIMEOUT_HEALTH=$2; shift ;;
    --outroot) OUTROOT=$2; shift ;;
    -h|--help) usage; exit 0 ;;
    *) die "unknown argument: $1 (see --help)" 2 ;;
  esac
  shift
done

STAGED=$CACHE/libcannmspti.so

# ------------------------------------------------------------------ preflight
# Prints "PASS/FAIL/SKIP  <id>  <name>  (<detail>)". Blocking FAIL -> BLOCKERS++.
res(){ # status id name detail [blocking]
  _st=$1; _id=$2; _nm=$3; _dt=$4; _bl=${5:-0}
  printf '%-4s %-4s %-42s %s\n' "$_st" "$_id" "$_nm" "$_dt"
  if [ "$_st" = FAIL ] && [ "$_bl" = 1 ]; then BLOCKERS=$((BLOCKERS+1)); fi
}
chk_file(){ # id name path blocking
  p=$(fp "$3")
  if [ -f "$p" ]; then res PASS "$1" "$2" "$3"; else res FAIL "$1" "$2" "missing: $3" "$4"; fi
}
chk_exec(){ # id name path apply_blocking
  p=$(fp "$3")
  if [ -x "$p" ]; then res PASS "$1" "$2" "$3"
  elif [ -f "$p" ]; then res FAIL "$1" "$2" "present but not executable: $3" "$([ "$MODE" = apply ] && echo "$4" || echo 0)"
  else res FAIL "$1" "$2" "missing: $3" "$4"; fi
}

preflight(){
  BLOCKERS=0
  say "== preflight (fs_root='${FS}') =="

  # C1 collector source + sha
  if [ -f "$(fp "$COLLECTOR")" ]; then
    if have sha256sum; then
      got=$(sha_of "$(fp "$COLLECTOR")")
      if [ "$got" = "$EXPECT_SHA" ]; then res PASS C1 "collector source sha" "$got"
      elif [ "$MODE" = apply ]; then res FAIL C1 "collector source sha" "want $EXPECT_SHA got $got" 1
      else res FAIL C1 "collector source sha" "want $EXPECT_SHA got $got (non-blocking in dry-run)"; fi
    else res SKIP C1 "collector source sha" "sha256sum unavailable"; fi
  else res FAIL C1 "collector source" "missing: $COLLECTOR" 1; fi

  # C2 staged == source
  if [ -f "$(fp "$STAGED")" ]; then
    if have sha256sum && [ -f "$(fp "$COLLECTOR")" ] && [ "$(sha_of "$(fp "$STAGED")")" = "$(sha_of "$(fp "$COLLECTOR")")" ]; then
      res PASS C2 "staged collector matches source" "$STAGED"
    else res FAIL C2 "staged collector matches source" "differs or unverifiable" "$([ "$MODE" = apply ] && echo 1 || echo 0)"; fi
  else res FAIL C2 "staged collector" "missing: $STAGED" 1; fi

  # C3 collector env fragment
  if [ -f "$(fp "$FRAGMENT")" ]; then
    if grep -q 'CANN_MSPTI_ONLY_CMDLINE=VLLM::Worker' "$(fp "$FRAGMENT")" 2>/dev/null; then
      res FAIL C3 "collector.env" "contains ONLY_CMDLINE=VLLM::Worker (would capture 0 rows)" 1
    elif ! grep -q 'CANN_MSPTI_MAX_BYTES' "$(fp "$FRAGMENT")" 2>/dev/null; then
      res FAIL C3 "collector.env" "missing CANN_MSPTI_MAX_BYTES" 1
    else res PASS C3 "collector.env" "$FRAGMENT"
    fi
  else res FAIL C3 "collector.env" "missing: $FRAGMENT" 1; fi

  if [ -n "$FS" ]; then
    chk_file C4 "msPTI library" "$MSPTI_LIB" 1
  elif have docker && docker exec "$CONTAINER" test -f "$MSPTI_LIB" >/dev/null 2>&1; then
    res PASS C4 "msPTI library (container)" "$CONTAINER:$MSPTI_LIB"
  else
    res FAIL C4 "msPTI library (container)" "missing: $CONTAINER:$MSPTI_LIB" 1
  fi
  chk_exec C5 "cann-ebpf binary" "$BIN" 1
  chk_file C6 "device merge script" "$MERGE" 1
  chk_file C7 "V4 systemd unit" "$UNIT" 1
  chk_file C8 "vllm-ascend profile" "$PROFILES/vllm-ascend.yaml" 1
  chk_file C17 "base config" "$PREFIX/configs/base.yaml" 1

  # C9 orinfer must be inactive (exclusive cards)
  if have systemctl && [ -z "$FS" ]; then
    if systemctl is-active --quiet "$ORINFER" 2>/dev/null; then
      res FAIL C9 "orinfer inactive (free cards)" "$ORINFER is ACTIVE; stop it before the window" 1
    else res PASS C9 "orinfer inactive (free cards)" "$ORINFER inactive"; fi
  else res SKIP C9 "orinfer inactive" "systemctl unavailable / rehearsal"; fi

  # C10 target service state (informational)
  if have systemctl && [ -z "$FS" ]; then
    cur=$(systemctl is-active "$SERVICE" 2>/dev/null || true)
    res PASS C10 "V4 current state" "${SERVICE}=${cur:-unknown} (primary will stop once if active)"
  else res SKIP C10 "V4 current state" "systemctl unavailable / rehearsal"; fi

  # C11 8-card exclusivity (manual)
  res SKIP C11 "8-card exclusivity" "MANUAL: npu-smi info, no non-V4 inference procs"

  # C12 existing collector block
  if [ -f "$(fp "$ENV_FILE")" ] && grep -q '^# BEGIN cann-ebpf collector' "$(fp "$ENV_FILE")" 2>/dev/null; then
    res SKIP C12 "env has no collector block" "already present; stage step will be idempotent"
  else res PASS C12 "env has no collector block" "$ENV_FILE"; fi

  # profiler gate
  pf_ready=1
  if [ -f "$(fp "$RUN_SCRIPT")" ] && grep -q 'profiler-config' "$(fp "$RUN_SCRIPT")" 2>/dev/null; then
    res PASS C13 "run script has --profiler-config" "$RUN_SCRIPT"
  else res FAIL C13 "run script has --profiler-config" "not found (profiler patch NOT applied)" "$([ "$MODE" = apply ] && [ "$REQUIRE_PROFILER" = 1 ] && echo 1 || echo 0)"; pf_ready=0; fi
  if [ -f "$(fp "$PATCH_MARKER")" ]; then
    res PASS C14 "wrapper patch marker" "$PATCH_MARKER"
  else res FAIL C14 "wrapper patch marker" "missing: $PATCH_MARKER (patch NOT applied)" "$([ "$MODE" = apply ] && [ "$REQUIRE_PROFILER" = 1 ] && echo 1 || echo 0)"; pf_ready=0; fi
  if [ -f "$(fp "$OPS_CONVERTER")" ]; then res PASS C15 "ops converter present" "$OPS_CONVERTER"
  else res SKIP C15 "ops converter present" "missing: $OPS_CONVERTER (ops.jsonl step will skip)"; fi
  if have docker && [ -z "$FS" ]; then res PASS C16 "profiler retrieve via docker" "docker available"
  else res SKIP C16 "profiler retrieve via docker" "docker unavailable / rehearsal"; fi

  # decide profiler state
  case "$WANT_PROFILER" in
    no) PROFILER_STATE=disabled ;;
    yes) [ "$pf_ready" = 1 ] && PROFILER_STATE=enabled || PROFILER_STATE=skipped_patch_not_ready ;;
    auto) [ "$pf_ready" = 1 ] && PROFILER_STATE=enabled || PROFILER_STATE=skipped_patch_not_ready ;;
  esac
  if [ "$WANT_PROFILER" = yes ] && [ "$pf_ready" != 1 ] && [ "$REQUIRE_PROFILER" != 1 ]; then
    PROFILER_STATE=skipped_patch_not_ready
  fi

  say "-- profiler decision: $PROFILER_STATE (want=$WANT_PROFILER patch_ready=$pf_ready) --"
  say "== preflight blockers: $BLOCKERS =="
}

# ------------------------------------------------------------------ guards
primary_stop(){
  [ "$PRIMARY_STOP_DONE" = 0 ] || die "primary_stop already executed once; refusing a second stop (no loops)" 3
  PRIMARY_STOP_DONE=1
  say "[primary_stop] systemctl stop $SERVICE"
  if [ -z "$FS" ] && have systemctl; then
    systemctl stop "$SERVICE" || warn "stop returned $? (139 is tolerated: data already flushed)"
    systemctl is-active --quiet "$SERVICE" && warn "service still active after stop" || true
  else say "  (rehearsal: skipped)"; fi
}
primary_start(){
  [ "$PRIMARY_START_DONE" = 0 ] || die "primary_start already executed once; refusing a second start (no loops)" 3
  PRIMARY_START_DONE=1
  say "[primary_start] systemctl start $SERVICE"
  if [ -z "$FS" ] && have systemctl; then
    systemctl reset-failed "$SERVICE" 2>/dev/null || true
    systemctl start "$SERVICE" || { warn "primary_start failed"; return 5; }
  else say "  (rehearsal: skipped)"; fi
}

# ------------------------------------------------------------------ dry-run plan
plan(){
  say ""
  say "================= PLAN (dry-run) ================="
  say "target service      : $SERVICE"
  say "exclusive service   : $ORINFER (must be inactive)"
  say "env file            : $ENV_FILE"
  say "collector (source)  : $COLLECTOR"
  say "collector (staged)  : $STAGED"
  say "evidence dir (would): $OUTROOT/v4-window-<UTC>"
  say "profiler            : $PROFILER_STATE"
  say ""
  say "1. backup $ENV_FILE, unit, $FRAGMENT, $RUN_SCRIPT -> <ev>/backup/"
  say "2. stage collector block (idempotent) via deploy/b1/stage-collector.sh --apply"
  [ "$PROFILER_STATE" = enabled ] && say "2b. (profiler ready) ensure --profiler-config is in $RUN_SCRIPT (patch-provided; NOT written by this script)"
  say "3. primary_stop  : systemctl stop $SERVICE        (exactly once)"
  say "4. primary_start : systemctl start $SERVICE       (exactly once)"
  say "5. wait          : /health=200 AND >=1 non-empty device-*.jsonl [AND profiler cmdline] (timeout ${TIMEOUT_HEALTH}s, NO restart)"
  say "6. capture       : merge device -> attach(host span, ${SPAN_DURATION}) [-> profiler ${PROF_DURATION} if ready] -> diagnose"
  say "7. success       : write acceptance.md, print WINDOW_OK"
  say "   failure       : rollback_once (restore env, one stop, one start of NORMAL V4), print WINDOW_ROLLED_BACK"
  if [ "$DECOMMISSION_ON_SUCCESS" = 1 ]; then
    say "   NOTE          : --decommission-on-success ON => success ALSO does the single rollback restart (2 starts total)."
  fi
  say "=================================================="
  say "dry-run complete: no changes made."
}

# ------------------------------------------------------------------ apply steps
backup_all(){
  EV="$OUTROOT/v4-window-$STAMP"
  mkdir -p "$EV/backup"
  say "[backup] evidence dir: $EV"
  for f in "$ENV_FILE" "$UNIT" "$FRAGMENT" "$RUN_SCRIPT"; do
    if [ -f "$f" ]; then cp -a "$f" "$EV/backup/$(basename "$f").$STAMP" && say "  backed up $f"; else warn "not found (skip backup): $f"; fi
  done
  have npu-smi && npu-smi info > "$EV/npu-smi-before.txt" 2>&1 || say "  (npu-smi unavailable)"
  have docker && docker ps > "$EV/docker-ps-before.txt" 2>&1 || true
  have systemctl && systemctl status "$SERVICE" --no-pager > "$EV/systemctl-status-before.txt" 2>&1 || true
}

stage_config(){
  say "[stage] appending collector block to $ENV_FILE (idempotent)"
  if grep -q '^# BEGIN cann-ebpf collector' "$ENV_FILE" 2>/dev/null; then
    say "  block already present; skipping append"
  else
    # prefer the repo/deploy helper if present next to this script
    if [ -x "$(dirname "$0")/../deploy/b1/stage-collector.sh" ] && [ -z "$FS" ]; then
      PARENT_COMM="$PARENT_COMM" DURATION="$COLLECTOR_DURATION" sh "$(dirname "$0")/../deploy/b1/stage-collector.sh" --apply --env-file "$ENV_FILE" >>"$EV/stage.log" 2>&1 || die "stage-collector failed" 6
      say "  staged via deploy/b1/stage-collector.sh --apply"
    else
      printf '\n%s\n' "$(cat "$FRAGMENT")" >> "$ENV_FILE"
      say "  appended $FRAGMENT contents directly"
    fi
  fi
  say "[stage] profiler=$PROFILER_STATE (this script never writes the vLLM-Ascend wrapper patch)"
}

wait_ready(){
  say "[wait] health 200 + device files (timeout ${TIMEOUT_HEALTH}s, interval ${WAIT_INTERVAL}s, NO restart)"
  max=$((TIMEOUT_HEALTH / WAIT_INTERVAL)); i=0
  while [ "$i" -lt "$max" ]; do
    i=$((i+1))
    code=$(curl -s -o /dev/null -w '%{http_code}' -m 3 "$HEALTH_URL" 2>/dev/null || echo 000)
    files=$(find "$CACHE" -maxdepth 1 -name 'device-*.jsonl' -size +0c 2>/dev/null | wc -l | tr -d ' ')
    pfp=ok
    if [ "$PROFILER_STATE" = enabled ]; then
      if ! grep -aq 'profiler-config' /proc/*/cmdline 2>/dev/null; then pfp=no; fi
    fi
    say "  loop=$i health=$code device_nonempty=$files profiler_cmdline=$pfp"
    if [ "$code" = 200 ] && [ "$files" -ge 1 ] && [ "$pfp" = ok ]; then
      say "[wait] ready"; return 0
    fi
    sleep "$WAIT_INTERVAL"
  done
  warn "wait timed out after ${TIMEOUT_HEALTH}s"
  return 1
}

capture(){
  say "[capture] device merge + host span + diagnose"
  mkdir -p "$EV/device-merged" "$EV/run"
  python3 "$MERGE" --input-dir "$CACHE" --out "$EV/device-merged" >>"$EV/capture.log" 2>&1 || warn "device merge failed"
  [ -s "$EV/device-merged/device.jsonl" ] && DEVICE_MERGE_OK=1
  find "$CACHE" -maxdepth 1 -name 'device-*.jsonl' -printf '%f %s\n' > "$EV/device-files.txt" 2>/dev/null || ls -l "$CACHE"/device-*.jsonl > "$EV/device-files.txt" 2>&1 || true

  "$BIN" attach --pid "$PROFILE_PID_AUTO" --profile vllm-ascend --profiles-dir "$PROFILES" \
    --duration "$SPAN_DURATION" --blocking on --hardware on \
    --device-jsonl "$EV/device-merged/device.jsonl" --output "$EV/run" --json \
    > "$EV/run/collector.json" 2> "$EV/run/collector.log" || warn "attach failed (see run/collector.log)"
  if [ -s "$EV/run/spans.jsonl" ]; then HOST_CAPTURE_OK=1; fi
  "$BIN" diagnose --input "$EV/run" --json --out "$EV/diagnose.json" >>"$EV/capture.log" 2>&1 || warn "diagnose failed"
  if [ -s "$EV/diagnose.json" ]; then DIAGNOSE_OK=1; fi

  if [ "$PROFILER_STATE" = enabled ]; then capture_profiler; else
    say "[capture] profiler=$PROFILER_STATE -> skipped" | tee "$EV/profiler-skipped.txt" >/dev/null
  fi
}

capture_profiler(){
  say "[capture] profiler: start -> ${PROF_DURATION} normal traffic -> stop -> collect"
  mkdir -p "$EV/profiler"
  curl -s -o "$EV/profiler/start.txt" -X POST "$BASE_URL/start_profile" || warn "start_profile failed"
  sleep "$PROF_DURATION"
  curl -s -o "$EV/profiler/stop.txt" -X POST "$BASE_URL/stop_profile" || warn "stop_profile failed"
  if have docker; then
    docker cp "$CONTAINER:/cache/vllm_profile/." "$EV/profiler/raw" >>"$EV/capture.log" 2>&1 || warn "docker cp profiler output failed"
  fi
  # optional: analyse + convert to ops.jsonl (best-effort, gated)
  if [ -f "$OPS_CONVERTER" ] && [ -d "$EV/profiler/raw" ]; then
    python3 "$OPS_CONVERTER" --input "$EV/profiler/raw" --out "$EV/ops.jsonl" --run-id "v4-$STAMP" --dtype bf16 >>"$EV/capture.log" 2>&1 \
      && say "  ops.jsonl written" || warn "ops conversion failed"
  else
    say "  ops conversion skipped (converter or profiler output missing)"
  fi
}

# ------------------------------------------------------------------ rollback (once)
rollback_once(){
  reason=$1
  [ "$ROLLBACK_DONE" = 0 ] || { warn "rollback already executed once; refusing a second rollback"; return 1; }
  ROLLBACK_DONE=1
  say "[rollback] REASON: $reason  (single rollback; no retries)"
  {
    say "# rollback $STAMP"
    say "reason: $reason"
    # 1. restore backups
    for f in "$ENV_FILE" "$RUN_SCRIPT" "$FRAGMENT"; do
      b="$EV/backup/$(basename "$f").$STAMP"
      if [ -f "$b" ]; then cp -a "$b" "$f" && say "restored $f from backup"; else say "no backup for $f (skip)"; fi
    done
    # 2. ensure normal env has no collector block (belt & braces)
    if [ -f "$ENV_FILE" ] && grep -q '^# BEGIN cann-ebpf collector' "$ENV_FILE" 2>/dev/null; then
      if [ -x "$(dirname "$0")/../deploy/b1/rollback-collector.sh" ] && [ -z "$FS" ]; then
        sh "$(dirname "$0")/../deploy/b1/rollback-collector.sh" --apply --env-file "$ENV_FILE" || say "rollback-collector failed"
      fi
    fi
    # 3. one stop + one start of normal V4
    if [ -z "$FS" ] && have systemctl; then
      systemctl stop "$SERVICE" 2>/dev/null || true
      systemctl reset-failed "$SERVICE" 2>/dev/null || true
      systemctl start "$SERVICE" || say "normal V4 start FAILED; keep stopped, restore from backup manually"
      max=$((ROLLBACK_TIMEOUT / WAIT_INTERVAL)); i=0
      while [ "$i" -lt "$max" ]; do
        i=$((i+1))
        c=$(curl -s -o /dev/null -w '%{http_code}' -m 3 "$HEALTH_URL" 2>/dev/null || echo 000)
        say "rollback wait=$i health=$c"
        [ "$c" = 200 ] && break
        sleep "$WAIT_INTERVAL"
      done
    else
      say "(rehearsal: skipped stop/start)"
    fi
  } | tee "$EV/rollback.md"
  printf '\nWINDOW_ROLLED_BACK reason=%s evidence=%s\n' "$reason" "$EV"
}

# ------------------------------------------------------------------ summary
write_acceptance(){
  status=$1  # OK | ROLLED_BACK
  {
    say "# V4 single-window acceptance ($STAMP)"
    say ""
    say "status: $status"
    say "service: $SERVICE"
    say "profiler: $PROFILER_STATE"
    say ""
    say "| # | criterion | result | evidence |"
    say "|---|---|---|---|"
    [ "$status" = OK ] && h=200 || h=unknown
    say "| S1 | health 200 | $([ "$status" = OK ] && echo PASS || echo FAIL) | systemctl-status-after.txt |"
    say "| S2 | device files non-empty | $([ "$DEVICE_MERGE_OK" = 1 ] && echo PASS || echo FAIL) | device-merged/device.jsonl |"
    say "| S3 | host span captured | $([ "$HOST_CAPTURE_OK" = 1 ] && echo PASS || echo FAIL) | run/spans.jsonl |"
    say "| S4 | host<->device join | $([ "$DIAGNOSE_OK" = 1 ] && echo "see diagnose.json (verify)" || echo unknown) | diagnose.json |"
    say "| S5 | profiler kernel_details | $([ "$PROFILER_STATE" = enabled ] && echo "see profiler/" || echo "skipped(patch_not_ready)") | profiler/ |"
    say "| S6 | ops.jsonl | $([ -f "$EV/ops.jsonl" ] && echo present || echo skipped) | ops.jsonl |"
    say "| S7 | evidence complete | see manifest.txt | - |"
    say "| S8 | no restart loop | PASS (guarded) | logs |"
    say ""
    say "NOT DONE / NOT CLAIMED: profiler patch (if skipped), Roofline ridge fix, host<->device join (must verify actual numbers)."
  } > "$EV/acceptance.md"
  have systemctl && systemctl status "$SERVICE" --no-pager > "$EV/systemctl-status-after.txt" 2>&1 || true
  have npu-smi && npu-smi info > "$EV/npu-smi-after.txt" 2>&1 || true
  # manifest
  {
    say "evidence: $EV"
    say "generated: $STAMP"
    find "$EV" -maxdepth 2 -type f -printf '%p %s bytes\n' 2>/dev/null || ls -lR "$EV"
  } > "$EV/manifest.txt"
}

# ------------------------------------------------------------------ main
say "mode=$MODE service=$SERVICE fs_root='${FS}' profiler_want=$WANT_PROFILER"

if [ "$MODE" = apply ] && [ -n "$FS" ]; then
  die "--apply cannot be used with FS_ROOT (that is rehearsal-only)" 2
fi

preflight
if [ "$MODE" = dry-run ]; then
  plan
  exit 0
fi

# ---- apply ----
[ "$(id -u 2>/dev/null || echo 1)" -eq 0 ] || die "requires root for --apply" 4
[ "$BLOCKERS" -eq 0 ] || die "blocking preflight failures: $BLOCKERS (fix before --apply)" 4

backup_all
stage_config
primary_stop
if ! primary_start; then
  rollback_once "primary_start_failed"
  write_acceptance ROLLED_BACK
  exit 5
fi
if ! wait_ready; then
  rollback_once "wait_ready_timeout"
  write_acceptance ROLLED_BACK
  exit 6
fi
capture
if [ "$HOST_CAPTURE_OK" != 1 ]; then
  write_acceptance PARTIAL
  say ""
  say "WINDOW_PARTIAL evidence=$EV profiler=$PROFILER_STATE (V4 health is restored; host capture failed)"
  exit 7
fi
write_acceptance OK

if [ "$DECOMMISSION_ON_SUCCESS" = 1 ]; then
  say "[success] decommission-on-success: single rollback restart back to NORMAL V4"
  rollback_once "decommission_on_success"
fi

say ""
say "WINDOW_OK evidence=$EV profiler=$PROFILER_STATE"
say "next: review acceptance.md; host<->device join must be verified in diagnose.json"
exit 0
