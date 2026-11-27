# 指标与 hook 语义：DCMI / NPU-smi HCCS / driver-ebpf

本文说明报告里三个底层指标源各自“挂在哪里、代表什么、应该怎么画”。
报告中的指标矩阵按 source 分开渲染，不把 DCMI、HCCS、驱动 hook、进程和主机指标混在一张图里。

## 0. 先分清三层

| 层 | 数据源 | 采样方式 | 回答的问题 |
|---|---|---|---|
| 设备健康/利用率 | DCMI | `dcmi_probe_ext`，默认 10s | 卡是否降频、过热、功耗/利用率异常、ECC/PCIe 是否报错 |
| 卡间链路 | NPU-smi HCCS | `npu-smi info -t hccs`，默认 30s | 8 卡之间链路是否健康、retry/error 是否增长、tx/rx 是否倾斜 |
| 主机驱动路径 | driver-ebpf | kprobe/kretprobe + syscalls tracepoint，默认 1s | 用户态 CANN 调用进入驱动后，卡在 ioctl、HDC、任务门铃、DMA/IOVA、SVM/xsmem 的哪一段 |

host span（`attach` 的 `spans.jsonl`）和内核栈（`stacks.folded`）是另外两层，分别回答“CANN API 耗时”和“内核在跑什么”。指标不能替代 span，span 也不能替代指标。

## 1. DCMI：设备级 38 项

DCMI 是设备管理接口，不是算子 profiler。它能证明“设备状态”，不能证明“某个 kernel 的算力利用率”。

| 组 | 指标 | 怎么看 |
|---|---|---|
| 利用率 | `npu_aicore_utilization_ratio`、`npu_aivector_utilization_ratio`、`npu_aicpu_utilization_ratio`、`npu_ctrlcpu_utilization_ratio`、`npu_utilization_ratio` | 8 卡折线或热力图；长期低但请求排队，说明瓶颈不在算力 |
| 频率 | `npu_aicore_clock_mhz`、`npu_aicore_clock_max_mhz`、`npu_aicore_clock_ratio`、`npu_hbm_clock_mhz`、`npu_ctrlcpu_clock_mhz` | 与功耗/温度叠图；频率掉到 max 的 70% 以下先查降频 |
| HBM | `npu_hbm_used_bytes`、`npu_hbm_free_bytes`、`npu_hbm_total_bytes`、`npu_hbm_used_ratio`、`npu_hbm_bandwidth_util_ratio` | 使用率看水位，带宽利用率看是否被 HBM 限制；两者不是一回事 |
| 温度 | `npu_temperature_celsius`、`npu_hbm_temperature_celsius`、`npu_soc_temperature_celsius`、`npu_ndie_temperature_celsius`、`npu_sfp_temperature_celsius` | 看趋势和卡间差异；单卡明显偏高要查散热/风道 |
| 功耗/电压 | `npu_power_watts`、`npu_voltage_volts` | 与频率、利用率对齐；功耗墙会表现为频率下降 |
| ECC | `npu_hbm_ecc_single_bit_*`、`npu_hbm_ecc_double_bit_*`、`npu_hbm_ecc_enabled` | 错误计数是累计值；看是否增长，双比特错误优先处理 |
| PCIe | `npu_pcie_ecrc_errors_total`、`npu_pcie_lcrc_errors_total`、`npu_pcie_retry_total`、`npu_pcie_rx_errors_total`、`npu_pcie_tx_errors_total` | 累计 counter；非零且持续增长才是问题 |
| 健康 | `npu_health`、`npu_health_error_count` | 状态表/告警，不画折线 |

推荐图型：
- 8 卡 × 指标的热力图：适合温度、功耗、AICore、HBM 使用率，一眼看卡间差异。
- 每卡一条线的小倍数：适合频率、功耗、温度随时间变化。
- ECC/PCIe/health：状态表 + 非零标红，不做“平滑折线”。

## 2. NPU-smi HCCS：8 卡 × 7 lane

HCCS 是卡间互联。`npu-smi info -t hccs -i N -c 0` 返回 7 个 lane 的累计计数器。

| 指标 | 含义 | 画法 |
|---|---|---|
| `npu_hccs_healthy` | 链路健康状态 | 8 卡状态条/表 |
| `npu_hccs_lane_mode` | lane 模式（raw） | 表，不画趋势 |
| `npu_hccs_link_speed` | 链路速率（raw） | 表，和 lane mode 一起看 |
| `npu_hccs_tx_packets_total` / `rx_packets_total` | 累计包数 | 先转 Δ/s，再画 8×7 热力图或每卡折线 |
| `npu_hccs_tx_bytes_total` / `rx_bytes_total` | 累计字节 | 先转 Δ/s；和 HCCS 带宽上限对比 |
| `npu_hccs_retry_total` | 重传计数 | 应为 0；非 0 用红色状态块，不混在流量图里 |
| `npu_hccs_error_total` | 错误计数 | 应为 0；非 0 优先排查 |

推荐图型：
- lane 热力图：横轴 7 lane，纵轴 8 卡，颜色 = Δbytes/s；能看出流量是否只压在某几个 lane。
- retry/error 单独一张 8×7 状态图：0 为灰/绿，非 0 为红；不要用连续色阶把 0 和 1 的差别淹没。
- tx/rx 每卡折线：用于和 Orinfer 的 prefill/decode、HCCL span 对齐。

## 3. driver-ebpf：33 个 probe

驱动层用 kprobe/kretprobe 挂在驱动函数入口/返回，用 tracepoint 拿 ioctl/mmap 的 syscall 上下文。
每个 probe 产生两类数据：
- counter：调用次数、字节数，报告里自动转 Δ/s。
- histogram：延迟，单位 ns，报告 detail 里看 p50/p95/p99。

### 3.1 ioctl 入口

| ID | 符号 | group/kind | 含义 |
|---|---|---|---|
| 1 | `drv_ascend_intf_ioctl` | ioctl / davinci | Ascend 设备接口 ioctl，用户态命令进入驱动的总入口 |
| 2 | `devmm_svm_ioctl` | ioctl / svm | SVM 共享虚拟内存管理 ioctl |
| 4 | `hdcdrv_ioctl` | ioctl / hdc | HDC host-device 通信驱动 ioctl |
| 5 | `trs_ioctl` | ioctl / task | TRS 任务提交 ioctl |

看 `npu_ioctl_total` 的 handler/cmd 维度和 `npu_ioctl_latency_ns` 的分位。ioctl 慢不等于设备执行慢，只说明“命令在驱动入口排队/拷贝/等锁”。

### 3.2 HDC host-device 通道

| ID | 符号 | group/kind | 含义 |
|---|---|---|---|
| 6 | `hdcdrv_send` | hdc_send / hdc | HDC 发送 |
| 7 | `hdcdrv_recv` | hdc_recv / hdc | HDC 接收 |
| 8 | `hdcdrv_msg_chan_send` | hdc_send / hdc | HDC 消息通道发送 |
| 9 | `hdcdrv_get_w_sq_desc` | hdc_submit / hdc | 获取发送队列描述符 |
| 30 | `hdccom_alloc_mem` | hdc_alloc / hdc | HDC 通信内存分配 |
| 32 | `hdcdrv_copy_sq_desc_to_remote` | hdc_submit / hdc | 复制发送队列描述符到远端 |

看 `npu_hdc_send_total`、`npu_hdc_recv_total`、`npu_hdc_submit_total`、`npu_hdc_latency_ns`、`npu_hdc_alloc_ops_total`。
HDC 指标适合和 HCCS tx/rx 对齐：HDC 是 host-device 通道，HCCS 是 device-device 链路，不要把两者混为一条“通信延迟”。

### 3.3 任务提交/完成

| ID | 符号 | group/kind | 含义 |
|---|---|---|---|
| 10 | `devdrv_msg_ring_doorbell` | task_submit / task | 消息门铃 |
| 11 | `devdrv_set_sq_doorbell` | task_submit / task | 设置发送队列门铃 |
| 31 | `devdrv_dma_done_task` | task_done / task | DMA 任务完成 |

看 `npu_task_submit_total`、`npu_task_doorbell_total`、`npu_task_done_total`、`npu_task_wait_ns`。
submit 与 done 的速率差、`npu_task_wait_ns` 的 p95，是“任务在驱动里等了多久”的直接证据。

### 3.4 DMA / IOVA

| ID | 符号 | group/kind | 含义 |
|---|---|---|---|
| 12 | `devdrv_dma_map_page` | dma_map / dma_map | 映射一页 |
| 13 | `devdrv_dma_map_single` | dma_map / dma_map | 映射单段 |
| 14 | `dma_map_page_attrs` | dma_map / dma_map | 内核 DMA API 映射页 |
| 15 | `iommu_dma_map_page` | dma_map / dma_map | IOMMU 映射页 |
| 16 | `devdrv_dma_unmap_page` | dma_unmap / dma_unmap | 解除映射页 |
| 17 | `dma_unmap_page_attrs` | dma_unmap / dma_unmap | 内核 DMA API 解除映射 |
| 18 | `iommu_dma_unmap_page` | dma_unmap / dma_unmap | IOMMU 解除映射 |
| 19 | `alloc_iova` | iova_alloc / iova | 分配 IOVA |
| 20 | `alloc_iova_fast` | iova_alloc / iova | 快速分配 IOVA |
| 21 | `iommu_dma_alloc_iova` | iova_alloc / iova | IOMMU 分配 IOVA |
| 22 | `free_iova` | iova_free / iova | 释放 IOVA |
| 23 | `free_iova_fast` | iova_free / iova | 快速释放 IOVA |

看 ops/s、bytes/s、latency p95。频繁小映射/解除映射是首 token 抖动的常见来源；`bytes` 只在 size 参数已验证的探针上输出，未验证的显示 unavailable，不补 0。

### 3.5 SVM / xsmem / mmap

| ID | 符号 | group/kind | 含义 |
|---|---|---|---|
| 24 | `devmm_svm_mmap` | svm_alloc / svm | SVM 映射 |
| 25 | `devmm_svm_munmap` | svm_free / svm | SVM 解除映射 |
| 26 | `xsmem_alloc` | xsmem_alloc / xsmem | xsmem 分配 |
| 27 | `xsmem_free` | xsmem_free / xsmem | xsmem 释放 |
| 28 | `xsmem_pool_alloc` | xsmem_pool_alloc / xsmem_pool | xsmem 池分配 |
| 29 | `xsmem_pool_free` | xsmem_pool_free / xsmem_pool | xsmem 池释放 |
| 33 | `sys_enter_mmap` | mmap / mmap | 用户态 mmap 系统调用 |

看 `npu_mmap_bytes`、`npu_svm_*`、`npu_xsmem_*`。
当前 910B2 上部分符号不可用：`devmm_svm_ioctl` / `devmm_svm_mmap` 可能因同名模块歧义被跳过，`xsmem_*`、`devmm_svm_munmap` 可能不存在，`npu_hdc_alloc_bytes` 可能因 size 参数未验证而不输出。报告必须保留 `npu_driver_metric_status` 的 unavailable 原因，不能把缺失值当 0。

### 3.6 驱动内部健康

`npu_driver_bpf_entries_total`、`npu_driver_bpf_exits_total`、`npu_driver_bpf_count_drops_total`、`npu_driver_bpf_hist_drops_total`、`npu_driver_bpf_pending_drops_total`、`npu_driver_bpf_update_errors_total`、`npu_driver_bpf_missing_inflight_total` 是采集器自身的健康指标。
先看这些是否为 0，再解释业务指标；否则“没有数据”可能只是 BPF map 丢了。

## 4. 四类推荐图型

| 类别 | 默认图 | 交互 | 不要这样画 |
|---|---|---|---|
| DCMI | 8 卡热力图 + 每卡折线 | 点指标看 8 卡曲线 | 把 38 个指标塞成一张无分组折线 |
| HCCS | 8×7 lane 热力图 + retry/error 状态图 | 点 lane 看 tx/rx | 把 retry=0 和 tx bytes 放同一 Y 轴 |
| driver | 按 handler/group 的 topN 条形 + p50/p95/p99 点图 | 点 handler 看 cmd 明细 | 把 5123 条 series 画成一张 sparkline 墙 |
| 进程/Orinfer/主机 | 小倍数折线 | 与 span 时间轴对齐 | 和 NPU 指标混在同一张矩阵 |

## 5. 1 / 3 / 5 / 10 分钟窗口

| 窗口 | 适合看 | 注意 |
|---|---|---|
| 1 分钟 | 瞬时峰值、单个慢请求、hook 是否工作 | DCMI 只有 6 个点、HCCS 2 个点，不看趋势 |
| 3 分钟 | 短窗口抖动、一次 prefill/decode 周期 | DCMI 18 点、HCCS 6 点，可看粗趋势 |
| 5 分钟 | 队列积压、频率/温度变化 | DCMI 30 点、HCCS 10 点，适合和 Orinfer 队列对齐 |
| 10 分钟 | 降频、ECC/PCIe 增长、内存泄漏、HCCS retry | DCMI 60 点、HCCS 20 点；报告体积和解析时间明显增加 |

判读顺序：
1. 先看 `npu_driver_metric_status` 和 `npu_driver_bpf_*`，确认采集链路健康。
2. 再看 DCMI 的频率/温度/功耗/HBM，排除设备降频和资源水位。
3. 再看 HCCS retry/error 和 lane 流量倾斜。
4. 最后看 driver 的 ioctl/HDC/task/DMA 分位，和 host span、Orinfer 队列对齐，判断阻塞在用户态、驱动入口还是设备执行。
