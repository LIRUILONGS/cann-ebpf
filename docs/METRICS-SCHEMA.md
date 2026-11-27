# CANN eBPF 独立指标采集（cann.metrics.v1）

日期：2026-11-27
状态：M1 后端实现与 schema 说明

本文定义 `cann-ebpf sample` 的输入、输出、退出码和 `cann.metrics.v1` 数据格式。指标采集与 trace 采集完全独立：采集指标不会修改 `spans.jsonl`、`device.jsonl` 或 `hardware.jsonl`。

`cann-ebpf attach` 复用同一组 source，在 eBPF capture 窗口内自动写入同一个 run 目录的
`metrics.jsonl` / `metrics.meta.json`。集成参数为 `--metrics auto|on|off`、
`--metrics-npu-backend auto|dcmi|npu-smi|both|off`、`--metrics-interval`、`--prometheus`、
`--prometheus-include`、`--metrics-proc`、`--metrics-system` 和 `--metrics-cgroup`。
采集完成后，两个 metrics 文件会作为 manifest artifact `metrics_jsonl` / `metrics_meta`
封存；`--report auto` 只读取这些落盘证据生成 HTML。metrics 失败只影响 metrics，不会回滚
spans/device 证据，也不会把缺失值补成 0。

### 1.1 报告投影与覆盖率字段

`metrics.meta.json` 的 `series` 是**采集端原始条数**；报告嵌入的是窗口投影后的子集。
`report` 在 `DATA.metrics` 中写入以下字段，页面顶部“指标覆盖”卡直接读这些值：

| 字段 | 含义 |
|---|---|
| `meta_series` | `metrics.meta.json` 登记的原始 series 数 |
| `projected_series` | 按窗口重建后的候选数；histogram 会展开成 p50/p95/p99，因此可能大于 `meta_series` |
| `filtered_series` | 被投影阶段过滤的 `*_created` / `*_info` 数；原始 `metrics.jsonl` 保留不删 |
| `eligible_series` | `projected_series - filtered_series`，可进入绘图的候选数 |
| `series_limit` | `--max-series` 上限 |
| `rendered_series` | 实际写入 `DATA.metrics.series` 的数量 |
| `truncated_series` | `eligible_series - rendered_series`，因上限未绘制的数量 |

过滤只发生在报告投影层：`*_created` 是 Prometheus 的进程创建时间戳，`*_info` 是配置
标签集，把它们画成性能曲线会产生误导性平线。原始 JSONL 不做任何改写。

## 1. 命令

```bash
# 默认源：dcmi_probe 可用则 DCMI，否则 npu-smi
cann-ebpf sample --out runs/metrics-20261127T101500Z

# 同时采集 DCMI、指定进程、cgroup 和 vLLM 的 Prometheus 端点
cann-ebpf sample \
  --interval 1s --duration 10m \
  --dcmi \
  --pid 1234 --pid 5678 \
  --cgroup /sys/fs/cgroup/system.slice/vllm.service \
  --prometheus vllm=http://127.0.0.1:8000/metrics \
  --include '^vllm:' \
  --out runs/metrics-20261127T101500Z
```

参数：

| 参数 | 默认 | 说明 |
|---|---|---|
| `--out` | 必填 | 输出目录；已存在的 `metrics.jsonl` 不会被覆盖 |
| `--interval` | `1s` | 基础采样间隔；`npu-smi` 最小为 `5s` |
| `--duration` | `0` | `0` 表示持续到 SIGINT/SIGTERM；支持 `10m`、`1h` |
| `--dcmi` | 自动 | 使用 `CANN_DCMI_PROBE` 或 PATH 中的 `dcmi_probe` |
| `--npu-smi` | 自动 | 运行 `npu-smi info` |
| `--system` | 关闭 | 从 procfs/sysfs 采集主机整体 CPU、load、memory、PSI、disk、net、thermal 和 cpufreq；详见 `docs/METRICS-SCHEMA-SYSTEM.md` |
| `--pid` | 无 | 从 `/proc/<pid>` 采集；可重复；不指定时不会主动读取未知 PID |
| `--cgroup` | 无 | cgroup v2 目录；可重复 |
| `--prometheus` | 无 | `[name=]http(s)://host/metrics`；可重复 |
| `--include` | 全部 | Prometheus 指标名 allowlist 正则；可重复 |
| `--json` | `false` | 输出 JSON 结果摘要 |

未指定任何源时：先探测 `dcmi_probe`，可用则只启动 DCMI；否则尝试 `npu-smi`。两者都不可用时退出 4，不会偷偷读取任意进程。

输出目录：

```text
runs/metrics-20261127T101500Z/
  metrics.jsonl          # 一行一个 Point 或 Histogram
  metrics.meta.json      # 采集窗口、时钟、series 摘要、字节数和 SHA256
```

## 2. `cann.metrics.v1` 行格式

Gauge / counter：

```json
{"schema":"cann.metrics.v1","mono_ns":123456789012345,"wall_ns":1760000000123456789,"source":"dcmi","device":"NPU-0","name":"npu_aicore_utilization_ratio","kind":"gauge","value":0.42,"unit":"ratio","labels":{"name":"910B2"}}
```

Histogram：

```json
{"schema":"cann.metrics.v1","mono_ns":123456789012345,"wall_ns":1760000000123456789,"source":"prometheus:vllm","device":"host","name":"vllm_e2e_latency_seconds","kind":"histogram","unit":"seconds","labels":{"model":"qwen3"},"buckets":[0.1,0.5,1,5],"counts":[3,8,10,12],"sum":4.2,"count":12,"cumulative":true}
```

字段约束：

- `schema` 必须是 `cann.metrics.v1`。
- `mono_ns` 必须大于 0，来自主机 `CLOCK_MONOTONIC`（非 Linux 开发环境退化为进程内单调时钟）。
- `wall_ns` 必须大于 0，来自墙钟，只用于人类阅读和跨进程初步对照。
- `source`、`name`、`unit` 非空。
- `kind` 只能是 `gauge`、`counter`、`histogram`。
- gauge / counter 必须提供有限数值 `value`，不得为 `NaN` 或 `±Inf`。
- `labels` 只允许低基数维度；禁止 `request_id`、`requestid`、`prompt`、`trace_id`、`traceid`、`span_id`、`spanid`、`user_id`、`session_id`、`url`、`uri`、`query`、`path`、`input`、`output`、`text` 等键。每个标签值最多 256 字节，最多 32 个标签。

### 2.1 Histogram 的 `+Inf` 处理

Prometheus 的 `_bucket{le="+Inf"}` 不写入 `buckets` 数组：`buckets` 只保存有限上界，且严格递增；`counts` 与 `buckets` 等长，保存对应累计计数；`count` 保存包含 `+Inf` 桶的总计数。这样 JSON 中不会出现无法编码的 `Inf`，同时总量不丢失。

`cumulative=true` 时 `counts` 必须非递减，且 `count >= counts[last]`。

## 3. `metrics.meta.json`

```json
{
  "schema": "cann.metrics.meta.v1",
  "tool_version": "0.6.0-m2",
  "host": "node-1",
  "kernel": "5.10.0",
  "started_at": "2026-11-27T10:15:00+08:00",
  "ended_at": "2026-11-27T10:25:00+08:00",
  "interval_ms": 1000,
  "sources": ["cgroup", "dcmi", "proc", "prometheus:vllm"],
  "clock": {
    "mono_start_ns": 123456789000000,
    "wall_start_ns": 1760000000000000000,
    "offset_ns": 1759876543211000000,
    "drift_ns": 1200
  },
  "series": [
    {"name":"npu_aicore_utilization_ratio","kind":"gauge","unit":"ratio","source":"dcmi","device":"NPU-0","labels":{"name":"910B2"},"points":10}
  ],
  "warnings": [],
  "status": "completed",
  "points": 120,
  "artifacts": {"metrics_jsonl":{"bytes":1234567,"sha256":"..."}}
}
```

`status`：

- `completed`：到达 `--duration` 或采集正常结束，且至少写出一条样本。
- `canceled`：收到 SIGINT/SIGTERM；已写出的样本保留，meta 仍会写入。
- `no_points`：采集结束但没有任何合法样本。
- `failed`：创建、写入、fsync 或 meta 写入失败。

`artifacts.metrics_jsonl.bytes` 和 `sha256` 在文件关闭并 fsync 后计算，用于证据完整性校验。

## 4. 采集源与指标名

### 4.1 DCMI / npu-smi

复用 `internal/hardware` 的解析器，把 `cann.hardware.v1` 的每设备样本转成 long-format：

| 指标 | 类型 | 单位 | 说明 |
|---|---|---|---|
| `npu_aicore_utilization_ratio` | gauge | ratio | AICore 百分比 / 100 |
| `npu_hbm_used_bytes` / `npu_hbm_total_bytes` / `npu_hbm_free_bytes` | gauge | bytes | HBM 使用/总量/余量 |
| `npu_hbm_used_ratio` | gauge | ratio | HBM 使用率 |
| `npu_memory_used_bytes` | gauge | bytes | 普通内存使用量（源支持时） |
| `npu_power_watts` | gauge | watts | 功耗 |
| `npu_temperature_celsius` | gauge | celsius | 温度 |
| `npu_health` | gauge | count | 值为 1，`status` 标签记录 OK/Warning 等 |

设备维度写入 `device` 字段（`NPU-0`）。DCMI 默认 1s，子进程超时 2s；npu-smi 默认且最小 5s，子进程超时 5s。

### 4.2 `/proc/<pid>`

| 指标 | 类型 | 单位 |
|---|---|---|
| `proc_cpu_user_seconds` / `proc_cpu_system_seconds` | counter | seconds |
| `proc_rss_bytes` | gauge | bytes |
| `proc_threads` | gauge | count |
| `proc_fds` | gauge | count |
| `proc_vm_size_bytes` | gauge | bytes |
| `proc_vm_rss_bytes` | gauge | bytes |
| `proc_context_switches_voluntary_total` | counter | count |
| `proc_context_switches_nonvoluntary_total` | counter | count |
| `proc_page_faults_minor_total` | counter | count |
| `proc_page_faults_major_total` | counter | count |
| `proc_sched_run_delay_seconds` | counter | seconds |
| `proc_sched_running_seconds` | counter | seconds |
| `proc_sched_timeslices` | counter | count |
| `proc_io_read_bytes` / `proc_io_write_bytes` | counter | bytes |

`proc_fds` 统计 `/proc/<pid>/fd` 目录项数量，不跟随符号链接。`proc_vm_size_bytes` 来自 `VmSize`，`proc_vm_rss_bytes` 来自 `VmRSS`（同时保留既有 `proc_rss_bytes` 兼容名）；均从 `status` 的 kB 换算为 bytes。上下文切换和 page fault 均保留内核累计值。`utime`/`stime` 按 Linux 常见 `USER_HZ=100` 换算；若目标内核使用不同 `USER_HZ`，该换算需要现场校准。标签为 `pid` 和 `comm`。可选文件缺失时跳过，不补零；读取失败记录 warning。

### 4.3 cgroup v2

读取 `cpu.stat`、`cpu.pressure`、`memory.pressure`、`io.pressure`、`memory.current`、`memory.max`、`memory.peak`、`memory.events`、`pids.current`、`pids.max`：

- `cgroup_cpu_usage_seconds`、`cgroup_cpu_user_seconds`、`cgroup_cpu_system_seconds`、`cgroup_cpu_throttled_seconds`
- `cgroup_cpu_nr_periods`、`cgroup_cpu_nr_throttled`
- `cgroup_memory_current_bytes`、`cgroup_memory_max_bytes`、`cgroup_memory_peak_bytes`
- `cgroup_memory_events_total{event="low|high|max|oom|oom_kill"}`
- `cgroup_pids_current`、`cgroup_pids_max`

PSI 指标：

| 指标 | 类型 | 单位 | 说明 |
|---|---|---|---|
| `cgroup_cpu_pressure_some_avg10_ratio` / `cgroup_cpu_pressure_some_avg60_ratio` / `cgroup_cpu_pressure_some_avg300_ratio` | gauge | ratio | `cpu.pressure` 的 `some` 平均占用比例（0–1） |
| `cgroup_cpu_pressure_some_total_seconds` | counter | seconds | `cpu.pressure` 的 `some total` 微秒值 / 1e6 |
| `cgroup_cpu_pressure_full_avg10_ratio` / `cgroup_cpu_pressure_full_avg60_ratio` / `cgroup_cpu_pressure_full_avg300_ratio` | gauge | ratio | `cpu.pressure` 的 `full` 平均占用比例（0–1） |
| `cgroup_cpu_pressure_full_total_seconds` | counter | seconds | `cpu.pressure` 的 `full total` 微秒值 / 1e6 |
| `cgroup_memory_pressure_some_avg10_ratio` / `cgroup_memory_pressure_some_avg60_ratio` / `cgroup_memory_pressure_some_avg300_ratio` | gauge | ratio | `memory.pressure` 的 `some` 平均占用比例（0–1） |
| `cgroup_memory_pressure_some_total_seconds` | counter | seconds | `memory.pressure` 的 `some total` 微秒值 / 1e6 |
| `cgroup_memory_pressure_full_avg10_ratio` / `cgroup_memory_pressure_full_avg60_ratio` / `cgroup_memory_pressure_full_avg300_ratio` | gauge | ratio | `memory.pressure` 的 `full` 平均占用比例（0–1） |
| `cgroup_memory_pressure_full_total_seconds` | counter | seconds | `memory.pressure` 的 `full total` 微秒值 / 1e6 |
| `cgroup_io_pressure_some_avg10_ratio` / `cgroup_io_pressure_some_avg60_ratio` / `cgroup_io_pressure_some_avg300_ratio` | gauge | ratio | `io.pressure` 的 `some` 平均占用比例（0–1） |
| `cgroup_io_pressure_some_total_seconds` | counter | seconds | `io.pressure` 的 `some total` 微秒值 / 1e6 |
| `cgroup_io_pressure_full_avg10_ratio` / `cgroup_io_pressure_full_avg60_ratio` / `cgroup_io_pressure_full_avg300_ratio` | gauge | ratio | `io.pressure` 的 `full` 平均占用比例（0–1） |
| `cgroup_io_pressure_full_total_seconds` | counter | seconds | `io.pressure` 的 `full total` 微秒值 / 1e6 |

PSI 的 `avg10`/`avg60`/`avg300` 字段单位是 percent，`total` 字段单位是微秒。某 `.pressure` 文件不存在时静默跳过；文件存在但格式错误时记录 warning，不输出该文件指标。`max` 值不伪造为数字，直接省略并保留原文件语义。标签 `cgroup` 为路径。

### 4.4 Prometheus

本节只覆盖 Prometheus source；`--system` 的主机整体指标单独定义在
`docs/METRICS-SCHEMA-SYSTEM.md`，不与进程、cgroup 或 NPU 指标混为同一归因层。

支持标准文本 exposition：

- `# TYPE` / `# HELP`；按 `# TYPE` 判定 gauge/counter/histogram。
- 标签值转义（`\"`、`\\`、`\n`）。
- histogram 的 `_bucket`/`_sum`/`_count` 合并为一条 `cann.metrics.v1` histogram 记录；`le` 只用于分桶，不进入 `labels`。
- `NaN`、`+Inf`、`-Inf` 样本跳过并写 warning。
- 没有 `# TYPE` 时：`_total` 视为 counter，其余视为 gauge。
- `--include` 为空表示全部；有值时按 metric family 名称匹配。
- 同 series 的 counter 值回退会记录 `counter reset` warning，但原始值照常写入；报告阶段应在 reset 处断开 delta，不能把 reset 当成负增量。

`--prometheus` 的 `name` 只影响 `source` 字段，例如 `vllm=http://127.0.0.1:8000/metrics` 生成 `source: "prometheus:vllm"`。不写 name 时使用主机名（冒号替换为下划线）。

## 5. 退出码与失败语义

| 退出码 | 含义 |
|---|---|
| `0` | 正常结束；至少写出一条合法样本。部分源失败只记 warning，不影响其他源 |
| `2` | 参数错误：缺 `--out`、非法 interval/duration/pid、非法正则或 Prometheus URL |
| `4` | IO/采集失败：无法创建输出文件、写入/fsync/hash/meta 失败，或非 cancel 情况下零样本 |

关键规则：

- 单个源失败（例如 DCMI 不存在、某个 PID 已退出、某个 Prometheus 端点 500）只记 warning，其他源继续；不会因为一个源拖垮整个采集。
- `metrics.jsonl` 创建成功后，即使采集失败或零样本，也会尽量写 `metrics.meta.json`，便于审计。
- 已有 `metrics.jsonl` 时使用 `O_EXCL` 拒绝覆盖，不修改原始证据。
- 收到 SIGINT/SIGTERM 时优雅退出：关闭文件、fsync、计算 SHA256、写 meta；若输出有效，退出码为 0，`status` 为 `canceled`。
- 输出按行 flush；任何时候都不留下半个 JSON 行。

## 6. 验证与测试

```bash
gofmt -l internal/metrics cmd/cann-ebpf/sample.go
go test -count=1 ./internal/metrics ./cmd/cann-ebpf
```

测试覆盖：

- schema 校验：gauge/counter/histogram、时间戳、单位、桶递增、labels 黑名单；
- Prometheus：`# TYPE`、标签转义、histogram `_bucket/_sum/_count` 合并、NaN/Inf、counter reset；
- collector：零样本、部分源失败、拒绝覆盖、meta 字节数/SHA256；
- procfs/cgroup：临时 fake 文件树，不依赖真实 NPU；
- DCMI/npu-smi：fake runner，不调用真实设备。
