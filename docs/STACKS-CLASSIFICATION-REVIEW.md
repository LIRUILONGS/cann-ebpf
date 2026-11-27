# NPU 内核符号分类与证据复核（2026-11-27）

> 范围：只复核 `.analysis/stacks-probe` 中现有真实栈证据与服务器只读
> `/proc/kallsyms`、`/proc/modules`、`modinfo`；未采集、未压测、未接触 vLLM
> 业务内容、未修改任何证据文件。
>
> 关键结论：**旧报告里的 NPU 帧加权占比 0.86% 是未接入共享分类的旧口径；
> 接入 `stacks.ClassifyName` 后，同一份 final4 原始 folded 的新交付口径为
> 4.61%（630/13658）。** 旧口径把 `devmm_*`、`trs_*`、`hdcdrv_*`、`hdccom_*`、
> `pcivnic_*` 等整类 NPU 驱动帧错误地留在 `other`，低报约 5.4 倍。
>
> **分类准确性判定：旧规则不准确（P0 级漏分类已修正）；修正后的共享名称
> 规则与 kallsyms 前缀证据一致，4.61% 可作为交付口径。模块级仍有 114 个
> 加权帧（诊断下界 +0.83pp）因通用函数名落 `other`；加上 `el0_sync`
> fallback、`hisi_`/`drv_` 偏宽等 P1/P2 残余，详见第 6 节。**

## 1. 数据来源与总览

### 1.1 证据来源

| 优先级 | 证据 | 用途 | 说明 |
|---|---|---|---|
| 主证据 | `.analysis/stacks-probe/final4/stacks.folded` + `stacks.meta.json` | 全部最终数字 | 2026-11-27 10.034801594 s 真实采样，原始 folded |
| 主证据交叉核对 | `.analysis/stacks-probe/final4/report-stacks-final4-classified.html` | 新交付口径 | 2026-11-27T11:40:00Z 生成，`npu_frame_pct=4.612681212476204` |
| 旧口径对照 | `.analysis/stacks-probe/final4/report-stacks-final4.html` | 旧报告 0.86% | 2026-11-27T11:21:29Z 生成，未接入共享分类 |
| 辅助证据 | `.analysis/stacks-probe/report-stacks-live-final2.html` 内嵌 JSON | 校验 8.10% 结论 | 不是原始 folded；由 HTML `DATA.flamegraph.tree` 重建，见 1.3 |
| 排除 | `.analysis/stacks-probe/stacks.folded` + `stacks.meta.json` | 不使用 | 旧 3 秒探针：samples=62、unique=30、stack_fail=484、target_pids 只有 3081988 |

主证据 meta：host `bms-84865758`，kernel
`5.10.0-136.108.0.188.31.oe2203sp1.bclinux.aarch64`，frequency 99 Hz，
duration 10.034801594 s，target_pids 10 个，samples=1488，unique_stacks=221，
stack_fail=6（`-17` EEXIST），filtered=0，lost=0，symbol_coverage=1，
unknown_pct=0，kallsyms SHA256
`e2393f6308f6554c1d2c654e7c00bc4d5b7fc9b9dd53beaca508419997a0f39d`。

### 1.2 总量

| 指标 | 数值 | 计算方式 |
|---|---:|---|
| 总样本 | 1488 | `sum(line.count)` |
| 唯一栈 | 221 | folded 行数 |
| 唯一帧名 | 274 | 所有栈按 `;` 拆分后的 distinct frame name |
| 帧总数（不加权） | 2272 | `sum(len(stack.frames))`，每条唯一栈计一次 |
| 加权帧数 | 13658 | `sum(len(stack.frames) * stack.count)` |
| 最大栈深 | 30 | 单条栈的最大帧数 |
| 未知帧 | 0 | `[unknown]` 计数 |

### 1.3 final2 辅助证据重建校验

`report-stacks-live-final2.html` 的 `DATA.flamegraph.tree` 被重建成
`.analysis/stacks-probe/classification/final2-reconstructed.folded`，校验结果：

| 指标 | HTML 内嵌值 | 重建值 |
|---|---:|---:|
| samples | 1420 | 1420 |
| unique_stacks | 218 | 218 |
| total_frames | 13209 | 13209 |
| max_depth | — | 30 |

因此 final2 可用于“8.10%”这一结论的复核，但报告中必须注明其来源是 HTML
内嵌数据，不是原始 folded。

## 2. Top 30 帧（final4，按出现样本数）

“出现样本数”= 包含该帧的栈的 count 之和；“self 样本数”= 该帧为栈顶叶子的
栈的 count 之和。分类列中 `a->b` 表示本次 YAML/共享规则修正前后变化。

| # | 帧 | 出现样本数 | self 样本数 | 分类（改前->改后） |
|---:|---|---:|---:|---|
| 1 | `el0_sync_handler` | 1402 | 1 | other |
| 2 | `el0_sync` | 1401 | 0 | other |
| 3 | `do_el0_svc` | 1393 | 3 | other |
| 4 | `el0_svc` | 1391 | 1 | other |
| 5 | `el0_svc_common.constprop.0` | 1384 | 188 | other |
| 6 | `invoke_syscall` | 944 | 4 | other |
| 7 | `do_sched_yield` | 247 | 46 | other |
| 8 | `__arm64_sys_pselect6` | 246 | 1 | other |
| 9 | `__arm64_sys_sched_yield` | 246 | 1 | other |
| 10 | `__se_sys_pselect6` | 242 | 3 | other |
| 11 | `schedule` | 210 | 6 | sync |
| 12 | `__schedule` | 204 | 201 | sync |
| 13 | `core_sys_select` | 188 | 9 | other |
| 14 | `do_select` | 178 | 17 | other |
| 15 | `__arm64_sys_write` | 159 | 2 | other |
| 16 | `syscall_trace_enter` | 158 | 23 | other |
| 17 | `schedule_hrtimeout_range_clock` | 155 | 2 | sync |
| 18 | `ksys_write` | 155 | 1 | other |
| 19 | `schedule_hrtimeout_range` | 155 | 0 | sync |
| 20 | `hrtimer_start_range_ns` | 136 | 127 | other |
| 21 | `__arm64_sys_futex` | 121 | 0 | other |
| 22 | `do_futex` | 119 | 1 | other |
| 23 | `vfs_write` | 112 | 11 | vfs |
| 24 | `__arm64_sys_ioctl` | 107 | 0 | other |
| 25 | `drv_ascend_intf_ioctl` | 101 | 0 | npu |
| 26 | `futex_wake` | 94 | 20 | sync |
| 27 | `__secure_computing` | 90 | 12 | other |
| 28 | `syscall_trace_exit` | 84 | 14 | other |
| 29 | `__seccomp_filter` | 80 | 32 | other |
| 30 | `trs_ioctl` | 80 | 0 | other->npu |

完整机器可读表：`.analysis/stacks-probe/classification/final4-top30.csv`。

## 3. 分类分布（按 `classify.go` 规则，final4）

统计口径：按 `stacks.ClassifyName(name, module, rules)` 对每个帧分类；module 来自
服务器 `/proc/kallsyms` 精确符号映射。帧数按“加权帧出现次数”统计；栈样本按
“至少包含一个该类别帧的唯一栈 count 之和”统计（同一栈同类别只计一次）。

| 类别 | 改前唯一帧 | 改前加权帧 | 改前帧占比 | 改前含该类栈样本 | 改前栈占比 | 改后唯一帧 | 改后加权帧 | 改后帧占比 | 改后含该类栈样本 | 改后栈占比 |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| npu | 14 | 117 | 0.8566% | 105 | 7.056% | 81 | 630 | 4.6127% | 108 | 7.258% |
| dma | 8 | 19 | 0.1391% | 6 | 0.403% | 8 | 19 | 0.1391% | 6 | 0.403% |
| net | 10 | 21 | 0.1538% | 9 | 0.605% | 10 | 21 | 0.1538% | 9 | 0.605% |
| sync | 13 | 956 | 6.9996% | 518 | 34.812% | 13 | 956 | 6.9996% | 518 | 34.812% |
| vfs | 3 | 137 | 1.0031% | 137 | 9.207% | 3 | 137 | 1.0031% | 137 | 9.207% |
| block | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 |
| sched | 2 | 15 | 0.1098% | 15 | 1.008% | 2 | 15 | 0.1098% | 15 | 1.008% |
| sample | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 |
| other | 224 | 12393 | 90.738% | 1488 | 100% | 157 | 11880 | 86.982% | 1488 | 100% |
| unknown | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 |

机器可读表：`.analysis/stacks-probe/classification/final4-category-distribution.csv`。

说明：`classify.go` 本身没有独立的 `kernel` 类别，通用内核/系统调用帧归
`other`；`unknown` 只在报告层由 `isUnknownFrame` 判定，本窗口为 0。

说明：`other` 的“含该类栈样本”恒为 1488，因为每条栈都有系统调用/内核通用帧；
这不代表 `other` 的“时间占比”。`npu_frame_pct` 只统计帧加权占比，不把
`dma`/`net`/`sync` 混入 NPU。

报告字段口径提醒：HTML 的 `flamegraph.categories[].samples` 是“栈顶叶帧类别”
的样本数（旧 final4 报告 npu=3/1488=0.20%，新 classified 报告 npu=41/1488=2.76%），
不是 `npu_frame_pct` 的分子；`npu_frame_pct=4.6127%` 对应的是 630/13658 的
加权帧出现次数。两者不能混用，旧 UI 的 self% 显示问题也应以此解释。
该 4.61% 是**共享名称规则口径**；按模块归属还有 114 个 NPU 驱动帧因通用函数名
残留 `other`（P1，见 6 节），若计入则为 744/13658=5.45%，但交付口径仍以
`npu_frame_pct=4.6127%` 为准，5.45% 仅作诊断性下界。

## 4. NPU/驱动帧重点复核

### 4.1 前缀、符号量、模块与 final4 帧贡献

“final4 帧”= 按前缀归组的唯一帧数/出现样本数；同一符号若在多个模块同时存在，
按精确符号映射可能对应多个模块。

| 前缀 | kallsyms 符号数 | 其中 builtin | 模块归属（符号数） | final4 唯一帧/样本 | 改前->改后类别 |
|---|---:|---:|---|---:|---|
| `devdrv_` | 933 | 0 | `drv_pcie_host`:638；`drv_devmng_host`:289；`ascend_uda`:5；`ascend_trs_pm_adapt`:1 | 13 / 16 | npu -> npu |
| `drv_` | 125 | 2 | `drv_dvpp_cmdlist`:64；`drv_davinci_intf_host`:41；`drv_devmng_host`:12；`drv_pcie_host`:5；`ascend_queue`:1 | 1 / 101 | npu -> npu |
| `davinci` | 7 | 0 | `drv_vascend`:4；`drv_davinci_intf_host`:3 | 0 / 0 | npu |
| `hisi_` | 314 | 82 | `hisi_sas_main`:79；`hisi_uncore_ddrc_pmu`:46；`hisi_uncore_l3c_pmu`:36；`hisi_uncore_hha_pmu`:35；`hisi_uncore_pmu`:21；`hisi_sas_v3_hw`:15 | 0 / 0 | npu（过宽，见 P2） |
| `hccn` | 0 | 0 | 未加载/未找到 | 0 / 0 | net（未验证） |
| `svm_` | 16 | 0 | `drv_devmm_host`:8；`drv_devmm_host_agent`:7；`drv_devmng_host`:1 | 0 / 0 | npu |
| `ascend` | 9 | 4 | `drv_davinci_intf_host`:5 | 0 / 0 | npu |
| `npu` | 0 | 0 | 未找到 | 0 / 0 | 无 `npu` 前缀 |
| `hdc_` | 4 | 0 | `drv_pcie_hdc_host`:4 | 0 / 0 | npu |
| `hdcdrv_` | 370 | 0 | `drv_pcie_hdc_host`:370 | 12 / 27 | other -> npu |
| `hdccom_` | 7 | 0 | `drv_pcie_hdc_host`:7 | 1 / 1 | other -> npu |
| `devmm_` | 1421 | 0 | `drv_devmm_host`:1015；`drv_devmm_host_agent`:405；`drv_devmng_host`:1 | 27 / 164 | other -> npu |
| `trs_` | 820 | 0 | `ascend_trs_pm_adapt`:365；`ascend_trs_core`:264；`ascend_trs_nvme_chan`:74；`ascend_trs_sec_eh_agent`:40；`ascend_trs_id_allocator`:38；`ascend_trs_sub_stars`:35；`ascend_trs_shrid`:4 | 23 / 317 | other -> npu |
| `pcivnic_` | 76 | 0 | `drv_pcie_vnic_host`:76 | 4 / 4 | other -> npu |
| `dms_` | 447 | 0 | `ascend_dms_smf`:213；`drv_devmng_host`:176；`ascend_urd`:30；`ascend_dms_dtm`:25；`drv_soft_fault`:3 | 0 / 0（final2 2 帧） | other -> npu |
| `tsagent_` | 17 | 0 | `ts_agent`:17 | 0 / 0（final2 2 帧） | other -> npu |
| `vdavinci` | 28 | 0 | `drv_vascend`:28 | 0 / 0 | other -> npu |
| `dbl_` | 7 | 0 | `ascend_uda`:4；`dbl_runenv_config`:3 | 0 / 0 | other -> npu |
| `ka_` | 54 | 0 | `ascend_kernel_adapt`:54 | 0 / 0（final2 1 帧） | other -> npu |
| `dma_` | 225 | 216 | `ts_agent`:3；`libata`:2；`drv_vascend`:2；`vfio_iommu_type1`:1；`ts_debug`:1 | 2 / 6（`dma_map_page_attrs`、`dma_unmap_page_attrs`） | dma（不并入 npu） |
| `smmu_` | 48 | 48 | 全部 builtin | 0 / 0 | dma |
| `iommu_` | 157 | 157 | 全部 builtin | 5 / 12 | dma |

### 4.2 Top NPU 改前/改后（final4）

改前（旧口径）只有 14 个 NPU 帧：

| 帧 | 样本 | self |
|---|---:|---:|
| `drv_ascend_intf_ioctl` | 101 | 0 |
| `devdrv_dma_map_page` | 4 | 0 |
| `devdrv_dma_done_handle` | 1 | 0 |
| `devdrv_dma_done_task` | 1 | 0 |
| `devdrv_dma_done_task_proc` | 1 | 0 |
| `devdrv_dma_map_single` | 1 | 0 |
| `devdrv_dma_unmap_page` | 1 | 0 |
| `devdrv_get_msg_chan` | 1 | 0 |
| `devdrv_get_msg_chan_by_id` | 1 | 0 |
| `devdrv_get_msg_chan_host_cq_head` | 1 | 1 |
| `devdrv_get_msg_chan_slave_sq_tail` | 1 | 0 |
| `devdrv_msg_ring_doorbell` | 1 | 0 |
| `devdrv_pci_ctrl_get` | 1 | 1 |
| `devdrv_set_sq_doorbell` | 1 | 1 |

改后（新交付口径）Top 10：

| 帧 | 样本 | 占 1488 样本 | self |
|---|---:|---:|---:|
| `drv_ascend_intf_ioctl` | 101 | 6.79% | 0 |
| `trs_ioctl` | 80 | 5.38% | 0 |
| `trs_id_pool_get` | 36 | 2.42% | 0 |
| `trs_res_id_alloc` | 35 | 2.35% | 0 |
| `trs_res_id_free` | 29 | 1.95% | 0 |
| `trs_id_alloc_ex` | 28 | 1.88% | 0 |
| `devmm_svm_ioctl` | 18 | 1.21% | 1 |
| `trs_res_mng_id_free` | 18 | 1.21% | 0 |
| `devmm_dispatch_ioctl_use_svm_proc` | 17 | 1.14% | 0 |
| `trs_id_free_ex` | 17 | 1.14% | 0 |

新报告 `report-stacks-final4-classified.html` 的 Top NPU 与上述一致：
`drv_ascend_intf_ioctl` 101、`trs_ioctl` 80、`trs_id_pool_get` 36、
`trs_res_id_alloc` 35、`trs_res_id_free` 29、`trs_id_alloc_ex` 28、
`devmm_svm_ioctl` 18。完整 40 行 Top NPU 合计 586 个帧出现样本（Top NPU 表
有 40 行上限，不是全部 630）。

### 4.3 通用 DMA/SMMU/IOMMU 与 NPU 驱动的区分

- `dma_map_page_attrs`、`dma_unmap_page_attrs`、`iommu_dma_map_page`、
  `iommu_dma_unmap_page`、`iommu_dma_alloc_iova`、`arm_smmu_map`、
  `arm_smmu_unmap`、`iommu_map_atomic`、`iommu_unmap_fast` 的 module 均为
  `[builtin]`，属于通用内核 DMA/IOMMU，不是 NPU 驱动；保留在 `dma` 类。
- `devdrv_dma_map_page`、`devdrv_dma_map_single`、`devmm_pa_node_list_dma_map`
  等虽然函数名含 `dma`，但模块分别是 `drv_pcie_host`、`drv_devmm_host`，且
  前缀 `devdrv_`/`devmm_` 在 `dma_` 之前命中，归 `npu` 正确。
- `dma_*` 不能整体并入 NPU：kallsyms 显示 225 个 `dma_` 符号中 216 个是
  builtin，只有 2 个在 `drv_vascend`；把 `dma_*` 全算 NPU 会引入大量通用
  内核误分类。
- `smmu_` 48 个、`iommu_` 157 个符号全部 builtin；当前 `dma` 类正确。
  `__iommu_dma_map`、`__iommu_map`、`__iommu_unmap`、`alloc_iova`、
  `arm_lpae_*` 等以 `__`/`alloc_`/`arm_` 开头的通用 IOMMU helper 仍落
  `other`（见 P2），但不是 NPU。

### 4.4 final4 非 builtin 帧的模块分布

| 模块 | 唯一帧 | 出现样本 | self | 改前类别 | 改后仍 `other` 的样本 |
|---|---:|---:|---:|---|---:|
| `ascend_trs_core` | 12 | 285 | 18 | other | 94 |
| `drv_devmm_host` | 24 | 127 | 10 | other | 18 |
| `drv_davinci_intf_host` | 1 | 101 | 0 | npu | 0 |
| `ascend_trs_id_allocator` | 7 | 87 | 3 | other | 0 |
| `drv_devmm_host_agent` | 6 | 55 | 3 | other | 0 |
| `drv_pcie_hdc_host` | 15 | 30 | 3 | other | 2 |
| `ascend_trs_sub_stars` | 5 | 28 | 4 | other | 0 |
| `drv_pcie_host` | 13 | 16 | 3 | npu | 0 |
| `ascend_trs_pm_adapt` | 1 | 11 | 0 | other | 0 |
| `drv_pcie_vnic_host` | 4 | 4 | 0 | other | 0 |

（模块级样本会重复计入同时存在于 `drv_devmm_host` 与 `drv_devmm_host_agent`
的同名符号，故表中总和略大于 630，仅用于显示分布。）

## 5. 服务器只读 kallsyms 实证

> 合规说明：以下探测仅通过 SSH 执行 `grep`/`awk`/`cat`/`modinfo`，读取
> `/proc/kallsyms`、`/proc/modules` 与模块元数据；未在服务器写入任何文件
> （包括 `/tmp`）、未采集、未重启服务、未修改 sysctl。SSH 失败时本地重试
> 最多 5 次；本次探测全部成功返回。

### 5.1 已加载模块与描述（`/proc/modules` + `modinfo`）

| 模块 | 大小 | 描述/版本 |
|---|---:|---|
| `drv_pcie_host` | 696320 | `devdrv host pcie driver` |
| `drv_devmng_host` | 954368 | `DAVINCI driver` |
| `drv_devmm_host` | 966656 | `devmm shared memory manager driver` |
| `drv_devmm_host_agent` | 233472 | `devmm shared memory manager driver` |
| `drv_pcie_hdc_host` | 339968 | `hdcdrv host driver` |
| `drv_pcie_vnic_host` | 65536 | `pcivnic host driver` |
| `ascend_trs_core` | 290816 | `TRS CORE DRIVER` |
| `ascend_trs_id_allocator` | 380928 | `TRS ID ALLOCATOR` |
| `ascend_trs_sub_stars` | 90112 | `TRS STARS` |
| `ascend_trs_pm_adapt` | 802816 | `TRS HOST PM ADAPT DRVIER` |
| `ascend_trs_nvme_chan` | 131072 | `NVME CHAN DRIVER` |
| `ascend_dms_smf` | 262144 | `DAVINCI driver` |
| `ascend_dms_dtm` | 36864 | `DAVINCI driver` |
| `ascend_urd` | 40960 | `DAVINCI driver` |
| `ascend_kernel_adapt` | 36864 | `kernel adapt module` |
| `ascend_uda` | 114688 | `UDA DRIVER` |
| `ts_agent` | 4702208 | version `v0.1` |
| `dbl_runenv_config` | 20480 | `DAVINCI driver` |
| `drv_vascend` | 126976 | version `v0.1` |

### 5.2 前缀符号数量与模块归属

完整数据：`.analysis/stacks-probe/classification/kallsyms-probe.txt` 和
`kallsyms-prefix-modules.csv`。关键结果：

| 前缀 | 符号数 | 模块分布 |
|---|---:|---|
| `devdrv_` | 933 | `drv_pcie_host`:638、`drv_devmng_host`:289、`ascend_uda`:5、`ascend_trs_pm_adapt`:1 |
| `drv_` | 125 | `drv_dvpp_cmdlist`:64、`drv_davinci_intf_host`:41、`drv_devmng_host`:12、`drv_pcie_host`:5、builtin:2、`ascend_queue`:1 |
| `hisi_` | 314 | builtin:82、`hisi_sas_main`:79、`hisi_uncore_ddrc_pmu`:46、`hisi_uncore_l3c_pmu`:36、`hisi_uncore_hha_pmu`:35、`hisi_uncore_pmu`:21、`hisi_sas_v3_hw`:15；**没有 NPU 模块** |
| `hccn` | 0 | 未加载 |
| `svm_` | 16 | `drv_devmm_host`:8、`drv_devmm_host_agent`:7、`drv_devmng_host`:1 |
| `ascend` | 9 | `drv_davinci_intf_host`:5、builtin:4 |
| `npu` | 0 | 未找到 |
| `hdc_` | 4 | `drv_pcie_hdc_host`:4 |
| `hdcdrv_` | 370 | `drv_pcie_hdc_host`:370 |
| `hdccom_` | 7 | `drv_pcie_hdc_host`:7 |
| `vdavinci` | 28 | `drv_vascend`:28 |
| `pcivnic_` | 76 | `drv_pcie_vnic_host`:76 |
| `devmm_` | 1421 | `drv_devmm_host`:1015、`drv_devmm_host_agent`:405、`drv_devmng_host`:1 |
| `trs_` | 820 | `ascend_trs_pm_adapt`:365、`ascend_trs_core`:264、`ascend_trs_nvme_chan`:74、`ascend_trs_sec_eh_agent`:40、`ascend_trs_id_allocator`:38、`ascend_trs_sub_stars`:35、`ascend_trs_shrid`:4 |
| `dms_` | 447 | `ascend_dms_smf`:213、`drv_devmng_host`:176、`ascend_urd`:30、`ascend_dms_dtm`:25、`drv_soft_fault`:3 |
| `tsagent_` | 17 | `ts_agent`:17 |
| `dbl_` | 7 | `ascend_uda`:4、`dbl_runenv_config`:3 |
| `ka_` | 54 | `ascend_kernel_adapt`:54 |
| `dma_map` | 3 | builtin:3 |
| `dma_unmap` | 3 | builtin:3 |
| `dma_` | 225 | builtin:216、`ts_agent`:3、`libata`:2、`drv_vascend`:2、`vfio_iommu_type1`:1、`ts_debug`:1 |
| `smmu_` | 48 | builtin:48 |
| `iommu_` | 157 | builtin:157 |

### 5.3 代表性符号（精确匹配）

- `drv_davinci_intf_host`：`drv_ascend_intf_ioctl`（`drv_` 前缀）。
- `drv_pcie_host`：`devdrv_dma_map_page`、`devdrv_dma_map_single`、
  `devdrv_dma_unmap_page`、`devdrv_dma_done_handle`、`devdrv_get_msg_chan*`、
  `devdrv_msg_ring_doorbell`、`devdrv_pci_ctrl_get`、`devdrv_set_sq_doorbell`。
- `drv_devmm_host`/`drv_devmm_host_agent`：`devmm_svm_ioctl`、
  `devmm_ioctl_dispatch`、`devmm_ioctl_memcpy_proc`、`devmm_memcpy_proc`、
  `devmm_make_single_dma_blk_list*`、`devmm_pa_node_list_dma_map/unmap`。
- `ascend_trs_core`：`trs_ioctl`、`ioctl_trs_res_id_comm`、`trs_res_id_alloc/free`、
  `trs_core_ts_inst_get/put`、`trs_proc_del_res`。
- `ascend_trs_id_allocator`：`trs_id_pool_get/put/free/add_node`、`trs_id_alloc_ex`、
  `trs_id_free_ex`、`trs_id_param_check`。
- `ascend_trs_sub_stars`：`trs_stars_soc_res_ctrl`、`trs_stars_func_res_id_ctrl`、
  `trs_stars_func_event_id_check_and_reset`、`trs_stars_reset_event`。
- `drv_pcie_hdc_host`：`hdcdrv_ioctl`、`hdcdrv_ioctl_com`、`hdcdrv_operation`、
  `hdcdrv_send/recv`、`hdcdrv_msg_chan_send`、`hdccom_alloc_mem`；另有
  `alloc_mem`、`free_mem` 两个通用名符号也在此模块（见 P1）。
- `drv_pcie_vnic_host`：`pcivnic_dma_map_single`、`pcivnic_forward_packet`、
  `pcivnic_pciedev_send`、`pcivnic_rx_msg_callback`。
- `ts_agent`：`tsagent_cqe_update`、`tsagent_set_sram_overflow_bit`、
  `tsagent_sqe_update`、`tsagent_device_init`。
- `drv_vascend`：`vdavinci_do_map`、`vdavinci_dma_map_page`、
  `vdavinci_dma_unmap_page`、`vdavinci_iommu_map`。
- `ascend_kernel_adapt`：`ka_kfree`、`ka_alloc_pages`、`ka_vzalloc`、
  `ka_pci_get_info`。

## 6. 漏分类/误分类清单

### P0（影响交付口径，必须修）

1. **`devmm_*` / `trs_*` / `hdcdrv_*` / `hdccom_*` / `pcivnic_*` 整类落入 `other`。**
   final4 中这五组分别贡献 27/23/12/1/4 个唯一帧、164/317/27/1/4 个加权帧，
   合计 **67 个唯一帧、513 个加权帧**，正是旧 NPU 口径 117 与修正后 630 的差值。
   模块实证分别落在 `drv_devmm_host(_agent)`、`ascend_trs_*`、
   `drv_pcie_hdc_host`、`drv_pcie_vnic_host`，均不是通用内核。
2. **旧报告 NPU 帧加权占比 0.86%（117/13658）是旧口径。**
   同一份 final4 folded 接入共享分类后为 **4.6127%（630/13658）**；
   旧口径低报 5.4 倍。`report-stacks-final4-classified.html` 已由主线程重新生成，
   本文以它作为新交付口径。

### P1（会误导排障，建议尽快修）

1. **`dms_`、`tsagent_`、`ka_` 在 final2 实际出现但仍被归 `other`。**
   final2 中 `dms_cmd_process`/`dms_ioctl` 各 2 样本、`tsagent_*` 2 样本、
   `ka_kfree` 1 样本；kallsyms 显示分别属于 `ascend_dms_*`/`ascend_urd`、
   `ts_agent`、`ascend_kernel_adapt`。已加入 YAML/DefaultSymbolRules。
2. **模块名匹配不完整，仍有 114 个加权帧残留在 `other`。**
   `ClassifyName` 只用 `devdrv`/`davinci`/`hisi` 三个子串做 module override，
   无法识别 `drv_pcie_hdc_host`、`drv_devmm_host`、`ascend_trs_*` 等模块。
   final4 中有 7 个帧（114 个加权帧，占 13658 的 0.8347%）位于 Ascend
   驱动模块却因函数名不含 NPU 前缀而落 `other`：`ioctl_trs_res_id_comm` 71
   （`ascend_trs_core`）、`_trs_res_id_free.part.0` 23（同）、
   `_devmm_ioctl_dispatch` 16（`drv_devmm_host`），以及 4 个各 1 样本的
   `_devmm_*`/`alloc_mem`/`free_mem`。因此 4.61%（630/13658）是
   名称-前缀口径的交付值，按模块归属的 NPU 驱动帧诊断下界为
   744/13658=5.45%。详见 `final4-npu-module-other-residual.csv`。
   正确修法是使用 Ascend 模块白名单或把 module/category 持久化到 folded
   sidecar，而不是给通用名加全局前缀。
3. **报告分类层与 YAML 仍需保持同步。** `internal/report/flamegraph.go`
   现在先调用 `stacks.ClassifyName(frame.Name, frame.Module, nil)`，因此报告
   使用 **DefaultSymbolRules**；`profiles/kernel-symbols.yaml` 必须与
   `DefaultSymbolRules` 完全一致，否则 `--symbols` 采集结果和重新生成报告会
   出现两套分类。`internal/stacks/classify_test.go` 已用
   `TestKernelSymbolsProfileMatchesDefaultRules` 锁定这一点。
4. **`el0_sync`/`el0_sync_handler` 仍会被报告 fallback 判成 `sync`。**
   `ClassifyName("el0_sync", "", nil)` 返回 `other`，随后
   `flamegraphFrameCategory` 的旧 fallback 因 `strings.Contains(text, "sync")`
   返回 `sync`。这会让类别 self 表中 `sync` 虚高（final4 旧/新报告均为
   344/1488=23.1%），不影响 `npu_frame_pct`，但会让“同步开销”看起来异常大。
   `__arm64_sys_ioctl` 仍是 `other`，没有被误判为 NPU，符合预期。

### P2（潜在风险/语义待定）

1. **`hisi_` 前缀过宽。** kallsyms 314 个 `hisi_` 符号中 82 个 builtin、
   79 个 `hisi_sas_main`、其余为 `hisi_uncore_*_pmu`/`hisi_sas_v3_hw`，
   没有任何 NPU 模块；当前 `hisi_` 归 NPU 且 `ClassifyName` 的 module
   子串 `hisi` 会把 `hisi_sas_main` 等也判 NPU。本窗口 0 个 `hisi_` 帧，
   暂无数字影响，但属于潜在误分类。
2. **`drv_` 含 2 个 builtin 通用符号**（`drv_attr_show`/`drv_attr_store`）；
   若未来出现在栈中会被误判 NPU。当前 final4 无这两个帧。
3. **`hccn_` 挂在 `net`，服务器 0 个 `hccn` 符号/模块未加载**；HCCN 属于
   Ascend 集合通信网络驱动，若未来加载，建议归 `npu` 或独立 `hccl/hccn`
   通信类，不应按通用 `net` 处理。
4. **`npu` 前缀不存在于 YAML。** kallsyms 0 个 `npu` 符号；当前没有证据支持
   添加，保留为待验证。
5. **通用 IOMMU helper 分类不一致。** `__iommu_dma_map`、`__iommu_map`、
   `__iommu_unmap`、`alloc_iova`、`alloc_iova_fast`、
   `__alloc_and_insert_iova_range`、`arm_lpae_*` 等为 builtin 通用内核，
   当前部分落 `other`（而不是 `dma`）；不影响 NPU 计数，但会污染
   `other`/`dma` 的对比。
6. **`folded` 不携带 module/category。** `ReadFolded` 重建的 `Frame` 没有
   module，报告无法区分同名通用符号与驱动模块内同名符号；本次用服务器精确
   符号映射补足分析，但生产报告若要稳定复现，需要扩展证据格式或侧车文件。
7. **前缀/模块匹配的大小写与边界语义。** 符号匹配是区分大小写的
   `strings.HasPrefix`，全大写符号（如 `DEVDRV_*`）不会命中；本窗口的
   kallsyms 与 folded 均为小写，无实际影响。`davinci`/`vdavinci`/`ascend`
   等不带下划线的前缀会匹配同前缀的更长标识符，后续新增前缀应保持 `_` 边界；
   module override 用 `strings.ToLower` 做子串匹配，大小写不敏感，但会把
   `hisi_sas_*` 误判（P2-1）。

## 7. YAML 改动前后差异

### 7.1 改动

`profiles/kernel-symbols.yaml` 的 `npu.prefixes` 在原有 9 个前缀后新增：

```text
hdcdrv_  hdccom_  devmm_  trs_  pcivnic_  dms_  tsagent_  vdavinci  dbl_  ka_
```

注释中已记录 2026-11-27 的 kallsyms 依据（前缀符号数、模块名）。同时主线程已把
同样 10 个前缀加入 `internal/stacks/classify.go` 的 `DefaultSymbolRules`，并让
`internal/report/flamegraph.go` 的 `flamegraphFrameCategory` 优先调用
`stacks.ClassifyName(frame.Name, frame.Module, nil)`。本文不重复修改这些
主线程已完成的源码改动，也不回退它们。

### 7.2 统计对比

同一份 final4 folded（samples=1488，unique=221，total_frames=13658）：

| 指标 | 改前（旧 YAML） | 改后（新 YAML/共享规则） | 变化 |
|---|---:|---:|---:|
| NPU 唯一帧 | 14 | 81 | +67 |
| NPU 加权帧 | 117 | 630 | +513（5.38x） |
| NPU 帧占比 | 0.8566% | 4.6127% | +3.7561pp |
| 含 NPU 帧的栈样本 | 105/1488（7.056%） | 108/1488（7.258%） | +3 样本 |
| `other` 唯一帧 | 224 | 157 | -67 |
| `other` 加权帧 | 12393 | 11880 | -513 |

注：表内 4.6127% 是共享名称规则口径；位于 Ascend 模块但名称仍落入 `other`
的 114 个加权帧（P1）未计入，计入后的诊断下界为 5.45%（744/13658）。

final2 辅助证据（samples=1420，unique=218，total_frames=13209）：

| 指标 | 改前（旧 YAML） | 改后（新 YAML/共享规则） | 变化 |
|---|---:|---:|---:|
| NPU 唯一帧 | 15 | 92 | +77 |
| NPU 加权帧 | 131 | 752 | +621 |
| NPU 帧占比 | 0.9917% | 5.6931% | +4.7014pp |
| 含 NPU 帧的栈样本 | 117/1420（8.239%） | 122/1420（8.592%） | +5 样本 |

机器可读的改前/改后逐帧表：
`.analysis/stacks-probe/classification/final4-npu-before-after.csv`。

## 8. 对“`drv_ascend_intf_ioctl` 约 8.10%”这一结论的影响

- **8.10% 的来源是 final2 HTML 内嵌数据**：`drv_ascend_intf_ioctl` 出现于
  115/1420 个采样栈 = **8.0986%**。这是“该帧出现在多少比例的采样栈中”，
  不是 NPU 总帧占比，也不是耗时占比。
- **final4 原始 folded 的同一指标是 101/1488 = 6.79%**（`report-stacks-final4-classified.html`
  Top NPU 首项同样为 101，6.79%）。因此 8.10% 只应作为 final2 单次窗口的
  栈命中率，不能当作稳定常数；两个 10 秒窗口的观测范围是 **6.79%–8.10%**。
- 该帧本身在分类修正前后不变（始终 `npu`，101 样本、self=0）；修正改变的是
  “NPU 总盘子”：旧口径 117/13658=0.86%，新交付口径 630/13658=4.61%。
  因此不能说“NPU 帧只有 0.86%”或把 8.10% 当作 NPU 总量。
- 按“包含任一 NPU 帧的栈样本”口径：final4 105/1488=7.06%（旧）→
  108/1488=7.26%（新）；final2 117/1420=8.24%→122/1420=8.59%。这说明
  `drv_ascend_intf_ioctl` 覆盖了大多数 NPU 相关栈，但 `trs_*`/`devmm_*`
  的深层帧贡献了绝大多数 NPU 加权帧次数。

结论表述建议：

> final2 中 `drv_ascend_intf_ioctl` 出现在 115/1420（8.10%）的采样栈中；
> final4 为 101/1488（6.79%）。该函数是 NPU 驱动栈的主要入口，但不代表
> NPU 总帧占比；按接入共享分类后的新口径，final4 NPU 帧加权占比为
> 4.61%（630/13658），旧报告 0.86% 是未接入共享分类的旧口径。

## 9. 测试与验证

在 WSL Ubuntu-22.04、工作目录
`/mnt/c/Users/liruilong/pentagi/cann-ebpf` 执行：

```bash
/tmp/go1.24.6/bin/go test ./internal/stacks -count=1
# ok  cann-ebpf/internal/stacks

/tmp/go1.24.6/bin/go test ./... -count=1
# ok  cann-ebpf/cmd/cann-ebpf
# ok  cann-ebpf/internal/config
# ok  cann-ebpf/internal/contextfeed
# ok  cann-ebpf/internal/device
# ok  cann-ebpf/internal/diagnose
# ok  cann-ebpf/internal/discovery
# ok  cann-ebpf/internal/elfutil
# ok  cann-ebpf/internal/export
# ok  cann-ebpf/internal/hardware
# ok  cann-ebpf/internal/metrics
# ok  cann-ebpf/internal/preflight
# ok  cann-ebpf/internal/probes
# ok  cann-ebpf/internal/report
# ok  cann-ebpf/internal/runlog
# ok  cann-ebpf/internal/stacks
# ok  cann-ebpf/internal/tracectx
# ok  cann-ebpf/internal/tracefs
# ok  cann-ebpf/poc/cpython
```

`internal/stacks/classify_test.go` 新增/加强覆盖：

- `TestKernelSymbolsProfileMatchesDefaultRules`：YAML 与 `DefaultSymbolRules`
  完全一致，并包含 10 个 kallsyms 实证前缀。
- `TestClassifyNameRealEvidenceSymbols`：`drv_ascend_intf_ioctl`、`devdrv_*`、
  `devmm_*`、`trs_*`、`hdcdrv_*`、`hdccom_*`、`pcivnic_*`、`dms_*`、
  `tsagent_*`、`vdavinci*`、`dbl_*`、`ka_*` 均命中 `npu`。
- `TestClassifyNameGenericDMAStaysDMA`：`dma_map_page*`、`iommu_dma_map_page`、
  `arm_smmu_*` 等通用 DMA/IOMMU 不误判为 `npu`。
- `TestClassifyNameCaseAndModuleMatching`：符号前缀大小写敏感、模块名大小写
  不敏感、无 NPU module marker 的模块不误判。
- `TestClassifyNameUnknownAndEmpty`：未知符号与空帧归 `other`。

主线程新增的 `internal/report/flamegraph_classify_test.go`
（`TestBuildFlamegraphUsesSharedNPUClassification`）也已通过；报告路径现在使用
共享 `ClassifyName`，不会回退到旧的硬编码短列表。

## 10. 剩余不确定性与后续建议

1. **单窗口证据**：final4 只有一次 10 秒真实负载采样；没有固定负载 A/B，
   不能把 4.61% 当作长期稳定值，也不能从栈样本占比推请求延迟或 AI Core
   执行时间。
2. **final2 是 HTML 内嵌数据重建**：虽然 samples/unique/total_frames 与 HTML
   完全一致，但不是原始 folded；8.10% 应以 final2 HTML 标注来源。
3. **kallsyms 是 2026-11-27 的服务器快照**：驱动版本/模块加载状态变化后，
   前缀与模块归属需要重跑本文命令复核。
4. **通用名符号的模块归属**：final4 有 7 个位于 Ascend 驱动模块的通用名帧
   （`ioctl_trs_res_id_comm`、`_trs_res_id_free.part.0`、`_devmm_ioctl_dispatch`、
   `_devmm_find_first_page_ref`、`_devmm_clear_page_ref_after_ioctl`、
   `alloc_mem`、`free_mem`）仍归 `other`，合计 114 个加权帧；4.61% 是名称
   口径，模块归属口径的诊断下界为 5.45%。建议在采集侧持久化 `module` 或
   提供 `symbol->module` sidecar，而不是把这些通用名加入 NPU 前缀（会误伤
   通用内核）。
5. **`hisi_` / `drv_` 过宽**：本窗口没有 `hisi_` 帧，但 kallsyms 已证明
   `hisi_` 全是 SAS/PMU，`drv_` 含 builtin `drv_attr_*`；建议后续把
   `hisi_` 从 NPU 前缀移出或限制到 NPU 模块，`drv_` 加 `drv_attr_` 排除规则；
   这需要同步修改 `DefaultSymbolRules` 与 YAML，并更新测试。
6. **`el0_sync` 报告 fallback**：当前 `flamegraphFrameCategory` 仍会把
   `el0_sync`/`el0_sync_handler` 判 `sync`；若报告要展示类别分布，建议删除
   该 fallback 的 `sync` 子串匹配或显式把这些帧归 `other`。这不影响 NPU
   数字，但会影响“同步开销”解读。
7. **`hccn`/`npu` 未验证**：服务器没有加载 `hccn`/`npu` 符号；若正式环境
   加载，需要按同一方法补做 kallsyms 证据后再决定前缀。

## 11. 复算命令与产物

以下命令均在 WSL Ubuntu-22.04 的
`/mnt/c/Users/liruilong/pentagi/cann-ebpf` 执行（Windows 路径
`C:\Users\liruilong\pentagi\cann-ebpf`）：

```bash
# final2 HTML 内嵌 JSON 提取 + 重建 folded（仅用于辅助对照，不修改原 HTML）
python .analysis/stacks-probe/classification/extract_final2_json.py
python .analysis/stacks-probe/classification/reconstruct_final2.py

# final4 唯一帧清单 + 服务器只读精确 symbol->module 映射
python .analysis/stacks-probe/classification/make_final4_list.py
bash .analysis/stacks-probe/classification/probe_kallsyms.sh
bash .analysis/stacks-probe/classification/probe_exact_symbols.sh \
  /mnt/c/Users/liruilong/pentagi/cann-ebpf/.analysis/stacks-probe/classification/final4-exact-symbols.list

# 改前/改后统计（Go 分析器直接调用 internal/stacks.ClassifyName）
go run ./.analysis/stacks-probe/classification/tool/main.go \
  .analysis/stacks-probe/final4/stacks.folded \
  .analysis/stacks-probe/classification/kernel-symbols.before.yaml \
  .analysis/stacks-probe/classification/kallsyms-final4-exact-symbols.txt \
  .analysis/stacks-probe/classification/final4-stats-before.json

go run ./.analysis/stacks-probe/classification/tool/main.go \
  .analysis/stacks-probe/final4/stacks.folded \
  profiles/kernel-symbols.yaml \
  .analysis/stacks-probe/classification/kallsyms-final4-exact-symbols.txt \
  .analysis/stacks-probe/classification/final4-stats-after.json

# 生成 Top30 / 类别分布 / NPU 前后对比 CSV
python .analysis/stacks-probe/classification/summarize.py

# 生成“位于 Ascend 模块但类别仍为 other”的残差清单
python .analysis/stacks-probe/classification/residual_module_other.py
```

产物清单：

- `final4-stats-before.json` / `final4-stats-after.json`：逐帧统计与类别统计
- `final4-stats-before-frames.csv` / `final4-stats-after-frames.csv`：逐帧 CSV
- `final4-top30.csv`：Top 30 帧
- `final4-category-distribution.csv`：类别改前/改后分布
- `final4-npu-before-after.csv`：NPU 相关帧改前/改后逐帧
- `final4-npu-module-other-residual.csv`：位于 Ascend 驱动模块但名称规则仍归
  `other` 的 7 个帧（114 个加权帧），用于量化 P1 残余
- `kallsyms-probe.txt`、`kallsyms-detail.txt`、`kallsyms-prefix-modules.csv`：
  `/proc/kallsyms`、`/proc/modules`、`modinfo` 只读实证
- `kallsyms-final4-exact-symbols.txt`：final4 274 个唯一帧的精确 symbol->module 映射
- `final2-reconstructed.folded` / `final2-*.json`：final2 HTML 内嵌数据重建与统计
- `kernel-symbols.before.yaml`：本次修改前的 YAML 快照（仅分析产物，不参与构建）
