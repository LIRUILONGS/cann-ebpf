# 系统级主机指标（`cann-ebpf sample --system`）

日期：2026-11-27
状态：已实现；Windows 本机只做夹具测试，真实 procfs/sysfs 采样待 Linux/NPU 目标机短窗口验收。

本文定义 `cann-ebpf sample --system` 从 Linux procfs/sysfs 采集的主机整体指标。该 source 的名称是 `system`，所有点的 `device` 都是 `host`。这些指标描述宿主机整体，**不等于进程指标、cgroup 指标或 NPU 指标**，不能直接归因到某个 Worker、容器或设备。

## 1. 启用方式

```bash
# 只采集主机系统指标，不要求 dcmi_probe 或 npu-smi
cann-ebpf sample --system --interval 1s --duration 5m --out runs/system-metrics

# 与现有 source 组合，各 source 仍独立采集、独立失败
cann-ebpf sample \
  --system \
  --pid 1234 \
  --cgroup /sys/fs/cgroup/system.slice/vllm.service \
  --prometheus vllm=http://127.0.0.1:8000/metrics \
  --out runs/mixed-metrics
```

`--system` 为布尔开关，默认关闭。只指定 `--system` 时，该 source 本身就是显式源，不会触发 DCMI/npu-smi 自动探测。`--out`、`--interval`、`--duration` 等现有参数行为不变。

## 2. 通用约定

- `source` 固定为 `system`，`device` 固定为 `host`。
- 计数器是内核暴露的累计值，本采集器不做差分；如需速率，由下游按时间窗口计算并处理重启导致的重置。
- gauge 是采样瞬间值。
- 没有读到的值不会补零，也不会推测；缺失或坏数据只会产生 warning 或静默跳过对应项。
- label 只使用稳定、低基数的设备/接口/模式名。

## 3. 指标清单

### 3.1 CPU 时间

来源：`/proc/stat` 的 aggregate `cpu` 行。实现只输出 aggregate，不输出 per-CPU（规范允许 per-CPU 可选）；因此没有 `cpu` label。

| 指标 | 类型 | 单位 | 标签 | 说明 |
|---|---|---|---|---|
| `node_cpu_seconds_total` | counter | seconds | `mode` | 8 个 CPU 时间模式：`user`、`nice`、`system`、`idle`、`iowait`、`irq`、`softirq`、`steal`；`/proc/stat` 的 jiffies 按 USER_HZ=100 换算为秒 |

扩展的 `guest`、`guest_nice` 字段不单独输出；按 Linux 语义，它们已经计入 `user`/`nice`。该指标是整机所有 CPU 的累计时间，不是使用率。

### 3.2 负载与任务数

来源：`/proc/loadavg`。

| 指标 | 类型 | 单位 | 标签 | 说明 |
|---|---|---|---|---|
| `node_load1` | gauge | load | 无 | 1 分钟 load average |
| `node_load5` | gauge | load | 无 | 5 分钟 load average |
| `node_load15` | gauge | load | 无 | 15 分钟 load average |
| `node_runnable_tasks` | gauge | count | 无 | loadavg 第 4 字段斜线左侧，处于 runnable 状态的 task 数 |
| `node_total_tasks` | gauge | count | 无 | loadavg 第 4 字段斜线右侧，系统 task 总数 |

load average 是任务数的指数滑动平均，不是 CPU 百分比；在 Linux 上它包含可运行任务和不可中断睡眠任务。

### 3.3 内存

来源：`/proc/meminfo`。仅采集下列字段，文件中的 `kB` 按 1 kB = 1024 bytes 换算。

| 指标 | 类型 | 单位 | 标签 | 来源字段 |
|---|---|---|---|---|
| `node_memory_mem_total_bytes` | gauge | bytes | 无 | `MemTotal` |
| `node_memory_mem_free_bytes` | gauge | bytes | 无 | `MemFree` |
| `node_memory_mem_available_bytes` | gauge | bytes | 无 | `MemAvailable` |
| `node_memory_buffers_bytes` | gauge | bytes | 无 | `Buffers` |
| `node_memory_cached_bytes` | gauge | bytes | 无 | `Cached` |
| `node_memory_swap_total_bytes` | gauge | bytes | 无 | `SwapTotal` |
| `node_memory_swap_free_bytes` | gauge | bytes | 无 | `SwapFree` |
| `node_memory_dirty_bytes` | gauge | bytes | 无 | `Dirty` |
| `node_memory_writeback_bytes` | gauge | bytes | 无 | `Writeback` |
| `node_memory_slab_bytes` | gauge | bytes | 无 | `Slab` |

旧内核缺少 `MemAvailable` 时不输出该点，不伪造。其他不在清单内的 meminfo 字段不采集。

### 3.4 PSI 压力

来源：`/proc/pressure/cpu`、`/proc/pressure/memory`、`/proc/pressure/io`。花括号表示展开后的独立指标名。

| 指标 | 类型 | 单位 | 标签 | 说明 |
|---|---|---|---|---|
| `node_pressure_{cpu,memory,io}_{some,full}_avg10` | gauge | percent | 无 | 10 秒压力平均值，值来自文件，范围通常为 0-100 |
| `node_pressure_{cpu,memory,io}_{some,full}_avg60` | gauge | percent | 无 | 60 秒压力平均值 |
| `node_pressure_{cpu,memory,io}_{some,full}_avg300` | gauge | percent | 无 | 300 秒压力平均值 |
| `node_pressure_{cpu,memory,io}_{some,full}_total_seconds` | counter | seconds | 无 | 累计 stall 时间；文件中为 microseconds，换算为 seconds |

例如 `node_pressure_cpu_some_avg10`、`node_pressure_memory_full_total_seconds`。若内核只暴露 `some` 没有 `full`，只输出实际存在的类型；PSI 文件缺失时静默跳过，不产生 warning。

### 3.5 块设备 I/O

来源：`/proc/diskstats`，按 Linux iostats 的 14 字段布局解析。每个 block device 一组点。

| 指标 | 类型 | 单位 | 标签 | 来源字段/换算 |
|---|---|---|---|---|
| `node_disk_reads_completed_total` | counter | count | `device` | reads completed |
| `node_disk_writes_completed_total` | counter | count | `device` | writes completed |
| `node_disk_read_bytes_total` | counter | bytes | `device` | sectors read × 512 |
| `node_disk_written_bytes_total` | counter | bytes | `device` | sectors written × 512 |
| `node_disk_io_time_seconds_total` | counter | seconds | `device` | ms doing I/O ÷ 1000 |

`/proc/diskstats` 的 sector 计数按内核 iostats 约定使用 512-byte 单位，即使设备逻辑扇区更大也不改用 4096。设备集合包含分区、loop、dm 等内核实际列出的 block device；采集器不按名称过滤。空设备名或字段数不足 14 的行会跳过并 warning。`io_time` 是设备忙时长，不是单次请求延迟。

### 3.6 网络接口

来源：`/proc/net/dev`。跳过 `lo`，其他接口（包括 veth、docker0 等虚拟接口）按文件实际内容采集。

| 指标 | 类型 | 单位 | 标签 | 说明 |
|---|---|---|---|---|
| `node_network_receive_bytes_total` | counter | bytes | `interface` | 接收字节数 |
| `node_network_transmit_bytes_total` | counter | bytes | `interface` | 发送字节数 |
| `node_network_receive_errs_total` | counter | count | `interface` | 接收错误数 |
| `node_network_transmit_errs_total` | counter | count | `interface` | 发送错误数 |
| `node_network_receive_drop_total` | counter | count | `interface` | 接收丢包数 |
| `node_network_transmit_drop_total` | counter | count | `interface` | 发送丢包数 |

空接口名或不足所需字段的行会跳过并 warning。错误/丢包是内核报告的累计计数，不推断丢包原因。

### 3.7 温度与 CPU 频率（存在才采）

| 指标 | 类型 | 单位 | 标签 | 来源与边界 |
|---|---|---|---|---|
| `node_thermal_celsius` | gauge | celsius | `zone`，可选 `type` | `/sys/class/thermal/thermal_zone*/temp`；毫摄氏度 ÷ 1000。`zone` 是低基数数字索引，`type` 读取同目录 `type` 文件且仅在非空、长度不超过 256 字节时添加 |
| `node_cpu_frequency_hz` | gauge | hertz | `cpu` | `/sys/devices/system/cpu/cpu*/cpufreq/scaling_cur_freq`；kHz × 1000，`cpu` 为数字编号 |

这两类 sysfs 文件在很多环境不可用。匹配目录或文件不存在时静默跳过；文件存在但格式错误时 warning 并跳过该点。温度可能为负值，按实际读数保留。

## 4. 缺失、坏数据与失败隔离

- 核心 procfs 文件（`stat`、`loadavg`、`meminfo`、`diskstats`、`net/dev`）打不开时产生 warning，继续采集其他文件和 source。
- `/proc/pressure/*` 缺失按规范静默跳过；存在但格式坏时 warning 并跳过该 pressure 文件。
- sysfs thermal/cpufreq 缺失静默跳过；坏值 warning 并跳过单个点。
- 单行格式错误只影响该行（或该文件无法整体解析时该文件），不会让 `SystemSource.Collect` 返回致命错误，也不会影响其他 source。
- 任何源的数据都通过 collector 的独立 goroutine/timeout 采集；`system` 挂起或出错不会阻塞 DCMI、proc、cgroup 或 Prometheus source。

## 5. 解释边界

- `system` 是主机整体视角：不能说明某个进程消耗了多少 CPU、某个 cgroup 的配额是否打满，也不能说明 NPU 的算力/HBM/温度。
- 若需要进程归因，使用 `--pid`；若需要容器/服务归因，使用 `--cgroup`；若需要 NPU 设备指标，使用 DCMI 或 npu-smi。
- 所有 counter 都是累计值。跨重启或设备重置后可能出现计数回退，计算 rate 的下游必须处理 reset。
- 本 source 不合成 CPU 使用率、磁盘利用率、PSI 百分比或网络带宽速率；这些应由下游基于合法 raw counter/gauge 计算。
