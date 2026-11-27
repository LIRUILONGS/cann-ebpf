# 生产可用范围（2026-11-27）

> 只写已验证、生产可用的能力；实验代码已移到 `experiments/`，不参与构建，不提供生产入口。

## 生产可用

| 能力 | 入口 | 说明 |
|---|---|---|
| 环境检查 | `cann-ebpf preflight` | 只读 |
| 进程/符号发现 | `cann-ebpf discover` | 只读 |
| host span / off-CPU | `cann-ebpf attach --blocking on` | 只读 uprobe，不重启 |
| 硬件基础指标 | `cann-ebpf sample --npu-smi` | 只读 |
| HBM 扩展字段 | `cann-ebpf sample --npu-smi-ext` | 频率/ECC/电压/PCIe 错误/HBM 利用率 |
| 进程/cgroup/system/Prometheus | `cann-ebpf sample` | 只读 |
| 离线报告 | `cann-ebpf report` | host + metrics；不可用视图会显式标注 |
| host-only 诊断 | `cann-ebpf diagnose` | 只读 |
| Prometheus exporter | `cann-ebpf metrics` | 只读 |
| 压测客户端 | `scripts/run_sweep.sh` | 会发负载；不重启服务；`load_latency` 在有 sweep 时可用，生产背景高时标 partial |

## 非生产 / 已移到 experiments

| 能力 | 位置 | 原因 |
|---|---|---|
| device timeline collector | `experiments/device-collector/` | LD_PRELOAD + 重启注入，实验 |
| profiler patch | `experiments/profiler-patch/` | kernel_details/ops 未打通 |
| V4 单次维护窗口 | `experiments/v4-maintenance-window.sh` | 实验脚本，不用于生产 |
| B1/B4 部署脚本 | `experiments/deploy-b1/`、`experiments/b1_merge_device.py`、`experiments/b4_hccl_probe.sh` | 实验 |

## 明确不支持

- 生产环境动态注入 device collector
- `critical_path` / `summed_delay` / `slack_cdf`
- `roofline`（无真实 `ops.jsonl` 生产者）
- `ab_diff`（无同 workload 两次 run）
- `change_point`（无 ≥5 分钟序列）
- HBM 绝对 GB/s（驱动仅给利用率 %）
- 风扇、降频因果
- 任何“为了采数据反复重启生产”的操作

## 当前服务器清理状态

- `/usr/local/libexec/run_vllm_ds4_8905.sh`：已回滚为原始版本，无 `--profiler-config`
- `/etc/vllm-ds4.env`：collector block 已移除
- V4：active，health 200
- 当前进程内仍可能有上次启动加载的 collector/profiler 库，已 idle；彻底卸载需下次自然重启或单独清理窗口，不再自动重启
