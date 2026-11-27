# experiments

这里放的是**实验/维护窗口代码**，不属于生产交付，不参与默认构建，不要在生产环境运行。

| 目录 | 内容 | 状态 |
|---|---|---|
| `device-collector/` | msPTI collector（LD_PRELOAD、device timeline） | 实验；需注入/重启，生产不可用 |
| `profiler-patch/` | vLLM-Ascend profiler 补丁、apply/rollback | 实验；未打通 kernel_details/ops |
| `v4-maintenance-window.sh` | 单次 V4 维护窗口脚本 | 实验；不用于生产 |
| `V4-SINGLE-WINDOW-PLAN.md` | 维护窗口方案 | 实验 |
| `VLLM-PROFILER-PATCH-PLAN.md` | profiler 补丁方案 | 实验 |

生产可用范围见 `docs/PRODUCTION-SCOPE.md`：

- host span / off-CPU / hardware / metrics / report
- `sample`（含 `--npu-smi-ext`）
- `sweep` 客户端 + `load_latency`（有 `sweep.json` 时）
- `diagnose`（host-only）

device 时间线、critical path、slope/slack、roofline、A/B、change point 未达到生产可用，不提供生产入口。
