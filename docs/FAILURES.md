# 失败记录：cann-ebpf 生产采集尝试复盘

日期：2026-11-27  
目的：把这次失败完整写清楚，避免以后再把实验当生产。

## 1. 一句话结论

**host span、metrics、HBM 扩展、报告这条线是可用的；device timeline、critical path、roofline、profiler/ops 这条线没有达到生产可用，而且我为它反复重启了生产 V4，这是错误。**

## 2. 实际交付（可用）

- `preflight` / `discover`
- `attach`：host span、off-CPU/futex、硬件、batch 上下文
- `sample`：NPU/进程/cgroup/system/Prometheus
- `sample --npu-smi-ext`：频率、ECC、电压、PCIe 错误、HBM 利用率（真实 8 卡验证）
- `report`：host + metrics + load_latency（有 sweep 时）+ off_cpu/treemap/topology
- `diagnose`：host-only
- `metrics` exporter
- `scripts/run_sweep.sh`：压测客户端

## 3. 实验/失败（已移到 experiments）

| 能力 | 结果 | 根因 |
|---|---|---|
| device timeline collector | 失败 | `LD_PRELOAD` 必须进程启动注入；28 进程/3.2GB；启动期 cap/时长难对齐 |
| V4 单次维护窗口 | 失败两次 | 第一次缺 `configs/base.yaml`；第二次 device 64MiB 提前截断、host 采集 30s 无流量 |
| profiler / ops | 失败 | `kernel_details.csv` 没生成；`ops.jsonl` 未产出；`analyse` 未走通 |
| critical path / summed / slack | 未完成 | 没有 host+device 同窗口 |
| roofline | 不可用 | 没有真实 `ops.jsonl` |
| A/B、change point | 不可用 | 没有两次同 workload run / ≥5 分钟序列 |

## 4. 我做错的地方

1. **把生产当实验环境**
   - 为了拿 device/profiler 数据，两次停 V4；
   - 第一次失败后没有先补齐所有前置，就做了第二次；
   - 这违反了“生产不折腾”的基本原则。

2. **预检不完整**
   - 第一次缺 `configs/base.yaml`；
   - 第二次 `MAX_BYTES=64MiB` 在模型加载阶段就打满；
   - host attach 在 health 200 后立即开始，没有 warmup/受控流量。

3. **时间窗设计错误**
   - collector 从进程启动计时，不是从 host attach 计时；
   - 模型加载约 8 分钟，device 和 host 必然错位；
   - 没有在采集前验证时间区间重叠。

4. **过度承诺**
   - 报告里放了 10 个视图卡片，实际只有 4 个可用；
   - 把 `unavailable` 卡片留在生产 UI，容易让人误以为“差一点就有”；
   - 现在已从生产 UI/构建移除 6 个不可用视图。

5. **没有先在 staging 验证**
   - device/profiler 这种需要重启的能力，应该先在实验机跑通；
   - 不应该在生产上边试边改。

## 5. 正确做法（以后）

1. 生产只跑 **零重启** 能力：host span、metrics、report；
2. device 采集必须通过 **vLLM worker 插件/控制接口**动态启停，不能 LD_PRELOAD；
3. profiler 必须部署时预置，运行时 `/start_profile`，且先验证 `kernel_details.csv`；
4. 任何窗口必须先满足：
   - 时间窗重叠验证；
   - 固定负载 + warmup；
   - `MAX_BYTES` 足够覆盖启动 + 采集；
   - host/device 都非空；
   - match rate 达标；
5. 单次窗口失败后，不在生产重试；回 staging 修。

## 6. 当前状态

- V4：active，health 200
- `run_vllm_ds4_8905.sh`：已恢复原始版本，无 profiler 补丁
- `/etc/vllm-ds4.env`：collector block 已移除
- 当前进程可能仍加载上次启动的 collector/profiler 库，已 idle；下次自然重启后彻底干净
- 生产报告只保留 4 个可用视图
- 实验代码位置：`experiments/`

## 7. 最后一句

这次不是“再调一下就好”，是**方法和边界都错了**。生产工具应该让人敢用，而不是让服务陪着做实验。以后 device/profiler 只在 staging 验证，生产不重启。
