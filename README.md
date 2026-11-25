# cann-ebpf

昇腾 CANN 主机侧低开销观测工具。当前交付版本为 **M1**：完成目标发现、uprobe
eBPF 采集、tracefs 后备采集、entry/return 配对、ringbuf 消费、JSONL 证据落盘和
summary 派生汇总。

> M1 只回答 **host 侧 CANN API 调用耗时**。它不是 msPTI 设备时间线，也不是请求级
> 归因；不能把本版本描述成 M3/M4。

## 当前状态

| 能力 | 状态 |
|---|---|
| M0 配置、进程发现、profile 匹配、ELF 符号解析 | 完成并在 V4 服务器验证 |
| M1 eBPF 后端 | 完成；V4 单 worker、8 worker、30 秒采集均通过 |
| M1 tracefs 后备后端 | 完成；V4 单 worker 已通过 |
| JSONL / manifest / summary | 完成；由 `internal/runlog` 生成 |
| V4 `vllm-ascend` profile | 已验证 8 个 `VLLM::Worker_TP` 并行采集 |
| Orinfer profile | 配置可校验；服务未运行，尚无真实运行窗口验证 |
| 请求级归因、msPTI 设备侧关联 | 未实现，属于后续里程碑 |

## 快速构建

Windows 本机测试：

```powershell
go test ./...
go vet ./...
```

Linux/arm64 静态二进制：

```powershell
$env:GOOS="linux"
$env:GOARCH="arm64"
$env:CGO_ENABLED="0"
go build -buildvcs=false -trimpath -ldflags "-s -w" -o dist/cann-ebpf-linux-arm64 ./cmd/cann-ebpf
Remove-Item Env:GOOS,Env:GOARCH,Env:CGO_ENABLED
```

仓库已包含 `internal/probes/bpf/cann.bpf.o`。修改 `cann.bpf.c` 后按文件头部注释用
clang BPF target 重新生成对象；不应在运行时修改目标进程或 CANN 库。

## CLI

```text
cann-ebpf validate       校验配置或 profile
cann-ebpf discover       只读发现目标进程、库和符号
cann-ebpf attach         eBPF uprobe 采集
cann-ebpf attach-tracefs tracefs 后备采集
cann-ebpf version        版本和平台
```

### 发现

```bash
./cann-ebpf-linux-arm64 validate --config profiles/vllm-ascend.yaml
./cann-ebpf-linux-arm64 discover \
  --profile vllm-ascend \
  --profiles-dir profiles \
  --dry-run --json
```

### eBPF：单 worker

```bash
./cann-ebpf-linux-arm64 attach \
  --pid 1452 \
  --profile vllm-ascend \
  --profiles-dir profiles \
  --duration 10s \
  --output runs/ebpf-1452
```

### eBPF：8 个 Worker_TP 并行

```bash
PIDS=$(ps -eo pid=,comm= | awk '$2=="VLLM::Worker_TP"{print $1}' | paste -sd, -)
./cann-ebpf-linux-arm64 attach \
  --pid "$PIDS" \
  --profile vllm-ascend \
  --profiles-dir profiles \
  --duration 30s \
  --output runs/ebpf-all-workers
```

### tracefs 后备：单 PID

```bash
./cann-ebpf-linux-arm64 attach-tracefs \
  --pid 1452 \
  --profile vllm-ascend \
  --profiles-dir profiles \
  --only sync \
  --duration 10s \
  --output runs/tracefs-1452
```

## 输出

每次运行生成：

```text
spans.jsonl      原始 span 事件，一行一个对象
manifest.json    运行环境、目标 PID、库 inode/size/mtime、统计和 SHA-256
summary.json     按 function/category 的 count、p50、p95、p99、max、total
summary.md       人类可读派生摘要
```

原始数值以 `spans.jsonl` 和 `manifest.json` 为准；summary 不补造缺失值，不修改原始事件。

## 已验证基线

环境：openEuler aarch64，内核 5.10，V4 `vllm-ds4-8905.service`，CANN 9.0.1。

| 后端 | 目标 | 时长 | spans | 采集后 health |
|---|---|---:|---:|---|
| eBPF | PID 1452 单 worker | 2s | 70 | 200 |
| eBPF | 8 个 Worker_TP | 2s | 487 | 200 |
| eBPF | 8 个 Worker_TP（最终件） | 30s | 2,088 | 200 |
| tracefs | PID 1452，`sync` | 10s | 199 | 200 |

上述运行期间服务保持 `active`，采集结束后检查 `/sys/kernel/tracing/uprobe_events`，
未发现 `cann_` 残留。详细数据和边界见 `docs/M1-VALIDATION.md`。

最终件 30 秒运行的 2,088 条 span 覆盖全部 8 个 worker，单 PID 分布为
`1452=450, 3962=248, 6568=242, 8890=237, 11340=234, 13505=231, 15629=225, 17758=221`。
该次运行还捕获到 1 条 `aclrtRecordEvent`。最终二进制 SHA256 为
`eaa661e7856a4776ff86c55648068785cd7d4be8baf4612e2e5fde69f0b09cf4`。

## 已知边界

- M1 已编译 8 类核心程序：`aclrtSynchronizeStream`、
  `aclrtSynchronizeStreamWithTimeout`、`aclrtSynchronizeEvent`、
  `aclrtRecordEvent`、`aclrtStreamWaitEvent`、`aclrtMemcpyAsync`、
  `aclrtLaunchKernel`、`HcclAllReduce`。
- profile 中还有 memory、HCCL 扩展、event 查询等符号；当前 M1 会显式输出
  `M1 has no probe program for: ...`，不会静默声称已覆盖。
- `aclrtSynchronize*` 的 span 是 host 线程真实等待时间；`submit`/`event` 的入口耗时不等于
  设备 kernel 执行时间。
- 当前没有请求 ID，不能把 TID 级数据写成请求级结论。
- Orinfer 未运行，不能用 V4 进程替代 Orinfer 验证。
- tracefs 后端当前一次只支持一个 PID；多 worker 并行验证使用 eBPF 后端。

## 安全约束

- 不启动、停止或重启 V4/Orinfer；不修改宿主 CANN、驱动或容器镜像。
- 不修改原始证据、不脱敏、不补造缺失值。
- tracefs 临时事件和 eBPF link 必须在退出时清理；失败时保留完整错误。

