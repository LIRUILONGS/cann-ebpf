# 指标与图表覆盖审计

日期：2026-11-28  
审计对象：`C:\Users\liruilong\pentagi\cann-ebpf`  
依据：`docs/METRICS-CATALOG.md`、`docs/DRIVER-METRICS.md`、`docs/DCMI-HCCS-METRICS.md`、`internal/report/metrics.go`、`internal/report/build_metrics.go`、`internal/report/views_v2.go`、`internal/report/build_views_v2.go`、`internal/report/template.html`，以及真实报告：

- `.analysis/metrics-full-20261127T184802Z/report.html`
- `.analysis/driver-fix-20261128T190100Z/report.html`

本审计只读实现、报告和原始 JSONL，不重新采集、不修改代码、不修改任何原始证据。本文只描述当前实现和两份真实报告实际能证明的内容，不把“采集器有能力”写成“报告已有专用图”。

## 1. 结论摘要

| 项目 | 结论 |
|---|---|
| 指标面板 | 生产报告只有 1 个指标专用区块 `metricsPanel`；其内部是 `指标矩阵` 和 `指标统计` 两块。 |
| 指标矩阵 | `指标矩阵` 按 `source + name + unit + kind` 分组，每组一行 sparkline；点击行可查看组内各 series 的实际曲线。它不是按指标族拆分的专用图。 |
| 专用指标图 | 只有 `指标统计` 的设备热力图使用 4 个 DCMI 指标：`npu_aicore_utilization_ratio`、`npu_hbm_used_ratio`、`npu_power_watts`、`npu_temperature_celsius`。没有 DCMI 总体面板、HCCS 专用面板或驱动层专用面板。 |
| 全量指标报告 | `metrics-full-20261127T184802Z`：49,319 points，1,413 meta series，1,429 projected / eligible / rendered series，147 个矩阵组。 |
| 驱动指标报告 | `driver-fix-20261128T190100Z`：29,247 points，3,582 meta series，4,600 projected，4,599 eligible / rendered series，55 个矩阵组。 |
| 两份报告的视图状态 | 都是 metrics-only（`body class="metrics-only"`），没有 spans；`views_v2` 的 4 个生产 host 视图全部 `available=false`，火焰图也不显示。 |
| 历史六视图 | `critical_path`、`summed_delay`、`slack_cdf`、`roofline`、`ab_diff`、`change_point` 在模板里仍有渲染函数/类型，但没有进入 `ReportViewsV2` 和 `buildReportViewsV2`，当前生产报告不会生成。 |
| 明确不可用 | 驱动层的 SVM/XSMEM 指标、`npu_hdc_alloc_bytes`、部分 DMA byte 指标以及 `devmm_svm_ioctl` 相关指标有明确 `unavailable` 状态；不能当 0，也不能当“已采集成功”。 |

## 2. 判定口径

- **已接入生产报告**：模板有专用绘制路径，且本次真实报告中实际出现非空数据。当前只适用于 `指标统计` 的设备热力图中的 4 个 DCMI 指标。
- **仅指标矩阵**：只作为 `指标矩阵` 的一行出现，没有独立图表面板、独立坐标轴或独立指标族页面。矩阵行仍是一条可视化曲线，但不能称为专用图。
- **未接入历史视图**：模板保留 JS 渲染函数或 Go 类型，但 `ReportViewsV2` 没有字段、`buildReportViewsV2` 没有 builder，生产报告 JSON 中不会产生该 key。
- **明确不可用**：采集端或报告明确给出 `unavailable` / `degraded` 原因，或本机字段不存在；不补零、不用推断值代替。
- **未采样**：采集入口存在，但这两份真实报告没有该来源。不能把“本报告没有”说成“不可用”。

## 3. 报告图表区块清单

当前模板有 14 个图表区块（不含 `notesPanel` 告警/边界区）；`viewsV2Panel` 内再展开 4 个 host 视图，按子视图展开可视为 17 种视图/图。指标相关只有 `metricsPanel`。

| # | 区块 / 子视图 | 数据来源 | 与指标的关系 | 两份真实报告 |
|---:|---|---|---|---|
| 1 | `viewsV2Panel` / `load_latency` | `sweep.json` + 每档 run 的 `metrics.jsonl`（其中使用 `npu_aicore_utilization_ratio` 的均值） | 唯一会消费 NPU 指标的 host 视图；需要至少 4 档并发 | 不可用：无 `sweep.json` |
| 2 | `viewsV2Panel` / `off_cpu` | `spans.jsonl` / `offcpu.jsonl` | 无指标依赖 | 不可用：无 spans |
| 3 | `viewsV2Panel` / `operator_treemap` | host/device span | 无指标依赖 | 不可用：无 span/device |
| 4 | `viewsV2Panel` / `service_topology` | `otel.jsonl` 或 host span | 无指标依赖 | 不可用：无 otel/spans |
| 5 | `flamegraphPanel` / 内核栈火焰图 | `stacks.folded` | 无指标依赖 | 不显示 |
| 6 | `timelinePanel` / 线程时间轴 | spans | 无指标依赖 | 被 metrics-only 隐藏 |
| 7 | 延迟散点 | spans | 无指标依赖 | 隐藏 |
| 8 | 时间热力图 | spans | 无指标依赖 | 隐藏 |
| 9 | TopN 函数 | spans | 无指标依赖 | 隐藏 |
| 10 | 延迟热图 | spans | 无指标依赖 | 隐藏 |
| 11 | 次秒偏移热图 | spans | 无指标依赖 | 隐藏 |
| 12 | 函数分位带 | spans | 无指标依赖 | 隐藏 |
| 13 | 尾 cohort 对比 | spans + off-CPU/futex | 无指标依赖 | 隐藏 |
| 14 | `metricsPanel` / `指标矩阵` | `metrics.jsonl` 的全部 metric series | 所有指标组均在此以一行 sparkline 呈现；不是专用图 | **显示** |
| 15 | `metricsPanel` / `指标统计` | `DATA.metrics.stats` | 设备热力图，仅 4 个 DCMI 指标；不是全量统计表 | **显示**（metrics-full 为 9 个设备行 × 4 个 DCMI 指标；driver 报告为 3 个设备行 × 4 个指标列，全部显示 `--`） |
| 16 | 延迟 CDF | spans | 无指标依赖 | 隐藏 |
| 17 | 设备四象限 / 设备忙率 | device.jsonl + spans | 无指标依赖 | 隐藏 |

说明：上表按 `template.html` 的 `section` / `viewsV2Panel` 子视图展开计数；`notesPanel` 不是图表，未计入 14 个图表区块。两份真实报告都是 metrics-only，`body.metrics-only .trace-only` 隐藏了 host 视图和 span/device 图，因此实际可见的只有 `metricsPanel`。

## 4. 实际报告计数与嵌入规模

| 字段 / 项目 | `metrics-full-20261127T184802Z` | `driver-fix-20261128T190100Z` |
|---|---:|---:|
| `metrics.samples` | 49,319 | 29,247 |
| `metrics.meta_series` | 1,413 | 3,582 |
| `metrics.projected_series` | 1,429 | 4,600 |
| `metrics.filtered_series` | 0 | 1 |
| `metrics.eligible_series` | 1,429 | 4,599 |
| `metrics.rendered_series` | 1,429 | 4,599 |
| `metrics.series_limit` | 1,500 | 5,000 |
| 实际嵌入 `metrics.series` | 1,429 | 4,599 |
| `metrics.stats` | 1,429 | 4,599 |
| 矩阵组数（按 `source + name + unit + kind`） | 147 | 55 |
| 来源 series 数 | `dcmi` 304、`npu-smi` 504、`proc` 16、`prometheus:orinfer` 33、`system` 572 | `driver-ebpf` 4,583、`proc` 16 |
| `views_v2` | 4 个全部 `available=false`（无 sweep/spans/otel） | 4 个全部 `available=false`（无 sweep/spans/otel） |
| 火焰图 | 不显示（无 stacks） | 不显示（无 stacks） |
| 报告体积 | 1,943,115 bytes | 6,318,301 bytes |

这两份报告都使用了较大的 `--max-series`（1,500 / 5,000），所以实际报告嵌入了全部可绘制 series。普通 `cann-ebpf report` 的 `--max-series` 默认值是 256；默认命令会产生截断，不能直接声称“全部指标都在报告里”。

## 5. 指标组覆盖明细

### 5.1 DCMI（NPU）

采集入口：

- `cann-ebpf sample --out <dir> --dcmi-ext`（扩展；基础为 `--dcmi`）
- `cann-ebpf attach --metrics --metrics-npu-backend dcmi --metrics-dcmi-ext`（基础只加 `--metrics-npu-backend dcmi`；`both` 可同时启用 DCMI 与 npu-smi）
- 前置条件：`dcmi_probe` 在 `PATH` 中，或用 `CANN_DCMI_PROBE` 指向可执行文件。

报告落点：

- **已接入生产报告（专用 + 矩阵）**：`npu_aicore_utilization_ratio`、`npu_hbm_used_ratio`、`npu_power_watts`、`npu_temperature_celsius`。这 4 个是 `指标统计` 设备热力图的列（实际 metrics-full 为 host + NPU-0..NPU-7 共 9 个设备行），同时也在 `指标矩阵` 中。
- **仅指标矩阵（34 个）**：`npu_aicore_clock_max_mhz`、`npu_aicore_clock_mhz`、`npu_aicore_clock_ratio`、`npu_aicpu_utilization_ratio`、`npu_aivector_utilization_ratio`、`npu_ctrlcpu_clock_mhz`、`npu_ctrlcpu_utilization_ratio`、`npu_hbm_bandwidth_util_ratio`、`npu_hbm_clock_mhz`、`npu_hbm_ecc_double_bit_errors`、`npu_hbm_ecc_double_bit_isolated_pages`、`npu_hbm_ecc_double_bit_next_isolated_pages`、`npu_hbm_ecc_double_bit_total`、`npu_hbm_ecc_enabled`、`npu_hbm_ecc_single_bit_errors`、`npu_hbm_ecc_single_bit_isolated_pages`、`npu_hbm_ecc_single_bit_next_isolated_pages`、`npu_hbm_ecc_single_bit_total`、`npu_hbm_free_bytes`、`npu_hbm_temperature_celsius`、`npu_hbm_total_bytes`、`npu_hbm_used_bytes`、`npu_health`、`npu_health_error_count`、`npu_ndie_temperature_celsius`、`npu_pcie_ecrc_errors_total`、`npu_pcie_lcrc_errors_total`、`npu_pcie_retry_total`、`npu_pcie_rx_errors_total`、`npu_pcie_tx_errors_total`、`npu_sfp_temperature_celsius`、`npu_soc_temperature_celsius`、`npu_utilization_ratio`、`npu_voltage_volts`。
- **文档列出但本报告未产出**：`npu_memory_used_bytes`。代码只在 DCMI JSON 提供 `MemoryUsedMB` 时输出；本次 `metrics-full` 的 `metrics.jsonl` 没有该指标，不能把它当作 0。
- **明确不可用（本机）**：`npu_vectorcore_clock_mhz` 在 910B2 / 25.2.1 返回 `-8255`，按 `docs/DCMI-HCCS-METRICS.md` 约定省略，不输出 0。

### 5.2 HCCS（npu-smi）

采集入口：

- `cann-ebpf sample --out <dir> --hccs --hccs-interval 30s`
- `cann-ebpf attach --metrics --metrics-hccs --metrics-hccs-interval 30s`

报告落点：**仅指标矩阵**，没有 HCCS 专用图。9 个组为：
`npu_hccs_tx_bytes_total`、`npu_hccs_rx_bytes_total`、`npu_hccs_tx_packets_total`、`npu_hccs_rx_packets_total`、`npu_hccs_retry_total`、`npu_hccs_error_total`、`npu_hccs_link_speed`、`npu_hccs_lane_mode`、`npu_hccs_healthy`。

本报告实测每个组 56 series（8 卡 × 7 lane）；counter 由报告端做差分，`link_speed` / `lane_mode` 保持驱动原始值。没有绝对 GB/s，也没有按 lane 的专用图。

### 5.3 进程指标（proc）

采集入口：

- `cann-ebpf sample --out <dir> --pid <pid>`
- `cann-ebpf attach --metrics --metrics-proc`（目标 PID 由 `--pid` 或 discovery 决定）

报告落点：**仅指标矩阵**，16 个组：
`proc_cpu_user_seconds`、`proc_cpu_system_seconds`、`proc_rss_bytes`、`proc_threads`、`proc_sched_run_delay_seconds`、`proc_sched_running_seconds`、`proc_sched_timeslices`、`proc_io_read_bytes`、`proc_io_write_bytes`、`proc_fds`、`proc_vm_size_bytes`、`proc_vm_rss_bytes`、`proc_context_switches_voluntary_total`、`proc_context_switches_nonvoluntary_total`、`proc_page_faults_major_total`、`proc_page_faults_minor_total`。

### 5.4 主机系统指标（system）

采集入口：

- `cann-ebpf sample --out <dir> --system`
- `cann-ebpf attach --metrics --metrics-system`

报告落点：**仅指标矩阵**，本报告 51 个组，包括：

- CPU/负载：`node_cpu_seconds_total`、`node_load1`、`node_load5`、`node_load15`、`node_runnable_tasks`、`node_total_tasks`
- 内存：`node_memory_mem_total_bytes`、`node_memory_mem_free_bytes`、`node_memory_mem_available_bytes`、`node_memory_buffers_bytes`、`node_memory_cached_bytes`、`node_memory_dirty_bytes`、`node_memory_writeback_bytes`、`node_memory_slab_bytes`、`node_memory_swap_total_bytes`、`node_memory_swap_free_bytes`
- PSI：`node_pressure_cpu_{some,full}_{avg10,avg60,avg300}`、`node_pressure_memory_{some,full}_{avg10,avg60,avg300}`、`node_pressure_io_{some,full}_{avg10,avg60,avg300}` 及对应 `_total_seconds`
- 磁盘：`node_disk_read_bytes_total`、`node_disk_written_bytes_total`、`node_disk_reads_completed_total`、`node_disk_writes_completed_total`、`node_disk_io_time_seconds_total`
- 网络：`node_network_{receive,transmit}_{bytes,errs,drop}_total`

这些组全部只有矩阵；没有主机/温度/网络专用图，也没有 `npu` 与主机指标的联动图。

### 5.5 Prometheus / Orinfer

采集入口：

- `cann-ebpf sample --out <dir> --prometheus 'orinfer=http://127.0.0.1:8000/metrics'`
- `cann-ebpf attach --metrics --prometheus 'orinfer=http://127.0.0.1:8000/metrics'`
- 可选 `--include` / `--prometheus-include` 做指标名过滤。

报告落点：**仅指标矩阵**，本报告 33 个组：

- 运行状态：`orinfer_requests_running`、`orinfer_requests_waiting`
- token/吞吐计数器：`orinfer_prompt_tokens_total`、`orinfer_generated_tokens_total`、`orinfer_cached_prompt_tokens_total`
- 计时计数器：`orinfer_admission_seconds_total`、`orinfer_batch_plan_seconds_total`、`orinfer_compute_seconds_total`、`orinfer_prefix_restore_seconds_total`
- 请求分位：`orinfer_request_decode_seconds:p50/p95/p99`、`orinfer_request_finalize_seconds:p50/p95/p99`、`orinfer_request_latency_seconds:p50/p95/p99`、`orinfer_request_prefill_seconds:p50/p95/p99`、`orinfer_request_prefix_restore_seconds:p50/p95/p99`、`orinfer_request_prefix_store_seconds:p50/p95/p99`、`orinfer_request_queue_seconds:p50/p95/p99`、`orinfer_request_time_to_first_token_seconds:p50/p95/p99`

没有 vLLM/Orinfer 专用的 TTFT 面板、KV cache 面板或请求瀑布图；只有矩阵行和点击后的组内曲线。

### 5.6 cgroup v2

采集入口：

- `cann-ebpf sample --out <dir> --cgroup <cgroup-path>`
- `cann-ebpf attach --metrics --metrics-cgroup <cgroup-path>`

报告落点：**仅指标矩阵（本报告未采样）**。核心组包括：
`cgroup_cpu_usage_seconds`、`cgroup_cpu_user_seconds`、`cgroup_cpu_system_seconds`、`cgroup_cpu_nr_periods`、`cgroup_cpu_nr_throttled`、`cgroup_cpu_throttled_seconds`、`cgroup_memory_current_bytes`、`cgroup_memory_max_bytes`、`cgroup_memory_peak_bytes`、`cgroup_memory_events_total`、`cgroup_pids_current`、`cgroup_pids_max`。

另有 24 个 PSI 组（3 类 pressure × some/full × avg10/avg60/avg300/total）：
`cgroup_cpu_pressure_*`、`cgroup_memory_pressure_*`、`cgroup_io_pressure_*`。两份真实报告都没有 cgroup 数据和 cgroup 图。

### 5.7 NPU 驱动层（driver-ebpf）

采集入口：

- `cann-ebpf sample --out <dir> --driver --pid <pid>`
- `cann-ebpf attach --metrics --metrics-driver`（目标 PID 由 `--pid` 或 discovery 决定；需要 root/CAP_BPF 等权限）

报告落点：**仅指标矩阵**。本报告 `driver-ebpf` 39 个组、4,583 series，另有 `proc` 16 个组。没有任何驱动专用面板、热力图或调用链图。

| 驱动指标组 | 状态 | 说明 |
|---|---|---|
| `npu_ioctl_total` | 仅指标矩阵（部分可用） | `drv_ascend_intf_ioctl`、`hdcdrv_ioctl`、`trs_ioctl` 可用；`devmm_svm_ioctl` 因两个模块同名 `ambiguous_kernel_symbol`，该 handler 不可用 |
| `npu_ioctl_latency_ns:p50/p95/p99` | 仅指标矩阵（部分可用） | 同上 |
| `npu_task_submit_total`、`npu_task_wait_ns:p50/p95/p99` | 仅指标矩阵 | `trs_ioctl` 路径可用；只代表该 ioctl 同步路径，不是设备执行时间 |
| `npu_task_doorbell_total`、`npu_task_done_total` | 仅指标矩阵 | 可用 |
| `npu_dma_map_latency_ns:p50/p95/p99`、`npu_dma_map_ops_total` | 仅指标矩阵（部分可用） | `devdrv_dma_map_page/single`、`iommu_dma_map_page` 可用；`dma_map_page_attrs` attach 失败 |
| `npu_dma_unmap_latency_ns:p50/p95/p99`、`npu_dma_unmap_ops_total` | 仅指标矩阵（部分可用） | `devdrv_dma_unmap_page`、`iommu_dma_unmap_page` 可用；`dma_unmap_page_attrs` attach 失败 |
| `npu_dma_map_bytes`、`npu_dma_unmap_bytes` | 仅指标矩阵（部分可用） | 只有 `iommu_dma_map_page` / `iommu_dma_unmap_page` 的 byte 可用；厂商 `devdrv_*` 为 `size_signature_unverified` |
| `npu_iova_alloc_free`、`npu_iova_latency_ns:p50/p95/p99` | 仅指标矩阵 | `alloc_iova`、`alloc_iova_fast`、`free_iova`、`free_iova_fast`、`iommu_dma_alloc_iova` 可用 |
| `npu_hdc_send_total`、`npu_hdc_recv_total`、`npu_hdc_submit_total`、`npu_hdc_latency_ns:p50/p95/p99`、`npu_hdc_alloc_ops_total` | 仅指标矩阵 | HDC 路径可用；`npu_hdc_alloc_bytes` 见下 |
| `npu_hdc_alloc_bytes` | 明确不可用 | `size_signature_unverified` |
| `npu_mmap_bytes`、`npu_mmap_ops_total` | 仅指标矩阵 | `sys_enter_mmap` tracepoint 可用，按 NPU fd 解析设备 |
| `npu_driver_bpf_count_drops_total`、`npu_driver_bpf_entries_total`、`npu_driver_bpf_exits_total`、`npu_driver_bpf_hist_drops_total`、`npu_driver_bpf_missing_inflight_total`、`npu_driver_bpf_pending_drops_total`、`npu_driver_bpf_update_errors_total` | 仅指标矩阵（内部诊断） | eBPF 自身丢事件/更新错误等自监控计数，不是 AI 性能指标 |
| `npu_driver_metric_status` | 仅指标矩阵（状态元数据） | 71 条 status series；是判断其他驱动指标是否可用的依据，不应画成性能结论 |
| `npu_driver_source_info` | 明确不可用 / 报告不显示 | 名称以 `_info` 结尾，被 `isContextOnlyMetric` 过滤；原始 `metrics.jsonl`/`metrics.meta.json` 有 `status=degraded`、`probes_attached=24`、`probes_failed=9`，HTML 不展示 |
| `npu_svm_alloc_bytes`、`npu_svm_free_bytes`、`npu_svm_alloc_ops_total`、`npu_svm_free_ops_total` | 明确不可用 | `devmm_svm_ioctl` / `devmm_svm_mmap` 同名歧义或 `devmm_svm_munmap` 缺失；不猜模块 |
| `npu_xsmem_alloc_bytes`、`npu_xsmem_free_bytes`、`npu_xsmem_alloc_ops_total`、`npu_xsmem_free_ops_total`、`npu_xsmem_pool_alloc_bytes`、`npu_xsmem_pool_free_bytes`、`npu_xsmem_pool_ops_total` | 明确不可用 | `/proc/kallsyms` 无对应符号，`symbol_not_found`；不补零 |

驱动报告中的 1 条 `filtered_series` 就是 `npu_driver_source_info`（`_info` 过滤）。`npu_driver_metric_status` 的最新快照是 47 个 `available`、24 个 `unavailable`；这是“按 handler 统计”的结果，不等于 24 个指标全部不可用。

## 6. 未接入的历史六视图

以下六个视图**不是当前生产报告的一部分**：

| 视图 | 模板/类型 | 当前生产状态 | 缺什么 |
|---|---|---|---|
| `critical_path` | `v2RenderCriticalPath`、`CriticalPathData` | **未接入历史视图** | `ReportViewsV2` 没有字段；无 `device.jsonl` 时间线/关键路径 builder |
| `summed_delay` | `v2RenderSummedDelay`、`SummedDelayData` | **未接入历史视图** | 依赖 critical path；无 builder |
| `slack_cdf` | `v2RenderSlackCDF`、`SlackCDFData` | **未接入历史视图** | 依赖 critical path；无 builder |
| `roofline` | `v2RenderRoofline`、`RooflineData` | **未接入历史视图** | `--ops` 路径虽被解析，但没有 builder 消费；无真实 `ops.jsonl` |
| `ab_diff` | `v2RenderABDiff`、`ABDiffData` | **未接入历史视图** | `--ab-a/--ab-b` 虽被解析到 `Options`，但没有 builder；无 `ReportViewsV2` 字段 |
| `change_point` | `v2RenderChangePoint`、`ChangePointData` | **未接入历史视图** | `--events` 路径虽被解析，但没有 builder；无至少 5 分钟稳定序列 |

证据：`internal/report/views_v2.go` 的 `ReportViewsV2` 只有 `LoadLatency`、`OffCPU`、`OperatorTreemap`、`ServiceTopology` 四个字段；`internal/report/build_views_v2.go` 的 `buildReportViewsV2` 也只构建这四个。模板 `VIEWS_V2_LABELS` 只有 `off_cpu`、`operator_treemap`、`service_topology`，加上 `load_latency` 共 4 个；但 `template.html` 仍保留上述六个 `v2Render*` 函数，属于“前端代码在、生产数据契约不在”。

`docs/PRODUCTION-SCOPE.md` 也把 `critical_path`、`summed_delay`、`slack_cdf`、`roofline`、`ab_diff`、`change_point` 列为“明确不支持”，与当前代码的 `ReportViewsV2` 四字段一致。

## 7. 文档与实现不一致（需要修正口径）

1. `docs/METRICS-CATALOG.md` 的“报告统计层”写“报告 `指标统计` 表显示每个 series 的当前值、均值、p95、最大值和 counter 速率”。实际 `template.html` 的 `renderMetricsStats()` 会用 `metrics.stats` 只生成 **4 个 DCMI 指标 × 设备列** 的热力图；静态 `metrics-stats-table` 虽然留在 HTML 中，但没有 JS 填充。逐 series 的 `current/mean/p95/max/rate` 只存在于 `DATA.metrics.stats` JSON，并未作为表格展示。
2. `docs/DRIVER-METRICS.md` 第 4 节要求输出 `npu_driver_source_info`，实际原始 `metrics.jsonl`/`metrics.meta.json` 有该点，但报告层 `isContextOnlyMetric` 会过滤所有以 `_info` 结尾的指标；HTML 只能看到 `npu_driver_metric_status`，看不到 `probes_attached/probes_failed/status=degraded` 的汇总。若需要报告展示，必须单独保留 source 状态而不是走 `_info` 过滤。
3. `docs/METRICS-CATALOG.md` 把 `npu_memory_used_bytes` 列为 NPU 指标；本次 DCMI 真实 run 未输出该字段，代码仅在 `sample.MemoryUsedMB != nil` 时生成。文档应写明“字段存在才输出”，不能把它算作本机已验证指标。
4. `sample --npu-smi-ext` 有独立扩展采样入口；当前 `attach` 只有 `--metrics-npu-backend npu-smi` / `both`，没有 `--metrics-npu-smi-ext` 这个 attach 开关。`npu-smi-ext` 的扩展字段只能通过 `sample` 采，不能写成 attach 已支持。
5. `docs/REPORT-REVIEW-SERVER-LIVE.md` 中的“3/10 视图可用”是旧口径；当前模板导航写“生产视图（4）”，`views_v2` 只有 4 个 key。本文以当前代码和 2026-11-28 的报告为准。

## 8. 复现命令与 attach 参数

| 指标来源 | `sample` 命令 | `attach` 参数 | 备注 |
|---|---|---|---|
| DCMI 基础 | `cann-ebpf sample --out runs/dcmi --dcmi` | `--metrics --metrics-npu-backend dcmi` | `dcmi_probe` 必须在 PATH 或用 `CANN_DCMI_PROBE` |
| DCMI 扩展 | `cann-ebpf sample --out runs/dcmi-ext --dcmi-ext --dcmi-ext-interval 10s` | `--metrics --metrics-npu-backend dcmi --metrics-dcmi-ext --metrics-dcmi-ext-interval 10s` | 扩展包含基础字段，不要同时开两个 DCMI source |
| npu-smi 基础 | `cann-ebpf sample --out runs/npusmi --npu-smi` | `--metrics --metrics-npu-backend npu-smi` | 5s 最小间隔 |
| npu-smi 扩展 | `cann-ebpf sample --out runs/npusmi-ext --npu-smi-ext` | 无独立 attach 开关 | 30s 周期；attach 当前不暴露 `--metrics-npu-smi-ext` |
| HCCS 被动计数 | `cann-ebpf sample --out runs/hccs --hccs --hccs-interval 30s` | `--metrics --metrics-hccs --metrics-hccs-interval 30s` | 不执行 `hccs-bw`，不主动 profiling |
| 进程 | `cann-ebpf sample --out runs/proc --pid <pid>` | `--metrics --metrics-proc` | PID 需同时传给 `--pid` 或由 discovery 选中 |
| cgroup | `cann-ebpf sample --out runs/cgroup --cgroup <path>` | `--metrics --metrics-cgroup <path>` | cgroup v2 |
| 主机系统 | `cann-ebpf sample --out runs/system --system` | `--metrics --metrics-system` | procfs/sysfs |
| Prometheus | `cann-ebpf sample --out runs/prom --prometheus 'orinfer=http://127.0.0.1:8000/metrics'` | `--metrics --prometheus 'orinfer=http://127.0.0.1:8000/metrics'` | 过滤用 `--include` / `--prometheus-include` |
| NPU 驱动层 | `cann-ebpf sample --out runs/driver --driver --pid <pid>` | `--metrics --metrics-driver` | root/CAP_BPF；无符号时输出 unavailable |
| 报告生成 | `cann-ebpf report --metrics runs/<name>/metrics.jsonl --out runs/<name>/report.html --max-series 1500` | — | 默认 `--max-series 256`；实际全量报告分别使用 1500 / 5000 |

## 9. 证据路径

- `.analysis/metrics-full-20261127T184802Z/report.html`（1,943,115 bytes）
- `.analysis/metrics-full-20261127T184802Z/metrics.meta.json`（1,413 meta series）
- `.analysis/metrics-full-20261127T184802Z/metrics.jsonl`（49,319 points）
- `.analysis/driver-fix-20261128T190100Z/report.html`（6,318,301 bytes）
- `.analysis/driver-fix-20261128T190100Z/metrics.meta.json`（3,582 meta series；含 `npu_driver_source_info status=degraded, probes_attached=24, probes_failed=9`）
- `.analysis/driver-fix-20261128T190100Z/metrics.jsonl`（29,247 points）

## 10. 审计结论

- 不能把“指标矩阵有 147/55 个组”写成“有 147/55 张专用图”；矩阵是统一的 sparkline 列表，只有 4 个 DCMI 指标有设备热力图。
- 不能把 `npu_driver_source_info` 的 `degraded` 状态当作 HTML 已展示；它在报告中因 `_info` 被过滤，只能从原始 metrics/meta 读取。
- 不能把 `critical_path`/`summed_delay`/`slack_cdf`/`roofline`/`ab_diff`/`change_point` 写成当前生产视图；它们只是模板遗留渲染函数/类型，当前 `ReportViewsV2` 未接入。
- `npu_svm_*`、`npu_xsmem_*`、`npu_hdc_alloc_bytes` 以及部分 DMA byte 指标必须按 `npu_driver_metric_status` 的 `unavailable` / `size_signature_unverified` / `ambiguous_kernel_symbol` 原样处理，不能补零。
