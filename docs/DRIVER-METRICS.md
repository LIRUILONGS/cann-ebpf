# NPU 驱动层 eBPF 指标

日期：2026-11-28
实现：`internal/metrics/driver_ebpf_linux.go` + `internal/metrics/driver_tracepoint_linux.go` + `internal/metrics/bpf/driver_metrics.bpf.c`

## 1. 它回答什么问题

现有 host span 只能看到 CANN 用户态 API（`aclrtSynchronizeStream`、`HcclAllReduce` 等）的耗时。本数据源在 **CANN 用户态 API 与设备执行之间**补一层内核/驱动探针：

- CANN API 返回后，驱动里还卡了多久；
- 慢在 ioctl 提交、DMA map/unmap、IOVA 分配、HDC 发送/接收，还是任务提交；
- SVM / XSMEM 分配是否拖慢首 token；
- HDC/HCCS 是否存在排队或重传迹象（以驱动函数耗时和次数为证据，不把耗时直接等同于重传）。

## 2. 指标目录

所有指标都是 `cann.metrics.v1` 点，`source=driver-ebpf`，由 metrics collector 统一打 `mono_ns`/`wall_ns` 时间戳。

| 指标 | 类型 | 单位 | 含义 |
|---|---|---|---|
| `npu_ioctl_total` | counter | count | 按 `handler/cmd/device/pid/tid/cgroup/kind` 聚合的 CANN 驱动 ioctl 次数 |
| `npu_ioctl_latency_ns` | histogram | ns | 驱动 ioctl 入口到返回的耗时分布 |
| `npu_svm_alloc_bytes` | counter | bytes | `devmm_svm_mmap` 映射的 SVM 区间字节数（按 `vm_end-vm_start` 实测） |
| `npu_svm_free_bytes` | counter | bytes | `devmm_svm_munmap` 解除映射的字节数 |
| `npu_svm_alloc_ops_total` / `npu_svm_free_ops_total` | counter | count | SVM 映射/解除映射调用次数；即使 size 读不到也保留次数 |
| `npu_xsmem_alloc_bytes` / `npu_xsmem_free_bytes` | counter | bytes | `xsmem_alloc` / `xsmem_free` 的 size 参数累计 |
| `npu_xsmem_pool_alloc_bytes` / `npu_xsmem_pool_free_bytes` | counter | bytes | `xsmem_pool_alloc` / `xsmem_pool_free` 的 size 参数累计 |
| `npu_xsmem_pool_ops_total` | counter | count | XSMEM pool 操作次数，`op=alloc|free` |
| `npu_dma_map_latency_ns` | histogram | ns | `devdrv_dma_map_page`、`devdrv_dma_map_single`、`dma_map_page_attrs`、`iommu_dma_map_page` 的耗时 |
| `npu_dma_unmap_latency_ns` | histogram | ns | DMA unmap 路径耗时 |
| `npu_dma_map_bytes` / `npu_dma_unmap_bytes` | counter | bytes | DMA map/unmap 的 size 参数累计；只有参数签名已确认的 handler 才输出值，未确认时状态为 `unavailable` |
| `npu_iova_alloc_free` | counter | count | IOVA 分配/释放次数，`op=alloc|free` |
| `npu_iova_latency_ns` | histogram | ns | IOVA 分配/释放耗时 |
| `npu_hdc_send_total` / `npu_hdc_recv_total` | counter | count | `hdcdrv_send` / `hdcdrv_recv` 调用次数 |
| `npu_hdc_submit_total` | counter | count | `hdcdrv_get_w_sq_desc`、`hdcdrv_copy_sq_desc_to_remote` 提交侧次数 |
| `npu_hdc_latency_ns` | histogram | ns | HDC 发送/接收/提交/分配耗时，`op` 区分 |
| `npu_hdc_alloc_bytes` / `npu_hdc_alloc_ops_total` | counter | bytes/count | `hdccom_alloc_mem` 的分配次数；字节数在参数签名未确认前不输出 |
| `npu_task_submit_total` | counter | count | `trs_ioctl` 的任务提交次数（一次提交调用） |
| `npu_task_doorbell_total` | counter | count | `devdrv_msg_ring_doorbell` / `devdrv_set_sq_doorbell` 的底层门铃次数；与 `npu_task_submit_total` 不是同一层，不直接相加 |
| `npu_task_wait_ns` | histogram | ns | `trs_ioctl` 的同步等待耗时；只代表该 ioctl 路径，不代表设备内 kernel 执行时间 |
| `npu_task_done_total` | counter | count | `devdrv_dma_done_task` 完成处理次数 |
| `npu_mmap_bytes` / `npu_mmap_ops_total` | counter | bytes/count | `sys_enter_mmap` tracepoint 的映射长度与次数；用 mmap 的 fd 解析真实 NPU 设备，避免依赖可能不存在的 `devmm_svm_mmap` 符号 |

注意：同一个请求可能经过多个驱动层 handler（例如 `hdcdrv_msg_chan_send` 与更底层发送函数），不同 `handler` 的计数不能直接相加当作唯一请求数；应按 handler 看分层耗时和次数。

## 3. 标签与证据置信度

每个数据点至少包含：

- `handler`：真实 kprobe 符号名；
- `pid` / `tid`：来自 `bpf_get_current_pid_tgid()`；BPF map key 按 tgid/tid 聚合；
- `cgroup`：从 `/proc/<pid>/cgroup` 读取；读不到时退化为 `cgroup:<bpf_get_current_cgroup_id>`；
- `device`：优先根据 ioctl 的 fd 解析 `/proc/<pid>/fd/<fd>` 的 `/dev/davinciN|devmm_svm|hisi_hdc`；没有 fd 时按进程打开的 NPU 设备推导；仍无法确定时为 `unknown`；
- `confidence`：
  - `high`：ioctl 有真实 fd，且 fd 指向已知 NPU 设备；
  - `medium`：能拿到 handler/pid/tid 和时间，但设备只能按进程推导；
  - `low`：只有进程和时间，设备未知；
- `cmd`：只在 ioctl 指标里出现，来自 `sys_enter_ioctl` tracepoint 的 `cmd`；缺 tracepoint 时为 `unavailable`，不猜；
- `op`：HDC/IOVA/XSMEM pool 等子操作。
- `signature`：size 类指标的取值依据，例如 `arg3_size` 表示函数第 4 个参数是 size，`vma_start_end` 表示读取 VMA 的 `vm_start/vm_end`；`argN_size_unverified` 表示只采到了参数但尚未用目标机 BTF/头文件确认，该字节指标不会作为可用数据输出。

## 4. 不可用语义

没有内核/驱动符号、没有 BPF 权限、架构不匹配，或 size 参数签名未确认时，数据源不会返回零值冒充“没发生”。它会输出：

- `npu_driver_source_info{status="unavailable|degraded|available",reason="...",arch="..."}`；
- `npu_driver_metric_status{metric="...",handler="...",status="available|unavailable",reason="..."}`；未确认的字节指标使用 `reason="size_signature_unverified"`。

因此查询方必须同时检查 `npu_driver_metric_status`。没有 `status=available` 的指标不能用于结论。

## 5. 采集方式与最低要求

- 目标机：Linux/arm64（当前 Ascend 910B2 / kernel 5.10）；
- 权限：root 或 `CAP_BPF` + `CAP_PERFMON`（老内核为 `CAP_SYS_ADMIN`）；
- 依赖：目标机不需要 clang；BPF 对象已编译并嵌入二进制；
- 开关：
  - `cann-ebpf attach --metrics-driver ...`
  - `cann-ebpf sample --driver --pid <vllm-pid> ...`

## 6. 已知边界（必须写进报告）

1. `npu_ioctl_latency_ns` 是驱动入口到返回的 host 时间，不是 AI Core 执行时间；设备执行时间仍来自 msPTI/vLLM profiler。
2. `npu_task_wait_ns` 只覆盖 `trs_ioctl` 同步路径，不能代表所有任务等待。
3. `npu_svm_alloc_bytes` 统计的是 `devmm_svm_mmap` 的 VMA 长度；如果目标驱动把实际分配放在 ioctl 内部，还需要后续用驱动符号或 msPTI 校验。
4. `xsmem_*` 只在目标机存在对应符号时可用；当前内核的已采集 kallsyms 证据里尚未看到 `xsmem_alloc` 等符号，因此默认应为 `unavailable`；即使符号存在，未确认 size 参数前也只输出操作次数。
5. `device` 解析依赖进程 fd 仍存在；短生命周期 fd 可能退化为进程级设备或 `unknown`，此时置信度会明确降低。
6. ioctl `cmd` 依赖 `syscalls/sys_enter_ioctl` tracepoint；加载器会先读取 tracefs format 校验 `args[]`/`fd`/`cmd` 偏移，偏移不匹配时不挂 tracepoint，cmd 标记为 `unavailable`，但 handler 和 latency 仍可采集。
7. 直方图 bucket 上限为 100ms；超过 100ms 的样本只进入 `Count` 和 `Sum`，不进入 `Counts[15]`，这是预期的隐式 `+Inf` 语义，不是丢样本。
8. 本节的 `npu_hdc_*` 是 host↔device 的 HDC 驱动路径；HCCS 是卡间互联，不能把 HDC 计数冒充 HCCS。HCCS 的 passive retry/error/吞吐看 `npu-smi` HCCS source，本驱动源没有 HCCS 私有符号证据。

## 7. 当前机器的符号证据

来源：`.analysis/stacks-probe/classification/kallsyms-final4-exact-symbols.txt`（目标机 5.10.0-136.108.0.188.31.oe2203sp1.bclinux.aarch64，2026-11-27 采集）。

| 指标组 | 已见符号 | 判断 |
|---|---|---|
| ioctl | `drv_ascend_intf_ioctl`、`devmm_svm_ioctl`、`hdcdrv_ioctl`、`trs_ioctl` | 可实现；还需要 root/CAP_BPF 真机 attach 验证 |
| DMA map/unmap | `devdrv_dma_map_page`、`devdrv_dma_map_single`、`dma_map_page_attrs`、`iommu_dma_map_page`、`devdrv_dma_unmap_page`、`dma_unmap_page_attrs`、`iommu_dma_unmap_page` | 可实现；通用内核 API 的 size 参数已确认，厂商 `devdrv_*` 的字节数先标 `size_signature_unverified`，只输出次数/耗时 |
| IOVA | `alloc_iova`、`alloc_iova_fast`、`iommu_dma_alloc_iova` 已见；`free_iova*` 未在本次精确符号清单出现 | alloc 可先验证；free 分支可能 `unavailable` |
| HDC | `hdcdrv_send`、`hdcdrv_recv`、`hdcdrv_msg_chan_send`、`hdcdrv_get_w_sq_desc`、`hdcdrv_copy_sq_desc_to_remote`、`hdccom_alloc_mem` | 可实现；`hdccom_alloc_mem` 的 size 参数待验证，字节数暂不输出 |
| 任务提交 | `trs_ioctl`、`devdrv_msg_ring_doorbell`、`devdrv_set_sq_doorbell`、`devdrv_dma_done_task` | 可实现 |
| SVM | `devmm_svm_mmap` / `devmm_svm_munmap` 未在本次精确符号清单出现 | 需真机 `kallsyms` 复核；缺失时明确 `unavailable`，不补零 |
| XSMEM | 未在本次精确符号清单出现 | 当前应视为 `unavailable`，除非后续确认 `ascend_xsmem` 导出的函数名 |

## 8. 真机验证（2026-11-28）

目标机 bms-84865758 / 36.137.90.82，aarch64，kernel 5.10.0-136.108.0.188.31.oe2203sp1.bclinux.aarch64，root。验证对象 orinfer PID 2796550，只读采样 12 秒，没有重启、停止或压测服务。

- binary: /tmp/cann-ebpf-driverfix-20261128
- binary sha256: 16d1c2fc11c5eaa1b6d475805ce2db88aba32f5a52bf8a1a3873fd4714596d3a
- run: /tmp/cann-ebpf-runs/driver-fix-20261128T190100Z
- metrics points: 29247
- metrics sha256: a46f1829a90c5700144b4bcd4097101b429478a9db43d7c3b7e735e288a0be8c
- sources: driver-ebpf, proc

结果：

- collection 正常加载；npu_driver_source_info 为 status=degraded，probes_attached=24，probes_failed=9。
- npu_driver_metric_status 最新快照：47 个 available，24 个 unavailable。
- devmm_svm_ioctl、devmm_svm_mmap 同时在 drv_devmm_host 和 drv_devmm_host_agent 导出；未猜模块，相关指标标 ambiguous_kernel_symbol 并跳过。
- xsmem_alloc/free/pool_* 在当前 /proc/kallsyms 不存在，标 symbol_not_found；不补零。
- trs_ioctl 的 npu_ioctl_total、npu_ioctl_latency_ns、npu_task_submit_total、npu_task_wait_ns 可用。
- iommu_dma_map_page 的 map latency/count 可用；部分厂商 devdrv_* 的 byte 指标仍因 size 签名未确认标 size_signature_unverified。

边界：source_info=degraded 是预期降级，不是全量驱动采集成功；报告必须同时读取 npu_driver_metric_status。
