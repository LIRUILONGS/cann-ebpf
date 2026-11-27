# 真实 V4 device 证据（2026-11-27，V4 未停机分析）

来源：V4 带 collector 启动时（14:42–14:50）产生的 device 文件；collector 已回滚，V4 已干净重启。
本次分析**只读离线**，未停 V4。

## 1. 证据文件

- 路径：`/data/cache/cann-ebpf/device-1174.jsonl`
- 大小：430,315,099 bytes
- SHA-256：`6c73d694bb848d3c30c6ed24b220a706682faf1b36fe34ee22bde94746f023fd`
- 时间跨度：258.545 秒
- 记录数：**1,321,452**

## 2. 记录构成

| kind | 数量 |
|---|---:|
| API | 1,160,770 |
| MEMCPY | 125,182 |
| KERNEL | 32,698 |
| HCCL | 2,802 |

Top API：`StreamSynchronize` 122,249、`MemCopySync` 122,166、`aclrtMemcpy` 122,166、
`aclrtSynchronizeStreamWithTimeout` 107,189、`aclmdlRICaptureGetInfo` 78,265、
`LaunchKernelV2` 53,222、`aclrtLaunchKernelWithHostArgs` 53,222。

## 3. HCCL（B4 关键）

| HCCL name | 数量 |
|---|---:|
| `hcom_allGather_` | 1,650 |
| `hcom_reduceScatter_` | 808 |
| `hcom_alltoall_` | 344 |

- source：**全部 `mspti_communication`**（2802/2802）
- **correlation_id：2802/2802 非零**
- 该文件共 182,568 个不同 correlationId

这证明：真实 V4 生产 HCCL 流量可以被 msPTI 记录，且 `Communication` 记录带 correlationId；
B4 的 correlation 路径成立（不再依赖 2-rank 合成）。

## 4. B1 结论

- **真实 V4 device 时间线已经采到**（API/KERNEL/MEMCPY/HCCL，258 秒，1.32M 条）；
- 但这次没有同时采到 host span（B1 attach 被 collector 文件量/merge 拖住后终止），
  **host↔device join 尚未验证**；
- collector 在 V4 所有 Python 进程里同时启动（28 个文件、3.2GB），开销过大；
  需要限制到目标 Worker、降低 max bytes，再做一次 host+device 同窗口采集。

## 5. 全量汇总（28 个文件，8 个非空 Worker）

对 `/data/cache/cann-ebpf/device-*.jsonl` 全量流式统计（`nice/ionice`，V4 未停）：

| 项 | 值 |
|---|---|
| 文件数 / 非空 | 28 / **8** |
| 总记录 | **10,556,941** |
| API | 9,271,490 |
| MEMCPY | 1,001,456 |
| KERNEL | 261,580 |
| HCCL | **22,415** |
| HCCL source | **全部 `mspti_communication`** |
| HCCL 名称 | `hcom_allGather_` 13,199、`hcom_reduceScatter_` 6,464、`hcom_alltoall_` 2,752 |
| correlationId 数 | **212,706** |
| 时间跨度 | **623.823 秒** |
| 空文件 | 20（未命中目标的 helper 进程） |

这证明：
- **8 个 Worker 都有真实 V4 device 记录**（API/KERNEL/MEMCPY/HCCL）；
- **B4 的 real V4 HCCL correlation 成立**（22,415/22,415 带 correlation_id）；
- 20 个空文件说明 collector 仍在过多 helper 进程中启动，需要 `CANN_MSPTI_ONLY_CMDLINE` 或 Worker 级启动进一步收敛。

## 6. 仍然待办

1. collector 退出析构 SIGSEGV；
2. 限制 collector 进程数/输出量（当前 28 个进程、3.2GB/几分钟，不可生产）；
3. 同窗口 host span + device 采集，验证 join/慢 Sync；
4. B2 A/B（需要 V4.1 或另一台机器）。
