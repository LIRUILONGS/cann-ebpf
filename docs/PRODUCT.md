# cann-ebpf 成品说明（2026-11-26）

## 1. 一句话定位

`cann-ebpf` 是昇腾 CANN 的**主机侧低开销观测工具**：回答“哪个进程/线程/调用、等了多久、host 是否阻塞、
对应哪些设备区间”，并输出可复核的原始证据和只读诊断报告。

它不是网关指标系统，也不是 msPTI 的替代品；HTTP 延迟、TTFT、QPS 仍看网关。

## 2. 当前版本与制品

| 项 | 值 |
|---|---|
| 源码 | `main` @ `5e2fc93`（in-process msPTI collector + host 接线） |
| 版本号 | `0.6.0-m2` + M3（建议正式交付前改为 `0.7.0-m3`） |
| Linux/arm64 二进制 | `dist/cann-ebpf-linux-arm64-m3` |
| 大小 / SHA-256 | `13,407,163` bytes / `3fe700bb4d2345e7cee459c2c9449a1478e0b720da8db5ade29fc6f4569769c3` |
| 构建/测试 | `go test ./...`、`go vet ./...`、Linux/arm64 交叉编译通过 |
| 运行时依赖 | 仅目标机内核 + root/CAP；不需要 Python（纯 eBPF 路径） |

`dist/` 不提交；交付时以本目录二进制与 `dist/SHA256SUMS.txt` 为准。

## 3. 成品由什么组成

### 3.1 一个 CLI（`cann-ebpf`）

```text
validate        校验配置/profile
preflight       只读环境检查（root/BTF/tracefs/4 个 tracepoint/perf/lockdown/memlock/nofile/npu-smi/cgroup）
discover        只读发现进程、库、符号、offset（--dry-run/--json）
attach          host span 采集：blocking / hardware / python-batch / device-jsonl
attach-tracefs  tracefs 后备采集
trace           按 trace_id 输出链路树
diagnose        只读读取证据，输出 JSON/Markdown 定位报告
version         版本与平台
```

### 3.2 证据文件（run 目录）

| 文件 | 内容 |
|---|---|
| `spans.jsonl` | host span 原始事件（37 函数、entry/exit、offcpu/futex 可选） |
| `manifest.json` | 运行环境、目标 PID、库 inode、统计、warnings、segments/artifacts SHA-256 |
| `summary.json` / `summary.md` | 派生 function/category/PID/batch/trace 汇总 |
| `python-batch-<pid>.json` | 纯 eBPF batch 逐 Worker 统计（可选） |
| `hardware.jsonl` | npu-smi 8 设备 HBM/AICore/功耗/温度（可选，manifest artifact） |
| `device.jsonl` | M3 设备时间线 `cann.device.v1`（由 in-process collector 产出，attach 导入） |
| `device.jsonl.meta.json` | collector 元数据 `cann.mspti.collector.v1` |
| `pidmap.json` | 容器 local→host pid/tid 快照 `cann.pidmap.v1` |
| `diagnose.json` / `.md` | 只读定位报告 `cann.diagnose.report.v1` |

### 3.3 配置与适配器

- `profiles/vllm-ascend.yaml`：V4/vLLM-Ascend 10 目标自动发现 + 37 函数；
- `profiles/orinfer.yaml`：Orinfer（未运行验证）；
- `profiles/cann-generic.yaml`：兜底 host span；
- `configs/base.yaml`：默认阈值/采样/硬件/安全；
- `adapters/vllm_ascend/`：vLLM general plugin（代码完成，**未安装/未激活**）；
- `poc/cpython/`：纯 eBPF CPython batch POC；
- `m3/mspti_collector/`：容器内 in-process msPTI collector（C++，合成 ACL 已验证）。

### 3.4 文档

`HANDOFF`、`PLAN`、`TASKS`、`GAPS`、`DIAGNOSE`、`M2-OFFCPU/M2-VALIDATION/M2-OVERHEAD`、
`HARDWARE`、`PREFLIGHT`、`M3-DEVICE-JOIN/M3-COLLECTOR/M3-FEASIBILITY/M3-VALIDATION`、
`NEXT-PLAN`、`WORK-SPLIT`，以及 `docs/templates/`。

## 4. 现在能做什么（已验证）

| 能力 | 状态与证据 |
|---|---|
| M0 发现/符号解析 | V4 10 目标、每目标 37/148 符号、4 库 |
| M1/M4 host 采集 | V4 10 秒 6,991 spans、dropped 0、8 Worker 有数据、health 200/200、残留 0 |
| 纯 eBPF vLLM batch | 真实 V4：10 batch、6 request ID、类型 `SchedulerOutput` |
| `diagnose` 只读报告 | 真实证据 6,991/164 spans 正常；时间窗/PID/batch/request/trace 过滤 |
| M2 host off-CPU/futex | V4 10 秒 7,561 spans、`attached=4 failed=0`、无效样本 0；合成锁竞争 off-CPU 99.7%/futex 99.9%，`host_lock_contention` 1.00 |
| E 层硬件 | V4 8 设备 HBM/AICore/功耗/温度；HBM free 6.3%、AICore 峰值 46% |
| `preflight` | V4 pass=15 warn=0 fail=0；`attach` 默认 enforce |
| M3 设备时间线 | 合成 ACL final7：`host_spans=11,859`、`device_spans=148,466`、慢 Sync **305/305（100%）**、clock offset 761ns、pidmap 27 条；KERNEL/MEMCPY correlationId 已关联 |

M3 final7 的总匹配率 44.9% 不是失败：分母包含大量无设备区间的 `aclrtGetDevice` 等调用；
验收指标是慢 Sync 305/305。详见 `docs/M3-VALIDATION.md`。

## 5. 现在还不能做什么（边界）

1. **没有真实 V4/Orinfer 设备时间线**：collector 需要 LD_PRELOAD/重启注入，只能在维护窗口做；
   目前只验证了合成 ACL，HCCL 的 correlationId 未验证。
2. **没有单 request 延迟**：batch 是 `SchedulerOutput.num_scheduled_tokens` 窗口，不是逐 request 执行时长。
3. **没有 `execute_model` 函数级耗时**：vLLM 插件未安装/未激活，纯 eBPF 的 `PyObject_GetAttr` 不是函数边界。
4. **没有设备侧生产结论**：`device_bound`/`launch_starvation` 等只在有真实 device 证据时输出；
   合成数据不能当性能结论。
5. **Orinfer 未运行验证**；M5 V4/Orinfer A/B 未做。
6. **M6 生产化未做**：Prometheus/OTLP、Grafana、systemd、安装/回滚、长跑 watchdog。
7. **固定负载吞吐门禁未做**：生产流量不可控，目前只证明 agent CPU +0.28pp 单核、RSS +1.3MB、0 丢事件。
8. 其他操作边界：嵌套 CANN span 不采 blocking；V4 上 740 个 uprobe 的进程退出清理约 40 秒
   （不影响业务，残留最终为 0）；`msprof --pid` 动态采集段错误，不可用。

## 6. 典型用法

```bash
# 1) 环境检查
./cann-ebpf-linux-arm64-m3 preflight

# 2) 发现
./cann-ebpf-linux-arm64-m3 discover --profile vllm-ascend --profiles-dir profiles --dry-run --json

# 3) host 采集（含阻塞与硬件）
OUT="runs/ebpf-$(date -u +%Y%m%dT%H%M%SZ)"
./cann-ebpf-linux-arm64-m3 attach --pid auto --profile vllm-ascend --profiles-dir profiles \
  --duration 10s --blocking on --hardware on --output "$OUT"

# 4) 导入 M3 设备时间线（collector 产出后）
./cann-ebpf-linux-arm64-m3 attach --pid auto --profile vllm-ascend --profiles-dir profiles \
  --duration 30s --device-jsonl /host/path/device.jsonl --output runs/ebpf-m3

# 5) 只读诊断
./cann-ebpf-linux-arm64-m3 diagnose --input "$OUT" --json --out "$OUT/diagnose.json"
```

## 7. 交付口径建议

- 对外可称：**`cann-ebpf 0.6.0-m2 + M3 设备时间线（合成验证）`**；
- 建议正式发版前把版本号统一为 `0.7.0-m3`，并在 HANDOFF 固化二进制 SHA；
- M3 生产验证（V4/Orinfer 注入）和 M6 生产化是下一阶段交付门槛，不应在本版本宣称已具备；
- vLLM 插件与纯 eBPF 路径必须分开描述：插件只解决 Python batch 边界，且未激活。
