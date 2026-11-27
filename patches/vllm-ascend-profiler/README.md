# vllm-ascend profiler patch bundle (V4 maintenance window)

Prepared 2026-11-27. **Nothing here is applied to production yet.**

Purpose: make the single V4 maintenance-window profiler capture produce FLOPs
evidence. Upstream vLLM-Ascend drops two flags and pins a non-FLOPs metric group,
so `kernel_details.csv` normally has no `cube_fops`/`vector_fops`.

Two changes, both reversible:

1. `files/torch_npu_profiler.py` – patched copy of
   `vllm_ascend/profiler/torch_npu_profiler.py`:
   - forwards `ProfilerConfig.torch_profiler_record_shapes` → `record_shapes=`
   - forwards `ProfilerConfig.torch_profiler_with_flops` → `with_flops=`
   - `aic_metrics`: `PipeUtilization` → `ArithmeticUtilization`
2. `files/run_vllm_ds4_8905.sh` – patched launcher:
   - adds a read-only single-file `--mount` of this bundle's
     `torch_npu_profiler.py` over the in-image module path (**this is the
     persistence mechanism**: the container is recreated on every start);
   - appends `--profiler-config '<json>'` (see `profiler-config.json`);
   - profiler output goes to `/cache/vllm_profile` → host `/data/cache/vllm_profile`
     (persistent bind mount, no `docker cp` needed).

## Files

| path | what |
|---|---|
| `orig/` | pristine copies downloaded from the running host/container (sha256 pinned) |
| `files/` | patched copies that get installed |
| `*.diff` | unified diffs (regenerate with `tools/gen_diff.py`) |
| `profiler-config.json` | the exact `--profiler-config` object |
| `apply.sh` | install (default dry-run; `--apply` to act). Never restarts anything. |
| `rollback.sh` | restore launcher + drop marker (default dry-run; `--apply`) |
| `BACKUP.md` | backup layout + manual fallback |
| `tools/validate_patch.py` | hermetic stub test (no torch/NPU) |
| `tools/server_validate.sh` | on-host validation incl. throwaway container |
| `tools/gen_diff.py` | regenerate the diffs |

## Hashes

```
orig/run_vllm_ds4_8905.sh            2a0f71cf7832d50da306c2282047ea447bd68f764dd47dcacb3b9fd4d03aebfd
orig/torch_npu_profiler.py           0c9ffcaf969a9b1fcb44a51bdd92e69f763a7c5be64a8efbd498d6536b9e09e5
files/run_vllm_ds4_8905.sh           b662ccb698d0905d99b388ddb3b633e910ca7179656b6e35545fa0070fc08ee5
files/torch_npu_profiler.py          971ac14c7a1e6fe03d345218558a31ec02fd67915b14d2d19aa0dffa1abdf612
```

Use LF line endings (`.gitattributes` enforces this for the shell scripts).

## Quick start (window only)

```bash
cd /opt/cann-ebpf/patches/vllm-ascend-profiler
sh apply.sh                    # dry-run: preflight + plan, changes nothing
sh apply.sh --apply            # install files + marker (still no restart)
# ... window Phase 4-5: one systemctl stop / one systemctl start ...
# ... verify + capture 5-10s, see docs/VLLM-PROFILER-PATCH-PLAN.md ...
sh rollback.sh --apply         # after capture, restores launcher (no restart)
```

See `docs/VLLM-PROFILER-PATCH-PLAN.md` for the full plan and validation commands.
