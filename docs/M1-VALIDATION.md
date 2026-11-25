# M1 验证记录与交付边界

日期：2026-11-26

本文记录 M1 最终交付件在 2026-11-26 的服务器验证事实、回归基线、尚未覆盖的范围，以及交付时必须遵守的证据边界。本文明确区分 eBPF 与 tracefs 两条实测路径，也明确区分 M1 host 侧观测与 M3/M4 的设备侧、请求级能力。

## 1. 结论摘要

| 项目 | 状态 | 可交付表述 |
|---|---|---|
| M0 discovery | 已在 V4 服务器只读验证 | 18 个目标、72 个库路径、666 个可用符号；未 attach |
| 交付二进制 | 已验证 | `version=0.1.0-m1 linux/arm64`；SHA256 `eaa661e7856a4776ff86c55648068785cd7d4be8baf4612e2e5fde69f0b09cf4` |
| profile validate | 已验证 | `vllm-ascend`、`orinfer` 两份 profile 均 OK |
| V4 服务 | 全程 active | `vllm-ds4-8905.service`；采集前后 `/health` 均 HTTP 200 |
| eBPF 单 PID | 已验证 | PID 1452，2 秒 70 spans |
| eBPF 8 Worker | 已验证 | 最终件 30 秒 2088 spans；回归批次 2 秒 487 spans、30 秒 2665 spans |
| tracefs 单 PID | 已验证 | PID 1452，`--only sync`，10 秒 199 spans |
| uprobe 清理 | 已验证 | `cann_` 在 `uprobe_events` 中无残留 |
| M1 探针覆盖 | 部分覆盖 | 只有 host 侧 8 个核心函数探针；缺少列出的 HCCL 函数及 memory/submit/event 等完整程序 |
| 请求级归因 | 未实现 | 没有 batch/request 级归因 |
| msPTI / 设备侧时间线 | 未实现 | M1 只回答 host 侧调用耗时，不回答设备 kernel 执行时间 |
| Orinfer | 未运行，未验证 | 不能用 V4 进程替代，也不能把 Orinfer 写成已完成 |

## 2. 验证环境

| 项 | 值 | 来源/说明 |
|---|---|---|
| 服务器 | `bms-84865758` | `docs/M0-VALIDATION.md` |
| 架构 | `aarch64` | `docs/M0-VALIDATION.md` |
| 内核 | `5.10.0-136.108.0.188.31.oe2203sp1.bclinux.aarch64` | 现场交接记录；仓库文档记录为 openEuler 5.10 |
| 系统 | openEuler 5.10 | `docs/M1-PROFILES.md` |
| V4 服务 | `vllm-ds4-8905.service` | `docs/M0-VALIDATION.md` |
| V4 容器/镜像 | `deepseek-v4-flash-8905` / `ds4/vllm-ascend:v0.23.0rc1-nonroot` | `docs/M0-VALIDATION.md` |
| 目标进程 | `VLLM::Worker_TP` PID `1452` | `docs/M1-PROFILES.md` |
| BTF | 存在 | 现场预检记录 |
| `perf_event_paranoid` | `2` | 现场预检记录 |
| `unprivileged_bpf_disabled` | `0` | 现场预检记录 |
| tracefs/debugfs | 已挂载，`uprobe_events` 可写 | `docs/M1-PROFILES.md` |
| 最终验证批次 | `final-20261126` | 最终二进制与最终运行证据 |
| 回归验证批次 | `2026-11-26b` | 修复失败路径 manifest 状态之前的成功路径回归 |
| 运行包 | `dist/cann-ebpf-m1-runtime-linux-arm64.tar.gz`；SHA256 `b30b5b911057d601d1192c5936985da9c18bbe6e7ce31849ed20e3ad8394a265` | 最终二进制 + configs/profiles/schema/scripts |
| 最终证据包 | `dist/m1-final-evidence-20261126.tar.gz`；SHA256 `5081a35218bae32b3ddb692c54d372010e1811eafe6dacfd240def1f0c341463` | 最终件 8 worker 30 秒原始输出 |
| 最终证据目录 | `/root/cann-ebpf/m1-final-20261126/runs/ebpf-all-workers-30s` | 见第 4 节 |
| 回归证据包 | `dist/m1-evidence-20261126.tar.gz`；SHA256 `1dc081db57ecacbbe3e7eada6afe1f605c81f20700db8df3f120da1f647a9d1a` | 单 PID、2 秒和 tracefs 回归 |

PID 只用于本次基线复核。profile 不写死 PID，服务重启后必须重新按 `comm`、`cmdline`、`maps` 和容器特征发现目标。

## 3. 两个后端的边界

### 3.1 tracefs 后备后端

`attach-tracefs` 通过 tracefs 临时创建 `p:` entry uprobe 和 `r:` return uprobe，从 `trace_pipe` 读取事件，按 TID 配对 entry/return，并过滤到目标 PID。

- 已验证：V4 PID 1452、`--only sync`、10 秒采集得到 199 spans；采集前后 `/health` 均为 HTTP 200。
- 已验证：采集结束后临时 uprobe 已清理；现场检查 `cann_` 在 `uprobe_events` 中无残留。
- 限制：tracefs 后端一次只 attach 一个 PID；8 Worker 并行覆盖由 eBPF 路径完成。
- 限制：临时 uprobe 会短暂修改 tracefs 事件列表，但不修改目标进程、CANN 库或服务配置；结束时必须恢复现场。

### 3.2 eBPF 后端

`internal/probes/bpf/cann.bpf.c`、嵌入的 `cann.bpf.o`、loader、ringbuf reader 和 attach API 已实现。当前对象包含 8 个核心函数，对应 16 个 entry/exit 程序。

- 已实现：目标进程匹配、CANN 库/符号解析、uprobe attach、entry/return 配对、ringbuf 消费、runlog 输出。
- 已在本机验证：`go test ./...` 通过；`linux/arm64` 交叉编译路径可用。
- 服务器状态：**已执行并成功**。最终件 8 Worker 30 秒 2088 spans；回归批次 PID 1452 2 秒 70 spans、8 Worker 2 秒 487 spans、8 Worker 30 秒 2665 spans；服务全程 active，采集前后 health 200。
- 覆盖边界：M1 只有 host 侧 8 个核心函数的 entry/exit 探针，不能把 profile 中的完整 `event`/`submit`/`memory`/`HCCL` 清单视为已实现。缺失项包括 `HcclAllGather`、`HcclAlltoAll`、`HcclBroadcast`、`HcclRecv`、`HcclReduceScatter`、`HcclSend`，以及 memory/submit/event 等完整程序。
- 归因边界：没有 batch/request 级归因；没有 msPTI 或设备侧 timeline。M1 不是 M4。

### 3.3 时间语义（profile 设计语义）

| 层 | M1 能回答的问题 | 不能回答的问题 |
|---|---|---|
| H1 `sync` | host 线程在同步调用中等待了多久 | 设备 kernel 真正执行了多久 |
| H2 `event`/`submit` | host 入队、轮询、依赖等待了多久 | 设备是否已经完成 |
| M `memory`/handle | host 侧分配、释放、句柄生命周期 | 设备内部内存带宽和算子耗时 |
| C `HCCL` | host 侧通信调用入队耗时 | 设备侧 HCCL timeline |
| 设备侧 timeline | 不属于 M1 | 需要 M3 的 msPTI join 后才能回答 |

上表解释的是 profile 各层的观测语义，不代表 M1 已经实现全部对应程序。当前 M1 只有 host 侧 8 个核心函数的探针，缺失 `HcclAllGather`、`HcclAlltoAll`、`HcclBroadcast`、`HcclRecv`、`HcclReduceScatter`、`HcclSend` 以及 memory/submit/event 等完整程序。M1 profile 中保留的 `mspti` 配置是兼容 M0 的配置，不代表 M1 已初始化 msPTI。

## 4. V4 服务器实测结果

### 4.0 最终交付件验证（final-20261126）

最终二进制已重新构建，包含失败路径 manifest 状态修正；该二进制在 V4 上重新完成 8 worker 30 秒采集。

| 项 | 值 |
|---|---|
| 二进制 SHA256 | `eaa661e7856a4776ff86c55648068785cd7d4be8baf4612e2e5fde69f0b09cf4` |
| 运行包 SHA256 | `b30b5b911057d601d1192c5936985da9c18bbe6e7ce31849ed20e3ad8394a265` |
| 目标 | `1452,3962,6568,8890,11340,13505,15629,17758` |
| 时长 | 30 秒 |
| spans | 2088 |
| 服务 | 采集前后均 `active`，health 均为 HTTP 200 |
| uprobe 清理 | `cann_` 无残留 |
| 证据包 | `dist/m1-final-evidence-20261126.tar.gz`；SHA256 `5081a35218bae32b3ddb692c54d372010e1811eafe6dacfd240def1f0c341463` |

最终件 8 个 PID 的 span 分布：

| PID | spans |
|---|---:|
| 1452 | 450 |
| 3962 | 248 |
| 6568 | 242 |
| 8890 | 237 |
| 11340 | 234 |
| 13505 | 231 |
| 15629 | 225 |
| 17758 | 221 |

最终件统计：

| 函数/汇总 | count | p50 | p95 | p99 | max |
|---|---:|---:|---:|---:|---:|
| `aclrtSynchronizeEvent` | 1730 | 32,782,513 | 130,517,108 | 170,624,322 | 204,527,871 |
| `aclrtSynchronizeStreamWithTimeout` | 357 | 17,906,260 | 19,354,868 | 22,559,386 | 24,733,158 |
| `aclrtRecordEvent` | 1 | 64,079,798 | 64,079,798 | 64,079,798 | 64,079,798 |
| `sync` 汇总 | 2087 | 24,434,187 | 121,120,836 | 167,697,516 | 204,527,871 |
| `event` 汇总 | 1 | 64,079,798 | 64,079,798 | 64,079,798 | 64,079,798 |

### 4.1 回归批次运行矩阵（2026-11-26b）

| 证据目录 | 后端 | 目标 | 采集参数 | 时长 | spans | 采集后 health |
|---|---|---|---|---:|---:|---|
| `ebpf-1452-2s` | eBPF | PID `1452` | 单 PID | 2 秒 | 70 | HTTP 200 |
| `ebpf-all-workers-2s` | eBPF | 8 个 Worker_TP | 多 PID | 2 秒 | 487 | HTTP 200 |
| `ebpf-all-workers-30s` | eBPF | 8 个 Worker_TP | 多 PID | 30 秒 | 2665 | HTTP 200 |
| `tracefs-1452-sync-10s` | tracefs | PID `1452` | `--only sync` | 10 秒 | 199 | HTTP 200 |

8 个 Worker_TP PID：`1452,3962,6568,8890,11340,13505,15629,17758`。

30 秒运行中，8 个 PID 均有独立 span，不是只 attach 成功但没有数据：

| PID | spans |
|---|---:|
| 1452 | 627 |
| 3962 | 312 |
| 6568 | 312 |
| 8890 | 296 |
| 11340 | 291 |
| 13505 | 285 |
| 15629 | 272 |
| 17758 | 270 |

运行期间 `vllm-ds4-8905.service` 全程为 `active`；采集前后 `/health` 均为 HTTP 200。采集结束后检查 `cann_` 在 `uprobe_events` 中无残留。

### 4.2 统计结果

下表数字来自本轮运行的 summary；单位均为 ns。

| 运行 | 函数/汇总 | count | p50 | p95 | p99 | max |
|---|---|---:|---:|---:|---:|---:|
| `ebpf-1452-2s` | `aclrtSynchronizeEvent` | 70 | 36,319,323 | 41,919,894 | 42,207,046 | 42,207,046 |
| `tracefs-1452-sync-10s` | `aclrtSynchronizeEvent` | 125 | 28,000 | 74,649,000 | 93,906,000 | 94,406,000 |
| `tracefs-1452-sync-10s` | `aclrtSynchronizeStreamWithTimeout` | 74 | 30,000 | 18,037,000 | 18,258,000 | 18,258,000 |
| `tracefs-1452-sync-10s` | `sync` 汇总 | 199 | 30,000 | 69,659,000 | 93,906,000 | 94,406,000 |
| `ebpf-all-workers-30s` | `aclrtSynchronizeEvent` | 2362 | 36,209,363 | 51,901,970 | 78,996,601 | 118,362,260 |
| `ebpf-all-workers-30s` | `aclrtSynchronizeStreamWithTimeout` | 303 | 17,493,917 | 18,282,322 | 18,415,493 | 18,548,373 |
| `ebpf-all-workers-30s` | `sync` 汇总 | 2665 | 35,596,529 | 49,172,615 | 78,475,239 | 118,362,260 |

`ebpf-all-workers-2s` 只记录到总数 487 spans，未提供按函数拆分的数据，因此这里不补造分位数。

### 4.3 证据与哈希

| 对象 | 值 |
|---|---|
| 最终服务器证据目录 | `/root/cann-ebpf/m1-final-20261126/runs/ebpf-all-workers-30s` |
| 回归服务器证据目录 | `/root/cann-ebpf/m1-20261126b/runs/` |
| 回归运行目录 | `ebpf-1452-2s`、`ebpf-all-workers-2s`、`ebpf-all-workers-30s`、`tracefs-1452-sync-10s` |
| 最终证据包 | `dist/m1-final-evidence-20261126.tar.gz` |
| 最终证据包 SHA256 | `5081a35218bae32b3ddb692c54d372010e1811eafe6dacfd240def1f0c341463` |
| 回归证据包 | `dist/m1-evidence-20261126.tar.gz` |
| 回归证据包 SHA256 | `1dc081db57ecacbbe3e7eada6afe1f605c81f20700db8df3f120da1f647a9d1a` |
| 运行包 SHA256 | `b30b5b911057d601d1192c5936985da9c18bbe6e7ce31849ed20e3ad8394a265` |
| 二进制 SHA256 | `eaa661e7856a4776ff86c55648068785cd7d4be8baf4612e2e5fde69f0b09cf4` |
| 二进制版本 | `0.1.0-m1 linux/arm64` |

### 4.4 复现命令

以下命令用于复现最终件结果；执行前必须确认 PID 仍匹配 profile，并保留原始证据目录，不得覆盖或改写已有运行。

```bash
cd /root/cann-ebpf/m1-final-20261126

./cann-ebpf-linux-arm64 validate --config profiles/vllm-ascend.yaml
./cann-ebpf-linux-arm64 validate --config profiles/orinfer.yaml

PIDS=$(ps -eo pid=,comm= | awk '$2=="VLLM::Worker_TP"{print $1}' | paste -sd, -)

./cann-ebpf-linux-arm64 attach \
  --pid "$PIDS" \
  --profile vllm-ascend \
  --profiles-dir profiles \
  --duration 30s \
  --output /root/cann-ebpf/m1-final-20261126/runs/ebpf-all-workers-30s-replay

# tracefs 后备路径（可选）
./cann-ebpf-linux-arm64 attach-tracefs \
  --pid "$(printf '%s' "$PIDS" | cut -d, -f1)" \
  --profile vllm-ascend \
  --profiles-dir profiles \
  --only sync \
  --duration 10s \
  --output /root/cann-ebpf/m1-final-20261126/runs/tracefs-sync-10s
```

采集前后检查 `/health`，具体地址使用现场端口；验收值必须是 HTTP 200。结束后检查清理：

```bash
grep cann_ /sys/kernel/tracing/uprobe_events
# 期望：无输出
```

如果 tracefs 路径与当前内核挂载点不同，使用 `/sys/kernel/debug/tracing/uprobe_events`；两个路径的选择必须记录在运行日志中。

### 4.5 历史短时记录

早期文档中的 3 秒 166 spans 和 2 秒 80 spans 属于历史短时记录，不替代最终件结果。交付时以 4.0 节最终件和 4.3 节最终哈希为准；4.1–4.2 的 2665 spans 是前一构建的回归基线。

## 5. eBPF 服务器验证状态

| 验证项 | 状态 | 证据 |
|---|---|---|
| BPF 对象嵌入 | 完成 | 对象文件和 loader 已存在，`go test ./...` 通过 |
| 16 个 entry/exit 程序 | 完成（代码层） | 8 个核心函数 × 2 |
| Linux/arm64 构建 | 已验证 | `version=0.1.0-m1 linux/arm64`；SHA256 `eaa661e7856a4776ff86c55648068785cd7d4be8baf4612e2e5fde69f0b09cf4` |
| 服务器 verifier/attach | 已验证 | PID 1452、8 Worker 的 2 秒和 30 秒采集均完成 |
| 服务器 health 回归 | 已验证 | `vllm-ds4-8905.service` 全程 active；采集前后 health 均 200 |
| tracefs/uprobe 清理 | 已验证 | `cann_` 在 `uprobe_events` 中无残留 |
| 探针范围 | 部分覆盖 | 只有 host 侧 8 个核心函数；缺失 `HcclAllGather`、`HcclAlltoAll`、`HcclBroadcast`、`HcclRecv`、`HcclReduceScatter`、`HcclSend` 以及 memory/submit/event 等完整程序 |
| 请求级归因 | 未实现 | 没有 batch/request 级归因 |
| msPTI/设备侧 | 未实现 | M1 不提供设备 kernel timeline；不能把 M1 写成 M4 |

### 5.1 eBPF 结果读取

单 PID 2 秒：70 spans，`aclrtSynchronizeEvent` 的 p50/p95/p99/max 为 `36,319,323 / 41,919,894 / 42,207,046 / 42,207,046 ns`。

最终件 8 Worker 30 秒：2088 spans，其中 `sync` 2087、`event` 1；完整统计见 4.0。回归批次 2 秒为 487 spans、30 秒为 2665 spans，统计见 4.1–4.2。这里不把 8 个 worker 的数字解释为设备执行时间，也不将其外推为吞吐或丢事件结论。

## 6. 构建与 CLI

### 6.1 Linux/arm64 构建

Linux/macOS shell：

```bash
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 \
  go build -buildvcs=false -trimpath -ldflags "-s -w" \
  -o dist/cann-ebpf-linux-arm64 ./cmd/cann-ebpf
```

PowerShell：

```powershell
$env:GOOS = "linux"
$env:GOARCH = "arm64"
$env:CGO_ENABLED = "0"
go build -buildvcs=false -trimpath -ldflags "-s -w" -o dist/cann-ebpf-linux-arm64 ./cmd/cann-ebpf
Remove-Item Env:GOOS, Env:GOARCH, Env:CGO_ENABLED
```

构建后应在目标机确认架构：

```bash
file /root/cann-ebpf/cann-ebpf-linux-arm64
/root/cann-ebpf/cann-ebpf-linux-arm64 version
```

### 6.2 CLI 子命令

| 子命令 | 用途 | 当前状态 |
|---|---|---|
| `validate` | 校验 profile 和 JSON Schema | 已验证 |
| `discover --dry-run` | 只读发现目标、库、符号和 offset | V4 已验证 |
| `attach-tracefs` | tracefs 后备采集，单 PID | V4 已验证 |
| `attach` | eBPF attach 和 ringbuf 采集 | V4 已验证；最终件 8 Worker 30 秒 2088 spans；回归单 PID 70、8 Worker 487/2665 spans |
| `version` | 打印版本、OS、架构 | 可用 |

## 7. 覆盖范围与未覆盖项

| 项目 | 已验证范围 | 未验证/交付限制 |
|---|---|---|
| V4 Worker_TP | 8 个 PID 在 eBPF 2 秒和 30 秒采集完成 | frontend/EngineCore 只做 discovery，未做 attach 基线 |
| 8 Worker 并行 | 已验证；最终件 30 秒 2088 spans，回归 2 秒 487 spans、30 秒 2665 spans | 不能外推为吞吐、丢事件或设备执行结论 |
| 30 秒采集 | 已验证；服务全程 active，health 前后 200 | 未做长期稳定性和 overhead 量化 |
| tracefs 事件残留 | 已验证；`cann_` 在 `uprobe_events` 中无残留 | 下一轮仍必须重新检查 |
| eBPF verifier/attach | 已验证；单 PID 与 8 Worker 均完成 | 不能把 tracefs 单 PID 覆盖说成 8 Worker |
| M1 探针范围 | host 侧 8 个核心函数 | 缺 `HcclAllGather`、`HcclAlltoAll`、`HcclBroadcast`、`HcclRecv`、`HcclReduceScatter`、`HcclSend` 及 memory/submit/event 等完整程序 |
| 请求级归因 | 未实现 | 没有 batch/request 级归因 |
| Orinfer | 当前未运行；历史 PID 1691816 已不存在 | 不得用 V4 目标冒充 Orinfer |
| msPTI/设备侧 | 未实现 | 需要 M3，不属于 M1 |
| M4 | 未完成 | 不能把 M1 写成 M4 |

## 8. 证据不可修改原则

1. `spans.jsonl` 和 `manifest.json` 是原始运行证据。采集完成后不得人工改写、裁剪、补字段、改 PID、改耗时或“修正”异常。
2. `summary.md` / `summary.json` 只能由原始 span 派生；派生文件必须能与原始 JSONL 对应，不能替代原始证据。
3. 采集失败时记录真实错误、`warnings`、`dropped_events` 和失败状态，不伪造成功运行。
4. 不修改目标服务、容器、CANN 库或驱动；不重启 V4/Orinfer 来“修复”采集结果。
5. tracefs 临时 uprobe 必须在结束、超时或失败路径删除；不能留下 `cann_<pid>_*` 事件。
6. 交付文档不得把 tracefs 后备路径的结果写成完整 eBPF 结果，也不得把 M1 host span 写成设备 kernel timeline。

## 9. 交付口径

本轮可以交付的事实是：

- `vllm-ascend` 与 `orinfer` 两份 profile 的 `validate` 均 OK；
- 交付二进制为 `0.1.0-m1 linux/arm64`，SHA256 为 `eaa661e7856a4776ff86c55648068785cd7d4be8baf4612e2e5fde69f0b09cf4`；
- eBPF 单 PID 1452 采集 2 秒得到 70 spans；
- 最终件 eBPF 8 Worker 采集 30 秒得到 2088 spans；回归批次 2 秒 487 spans、30 秒 2665 spans；
- tracefs 单 PID 1452 使用 `--only sync` 采集 10 秒得到 199 spans；
- `vllm-ds4-8905.service` 全程 active，采集前后 `/health` 均为 HTTP 200；
- 现场检查 `cann_` 在 `uprobe_events` 中无残留；
- 最终件服务器证据目录为 `/root/cann-ebpf/m1-final-20261126/runs/ebpf-all-workers-30s`；
- 最终证据包为 `dist/m1-final-evidence-20261126.tar.gz`，SHA256 为 `5081a35218bae32b3ddb692c54d372010e1811eafe6dacfd240def1f0c341463`；
- 回归证据目录为 `/root/cann-ebpf/m1-20261126b/runs/{ebpf-1452-2s,ebpf-all-workers-2s,ebpf-all-workers-30s,tracefs-1452-sync-10s}`，回归证据包 SHA256 为 `1dc081db57ecacbbe3e7eada6afe1f605c81f20700db8df3f120da1f647a9d1a`。

必须同时保留的边界：M1 只有 host 侧 8 个核心探针，缺少上述 HCCL 函数及 memory/submit/event 等完整程序；没有请求级归因；没有 msPTI/设备侧；Orinfer 未运行未验证。不能用 M1 的结果替代 M4，也不能把 host span 解释成设备 kernel 执行时间。

## 10. 交付前校验清单

- [x] `go test ./... -count=1` 通过
- [x] `go vet ./...` 通过
- [x] Linux/arm64 静态构建通过
- [x] `vllm-ascend`、`orinfer` profile validate 通过
- [x] 单 PID、8 Worker、30 秒 eBPF 采集通过
- [x] tracefs 单 PID 采集通过
- [x] 采集前后 health 均为 200，服务保持 active
- [x] `uprobe_events` 无 `cann_` 残留
- [x] 原始证据归档并计算 SHA-256
- [ ] Orinfer 真实运行窗口验证
- [ ] msPTI 设备侧 join、请求级归因和生产化可观测性
