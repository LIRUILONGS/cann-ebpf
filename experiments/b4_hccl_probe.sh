#!/bin/sh
# B4 real-HCCL probe harness (offline preparation; plan by default).
#
# Purpose: on a set of EXCLUSIVELY owned NPUs, run a 2/8-rank all_reduce under
# the msPTI collector so we can answer two B4 questions:
#   1. does real HCCL show up as device records (kind=HCCL)?
#   2. is correlationId usable, or must join fall back to a time window?
#
# Two routes:
#   torch      torchrun + torch_npu + torch.distributed all_reduce (preferred)
#   hccl-test  build/call CANN tools/hccl_test
#
# SAFETY: this must never run on shared cards. V4/Orinfer occupy the 8 NPUs and
# there is little HBM headroom; a collective there can OOM or stall online
# requests. Default mode only prints the plan (--dry-run). --apply additionally
# requires --confirm-exclusive, and the script refuses if it sees a live
# inference process. See docs/B4-HARNESS.md and docs/B4-HCCL-PLAN.md.
#
# Usage:
#   sh scripts/b4_hccl_probe.sh --dry-run
#   sh scripts/b4_hccl_probe.sh --dry-run --route hccl-test --ranks 8 --devices 0,1,2,3,4,5,6,7
#   sh scripts/b4_hccl_probe.sh --apply --confirm-exclusive --ranks 2 --devices 0,1
#
# Exit codes: 0 ok/plan, 2 usage or safety refusal, 3 busy cards detected,
#             4 missing tooling/library, 5 probe or merge failed.
set -eu

MODE=plan                 # plan | run
ROUTE=auto                # auto | torch | hccl-test
RANKS=2
DEVICES=
OUT_DIR=${B4_OUT_DIR:-/tmp/b4}
ITERS=${B4_ITERS:-20}
ELEMS=${B4_ELEMS:-4096}
DURATION=${B4_DURATION:-0}      # 0 = collect until the process exits
MAX_BYTES=${B4_MAX_BYTES:-536870912}
MASTER_PORT=${B4_MASTER_PORT:-29591}
TIMEOUT=${B4_TIMEOUT:-900}
COLLECTOR_LIB=${COLLECTOR_LIB:-/opt/cann-ebpf/libcannmspti.so}
MSPTI_LIB=${MSPTI_LIB:-/usr/local/Ascend/ascend-toolkit/latest/tools/mspti/lib64/libmspti.so}
HCCL_SRC=${HCCL_SRC:-/usr/local/Ascend/ascend-toolkit/latest/tools/hccl_test}
HCCL_ARGS=${HCCL_ARGS:-}
HCCL_CMD=${HCCL_CMD:-}
RUN_ID=
CONFIRM=0
SKIP_BUSY=0
RESOLVED_ROUTE=

usage() {
  cat <<'EOF'
usage: b4_hccl_probe.sh [options]

  --dry-run              print the plan only (default; no NPU, no container)
  --apply                actually run the probe (needs --confirm-exclusive)
  --confirm-exclusive    attest that DEVICES are exclusively ours (required by --apply)
  --skip-busy-check      skip the docker/ps heuristic (only after manual verification)

  --route auto|torch|hccl-test   collection route (default: auto)
  --ranks N              world size, 2..8 (default: 2)
  --devices LIST         logical NPUs, e.g. 0,1 (default: 0..ranks-1)
  --out DIR              output dir (default: /tmp/b4)
  --iterations N         all_reduce iterations (default: 20)
  --elems N              fp32 elements per rank (default: 4096 = 16KiB)
  --duration SEC         CANN_MSPTI_DURATION, 0 = until exit (default: 0)
  --max-bytes N          CANN_MSPTI_MAX_BYTES (default: 536870912)
  --run-id ID            collector run id (default: b4-hccl-<UTC stamp>)
  --master-port PORT     torchrun rendezvous port (default: 29591)

  --collector-lib PATH   libcannmspti.so (default: /opt/cann-ebpf/libcannmspti.so)
  --mspti-lib PATH       libmspti.so (default: CANN toolkit path)
  --hccl-src DIR         hccl_test source tree
  --hccl-args ARGS       extra args appended to the default hccl_test template
  --hccl-cmd CMD         full hccl_test command line (run via sh -c)
  -h, --help             this help

Env overrides: B4_OUT_DIR B4_ITERS B4_ELEMS B4_DURATION B4_MAX_BYTES
               B4_MASTER_PORT B4_TIMEOUT COLLECTOR_LIB MSPTI_LIB HCCL_SRC HCCL_ARGS

Merge afterwards:
  python3 scripts/b1_merge_device.py --input-dir /tmp/b4 --out /tmp/b4/merged
EOF
}

# fail CODE MESSAGE
fail() {
  code=$1
  shift
  echo "b4_hccl_probe: $*" >&2
  exit "$code"
}

while [ $# -gt 0 ]; do
  case "$1" in
    --dry-run) MODE=plan ;;
    --apply|--run) MODE=run ;;
    --confirm-exclusive) CONFIRM=1 ;;
    --skip-busy-check) SKIP_BUSY=1 ;;
    --route) ROUTE=$2; shift ;;
    --ranks) RANKS=$2; shift ;;
    --devices) DEVICES=$2; shift ;;
    --out) OUT_DIR=$2; shift ;;
    --iterations) ITERS=$2; shift ;;
    --elems) ELEMS=$2; shift ;;
    --duration) DURATION=$2; shift ;;
    --max-bytes) MAX_BYTES=$2; shift ;;
    --run-id) RUN_ID=$2; shift ;;
    --collector-lib) COLLECTOR_LIB=$2; shift ;;
    --mspti-lib) MSPTI_LIB=$2; shift ;;
    --hccl-src) HCCL_SRC=$2; shift ;;
    --hccl-args) HCCL_ARGS=$2; shift ;;
    --hccl-cmd) HCCL_CMD=$2; shift ;;
    --master-port) MASTER_PORT=$2; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown option: $1" >&2; usage >&2; exit 2 ;;
  esac
  shift
done

# ---- argument validation -------------------------------------------------
case "$ROUTE" in
  auto|torch|hccl-test) ;;
  *) fail 2 "bad --route: $ROUTE (auto|torch|hccl-test)" ;;
esac

case "$RANKS" in
  ''|*[!0-9]*) fail 2 "--ranks must be an integer: $RANKS" ;;
esac
[ "$RANKS" -ge 2 ] && [ "$RANKS" -le 8 ] || fail 2 "--ranks must be 2..8 (got $RANKS)"

case "$ITERS" in ''|*[!0-9]*) fail 2 "--iterations must be an integer: $ITERS" ;; esac
case "$ELEMS" in ''|*[!0-9]*) fail 2 "--elems must be an integer: $ELEMS" ;; esac
[ "$ITERS" -ge 1 ] || fail 2 "--iterations must be >= 1"
[ "$ELEMS" -ge 1 ] || fail 2 "--elems must be >= 1"

if [ -z "$DEVICES" ]; then
  i=0
  while [ "$i" -lt "$RANKS" ]; do
    if [ -z "$DEVICES" ]; then DEVICES=$i; else DEVICES=$DEVICES,$i; fi
    i=$((i + 1))
  done
fi
case "$DEVICES" in
  *[!0-9,]*) fail 2 "--devices must be comma-separated integers: $DEVICES" ;;
esac
NDEV=$(printf '%s' "$DEVICES" | awk -F, '{print NF}')
[ "$NDEV" -ge "$RANKS" ] || fail 2 "need >= $RANKS devices, got $NDEV ($DEVICES)"

[ -n "$RUN_ID" ] || RUN_ID="b4-hccl-$(date -u +%Y%m%dT%H%M%SZ)"

SELF_DIR=$(cd "$(dirname "$0")" && pwd)
REPO_ROOT=$(cd "$SELF_DIR/.." && pwd)
MERGE_PY="$REPO_ROOT/scripts/b1_merge_device.py"

# ---- route resolution ----------------------------------------------------
resolve_route() {
  if [ "$ROUTE" != auto ]; then
    RESOLVED_ROUTE=$ROUTE
    return 0
  fi
  if [ -n "$HCCL_CMD" ]; then
    RESOLVED_ROUTE=hccl-test
    return 0
  fi
  if command -v torchrun >/dev/null 2>&1; then
    RESOLVED_ROUTE=torch
  else
    RESOLVED_ROUTE=hccl-test
  fi
}
resolve_route

# ---- hccl_test helpers ---------------------------------------------------
find_hccl_bin() {
  for c in "$HCCL_SRC/hccl_test" "$HCCL_SRC/bin/hccl_test" \
           "$HCCL_SRC/opbase_test/hccl_test" "$HCCL_SRC/opbase_test/bin/hccl_test"; do
    if [ -x "$c" ]; then printf '%s' "$c"; return 0; fi
  done
  found=$(find "$HCCL_SRC" -maxdepth 3 -type f -name hccl_test 2>/dev/null | head -n 1 || true)
  if [ -n "$found" ] && [ -x "$found" ]; then printf '%s' "$found"; return 0; fi
  return 1
}

busy_scan() {
  hits=
  if command -v docker >/dev/null 2>&1; then
    names=$(docker ps --format '{{.Names}}' 2>/dev/null || true)
    if printf '%s\n' "$names" | grep -Eiq 'vllm|orinfer|mindie|raylet'; then
      hits="$hits docker:[$(printf '%s' "$names" | grep -Ei 'vllm|orinfer|mindie|raylet' | tr '\n' ' ')]"
    fi
  fi
  procs=$(ps -eo args 2>/dev/null | grep -Eiv 'grep|b4_hccl_probe' || true)
  if printf '%s\n' "$procs" | grep -Eiq 'vllm|orinfer|mindie|raylet|ray::'; then
    hits="$hits ps:[$(printf '%s' "$procs" | grep -Ei 'vllm|orinfer|mindie|raylet|ray::' | head -n 3 | tr '\n' ' ')]"
  fi
  printf '%s' "$hits"
}

LD_PRELOAD_VALUE="$MSPTI_LIB:$COLLECTOR_LIB"
if [ -n "${LD_PRELOAD:-}" ]; then
  LD_PRELOAD_VALUE="$LD_PRELOAD_VALUE:$LD_PRELOAD"
fi

print_env_block() {
  cat <<EOF
  export LD_PRELOAD=$LD_PRELOAD_VALUE
  export CANN_MSPTI_ACTIVITIES=api,acl_api,runtime_api,kernel,memcpy,hccl
  export CANN_MSPTI_OUTPUT=$OUT_DIR/device-{pid}.jsonl
  export CANN_MSPTI_RUN_ID=$RUN_ID
  export CANN_MSPTI_DURATION=$DURATION
  export CANN_MSPTI_MAX_BYTES=$MAX_BYTES
  export CANN_MSPTI_FLUSH_INTERVAL_MS=1000
  export CANN_MSPTI_CLOCK=auto
  export ASCEND_RT_VISIBLE_DEVICES=$DEVICES
  export HCCL_CONNECT_TIMEOUT=${HCCL_CONNECT_TIMEOUT:-120}
EOF
}

print_next_steps() {
  cat <<EOF

next: merge the per-rank files, then inspect the HCCL sources
  python3 scripts/b1_merge_device.py --input-dir $OUT_DIR --out $OUT_DIR/merged --run-id $RUN_ID --dry-run
  python3 scripts/b1_merge_device.py --input-dir $OUT_DIR --out $OUT_DIR/merged --run-id $RUN_ID
  python3 - $OUT_DIR/merged/device.jsonl <<'PY'
import collections, json, sys
kinds, src, nonzero = collections.Counter(), collections.Counter(), 0
for line in open(sys.argv[1], encoding="utf-8-sig", errors="replace"):
    line = line.strip()
    if not line:
        continue
    try:
        r = json.loads(line)
    except Exception:
        continue
    if r.get("schema") != "cann.device.v1":
        continue
    kinds[r.get("kind", "?")] += 1
    if r.get("kind") == "HCCL":
        src[r.get("source", "?")] += 1
        nonzero += 1 if r.get("correlation_id") else 0
print("kinds:", dict(kinds))
print("hccl_source:", dict(src))
print("hccl_with_nonzero_correlation_id:", nonzero)
PY
EOF
}

# ---- plan banner ---------------------------------------------------------
echo "== B4 HCCL probe =="
echo "mode=$MODE route=$ROUTE resolved=$RESOLVED_ROUTE ranks=$RANKS devices=$DEVICES"
echo "out=$OUT_DIR run_id=$RUN_ID iterations=$ITERS elems=$ELEMS (fp32 $((ELEMS * 4))B/rank) duration=$DURATION"
echo
echo "gates (all must hold before --apply):"
echo "  [G1] $DEVICES are exclusively ours; no V4/Orinfer/other inference job"
echo "  [G2] >=16GB HBM free per card (record it: npu-smi info -> $OUT_DIR/npu-smi-before.txt)"
echo "  [G3] busy scan finds no vllm|orinfer|mindie|raylet process (docker ps, ps)"
echo "  [G4] collector lib + msPTI lib present: $COLLECTOR_LIB , $MSPTI_LIB"
echo "  [G5] CANN toolkit env sourced (source /usr/local/Ascend/ascend-toolkit/set_env.sh)"
echo
echo "environment injected into the probe process:"
print_env_block
echo

if [ "$MODE" = plan ]; then
  echo "steps (dry-run, nothing is executed):"
  echo "  1. mkdir -p $OUT_DIR"
  echo "  2. npu-smi info > $OUT_DIR/npu-smi-before.txt   # evidence, no workload"
  if [ "$SKIP_BUSY" = 1 ]; then
    echo "  3. busy scan: SKIPPED (--skip-busy-check)"
  else
    echo "  3. busy scan: docker ps --format '{{.Names}}' + ps -eo args | grep -Ei 'vllm|orinfer|mindie|raylet'"
  fi
  case "$RESOLVED_ROUTE" in
    torch)
      echo "  4. write $OUT_DIR/hccl_allreduce.py (torch_npu all_reduce, elems=$ELEMS iters=$ITERS)"
      echo "  5. B4_ELEMS=$ELEMS B4_ITERS=$ITERS timeout $TIMEOUT torchrun \\"
      echo "       --nproc_per_node=$RANKS --master_port=$MASTER_PORT $OUT_DIR/hccl_allreduce.py"
      ;;
    hccl-test)
      echo "  4. hccl_test binary: search $HCCL_SRC (build with 'make -j4' if absent)"
      if [ -n "$HCCL_CMD" ]; then
        echo "  5. timeout $TIMEOUT sh -c '$HCCL_CMD'"
      else
        echo "  5. (cd <bin dir> && timeout $TIMEOUT ./hccl_test -p $RANKS -b 8K -e 8M -f 1 $HCCL_ARGS)"
        echo "     note: hccl_test flags vary by CANN build; confirm against tools/hccl_test/README"
      fi
      ;;
  esac
  echo "  6. npu-smi info > $OUT_DIR/npu-smi-after.txt"
  echo "  7. list $OUT_DIR/device-*.jsonl and print per-file meta summary"
  print_next_steps
  echo
  echo "dry-run: nothing executed (no NPU, no container, no build, no merge)."
  echo "shared cards are forbidden for this probe; --apply requires --confirm-exclusive."
  exit 0
fi

# ---- run mode ------------------------------------------------------------
[ "$CONFIRM" = 1 ] || fail 2 "refusing to run: shared cards are forbidden. Verify G1/G2, then re-run with --apply --confirm-exclusive"
case "$(uname -s)" in
  Linux) ;;
  *) fail 2 "--apply targets the Linux NPU host (uname=$(uname -s)); use --dry-run elsewhere" ;;
esac

[ -f "$COLLECTOR_LIB" ] || fail 4 "collector lib not found: $COLLECTOR_LIB (build m3/mspti_collector first)"
[ -f "$MSPTI_LIB" ] || fail 4 "msPTI lib not found: $MSPTI_LIB (source set_env.sh / check CANN)"
[ -f "$MERGE_PY" ] || fail 4 "merge tool not found: $MERGE_PY"

if [ "$SKIP_BUSY" = 1 ]; then
  echo "busy scan: skipped by --skip-busy-check"
else
  hits=$(busy_scan)
  if [ -n "$hits" ]; then
    fail 3 "busy inference process detected:$hits -- cards are not exclusive; refusing to run"
  fi
  echo "busy scan: clean"
fi

mkdir -p "$OUT_DIR"
if command -v npu-smi >/dev/null 2>&1; then
  npu-smi info > "$OUT_DIR/npu-smi-before.txt" 2>&1 || true
  echo "wrote $OUT_DIR/npu-smi-before.txt"
fi
date -u +%FT%TZ > "$OUT_DIR/started_at.txt"

export LD_PRELOAD="$LD_PRELOAD_VALUE"
export CANN_MSPTI_ACTIVITIES=api,acl_api,runtime_api,kernel,memcpy,hccl
export CANN_MSPTI_OUTPUT="$OUT_DIR/device-{pid}.jsonl"
export CANN_MSPTI_RUN_ID="$RUN_ID"
export CANN_MSPTI_DURATION="$DURATION"
export CANN_MSPTI_MAX_BYTES="$MAX_BYTES"
export CANN_MSPTI_FLUSH_INTERVAL_MS=1000
export CANN_MSPTI_CLOCK=auto
export ASCEND_RT_VISIBLE_DEVICES="$DEVICES"
export HCCL_CONNECT_TIMEOUT="${HCCL_CONNECT_TIMEOUT:-120}"
export B4_ELEMS="$ELEMS"
export B4_ITERS="$ITERS"

rc=0
case "$RESOLVED_ROUTE" in
  torch)
    command -v torchrun >/dev/null 2>&1 || fail 4 "torchrun not found; install torch_npu or use --route hccl-test"
    cat > "$OUT_DIR/hccl_allreduce.py" <<'PY'
import json
import os
import time

import torch
import torch.distributed as dist


def main():
    local_rank = int(os.environ.get("LOCAL_RANK", "0"))
    world = int(os.environ.get("WORLD_SIZE", "1"))
    elems = int(os.environ.get("B4_ELEMS", "4096"))
    iters = int(os.environ.get("B4_ITERS", "20"))

    torch.npu.set_device(local_rank)
    dist.init_process_group("hccl")
    rank = dist.get_rank()

    x = torch.full((elems,), float(rank + 1), dtype=torch.float32, device="npu")
    torch.npu.synchronize()
    dist.barrier()

    started = time.time()
    for _ in range(iters):
        dist.all_reduce(x, op=dist.ReduceOp.SUM)
    dist.barrier()
    torch.npu.synchronize()
    elapsed = time.time() - started

    expected = float(world * (world + 1) // 2)
    got = float(x[0].item())
    print(json.dumps({
        "rank": rank,
        "world": world,
        "elems": elems,
        "bytes_per_rank": elems * 4,
        "iters": iters,
        "elapsed_s": round(elapsed, 6),
        "value0": got,
        "expected": expected,
        "ok": abs(got - expected) < 1e-3,
    }), flush=True)
    dist.destroy_process_group()


if __name__ == "__main__":
    main()
PY
    echo "+ torchrun --nproc_per_node=$RANKS --master_port=$MASTER_PORT $OUT_DIR/hccl_allreduce.py"
    timeout "$TIMEOUT" torchrun --nproc_per_node="$RANKS" --master_port="$MASTER_PORT" \
      "$OUT_DIR/hccl_allreduce.py" > "$OUT_DIR/run.log" 2>&1 || rc=$?
    ;;
  hccl-test)
    if [ -n "$HCCL_CMD" ]; then
      echo "+ sh -c '$HCCL_CMD'"
      timeout "$TIMEOUT" sh -c "$HCCL_CMD" > "$OUT_DIR/run.log" 2>&1 || rc=$?
    else
      [ -d "$HCCL_SRC" ] || fail 4 "hccl_test source tree not found: $HCCL_SRC"
      bin=$(find_hccl_bin || true)
      if [ -z "$bin" ]; then
        echo "+ (cd $HCCL_SRC && make -j4)   # no binary found; building"
        ( cd "$HCCL_SRC" && make -j4 ) > "$OUT_DIR/build.log" 2>&1 || fail 5 "hccl_test build failed; see $OUT_DIR/build.log"
        bin=$(find_hccl_bin || true)
        [ -n "$bin" ] || fail 5 "build produced no hccl_test binary under $HCCL_SRC"
      fi
      echo "using $bin"
      bin_dir=$(dirname "$bin")
      echo "+ (cd $bin_dir && ./hccl_test -p $RANKS -b 8K -e 8M -f 1 $HCCL_ARGS)"
      ( cd "$bin_dir" && timeout "$TIMEOUT" ./hccl_test -p "$RANKS" -b 8K -e 8M -f 1 $HCCL_ARGS ) \
        > "$OUT_DIR/run.log" 2>&1 || rc=$?
    fi
    ;;
esac

echo "$rc" > "$OUT_DIR/exit-code.txt"
date -u +%FT%TZ > "$OUT_DIR/ended_at.txt"
echo "---- run.log (tail) ----"
tail -n 20 "$OUT_DIR/run.log" 2>/dev/null || true
echo "---- exit code: $rc ----"

if command -v npu-smi >/dev/null 2>&1; then
  npu-smi info > "$OUT_DIR/npu-smi-after.txt" 2>&1 || true
fi

echo "device files:"
ls -1 "$OUT_DIR"/device-*.jsonl 2>/dev/null || echo "  (none written; check LD_PRELOAD and that msPTI loaded before ACL init)"
for f in "$OUT_DIR"/device-*.jsonl; do
  [ -f "$f" ] || continue
  m="$f.meta.json"
  if [ -f "$m" ]; then
    echo "  $f  meta: $(grep -o '"records_written":[0-9]*' "$m" 2>/dev/null || echo '?')"
  else
    echo "  $f"
  fi
done

print_next_steps
[ "$rc" -eq 0 ] || fail 5 "probe exited rc=$rc; see $OUT_DIR/run.log"
echo "probe done: $OUT_DIR (run_id=$RUN_ID)"
