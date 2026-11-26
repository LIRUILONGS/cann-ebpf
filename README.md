# cann-ebpf

昇腾 CANN 主机侧低开销观测工具。当前交付版本为 **`0.4.0-m4`**：

- 37 个 CANN/ACL/HCCL host uprobe 程序，entry/return 配对；
- vLLM-Ascend 自动发现、8 个 `VLLM::Worker_TP` 并行 attach、frontend/EngineCore 覆盖；
- batch 上下文协议与 vLLM general plugin 适配器；
- JSONL、manifest、summary 证据链；
- 采集前后健康检查、uprobe 残留检查和不覆盖旧证据的运行脚本。

> 本版本的 `M4` 指 **vLLM-Ascend host profile + batch 上下文协议**。它仍然不是
> msPTI 设备时间线，也不是逐 request 的执行时间归因。没有插件上下文时，span 会明确
> 标记为 `attribution.level=thread`，不会伪造 batch。

## 当前状态

| 能力 | 状态 |
|---|---|
| M0 配置、发现、profile 匹配、ELF 符号解析 | 完成并在 V4 服务器验证 |
| 37 个 host uprobe / 74 个 entry-exit 程序 | 完成；V4 verifier/attach 通过 |
| V4 自动发现 | 完成；最终发现 10 个目标：8 Worker_TP + frontend + EngineCore |
| V4 自动 attach | 完成；10 秒采集 6,991 spans，8 个 Worker 均有数据 |
| batch 上下文协议 | 完成；非 root UID 982 客户端 smoke 通过 |
| vLLM general plugin | 代码和单测完成；**未装入/重启正在运行的 V4** |
| JSONL / manifest / summary | 完成；manifest 与 JSONL 的 count、bytes、SHA-256 一致 |
| Orinfer profile | 配置校验通过；服务未运行，尚无运行窗口验证 |
| msPTI / 设备 kernel 时间线 | 未实现，属于 M3 |
| 逐 request 执行时长 | 未实现；当前只做 batch 窗口归因 |

## 快速构建

Windows 本机：

```powershell
$env:CGO_ENABLED = "0"
go test ./...
go vet ./...

$env:GOOS = "linux"
$env:GOARCH = "arm64"
go build -buildvcs=false -trimpath -o dist/cann-ebpf-linux-arm64-m4 ./cmd/cann-ebpf
Remove-Item Env:GOOS, Env:GOARCH, Env:CGO_ENABLED
```

当前 Linux/arm64 二进制 SHA-256：

```text
cf441013b88e3545b986eea16fa615be4fd0ca3cb8f4f694577b832812095f03
```

仓库已包含 `internal/probes/bpf/cann.bpf.o`。修改 `cann.bpf.c` 后必须用 clang BPF
target 重新生成对象；不要依赖目标机现编，也不要修改目标进程或 CANN 库。

## CLI

```text
cann-ebpf validate        校验配置或 profile
cann-ebpf discover        只读发现进程、库、符号和 offset
cann-ebpf attach          eBPF uprobe 采集
cann-ebpf attach-tracefs  tracefs 后备采集
cann-ebpf version         版本和平台
```

### 校验 profile

```bash
./cann-ebpf-linux-arm64 validate --config profiles/vllm-ascend.yaml
./cann-ebpf-linux-arm64 validate --config profiles/orinfer.yaml
```

### 只读发现

```bash
./cann-ebpf-linux-arm64 discover \
  --profile vllm-ascend \
  --profiles-dir profiles \
  --dry-run --json > discover.json
```

最终 V4 discovery 发现 10 个目标；每个目标可解析 37 个已实现函数，覆盖
`libruntime.so`、`libascendcl.so`、`libhccl.so`、`libtorch_npu.so` 四类库。
缺失符号只保留为 discovery 信息，不会创建假 attach。

### 自动 attach

```bash
OUT="runs/ebpf-$(date -u +%Y%m%dT%H%M%SZ)"
./cann-ebpf-linux-arm64 attach \
  --pid auto \
  --profile vllm-ascend \
  --profiles-dir profiles \
  --duration 10s \
  --output "$OUT"
```

`vllm-ascend` profile 的 `handle` 探针默认按 `1/128` 采样，避免高频
`aclrtDestroyEvent` 淹没同步和事件 span。同步、event、memory、submit 和 HCCL
使用各自 profile 参数；只有真实触发且通过阈值/采样的调用才写入 JSONL。

### 上下文协议 smoke

```bash
OUT="runs/context-test-$(date -u +%Y%m%dT%H%M%SZ)"
OUT="$OUT" sh scripts/context-smoke.sh
```

脚本会：

1. 选择第一个 `VLLM::Worker_TP`；
2. 启动 collector 和 Unix socket；
3. 切换到目标进程 UID/GID 后发送 `batch_begin` / `batch_end`；
4. 校验 `batch_id`、request IDs、归因字段和 summary；
5. 检查 health、service 状态和 uprobe 残留。

最终 smoke 以 UID `982`、GID `979` 连接 socket，验证了容器同 UID 的
`SO_PEERCRED` 路径，而不是只用 root 自测。

### 自动服务器采集

```bash
OUT="/root/cann-ebpf/m4-20261126/runs/ebpf-auto-10s-$(date -u +%Y%m%dT%H%M%SZ)" \
DURATION=10s \
sh scripts/server_auto_capture.sh
```

脚本拒绝覆盖已有证据目录，并记录开始/结束时间、退出码、前后 health、service
状态和 uprobe 残留。

## 输出

```text
spans.jsonl      原始 host span，一行一个对象
manifest.json    运行环境、目标 PID、库 inode/size/mtime、统计、warnings、SHA-256
summary.json     派生 function/category/PID/context/batch 汇总
summary.md       人类可读派生摘要
```

Span 新增字段：

```json
{
  "start_ns": 10162267042648145,
  "end_ns": 10162267057374196,
  "api": "aclrtSynchronizeEvent",
  "batch_id": "ctx-smoke-1",
  "request_ids": ["req-smoke-1", "req-smoke-2"],
  "attribution": {
    "level": "batch",
    "source": "protocol_smoke",
    "confidence": 1,
    "verdict": "batch_window"
  }
}
```

原始值以 `spans.jsonl` 和 `manifest.json` 为准。summary 不补造时间，不修改原始
事件；内存 span 缓冲达到上限时，summary 从完整 JSONL 重新生成，并在 warnings
中显式记录。

## 最终 V4 实测

环境：`bms-84865758`，openEuler aarch64，内核 `5.10.0-136...`，CANN 9.0.1，
`vllm-ds4-8905.service`，镜像 `ds4/vllm-ascend:v0.23.0rc1-nonroot`。

| 项目 | 结果 |
|---|---|
| discovery | 10 targets；8 Worker_TP + frontend + EngineCore；每个 37/148 symbols |
| eBPF 10 秒 | 6,991 spans；dropped 0；8 个 Worker 均有 span |
| 观测函数 | `SynchronizeStream`、`SynchronizeStreamWithTimeout`、`SynchronizeEvent`、`DestroyEvent`、`RecordEvent`、`QueryEventStatus`；另一次运行观测到 1 条 `HcclReduceScatter` |
| 服务影响 | 采集前后 `/health` 均为 200；service 前后均为 `active` |
| uprobe 清理 | `cann_` 残留计数为 0 |
| 上下文 smoke | 164 spans；21 条 batch span；1 个 batch；2 个 request ID；验证失败项 0 |
| JSONL 完整性 | eBPF 6,991 行，SHA-256 `fb2625ba842c27c2480aa1750035acc6f61769849d31c23af462cd34fdd1c312` |

详细记录见 `docs/M4-VALIDATION.md`，协议见 `docs/M4-CONTEXT-PROTOCOL.md`。

## 已知边界

- 37 个函数已编译和 attach，但一次 10 秒窗口只观测到 6 类真实调用；不能宣称
  37 个函数全部在该窗口触发。
- vLLM plugin 尚未安装到运行中的容器，也未重启 V4 激活；smoke 使用的是同协议、
  同 UID 的合成客户端。真实插件请求级验证需要明确的维护窗口。
- `attribution.level=batch` 表示 span 时间窗与插件 batch 窗口重叠；不是逐 request
  的执行时间拆分。request IDs 是关联键，不是业务内容。
- 如果容器使用独立 time namespace，插件与 host eBPF 的单调时钟可能不一致；上线
  插件前必须做同一命名空间/跨命名空间时钟校准。
- msPTI、kernel/memcpy/HCCL 设备执行时间、correlationId join 未实现。
- Orinfer 当前未运行；profile 只能声明配置校验通过，不能写成真实采集验证。
- tracefs 仍是单 PID 后备路径，本轮没有用 tracefs 重新验证 37 函数。
- 自动 attach 会先解析库、创建 uprobe，10 秒采集的整次进程墙钟时间明显长于采集
  窗口；这是 setup 成本，不能把 wall time 当作采集时长。

## 安全约束

- 不停止、不重启 V4/Orinfer，不修改目标进程、容器、CANN 库、驱动或 sysctl。
- 不覆盖旧证据目录；每次运行生成新目录。
- 不把 prompt、响应、token tensor 或业务文本写入上下文协议。
- 上下文 socket 只接受 collector UID 和显式目标 UID；目录 `0755`，socket 默认
  `0600`，目标为非 root 时 chown 给目标 UID/GID 并设为 `0660`。
- 插件字段采用白名单解码，未知字段直接拒绝。
- 失败时保留真实错误、warnings、dropped events 和 `status=error`，不伪造成功。

## 文档

- `docs/M4-VALIDATION.md`：当前交付的实测数据、复现命令、证据和边界。
- `docs/M4-CONTEXT-PROTOCOL.md`：batch 协议、权限模型、插件安装与激活步骤。
- `docs/M1-VALIDATION.md`：历史 M1 8 探针基线和 tracefs 记录。
- `docs/M1-OUTPUT.md`：JSONL / manifest / summary 证据层契约。
- `docs/M1-PROFILES.md`：profile 匹配和只读 smoke 说明。
- `docs/PLAN.md`：总体技术方案、阶段和采集模型。
- `adapters/vllm_ascend/README.md`：vLLM general plugin 安装与限制。
