# Metrics Catalog

日期：2026-11-27

本文区分“已有采集器能拿到”“报告已经能显示”“需要新增采集端”和“LLM 停机时会缺什么”。
没有采到的数据不补造。

## 1. 当前已实现

### NPU（DCMI / npu-smi）

| 指标 | 类型 | 单位 | 说明 |
|---|---|---|---|
| `npu_aicore_utilization_ratio` | gauge | ratio | 整卡 AICore 利用率；不能归因到单个 Worker |
| `npu_hbm_used_bytes` / `npu_hbm_total_bytes` / `npu_hbm_free_bytes` | gauge | bytes | HBM 使用/总量/余量 |
| `npu_hbm_used_ratio` | gauge | ratio | HBM 使用率 |
| `npu_memory_used_bytes` | gauge | bytes | 设备内存使用 |
| `npu_power_watts` | gauge | watts | 整卡功耗 |
| `npu_temperature_celsius` | gauge | celsius | 温度 |
| `npu_health` | gauge | count | health label；值是 1，状态在 `status` label |

### 进程（`sample --pid`）

| 指标 | 类型 | 单位 |
|---|---|---|
| `proc_cpu_user_seconds` / `proc_cpu_system_seconds` | counter | seconds |
| `proc_rss_bytes` | gauge | bytes |
| `proc_threads` | gauge | count |
| `proc_sched_run_delay_seconds` | counter | seconds |
| `proc_sched_running_seconds` | counter | seconds |
| `proc_sched_timeslices` | counter | count |
| `proc_io_read_bytes` / `proc_io_write_bytes` | counter | bytes |

### 主机系统（`sample --system`）

从 Linux procfs/sysfs 采集主机整体视角，`source=system`、`device=host`：

- `/proc/stat`：aggregate CPU 时间（8 个 mode）
- `/proc/loadavg`：load1/5/15、runnable task、total task
- `/proc/meminfo`：内存、swap、dirty/writeback、slab
- `/proc/pressure/*`：CPU/memory/io 的 some/full PSI
- `/proc/diskstats`：每块设备读写计数、字节数和 io_time
- `/proc/net/dev`：每接口收发字节、错误和丢包
- `/sys/class/thermal/*`、`/sys/devices/system/cpu/*/cpufreq`：温度和 CPU 频率（存在才采）

完整指标名、单位和缺失文件语义见 `docs/METRICS-SCHEMA-SYSTEM.md`。

### cgroup v2（`sample --cgroup`）

| 指标 | 类型 | 单位 |
|---|---|---|
| `cgroup_cpu_usage_seconds` / `cgroup_cpu_user_seconds` / `cgroup_cpu_system_seconds` | counter | seconds |
| `cgroup_cpu_nr_periods` / `cgroup_cpu_nr_throttled` | counter | count |
| `cgroup_cpu_throttled_seconds` | counter | seconds |
| `cgroup_memory_current_bytes` / `cgroup_memory_max_bytes` / `cgroup_memory_peak_bytes` | gauge | bytes |
| `cgroup_memory_events_total` | counter | count |
| `cgroup_pids_current` / `cgroup_pids_max` | gauge | count |

### vLLM / Prometheus（`sample --prometheus`）

Prometheus source 按低基数 label 落盘指标，不硬编码 vLLM metric 名。只要 endpoint 暴露，
典型可显示：

- `vllm_requests_running` / `vllm_requests_waiting`
- KV cache 使用率
- TTFT / inter-token latency / E2E latency histogram
- prompt tokens / generation tokens
- 抢占、前缀缓存命中、请求完成/失败计数

### span 派生

- span 数量、p50/p95/p99/max、慢调用数
- 线程时间轴、延迟热图、次秒偏移热图、函数分位带、尾 cohort
- host off-CPU/futex、设备 busy/kernel/memcpy/HCCL（有对应证据时）

## 2. 本轮并行开发新增

### 报告统计层

每个 series 增加：

- `points`、`first`、`last`、`min`、`max`、`mean`
- `p50`、`p95`、`p99`
- `rate_last` / `rate_mean`（仅 counter，单位/秒）

统计基于 downsample 前的原始点；缺采不插值，histogram 使用 p95 点值。

报告 `指标统计` 表显示每个 series 的当前值、均值、p95、最大值和 counter 速率；
统计基于完整 metrics 样本，span 时间窗裁剪只影响绘图序列，不重新计算统计值。

### 主机压力与调度补充

- cgroup PSI：`cpu.pressure`、`memory.pressure`、`io.pressure` 的 some/full avg10/avg60/avg300 与 total
- 进程 fd 数、虚拟内存/RSS、上下文切换、major/minor page fault
- `sample --system`：主机 CPU/load/memory/PSI/disk/net/thermal/cpufreq 快照

进程/cgroup/Prometheus 指标名以 `docs/METRICS-SCHEMA.md` 的实现为准；system 指标以 `docs/METRICS-SCHEMA-SYSTEM.md` 为准。

## 3. 建议的下一批指标

| 指标族 | 价值 | 前提 |
|---|---|---|
| NPU 频率、HBM 带宽、HCCS/PCIe 吞吐 | 判断是算力、显存带宽还是互联瓶颈 | 扩展 DCMI helper 或使用 `npu-smi` 其他查询模式 |
| ECC/错误/降频原因/风扇/电压 | 解释异常抖动和性能退化 | 设备驱动接口可用 |
| CPU 频率、IRQ/softirq、网络重传、磁盘延迟 | 判断 host 侧和输入侧瓶颈 | 新增 procfs/tracepoint source |
| `tokens/s`、`requests/s`、队列等待、KV pressure、p99/p50 尾放大 | 把资源和业务延迟连起来 | 需要 vLLM Prometheus 与运行负载 |
| msPTI kernel/memcpy/HCCL 时间线 | 回答设备内部时间花在哪 | 需要目标进程内的 collector 和维护窗口 |
| 逐 request TTFT/ITL/queue time | 回答单个请求为什么慢 | 需要 request ID 或 vLLM request 级指标 |

## 4. LLM 停机影响

| 工作 | 停机时是否可做 |
|---|---|
| 代码开发、单测、合成 fixture、HTML 验收 | 可以 |
| NPU 空闲态 DCMI/npu-smi 采样 | 可以，但只能看空闲基线 |
| 进程/cgroup 指标采集 | 可以采样其他进程；没有 vLLM Worker 时可验证参数和错误处理 |
| 主机 `--system` 指标采集 | 可以，能拿到空闲态 CPU/内存/PSI/磁盘/网络基线 |
| vLLM Prometheus running/waiting、KV cache、TTFT/ITL | 不可以，endpoint 不存在或无请求数据 |
| request 级关联、生产延迟结论 | 不可以 |
| msPTI 真实设备时间线 | 不可以，目标进程不在运行 |

因此本轮先把采集器、统计层和 UI 做扎实；真实业务指标等 LLM 恢复后再做一次短窗口验收。

