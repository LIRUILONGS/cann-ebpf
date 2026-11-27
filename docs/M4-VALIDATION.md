# M4 验证记录：vLLM-Ascend host profile 与上下文协议

日期：2026-11-26

本文只记录已经发生在服务器上的事实。它区分三件事：

1. **已完成**：37 个 host uprobe、V4 自动发现、10 秒实采、上下文协议 smoke、证据完整性检查；
2. **代码完成但未激活**：vLLM general plugin 已实现并通过单测，但没有安装到运行中的 V4，也没有重启服务；
3. **未完成**：真实插件请求路径、逐 request 时长、msPTI 设备侧关联、Orinfer 运行窗口。

不能把第 2 项写成“已在 V4 验证”，也不能把第 3 项写成“已实现”。

## 1. 结论摘要

| 项目 | 结果 | 可交付表述 |
|---|---|---|
| 二进制 | `cann-ebpf 0.4.0-m4 linux/arm64` | SHA-256 `cf441013b88e3545b986eea16fa615be4fd0ca3cb8f4f694577b832812095f03` |
| host 探针 | 37 个函数、74 个 entry/exit 程序 | 已编译、已 attach、已在 V4 触发其中 6 类调用 |
| V4 discovery | 10 个目标 | 8 个 Worker_TP + frontend + EngineCore；每目标 37/148 符号 |
| V4 10 秒采集 | 6,991 spans | 8 个 Worker_TP 均有 span；dropped 0；health 前后 200 |
| 上下文协议 | smoke 通过 | 164 spans；21 条 batch span；UID 982/GID 979 连接成功 |
| 真实 vLLM 插件 | 未激活 | 代码和单测完成；未装入/重启运行中的 V4 |
| 逐 request 时长 | 未实现 | 当前只有 batch 窗口归因，没有 request 级拆分 |
| msPTI 设备侧 | 未实现 | 属于 M3；本轮没有初始化 msPTI |
| Orinfer | 未运行验证 | profile validate 通过；没有真实采集 |

## 2. 验证环境

| 项 | 值 | 来源 |
|---|---|---|
| 主机 | `target-host` | `delivery-meta/uname.txt` |
| 架构 | `aarch64` | 同上 |
| 内核 | `5.10.0-136.108.0.188.31.oe2203sp1.bclinux.aarch64` | 同上 |
| 目标服务 | `vllm-ds4-8905.service` | `service-state.txt` |
| 服务 health | `http://127.0.0.1:8905/health`，采集前后均为 `200` | `health-before.txt` / `health-after.txt` |
| 目标进程 | 8 个 `VLLM::Worker_TP`、1 个 `vllm`、1 个 `VLLM::EngineCor` | `targets.txt` / discovery JSON |
| 目标 UID/GID | `982:979` | `context-test-final-20261126/target-identity.txt` |
| V4 目标进程内 CANN | `cann-9.0.1` | discovery 库路径 |
| 容器镜像 | `ds4/vllm-ascend:v0.23.0rc1-nonroot` | M1/M0 现场记录 |

说明：`PLAN.md` 中的 CANN `9.2.0` 是宿主侧 msPTI 规划环境；本轮 V4 目标进程实际映射的是
`/usr/local/Ascend/cann-9.0.1/...`。两者不能混写成一个版本。

PID 只用于本次基线复核。profile 不写死 PID，服务重启后由 `comm/cmdline/container` 规则重新发现。

## 3. 二进制与源码

最终 Linux/arm64 二进制：

```text
dist/cann-ebpf-linux-arm64-m4
sha256  cf441013b88e3545b986eea16fa615be4fd0ca3cb8f4f694577b832812095f03
version cann-ebpf 0.4.0-m4 linux/arm64
```

服务器副本：

```text
/opt/cann-ebpf/m4-20261126/cann-ebpf-linux-arm64
sha256  cf441013b88e3545b986eea16fa615be4fd0ca3cb8f4f694577b832812095f03
```

构建方式：

```bash
CGO_ENABLED=0 go test ./... -count=1
CGO_ENABLED=0 go vet ./...
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
  go build -buildvcs=false -trimpath -o dist/cann-ebpf-linux-arm64-m4 ./cmd/cann-ebpf
```

`internal/probes/bpf/cann.bpf.o` 已由 `cann.bpf.c` 重新生成并嵌入二进制。运行时不需要在目标机编译 BPF。

## 4. 探针覆盖

`internal/probes/functions.go` 当前声明 37 个函数：

| 类别 | 函数 |
|---|---|
| sync | `aclrtSynchronizeStream`、`aclrtSynchronizeStreamWithTimeout`、`aclrtSynchronizeEvent` |
| event | `aclrtRecordEvent`、`aclrtStreamWaitEvent`、`aclrtQueryEventStatus`、`aclrtQueryEventWaitStatus`、`aclrtQueryEvent` |
| handle | `aclrtSetDevice`、`aclrtGetDevice`、`aclrtSetCurrentContext`、`aclrtCreateStream`、`aclrtDestroyStream`、`aclrtCreateEvent`、`aclrtCreateEventWithFlag`、`aclrtResetEvent`、`aclrtDestroyEvent` |
| memory | `aclrtMalloc`、`aclrtFree`、`aclrtMallocHost`、`aclrtFreeHost`、`aclrtHostRegister`、`aclrtHostUnregister`、`aclrtGetMemInfo`、`rtMemGetInfoEx` |
| submit | `aclrtMemcpyAsync`、`aclrtLaunchKernel`、`rtKernelLaunch`、`rtKernelLaunchWithFlagV2`、`rtMemcpyAsync` |
| hccl | `HcclAllReduce`、`HcclAlltoAll`、`HcclAllGather`、`HcclReduceScatter`、`HcclBroadcast`、`HcclSend`、`HcclRecv` |

每个函数有 entry/exit 两个 uprobe 程序，共 74 个程序。`vllm-ascend.yaml` 对
`handle` 使用 `1/128` 采样，对 `submit` 使用 `1/64` 采样，对 `hccl` 使用 `1/16`
采样；同步和 event 的阈值/模式由 profile 决定。

**覆盖边界**：37 个函数“已 attach 并可解析”不等于“在一个窗口里全部触发”。最终 10 秒
窗口实际只观测到 6 类函数，见第 6 节；不能宣称 37 个函数在同一窗口都有调用样本。

## 5. Discovery

最终 discovery 命令：

```bash
./cann-ebpf-linux-arm64 discover \
  --profile vllm-ascend \
  --profiles-dir profiles \
  --dry-run --json > runs/discover-vllm-m4-final.json
```

结果：10 个目标，每个目标在 4 个库上查询 148 个符号，解析到 37 个函数。

| PID | comm | 匹配规则 | 库数 | 符号 |
|---:|---|---|---:|---:|
| 1452 | `VLLM::Worker_TP` | `any: comm=VLLM::Worker_TP` | 4 | 37/148 |
| 3962 | `VLLM::Worker_TP` | 同上 | 4 | 37/148 |
| 6568 | `VLLM::Worker_TP` | 同上 | 4 | 37/148 |
| 8890 | `VLLM::Worker_TP` | 同上 | 4 | 37/148 |
| 11340 | `VLLM::Worker_TP` | 同上 | 4 | 37/148 |
| 13505 | `VLLM::Worker_TP` | 同上 | 4 | 37/148 |
| 15629 | `VLLM::Worker_TP` | 同上 | 4 | 37/148 |
| 17758 | `VLLM::Worker_TP` | 同上 | 4 | 37/148 |
| 4188787 | `vllm` | `any: comm=vllm` | 4 | 37/148 |
| 4192024 | `VLLM::EngineCor` | `any: comm=VLLM::EngineCor` | 4 | 37/148 |

Discovery 只读 `/proc`、目标 root 下的库和 ELF 符号，不 attach、不修改服务。`vllm-ascend`
和 `orinfer` 两份 profile 的 `validate` 均返回 OK；Orinfer 未运行，不能据此声称采集过 Orinfer。

## 6. 10 秒自动 attach

命令：

```bash
OUT=/opt/cann-ebpf/m4-20261126/runs/ebpf-auto-10s-final-20261126 \
DURATION=10s \
sh scripts/server_auto_capture.sh
```

证据目录：

```text
/opt/cann-ebpf/m4-20261126/runs/ebpf-auto-10s-final-20261126
```

关键结果：

| 项 | 值 |
|---|---|
| run_id | `vllm-ascend-20261125T235437.683771465Z` |
| status | `complete` |
| target_pids | 8 Worker + frontend + EngineCore，共 10 个 |
| spans / event_count | `6991` / `6991` |
| JSONL 行数 | `6991` |
| dropped_events | `0` |
| JSONL bytes | `4,372,839` |
| JSONL SHA-256 | `fb2625ba842c27c2480aa1750035acc6f61769849d31c23af462cd34fdd1c312` |
| health before/after | `200 / 200` |
| service before/after | `active / active` |
| uprobe 残留 | `0` |
| collector exit | `0` |
| wall-seconds | `65` |

`wall-seconds=65` 包含库解析、10 个 PID 的 uprobe setup 和清理，不等于 10 秒采集窗口；
不能把它写成“采集耗时 65 秒”或吞吐指标。

### 6.1 按 PID

| PID | spans | batch spans | thread spans |
|---:|---:|---:|---:|
| 1452 | 1526 | 0 | 1526 |
| 3962 | 1097 | 0 | 1097 |
| 6568 | 990 | 0 | 990 |
| 8890 | 833 | 0 | 833 |
| 11340 | 785 | 0 | 785 |
| 13505 | 684 | 0 | 684 |
| 15629 | 583 | 0 | 583 |
| 17758 | 493 | 0 | 493 |

frontend `4188787` 和 EngineCore `4192024` 在该 10 秒窗口没有产生 span。它们仍在目标列表中，
也有 discovery 符号结果；不能把“目标已发现”写成“该窗口有调用证据”。

### 6.2 按函数

| 函数 | count | p50 ns | p95 ns | p99 ns | max ns |
|---|---:|---:|---:|---:|---:|
| `aclrtSynchronizeStream` | 2187 | 48088594 | 55781856 | 59046804 | 78739343 |
| `aclrtSynchronizeStreamWithTimeout` | 2271 | 6766087 | 16607121 | 19145915 | 23471469 |
| `aclrtSynchronizeEvent` | 588 | 14098478 | 35122663 | 55497765 | 94050087 |
| `aclrtDestroyEvent` | 1942 | 4580 | 13760 | 15820 | 210741 |
| `aclrtRecordEvent` | 2 | 1152336 | 1454218 | 1454218 | 1454218 |
| `aclrtQueryEventStatus` | 1 | 2498563 | 2498563 | 2498563 | 2498563 |

分类汇总：`sync` 5,046 条（72.18%），`handle` 1,942 条（27.78%），`event` 3 条（0.04%）。
这些是多个线程的 host span 合计，不能当作墙钟时间或设备利用率。

最终窗口没有 HCCL span。另一次 10 秒运行曾观测到 1 条 `HcclReduceScatter`；该条不在最终
交付 JSONL 中，不能混入最终窗口统计。

## 7. 上下文协议 smoke

命令：

```bash
OUT=/opt/cann-ebpf/m4-20261126/runs/context-test-final-20261126 \
sh scripts/context-smoke.sh
```

证据目录：

```text
/opt/cann-ebpf/m4-20261126/runs/context-test-final-20261126
```

该脚本自动选择第一个 `VLLM::Worker_TP`，读取目标 UID/GID，以该 UID/GID 运行 Python 客户端，
发送 `batch_begin` / `batch_end`，然后校验 JSONL 与 summary。它是**协议 smoke**，不是
运行中的 vLLM 插件流量；`source` 必须是 `protocol_smoke`。

| 项 | 值 |
|---|---|
| target PID | `1452` |
| target UID/GID | `982 / 979` |
| socket owner | `982:979` |
| allowed_uids | `[982]` |
| spans | `164` |
| batch spans | `21` |
| thread fallback spans | `143` |
| batch | `ctx-smoke-1` |
| request IDs | `req-smoke-1`、`req-smoke-2` |
| batch span 比例 | `12.804878%` |
| validation | `ok=true`，`failures=[]` |
| dropped_events | `0` |
| JSONL bytes | `102,994` |
| JSONL SHA-256 | `60e96d858864a156892ec18b74ac0255785a56878e561f56aa6644a509193e72` |
| health before/after | `200 / 200` |
| service before/after | `active / active` |
| uprobe 残留 | `0` |

样本 span 的 `attribution` 为 `level=batch`、`source=protocol_smoke`、`confidence=1`、
`verdict=batch_window`。这证明协议、UID 权限和 batch 窗口标注链路可用；它**不证明**真实
vLLM request 已被插件标注。

## 8. 证据完整性

最终证据目录本地副本：

```text
dist/m4-delivery-evidence-20261126/runs/
```

包含：

```text
discover-vllm-m4-final.json
discovery-m4-final/
ebpf-auto-10s-final-20261126/
context-test-final-20261126/
delivery-meta/
```

一致性检查：

- eBPF manifest `event_count=6991`，JSONL 行数 `6991`，`dropped_events=0`；
- context manifest `event_count=164`，JSONL 行数 `164`，`dropped_events=0`；
- 两个 manifest 的 `segments[0].bytes` / `sha256` 与对应 JSONL 文件一致；
- 采集前后 `/health` 均为 200，`vllm-ds4-8905.service` 均为 active；
- `/sys/kernel/tracing/uprobe_events` 中 `cann_` 残留为 0；
- collector 退出码为 0；
- context 校验 `ok=true`，`failures=[]`。

证据不做脱敏、不改写、不补造。文档中的聚合值来自 `summary.json` / `manifest.json`，原始值以
`spans.jsonl` 为准。

## 9. 复现命令

以下命令在服务器证据根目录 `/opt/cann-ebpf/m4-20261126` 下执行。每次运行必须使用新的输出
目录；脚本拒绝覆盖已有证据。

校验：

```bash
./cann-ebpf-linux-arm64 version
./cann-ebpf-linux-arm64 validate --config profiles/vllm-ascend.yaml
./cann-ebpf-linux-arm64 validate --config profiles/orinfer.yaml
```

只读 discovery：

```bash
./cann-ebpf-linux-arm64 discover \
  --profile vllm-ascend \
  --profiles-dir profiles \
  --dry-run --json > runs/discover-$(date -u +%Y%m%dT%H%M%SZ).json
```

10 秒自动采集：

```bash
OUT="/opt/cann-ebpf/m4-20261126/runs/ebpf-auto-10s-$(date -u +%Y%m%dT%H%M%SZ)" \
DURATION=10s \
sh scripts/server_auto_capture.sh
```

上下文协议 smoke：

```bash
OUT="/opt/cann-ebpf/m4-20261126/runs/context-test-$(date -u +%Y%m%dT%H%M%SZ)" \
sh scripts/context-smoke.sh
```

检查：

```bash
cat "$OUT/manifest.json"
cat "$OUT/summary.json"
wc -l "$OUT/spans.jsonl"
sha256sum "$OUT/spans.jsonl"
cat "$OUT/uprobe-residual.txt"
```

不要把 `server_auto_capture.sh` 的 `wall-seconds` 当作采集时长；不要把 smoke 的
`source=protocol_smoke` 改写成 `vllm_general_plugin`。

## 10. 单元测试与静态检查

已执行并通过：

```text
go test ./... -count=1
go vet ./...
python -m unittest discover -s adapters/vllm_ascend/tests -v
```

Python 插件单测为 5/5 通过。覆盖点包括：只发送标识符、request ID 提取、后台队列不阻塞、
发送失败计数、`batch_begin`/`batch_end` 配对。

## 11. 未完成与边界

### 11.1 真实插件未激活

`adapters/vllm_ascend` 是 vLLM general plugin，只在进程启动时通过 entry point 加载。当前没有：

- 安装到运行中的 V4 Python 环境；
- 重启 V4 或启动新实例；
- 用真实 vLLM request 生成 `attribution.level=batch` 的证据。

激活需要单独维护窗口，步骤见 `docs/M4-CONTEXT-PROTOCOL.md` 第 6 节。激活前还必须校准容器与宿主
的 `CLOCK_MONOTONIC`，否则 batch 窗口匹配可能整体偏移。

### 11.2 归因级别

`batch` 只表示 host span 与插件 batch 窗口在时间上重叠。一个 ACL 调用可能对应多条 request；
当前没有 request 级 duration 拆分，也没有把 request ID 当作业务内容读取。

### 11.3 设备侧未实现

没有 msPTI collector、没有 `correlationId` join，也没有 kernel / memcpy / HCCL 设备执行时间线。
`aclrtSynchronizeStream` 的 host 阻塞时长不能直接解释为设备 kernel 执行时长。

### 11.4 Orinfer 未验证

Orinfer profile 只完成了 YAML/schema 校验；Orinfer 当前未运行，没有真实 discovery 或 attach。

### 11.5 采样与窗口

`handle` 和 `submit` 按 profile 采样；未观测到某函数不等于该函数从未被调用。10 秒窗口只代表
该窗口，不可外推吞吐、P99 或设备利用率结论。

## 12. 交付判定

本轮可以交付的是：

- M4 的 **vLLM-Ascend host profile、自动发现、37 个 host 探针、10 秒实采证据**；
- **batch 上下文协议**及其非 root UID smoke；
- JSONL / manifest / summary 证据链和可复现脚本；
- vLLM general plugin 的代码、单测和激活说明。

本轮不能交付为“已完成”的是：

- 真实 vLLM 插件请求路径验证；
- 逐 request 执行时长；
- msPTI 设备侧关联；
- Orinfer 真实采集；
- M5/M6 的 A/B 对比和生产化。
