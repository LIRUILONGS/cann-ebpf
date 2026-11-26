# cann-ebpf

> 交接文档：`docs/HANDOFF.md`（项目地址、已完成内容、下一步工作方向、硬约束）。

昇腾 CANN 主机侧低开销观测工具。当前交付版本为 **`0.6.0-m2`**，并已合入 M3 设备时间线采集器：

- 37 个 CANN/ACL/HCCL host uprobe 程序，entry/return 配对；
- vLLM-Ascend 自动发现、8 个 `VLLM::Worker_TP` 并行 attach、frontend/EngineCore 覆盖；
- batch 上下文协议、可选 vLLM general plugin 适配器，以及不依赖插件的纯 eBPF batch 采集；
- JSONL、manifest、summary 证据链；
- 只读 `diagnose` 定位命令（JSON/Markdown，不 attach）；
- M2 host off-CPU/futex（`sched_switch` + futex syscall tracepoint，已在 V4 和合成锁竞争上验证）；
- E 层 npu-smi 硬件采样（HBM/AICore/功耗/温度，已上机验证）；
- M3 device 时间线 join 管线 + in-process msPTI collector（合成 ACL 已验证，生产注入待维护窗口）；
- 采集前后健康检查、uprobe 残留检查和不覆盖旧证据的运行脚本。

> 本版本的 `M4` 指 **vLLM-Ascend host profile + batch 上下文协议**。它仍然不是
> msPTI 设备时间线，也不是逐 request 的执行时间归因。没有插件上下文时，host span 会明确
> 标记为 `attribution.level=thread`；纯 eBPF batch span 单独标记为
> `attribution.source=ebpf_scheduler_output_dict`，不会伪造 request 级数据。

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
| 链路追踪 | 完成；batch trace 传播、summary trace 覆盖、trace 链路树 |
| 纯 eBPF CPython POC | 完成；合成 Python 3.12 和真实 V4 Worker 1452 均验证通过 |
| 纯 eBPF vLLM batch 标准采集 | 完成；`attach --python-batch` 已并入 `spans.jsonl`/`summary`，真实 V4 验证 10 个 batch、6 个 request ID，类型确认为 `SchedulerOutput` |
| `diagnose` 只读定位命令 | 完成；JSON `cann.diagnose.report.v1` + Markdown，单测覆盖全部过滤与判定分支，并已在 V4 真实 evidence 上只读运行 |
| M2 host off-CPU/futex | 完成并上机验证；V4 10 秒 `attached=4 failed=0`，合成锁竞争 `host_lock_contention` 1.00 |
| E 层 npu-smi 硬件指标 | 完成并上机验证；8 设备 HBM/AICore/功耗/温度，`hbm_pressure` |
| `preflight` 环境检查 | 完成并上机验证；V4 pass=15 warn=0 fail=0 |
| M2 开销 | agent +0.28pp 单核 CPU、+1.3MB RSS、0 丢事件；固定负载吞吐门禁未做 |
| M3 device 时间线 join | join/schema/diagnose 完成；真实 V4 span + 合成时间线 100% 匹配；`msprof --pid` 段错误，生产端待维护窗口 |
| M3 msPTI collector | 完成并在合成 ACL 验证；final7 慢 Sync 305/305（100%）；真实 V4/Orinfer 注入待维护窗口 |
| Orinfer profile | 配置校验通过；服务未运行，尚无运行窗口验证 |
| msPTI / 设备 kernel 时间线 | 代码 + 合成验证完成；真实 V4/Orinfer 设备时间线待维护窗口 |
| 逐 request 执行时长 | 未实现；当前只做 batch 窗口归因 |
| `execute_model` 函数级耗时 | 未实现；本轮不激活插件 |

## 快速构建

Windows 本机：

```powershell
$env:CGO_ENABLED = "0"
go test ./...
go vet ./...

$env:GOOS = "linux"
$env:GOARCH = "arm64"
go build -buildvcs=false -trimpath -o dist/cann-ebpf-linux-arm64-m3 ./cmd/cann-ebpf
go build -buildvcs=false -trimpath -o dist/cpy-poc-linux-arm64 ./cmd/cpy-poc
go build -buildvcs=false -trimpath -o dist/cpy-batch-poc-linux-arm64 ./cmd/cpy-batch-poc
Remove-Item Env:GOOS, Env:GOARCH, Env:CGO_ENABLED
```

当前 Linux/arm64 二进制 SHA-256 以交付包 `SHA256SUMS` 为准，构建后不得手工修改：

```text
cann-ebpf-linux-arm64-m3  见 dist/SHA256SUMS.txt（本次 M3 构建）
cpy-poc-linux-arm64       08a5f05f92c15dd2d4b6475805ebbb3bfc3188b024c900745cb604a30826be53
cpy-batch-poc-linux-arm64 见 batch POC 交付目录 SHA256SUMS
```

`cann-ebpf-linux-arm64-m3` 已在 `bms-84865758` 上完成 V4、合成锁竞争、硬件层、preflight 和
M3 合成 ACL 验证；M3 生产注入仍待维护窗口，详见 `docs/M3-FEASIBILITY.md`、`docs/M3-VALIDATION.md`。

仓库已包含 `internal/probes/bpf/cann.bpf.o`。修改 `cann.bpf.c` 后必须用 clang BPF
target 重新生成对象；不要依赖目标机现编，也不要修改目标进程或 CANN 库。

## CLI

```text
cann-ebpf validate        校验配置或 profile
cann-ebpf preflight       只读环境检查（根/追踪点/权限/npu-smi）
cann-ebpf discover        只读发现进程、库、符号和 offset
cann-ebpf attach          eBPF uprobe 采集
cann-ebpf attach-tracefs  tracefs 后备采集
cann-ebpf trace           按 trace_id/parent 聚合 spans.jsonl
cann-ebpf diagnose        只读定位报告（JSON/Markdown）
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

### 纯 eBPF batch 采集（不装插件）

```bash
OUT="runs/ebpf-batch-$(date -u +%Y%m%dT%H%M%SZ)"
./cann-ebpf-linux-arm64 attach \
  --pid auto \
  --profile vllm-ascend \
  --profiles-dir profiles \
  --duration 10s \
  --python-batch \
  --no-context \
  --output "$OUT"
```

`--python-batch` 是可选的短时只读 uprobe。它不写目标进程、不安装 Python 包、不重启
V4，只读取 `SchedulerOutput.num_scheduled_tokens` 的请求 ID 键和 dict 身份。
`PyObject_GetAttr` 是高频 C API，默认不开启；`--no-context` 明确关闭插件 socket，
只保留纯 eBPF span。开启后会为每个命中 Worker 写一份
`python-batch-<pid>.json`，同时把 batch 窗口作为标准 `category=batch` span 写入
同一份 `spans.jsonl`。

### 只读定位：`diagnose`

```bash
# 直接读已有证据，不 attach
./cann-ebpf-linux-arm64-m2 diagnose --input runs/ebpf-20261126T000000Z
./cann-ebpf-linux-arm64-m2 diagnose --input runs/ebpf-... --pid 1452 --last 2s
./cann-ebpf-linux-arm64-m2 diagnose --input runs/ebpf-... --batch-id b-3 --json --out diagnose.json
```

输出命中 PID/TID/batch/request、top slow function/category、host off-CPU/futex 比例、证据 span，
以及“当前无法判定”的原因（缺 off-CPU、缺 msPTI、batch 非 request、插件未激活）。
时间窗使用与 span 相同的单调时钟 ns，详见 `docs/DIAGNOSE.md`。

### M2 host off-CPU/futex 采集

```bash
OUT="runs/ebpf-m2-$(date -u +%Y%m%dT%H%M%SZ)"
./cann-ebpf-linux-arm64-m2 attach \
  --pid auto \
  --profile vllm-ascend \
  --profiles-dir profiles \
  --duration 10s \
  --blocking on \
  --no-context \
  --output "$OUT"
```

`--blocking auto|on|off`：`auto` 跟随 profile（`vllm-ascend`/`orinfer` 当前为 true），`on` 强制开启，
`off` 关闭。成功后最外层 CANN span 带可选 `offcpu_ns`/`futex_ns`（字段缺失=未测量，0=测得 0）；
嵌套 span 不采样，避免父子重复累计。blocking 程序单独加载，attach 失败只产生 warning，
不影响 host span 采集。

已在 `bms-84865758` 验证：V4 10 秒 `attached=4 failed=0`、7,561 spans、无效样本 0；
合成 pthread mutex 竞争 off-CPU 99.7%、futex 99.9%、`host_lock_contention` 1.00。
数据与边界见 `docs/M2-VALIDATION.md` 与 `docs/M2-OFFCPU.md`。

### E 层硬件采样（npu-smi）

```bash
./cann-ebpf-linux-arm64-m2 attach --pid auto --profile vllm-ascend --profiles-dir profiles \
  --duration 10s --blocking off --hardware on --output runs/ebpf-hw
```

`--hardware auto|on|off`：`auto` 按 profile；采样写入 `hardware.jsonl`（manifest artifact），
`diagnose` 输出 8 设备 HBM/AICore/功耗/温度，并在 HBM free <5% 且存在慢 memory 调用时给出
`hbm_pressure`。硬件是 run 级 wall-clock 数据，不能逐调用 join。详见 `docs/HARDWARE.md`。

### device 时间线 join（M3，生产端待维护窗口）

外部 collector 产出 `device.jsonl` 后，用 attach 导入并抓 pidmap：

```bash
./cann-ebpf-linux-arm64-m3 attach --pid auto --profile vllm-ascend --profiles-dir profiles \
  --duration 30s --device-jsonl /host/path/device.jsonl --output runs/ebpf-m3
```

`diagnose` 会自动读取 run 目录的 `device.jsonl`（schema `cann.device.v1`），按
`(pid,tid,start±100µs)` 匹配 API record，再用 `correlationId` 关联 KERNEL/MEMCPY/HCCL；
有设备证据才输出 `device_bound` / `launch_starvation` / `copy_bound` / `communication_bound`。
`msprof --pid` 动态采集已确认段错误，真实 device 数据需要容器内 in-process collector + 维护窗口。
详见 `docs/M3-DEVICE-JOIN.md`、`docs/M3-FEASIBILITY.md`、`docs/WORK-SPLIT.md`。

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
python-batch-<pid>.json  纯 eBPF batch 的逐 Worker 统计、类型确认和 warnings
```

Span 新增字段：

```json
{
  "start_ns": 10162267042648145,
  "end_ns": 10162267057374196,
  "api": "aclrtSynchronizeEvent",
  "batch_id": "ctx-smoke-1",
  "request_ids": ["req-smoke-1", "req-smoke-2"],
  "offcpu_ns": 850000,
  "futex_ns": 700000,
  "attribution": {
    "level": "batch",
    "source": "protocol_smoke",
    "confidence": 1,
    "verdict": "batch_window"
  }
}
```

`offcpu_ns` / `futex_ns` 只在 M2 blocking 探针真正采集到该 span 时出现；没有字段表示未测量，
字段为 0 表示测得 0。它们属于调用线程，不是设备执行时间。

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
- msPTI kernel/memcpy/HCCL 的真实 V4/Orinfer 设备时间线尚未采集；collector 已在合成 ACL 上验证。
- M2 已上机验证，但只覆盖最外层 CANN span 的 host 阻塞；嵌套 span 没有 blocking 字段是设计行为。
  本次 V4 窗口没有真实 futex 竞争（futex≈0），`host_lock_contention` 由合成锁竞争目标验证。
- `offcpu_ns`/`futex_ns` 是 host 线程的等待时间，不是 request 延迟、不是 NPU 执行时间；
  没有 msPTI 时仍然不能下 `device_bound`/`launch_starvation`。
- 硬件层是周期性 wall-clock 快照，AICore/HBM 是整卡值，不能归因到单个 Worker 或单次调用。
- M3 的 final7 结果来自**合成 ACL**，不是 V4 生产流量；`msprof --pid` 动态采集段错误，真实设备数据
  需要维护窗口的 in-process collector。
- V4 上 740 个 uprobe 的进程退出清理约 40 秒（内核串行 detach，残留最终为 0），
  这是命令返回时间，不是采集窗口、也不影响业务；`capture_stop` 之后的清理不计入窗口。
- `diagnose` 只读已有证据；没有 off-CPU 采样时只输出 `blocking_unknown`，没有 msPTI 时永远不输出
  `device_bound` / `launch_starvation`。
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

## M5 链路追踪与纯 eBPF 采集

本轮新增 W3C 形状的 batch/CANN 链路追踪：

- batch span 是父节点，命中的 CANN span 派生 child `span_id`，写入 `trace` 对象；
- `summary.json` 增加 `trace_coverage`、`trace_sources` 和 `top_slow_traces`；
- `cann-ebpf trace` 可直接输出 trace 到 batch 再到 CANN span 的链路树；
- 旧插件没有 trace 字段时，collector 生成 `collector_local_batch` 本地 trace；
- 插件支持 `CANN_EBPF_TRACEPARENT` 做进程级上游继承；逐 HTTP request 传播尚未实现；
- 子 span 的 `batch_parent_span_id` 保留 batch 的上游父 span，batch span 本身仍不写入 JSONL。

`poc/cpython` 是纯 eBPF CPython 3.12 采集实现，现由 `attach --python-batch` 直接
使用：只挂 `PyObject_GetAttr` entry/return，过滤 `num_scheduled_tokens`，读取返回
dict 的 request ID 键，不读 value、prompt、响应或 tensor。支持 combined/split
unicode dict，单次最多 64 个 key；batch span 写入标准 `spans.jsonl`，逐 Worker
统计写入 `python-batch-<pid>.json` 并由 manifest 计算 SHA-256。合成 Python 3.12
和真实 V4 Worker 都已验证；该路径不能推导逐 request 时长。

详细 schema、证据和边界见 `docs/M5-TRACE-AND-CPY-POC.md` 与
`poc/cpython/README.md`。

## 文档

- `docs/PRODUCT.md`：成品说明（能力矩阵、证据文件、已验证/未验证边界）。
- `docs/HANDOFF.md`：交接总览、已完成/未完成、下一步与硬约束。
- `docs/DIAGNOSE.md`：`diagnose` 用法、时间窗语义、判定规则和边界。
- `docs/M2-OFFCPU.md`：M2 设计、判定口径和边界。
- `docs/M2-VALIDATION.md`：M2 上机验证数据、修复记录和证据位置。
- `docs/M2-OVERHEAD.md`：M2 agent 开销基线与吞吐不可测说明。
- `docs/PREFLIGHT.md`：preflight 检查项、退出码与 V4 结果。
- `docs/HARDWARE.md`：E 层 npu-smi 采样、schema、验证与边界。
- `docs/M3-DEVICE-JOIN.md`：device 时间线 schema、join 算法与合成验证。
- `docs/M3-COLLECTOR.md`：in-process msPTI collector、环境变量、pidmap 与生产启用方式。
- `docs/M3-VALIDATION.md`：M3 上机验证数据、慢 Sync 修复前后对比和边界。
- `docs/M3-FEASIBILITY.md`：msprof/msPTI 动态采集可行性结论。
- `docs/NEXT-PLAN.md`：M2 之后的分阶段方案（M3 可行性、E 层指标、开销基线、生产化）。
- `docs/WORK-SPLIT.md`：两个并行任务的文件所有权、接口约定与评审备注。
- `docs/NEXT-WORK.md`：M3 合成验证之后的下一阶段工作与分工。
- `docs/M4-VALIDATION.md`：当前交付的实测数据、复现命令、证据和边界。
- `docs/M4-CONTEXT-PROTOCOL.md`：batch 协议、权限模型、插件安装与激活步骤。
- `docs/M5-TRACE-AND-CPY-POC.md`：链路追踪 schema、POC 设计、真实 V4 证据和边界。
- `poc/cpython/README.md`：纯 eBPF CPython 采集的构建、运行和限制。
- `docs/M1-VALIDATION.md`：历史 M1 8 探针基线和 tracefs 记录。
- `docs/M1-OUTPUT.md`：JSONL / manifest / summary 证据层契约。
- `docs/M1-PROFILES.md`：profile 匹配和只读 smoke 说明。
- `docs/PLAN.md`：总体技术方案、阶段和采集模型。
- `adapters/vllm_ascend/README.md`：vLLM general plugin 安装与限制。
