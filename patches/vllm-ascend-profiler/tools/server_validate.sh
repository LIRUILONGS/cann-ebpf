#!/bin/sh
# server_validate.sh - validate the staged patch on the V4 host WITHOUT touching V4.
#
# Read-only w.r.t. production: writes only under /tmp, and starts one throwaway
# container from the same image (no NPU devices, --network none, --rm) to prove
# the single-file bind mount works. It never touches the running container.
#
# Usage: PATCH_DIR=/tmp/vllm-profiler-validate sh tools/server_validate.sh
set -eu

PATCH_DIR="${PATCH_DIR:-/tmp/vllm-profiler-validate}"
IMAGE="${IMAGE:-ds4/vllm-ascend:v0.23.0rc1-nonroot}"
PY_REL="vllm_ascend/profiler/torch_npu_profiler.py"
PY_IN_CTR="/vllm-workspace/vllm-ascend/$PY_REL"

say() { printf '%s\n' "$*"; }
fail=0
ok()  { say "PASS  $*"; }
bad() { say "FAIL  $*"; fail=$((fail+1)); }

say "== 1. shell syntax =="
if bash -n "$PATCH_DIR/files/run_vllm_ds4_8905.sh"; then ok "bash -n run_vllm_ds4_8905.sh"; else bad "bash -n"; fi
if bash -n "$PATCH_DIR/apply.sh"; then ok "bash -n apply.sh"; else bad "bash -n apply.sh"; fi
if bash -n "$PATCH_DIR/rollback.sh"; then ok "bash -n rollback.sh"; else bad "bash -n rollback.sh"; fi

say "== 2. python syntax (staged module) =="
if PYTHONPYCACHEPREFIX=/tmp/pyc-validate python3 -m py_compile "$PATCH_DIR/files/torch_npu_profiler.py"; then
  ok "py_compile files/torch_npu_profiler.py"
else
  bad "py_compile"
fi

say "== 3. hermetic param check (stub torch_npu) =="
if python3 "$PATCH_DIR/tools/validate_patch.py"; then ok "validate_patch.py"; else bad "validate_patch.py"; fi

say "== 4. launcher argv quoting (stub docker) =="
TMP="$(mktemp -d /tmp/vp-argv.XXXXXX)"
mkdir -p "$TMP/bin"
cat > "$TMP/bin/docker" <<'EOS'
#!/bin/sh
: > "${ARGV_OUT:?}"
for a in "$@"; do printf '%s\n' "$a" >> "$ARGV_OUT"; done
exit 0
EOS
chmod +x "$TMP/bin/docker"
# Use a temp copy with the absolute docker path swapped for PATH lookup.
sed 's#exec /usr/local/bin/docker run#exec docker run#' \
  "$PATCH_DIR/files/run_vllm_ds4_8905.sh" > "$TMP/run_stub.sh"
if ARGV_OUT="$TMP/argv.txt" PATH="$TMP/bin:$PATH" bash "$TMP/run_stub.sh"; then
  if python3 - "$TMP/argv.txt" "$PATCH_DIR/profiler-config.json" <<'EOF'
import json, sys
args = open(sys.argv[1]).read().split("\n")[:-1]
cfg = json.load(open(sys.argv[2]))
assert "--profiler-config" in args, "flag missing"
i = args.index("--profiler-config")
assert i + 1 < len(args), "flag has no value"
val = json.loads(args[i+1])
assert val == cfg, f"json mismatch: {val} != {cfg}"
# bind mount must be one argv, readonly, exact src/dst
mounts = [a for a in args if a.startswith("type=bind,src=")]
want_src = "/opt/cann-ebpf/patches/vllm-ascend-profiler/torch_npu_profiler.py"
assert any(a.startswith(f"type=bind,src={want_src},dst=/vllm-workspace/vllm-ascend/vllm_ascend/profiler/torch_npu_profiler.py,readonly") for a in mounts), "py bind mount missing"
print(f"OK: --profiler-config passed as 1 argv ({len(args)} args total), bind mount present")
EOF
  then ok "argv quoting + --profiler-config single arg"; else bad "argv quoting"; fi
else
  bad "stub docker run"
fi
rm -rf "$TMP"

say "== 5. image original sha (unmodified) =="
orig_in_img="$(docker run --rm --network none --entrypoint python3 "$IMAGE" \
  -c "import hashlib;print(hashlib.sha256(open('$PY_IN_CTR','rb').read()).hexdigest())" 2>/dev/null || true)"
say "image $PY_REL sha256=$orig_in_img"
[ -n "$orig_in_img" ] && ok "image file readable" || bad "image file readable"

say "== 6. throwaway container: single-file bind mount overrides module =="
docker run --rm --network none --user 0 \
  --mount "type=bind,src=$PATCH_DIR/files/torch_npu_profiler.py,dst=$PY_IN_CTR,readonly" \
  --entrypoint python3 "$IMAGE" -c "
import py_compile, hashlib
p='$PY_IN_CTR'
s=open(p,'rb').read()
py_compile.compile(p, cfile='/tmp/chk.pyc', doraise=True)
print('sha256='+hashlib.sha256(s).hexdigest())
print('patch_marker_count=%d' % s.count(b'cann-ebpf-patch v1'))
print('has_record_shapes=%s' % (b'record_shapes=profiler_config.torch_profiler_record_shapes' in s))
print('has_arith=%s' % (b'AiCMetrics.ArithmeticUtilization' in s))
" 2>&1 | sed 's/^/      /'
if docker run --rm --network none --user 0 \
  --mount "type=bind,src=$PATCH_DIR/files/torch_npu_profiler.py,dst=$PY_IN_CTR,readonly" \
  --entrypoint python3 "$IMAGE" -c "
import sys
s=open('$PY_IN_CTR','rb').read()
sys.exit(0 if (s.count(b'cann-ebpf-patch v1')==1 and b'AiCMetrics.ArithmeticUtilization' in s) else 1)
" >/dev/null 2>&1; then
  ok "bind mount overrides in-container module (patch visible, syntax OK)"
else
  bad "bind mount override"
fi

say ""
if [ "$fail" -ne 0 ]; then say "RESULT: FAIL ($fail)"; exit 1; fi
say "RESULT: OK"
