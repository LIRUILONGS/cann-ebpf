#!/usr/bin/env bash
# cann-ebpf M1 smoke test.
#
# Default: validate + discover --dry-run only. Nothing is attached.
# --attach: run one short attach after a successful discovery and preflight.
#
# This script never starts, stops, or restarts vLLM/Orinfer.

set -Eeuo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd -- "${SCRIPT_DIR}/.." && pwd)"

PROFILE="vllm-ascend"
PID=""
DURATION="30s"
DURATION_SECONDS=30
OUT_DIR=""
PROFILES_DIR="${ROOT_DIR}/profiles"
BINARY="${ROOT_DIR}/dist/cann-ebpf-linux-arm64"
DO_ATTACH=0
EXPECT_WORKERS=""
ONLY="sync,memory,handle"

usage() {
  cat <<'EOF'
Usage: scripts/m1-smoke.sh [options]

Read-only by default: validate the selected profile and run discover --dry-run.
No target process is attached unless --attach is explicitly supplied.

Options:
  --profile NAME       vllm-ascend (default) or orinfer
  --pid PID            limit discovery/attach to one PID
  --duration N[s]      attach duration, 1..300 seconds (default: 30s)
  --out-dir DIR        output directory (default: runs/m1-smoke-<profile>-<utc>)
  --profiles-dir DIR   profile directory (default: <repo>/profiles)
  --binary PATH        cann-ebpf binary (default: dist/cann-ebpf-linux-arm64)
  --expect-workers N   expected VLLM::Worker_TP count when no --pid
                       (default: 8 for vllm-ascend, 0 for orinfer)
  --only LIST          attach categories (default: sync,memory,handle; all = no filter)
  --attach             perform one short attach-tracefs after discovery/preflight
                       vLLM requires --pid; Orinfer auto-selects the first target
  --dry-run            explicit no-op alias for the default read-only mode
  -h, --help           show this help

Exit codes:
  0 success (Orinfer not running is a SKIP in read-only mode)
  2 usage/configuration error
  3 target not found or required coverage missing
  4 attach preflight failed
  other non-zero codes are returned by cann-ebpf attach
EOF
}

die() {
  local code="$1"
  shift
  printf 'ERROR: %s\n' "$*" >&2
  exit "$code"
}

note() {
  printf '%s\n' "$*"
}

while (($# > 0)); do
  case "$1" in
    --profile)
      (($# >= 2)) || die 2 "--profile requires a value"
      PROFILE="$2"
      shift 2
      ;;
    --pid)
      (($# >= 2)) || die 2 "--pid requires a value"
      PID="$2"
      shift 2
      ;;
    --duration)
      (($# >= 2)) || die 2 "--duration requires a value"
      DURATION="$2"
      shift 2
      ;;
    --out-dir|--output-dir|--out)
      (($# >= 2)) || die 2 "$1 requires a value"
      OUT_DIR="$2"
      shift 2
      ;;
    --profiles-dir)
      (($# >= 2)) || die 2 "--profiles-dir requires a value"
      PROFILES_DIR="$2"
      shift 2
      ;;
    --binary)
      (($# >= 2)) || die 2 "--binary requires a value"
      BINARY="$2"
      shift 2
      ;;
    --expect-workers)
      (($# >= 2)) || die 2 "--expect-workers requires a value"
      EXPECT_WORKERS="$2"
      shift 2
      ;;
    --only)
      (($# >= 2)) || die 2 "--only requires a value"
      ONLY="$2"
      shift 2
      ;;
    --attach)
      DO_ATTACH=1
      shift
      ;;
    --dry-run)
      DO_ATTACH=0
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      die 2 "unknown option: $1"
      ;;
  esac
done

[[ -n "$PROFILE" ]] || die 2 "profile must not be empty"

if [[ "$PID" != "" && ! "$PID" =~ ^[0-9]+$ ]]; then
  die 2 "--pid must be numeric"
fi
if [[ "$PID" == "0" ]]; then
  PID=""
fi

if [[ ! "$DURATION" =~ ^([0-9]+)s?$ ]]; then
  die 2 "--duration must be an integer number of seconds, optionally suffixed with s"
fi
DURATION_SECONDS=$((10#${BASH_REMATCH[1]}))
if ((DURATION_SECONDS < 1 || DURATION_SECONDS > 300)); then
  die 2 "--duration must be between 1 and 300 seconds"
fi
DURATION="${DURATION_SECONDS}s"

if [[ "$EXPECT_WORKERS" == "" ]]; then
  if [[ "$PROFILE" == "vllm-ascend" ]]; then
    EXPECT_WORKERS=8
  else
    EXPECT_WORKERS=0
  fi
fi
[[ -n "$ONLY" ]] || die 2 "--only must not be empty (use 'all' to disable the filter)"
if [[ ! "$EXPECT_WORKERS" =~ ^[0-9]+$ ]]; then
  die 2 "--expect-workers must be numeric"
fi

resolve_from_root() {
  local path="$1"
  if [[ "$path" = /* ]]; then
    printf '%s\n' "$path"
  else
    printf '%s\n' "${ROOT_DIR}/${path}"
  fi
}

PROFILES_DIR="$(resolve_from_root "$PROFILES_DIR")"
BINARY="$(resolve_from_root "$BINARY")"

if [[ "$PROFILE" == */* || "$PROFILE" == *.yaml || "$PROFILE" == *.yml ]]; then
  PROFILE_FILE="$(resolve_from_root "$PROFILE")"
  PROFILE_ARG="$PROFILE_FILE"
  PROFILE_LABEL="$(basename "$PROFILE_FILE" | sed -E 's/\.ya?ml$//')"
else
  PROFILE_FILE="${PROFILES_DIR}/${PROFILE}.yaml"
  PROFILE_ARG="$PROFILE"
  PROFILE_LABEL="$PROFILE"
fi

if [[ "$OUT_DIR" == "" ]]; then
  OUT_DIR="${ROOT_DIR}/runs/m1-smoke-${PROFILE_LABEL}-$(date -u +%Y%m%dT%H%M%SZ)"
else
  OUT_DIR="$(resolve_from_root "$OUT_DIR")"
fi
mkdir -p -- "$OUT_DIR"
chmod 700 -- "$OUT_DIR" 2>/dev/null || true

[[ -x "$BINARY" ]] || die 2 "binary is not executable: $BINARY (copy linux/arm64 build here or use --binary)"
[[ -r "$PROFILE_FILE" ]] || die 2 "profile not found or unreadable: $PROFILE_FILE"

MODE="dry-run"
if ((DO_ATTACH == 1)); then
  MODE="attach"
fi

note "cann-ebpf M1 smoke"
note "  mode=${MODE} profile=${PROFILE_LABEL} pid=${PID:-auto} duration=${DURATION}"
note "  binary=${BINARY}"
note "  out=${OUT_DIR}"

VALIDATE_LOG="${OUT_DIR}/validate.txt"
note ""
note "[1/3] validate"
if ! "$BINARY" validate --config "$PROFILE_FILE" >"$VALIDATE_LOG" 2>&1; then
  sed -n '1,80p' "$VALIDATE_LOG" >&2 || true
  die 2 "profile validation failed; log: $VALIDATE_LOG"
fi
cat "$VALIDATE_LOG"

DISCOVER_LOG="${OUT_DIR}/discover.txt"
DISCOVER_ARGS=(discover --profile "$PROFILE_ARG" --profiles-dir "$PROFILES_DIR" --dry-run)
if [[ -n "$PID" ]]; then
  DISCOVER_ARGS+=(--pid "$PID")
fi

note ""
note "[2/3] discover --dry-run"
set +e
"$BINARY" "${DISCOVER_ARGS[@]}" >"$DISCOVER_LOG" 2>&1
DISCOVER_RC=$?
set -e

if ((DISCOVER_RC != 0)); then
  note "discover exit=${DISCOVER_RC}; log: $DISCOVER_LOG"
  sed -n '1,40p' "$DISCOVER_LOG" >&2 || true
  if ((DISCOVER_RC == 3)) && [[ "$PROFILE_LABEL" == "orinfer" ]] && \
     grep -Eq 'no process matched|pid [0-9]+ not found' "$DISCOVER_LOG"; then
    if ((DO_ATTACH == 1)); then
      die 3 "Orinfer is not running; --attach was requested, so this is not a success"
    fi
    note "SKIP: Orinfer is not running; no process matched and no false-positive target was selected."
    exit 0
  fi
  die "$DISCOVER_RC" "discovery failed; see $DISCOVER_LOG"
fi

target_count=$(grep -cE '^process pid=' "$DISCOVER_LOG" || true)
worker_count=$(grep -cE '^process pid=.* comm=VLLM::Worker_TP$' "$DISCOVER_LOG" || true)
frontend_count=$(grep -cE '^process pid=.* comm=vllm$' "$DISCOVER_LOG" || true)
engine_count=$(grep -cE '^process pid=.* comm=VLLM::EngineCor$' "$DISCOVER_LOG" || true)
library_line=$(grep -m1 -E '^libraries=' "$DISCOVER_LOG" || true)

note "  targets=${target_count} workers=${worker_count} frontend=${frontend_count} enginecore=${engine_count}"
if [[ -n "$library_line" ]]; then
  note "  ${library_line} (per target; see discover.txt for every PID)"
fi
note "  target processes:"
grep -E '^process pid=' "$DISCOVER_LOG" | sed -E 's/^process pid=([0-9]+) ppid=[0-9]+ comm=(.*)$/    pid=\1 comm=\2/' || true

if [[ "$PROFILE_LABEL" == "vllm-ascend" && -z "$PID" ]] && ((EXPECT_WORKERS > 0)); then
  if ((worker_count != EXPECT_WORKERS)); then
    die 3 "expected ${EXPECT_WORKERS} VLLM::Worker_TP targets, found ${worker_count}; discovery is incomplete"
  fi
  if ((frontend_count < 1)); then
    die 3 "vLLM frontend was not discovered"
  fi
  if ((engine_count < 1)); then
    die 3 "vLLM EngineCore was not discovered"
  fi
fi

if [[ "$PROFILE_LABEL" != "vllm-ascend" && "$PROFILE_LABEL" != "orinfer" ]]; then
  note "WARN: custom profile; no framework-specific process count checks were applied."
fi

if ((DO_ATTACH == 0)); then
  note ""
  note "[3/3] attach skipped (default read-only mode)"
  note "OK: validate + discovery passed; log: $DISCOVER_LOG"
  exit 0
fi

ATTACH_PID="$PID"
if [[ -z "$ATTACH_PID" ]]; then
  if [[ "$PROFILE_LABEL" == "vllm-ascend" ]]; then
    die 2 "vLLM attach is single-PID with the M1 tracefs backend; rerun with --pid (for example --pid 4188787)"
  fi
  ATTACH_PID="$(sed -nE 's/^process pid=([0-9]+) ppid=.*/\1/p' "$DISCOVER_LOG" | head -n 1)"
  [[ -n "$ATTACH_PID" ]] || die 3 "discovery succeeded but no attach PID could be selected"
fi

note ""
note "[3/3] attach preflight"
preflight_problem=0

if [[ "$(id -u)" != "0" ]]; then
  note "WARN: not running as root; CAP_BPF/CAP_PERFMON/CAP_SYS_ADMIN may still work, but discovery/attach may fail."
fi

if [[ ! -r /sys/kernel/btf/vmlinux ]]; then
  note "FAIL: /sys/kernel/btf/vmlinux is missing or unreadable; CO-RE attach must not silently fall back." >&2
  preflight_problem=1
else
  note "OK: BTF present at /sys/kernel/btf/vmlinux"
fi

perf_paranoid="unknown"
if [[ -r /proc/sys/kernel/perf_event_paranoid ]]; then
  perf_paranoid="$(tr -d '[:space:]' < /proc/sys/kernel/perf_event_paranoid)"
  note "INFO: perf_event_paranoid=${perf_paranoid}"
  if [[ "$perf_paranoid" =~ ^-?[0-9]+$ ]] && ((perf_paranoid > 2)) && [[ "$(id -u)" != "0" ]]; then
    note "FAIL: perf_event_paranoid > 2 for a non-root run." >&2
    preflight_problem=1
  fi
else
  note "WARN: cannot read /proc/sys/kernel/perf_event_paranoid"
fi

uprobe_events=""
for candidate in /sys/kernel/debug/tracing/uprobe_events /sys/kernel/tracing/uprobe_events; do
  if [[ -e "$candidate" ]]; then
    uprobe_events="$candidate"
    break
  fi
done
if [[ -z "$uprobe_events" ]]; then
  note "FAIL: uprobe_events is not mounted under tracefs/debugfs." >&2
  preflight_problem=1
elif [[ ! -w "$uprobe_events" ]]; then
  note "FAIL: ${uprobe_events} is not writable; root/CAP_SYS_ADMIN is required." >&2
  preflight_problem=1
else
  note "OK: ${uprobe_events} writable"
fi

fd_limit="$(ulimit -n 2>/dev/null || printf 'unknown')"
note "INFO: ulimit -n=${fd_limit}"
if [[ "$fd_limit" =~ ^[0-9]+$ ]] && ((fd_limit < 4096)); then
  note "WARN: fd limit < 4096; per-TID perf events may be incomplete."
fi

if [[ ! -d "/proc/${ATTACH_PID}" ]]; then
  note "FAIL: PID ${ATTACH_PID} does not exist." >&2
  preflight_problem=1
fi

if ((preflight_problem != 0)); then
  die 4 "attach preflight failed; no service was restarted and no attach was attempted"
fi

ATTACH_OUT="${OUT_DIR}/attach"
mkdir -p -- "$ATTACH_OUT"
chmod 700 -- "$ATTACH_OUT" 2>/dev/null || true

ATTACH_ARGS=(attach-tracefs --pid "$ATTACH_PID" --profile "$PROFILE_ARG" --profiles-dir "$PROFILES_DIR" --duration "$DURATION" --output "$ATTACH_OUT")
if [[ "$ONLY" != "all" ]]; then
  ATTACH_ARGS+=(--only "$ONLY")
fi

note "running: ${BINARY} ${ATTACH_ARGS[*]}"
set +e
"$BINARY" "${ATTACH_ARGS[@]}" 2>&1 | tee "${OUT_DIR}/attach.txt"
ATTACH_RC=${PIPESTATUS[0]}
set -e

if ((ATTACH_RC != 0)); then
  note "attach exit=${ATTACH_RC}; log: ${OUT_DIR}/attach.txt" >&2
  exit "$ATTACH_RC"
fi

note "OK: short attach completed; output: ${ATTACH_OUT}"
