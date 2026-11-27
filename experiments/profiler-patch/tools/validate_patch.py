#!/usr/bin/env python3
"""Hermetic validation for the vllm-ascend profiler patch.

No torch / torch_npu / vllm / NPU is required: the target module's imports are
replaced by minimal stubs so we can prove exactly which kwargs reach
``torch_npu.profiler.profile(...)`` and which ``aic_metrics`` reaches
``_ExperimentalConfig``.

Checks
------
1. patched module imports cleanly through stubs (syntax + import surface);
2. ``record_shapes`` / ``with_flops`` are forwarded from ProfilerConfig;
3. ``aic_metrics`` == ``ArithmeticUtilization``;
4. the pristine (orig) module does NOT forward record_shapes/with_flops
   (proves the patch is what closes the gap);
5. ``profiler-config.json`` is valid and only uses real ProfilerConfig fields;
6. the JSON embedded in run_vllm_ds4_8905.sh.patched equals profiler-config.json.

Exit code 0 = all checks passed.
"""
from __future__ import annotations

import json
import pathlib
import re
import sys
import types

# Never leave __pycache__ next to the staged artefacts (staging dir is also the
# bind-mount source directory on the host).
sys.dont_write_bytecode = True

HERE = pathlib.Path(__file__).resolve().parent.parent

# ProfilerConfig fields verified against
# /vllm-workspace/vllm/vllm/config/profiler.py (vllm 0.23.0+empty) on 2026-11-27.
PROFILER_CONFIG_FIELDS = {
    "profiler",
    "torch_profiler_dir",
    "torch_profiler_with_stack",
    "torch_profiler_with_flops",
    "torch_profiler_use_gzip",
    "torch_profiler_dump_cuda_time_total",
    "torch_profiler_record_shapes",
    "torch_profiler_with_memory",
    "ignore_frontend",
    "delay_iterations",
    "max_iterations",
    "warmup_iterations",
    "active_iterations",
    "wait_iterations",
}

captured: dict = {}


class _FakeProfiler:
    def start(self) -> None:
        pass

    def stop(self) -> None:
        pass


class _FakeExpCfg:
    def __init__(self, **kwargs):
        captured["experimental"] = kwargs


def _fake_profile(**kwargs):
    captured["profile"] = kwargs
    return _FakeProfiler()


def _fake_trace_handler(path, worker_name=None):
    captured["trace"] = {"path": path, "worker_name": worker_name}
    return lambda *a, **k: None


def _install_stubs() -> None:
    m = types.ModuleType("torch_npu")
    prof = types.ModuleType("torch_npu.profiler")
    prof._ExperimentalConfig = _FakeExpCfg
    prof.profile = _fake_profile
    prof.tensorboard_trace_handler = _fake_trace_handler

    class ExportType:
        Text = "Text"
        Db = "Db"

    class ProfilerLevel:
        Level0 = "Level0"
        Level1 = "Level1"
        Level2 = "Level2"
        Level_none = "Level_none"

    class AiCMetrics:
        PipeUtilization = "PipeUtilization"
        ArithmeticUtilization = "ArithmeticUtilization"
        Memory = "Memory"

    class ProfilerActivity:
        CPU = "CPU"
        NPU = "NPU"

    prof.ExportType = ExportType
    prof.ProfilerLevel = ProfilerLevel
    prof.AiCMetrics = AiCMetrics
    prof.ProfilerActivity = ProfilerActivity
    m.profiler = prof

    vllm = types.ModuleType("vllm")
    vllm_config = types.ModuleType("vllm.config")
    vllm_config.ProfilerConfig = object
    vllm.config = vllm_config

    vllm_profiler = types.ModuleType("vllm.profiler")
    wrapper = types.ModuleType("vllm.profiler.wrapper")

    class WorkerProfiler:
        def __init__(self, profiler_config):
            self._profiler_config = profiler_config

    wrapper.WorkerProfiler = WorkerProfiler
    vllm_profiler.wrapper = wrapper
    vllm.profiler = vllm_profiler

    va = types.ModuleType("vllm_ascend")
    envs = types.ModuleType("vllm_ascend.envs")
    envs.MSMONITOR_USE_DAEMON = False
    va.envs = envs

    ascend_config = types.ModuleType("vllm_ascend.ascend_config")

    class _AscendCfg:
        msmonitor_use_daemon = False

    ascend_config.get_ascend_config = lambda: _AscendCfg()
    va.ascend_config = ascend_config

    va_profiler = types.ModuleType("vllm_ascend.profiler")
    va_profiler.__path__ = []  # namespace pkg so submodule import works
    va.profiler = va_profiler

    sys.modules.update(
        {
            "torch_npu": m,
            "torch_npu.profiler": prof,
            "vllm": vllm,
            "vllm.config": vllm_config,
            "vllm.profiler": vllm_profiler,
            "vllm.profiler.wrapper": wrapper,
            "vllm_ascend": va,
            "vllm_ascend.envs": envs,
            "vllm_ascend.ascend_config": ascend_config,
            "vllm_ascend.profiler": va_profiler,
        }
    )


def _load(path: pathlib.Path, name: str):
    import importlib.util

    spec = importlib.util.spec_from_file_location(name, str(path))
    assert spec and spec.loader, f"cannot build spec for {path}"
    mod = importlib.util.module_from_spec(spec)
    sys.modules[name] = mod
    spec.loader.exec_module(mod)
    return mod


class FakeProfilerConfig:
    profiler = "torch"
    torch_profiler_dir = "/cache/vllm_profile"
    torch_profiler_with_stack = False
    torch_profiler_with_flops = True
    torch_profiler_record_shapes = True
    torch_profiler_with_memory = False


failures: list[str] = []


def check(cond: bool, msg: str) -> None:
    if cond:
        print(f"  PASS  {msg}")
    else:
        print(f"  FAIL  {msg}")
        failures.append(msg)


def main() -> int:
    _install_stubs()

    print("[1] patched module imports through stubs")
    patched = _load(HERE / "files" / "torch_npu_profiler.py", "patched_profiler")
    check(hasattr(patched, "TorchNPUProfilerWrapper"), "TorchNPUProfilerWrapper exists")

    print("[2] patched module forwards the two flags")
    captured.clear()
    patched.TorchNPUProfilerWrapper._create_profiler(FakeProfilerConfig(), "dp0_tp0_rank0")
    p = captured.get("profile", {})
    check(p.get("record_shapes") is True, "record_shapes=True forwarded to profile()")
    check(p.get("with_flops") is True, "with_flops=True forwarded to profile()")
    check(p.get("with_modules") is False, "with_modules comes from torch_profiler_with_stack=False")
    check(p.get("with_stack") is False, "with_stack stays False")
    check(captured.get("experimental", {}).get("aic_metrics") == "ArithmeticUtilization",
          "aic_metrics == ArithmeticUtilization")
    check(captured.get("trace", {}).get("path") == "/cache/vllm_profile",
          "profiler dir == /cache/vllm_profile")
    check(captured.get("trace", {}).get("worker_name") == "dp0_tp0_rank0",
          "worker_name passthrough intact")

    print("[3] pristine module does NOT forward them (gap proof)")
    captured.clear()
    orig = _load(HERE / "orig" / "torch_npu_profiler.py", "orig_profiler")
    orig.TorchNPUProfilerWrapper._create_profiler(FakeProfilerConfig(), "dp0_tp0_rank0")
    op = captured.get("profile", {})
    check("record_shapes" not in op, "orig has no record_shapes kwarg")
    check("with_flops" not in op, "orig has no with_flops kwarg")
    check(captured.get("experimental", {}).get("aic_metrics") == "PipeUtilization",
          "orig aic_metrics == PipeUtilization (needs patch)")

    print("[4] profiler-config.json validity")
    cfg = json.loads((HERE / "profiler-config.json").read_text(encoding="utf-8"))
    unknown = set(cfg) - PROFILER_CONFIG_FIELDS
    check(not unknown, f"all keys are real ProfilerConfig fields (unknown={sorted(unknown)})")
    check(cfg.get("profiler") == "torch", "profiler == torch")
    check(cfg.get("torch_profiler_dir") == "/cache/vllm_profile", "torch_profiler_dir present")
    check(cfg.get("torch_profiler_record_shapes") is True, "record_shapes true")
    check(cfg.get("torch_profiler_with_flops") is True, "with_flops true")
    check(cfg.get("torch_profiler_with_stack") is False, "with_stack false (low overhead)")

    print("[5] run script embeds the same JSON")
    sh = (HERE / "files" / "run_vllm_ds4_8905.sh").read_text(encoding="utf-8")
    m = re.search(r"--profiler-config '([^']+)'", sh)
    check(m is not None, "run script contains --profiler-config '<json>'")
    if m:
        embedded = json.loads(m.group(1))
        check(embedded == cfg, "embedded JSON == profiler-config.json")

    print()
    if failures:
        print(f"RESULT: FAIL ({len(failures)} check(s) failed)")
        return 1
    print("RESULT: OK (all checks passed)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
