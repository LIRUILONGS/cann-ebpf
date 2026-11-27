# 基于开源栈的新方案：运行中可 attach 优先，官方 profiler 作为可选深度模式

日期：2026-11-28
修订：根据实测约束，删除“用 vLLM `--profiler-config` 作为主线”的假设。

## 1. 结论先行

**主线必须是零重启 attach。** 当前生产进程不能因为要采集而重启，也不能假设 `/start_profile` 已经存在。vLLM-Ascend 官方文档已经写明：`/start_profile` 和 `/stop_profile` 只有在启动时设置了非空 `--profiler-config` 才会注册；否则请求返回 404。因此，**vLLM 内置 profiler 不是当前运行中服务的主数据源**。

同样，`msserviceprofiler` 需要在进程启动前设置 `SERVICE_PROF_CONFIG_PATH` 并完成 vLLM 侧接入。它启动后的 `enable: 0 -> 1` 是“已经接入后的动态开关”，不是对任意现存进程的热插桩。

新方案按“能采到的证据”和“采不到的证据”分层：

```text
核心（当前进程即可）
  边界请求证据   nginx/网关日志、已有 /metrics、HTTP 边界事件
  主机时间线     cann-ebpf host span + off-CPU + driver ioctl
  主机火焰图     OpenTelemetry eBPF Profiler 或 Parca Agent
  硬件背景       DCMI / npu-smi / HCCS / Prometheus

可选（需要预置或维护窗口）
  服务语义       msserviceprofiler（启动前注入）
  算子/设备轨迹  vLLM-Ascend profiler（启动参数预置 + HTTP start/stop）
  设备深挖       msPTI / msprof（预加载或维护窗口）
```

`cann-ebpf` 的职责收敛为四件事：

1. **编排**：按时间窗口启动/停止可 attach 的数据源，保存原始产物和 SHA256。
2. **规范化**：把 host span、火焰图样本、边界请求、DCMI/HCCS 映射到统一时间轴。
3. **关联**：request_id、PID、TID、时间窗；没有 request_id 时明确降级为时间窗关联。
4. **报告**：单文件 HTML；缺失的层显示“未采集/不可用原因”，不补零、不推断、不伪造。

## 2. 为什么改

### 2.1 vLLM-Ascend 官方 profiler：只适合作为可选深度模式

`vllm-ascend` 文档明确支持：

- 启动参数：`--profiler-config '{"profiler":"torch","torch_profiler_dir":"./vllm_profile","torch_profiler_with_stack":false}'`
- 控制接口：`POST /start_profile`、`POST /stop_profile`
- 产物：`trace_view.json`（Chrome Tracing）、`kernel_details.csv`、`operator_details.csv`、`op_statistic.csv`、`step_trace_time.csv`
- 可用 `torch_npu.profiler.profiler.analyse()` 解析

但注册条件决定它不能作为当前运行进程的零重启数据源：

- 未设置非空 `--profiler-config` 时，`/start_profile`、`/stop_profile` 不注册，调用返回 404。
- 该参数只能在服务启动时给出；对已经在跑的 vLLM，无法通过 HTTP 端点临时开启。
- 因此主线不依赖它。只有测试实例或维护窗口预置了 profiler 的情况下，才把官方产物作为设备/算子 timeline 的可选增强；没有产物时报告必须标注 `unavailable` 及原因。

### 2.2 msserviceprofiler：可选服务语义源，不是当前进程主路径

`Ascend/msserviceprofiler`（GitHub/Gitee）支持：

- vLLM / SGLang / MindIE 服务化采集
- 按官方接入方式预置（非运行中热插桩）
- 请求、KV Cache、Batch、模型执行、通信等 domain
- Chrome Tracing、`profiler.db`、`request.csv`、`kvcache.csv`、`batch.csv`
- 2025-11 起对接 OpenTelemetry，2026-03 起支持 Prometheus 在线监测
- 2025-12 起支持 Torch Profiler 数据采集与解析

这比我们在 eBPF 里追 Python 函数更合适：服务语义在应用层，eBPF 不应该承担“解释 Python 调用栈”的职责。但它需要在进程启动前设置 `SERVICE_PROF_CONFIG_PATH` 并完成 vLLM 侧接入；启动后的 `enable: 0 -> 1` 只是“已接入实例”的动态开关，不是对任意现存进程的热插桩。默认不依赖它：未预置时，请求边界由 nginx/网关日志和已有 `/metrics` 提供，报告把 Service Gantt 标为 `unavailable`。

### 2.3 主机栈火焰图有成熟开源实现

`open-telemetry/opentelemetry-ebpf-profiler`：

- Linux only，amd64/arm64 都支持
- ARM64 支持所有 unwinder（.NET 除外）
- 支持 native C/C++/Rust/Go，以及 Python/Ruby/PHP/Node/Java 等
- 混合内核栈 + 用户态栈，不需要改应用、不需要重启
- 文档给出 1% CPU / 250MB 内存的量级上限
- 输出 OTel Profiles（pprof 生态可转换）

`parca-dev/parca-agent`：

- ARM64 DWARF unwinding 从 v0.25.0 起支持
- Python/Ruby 等解释器栈支持
- 输出 pprof，适合和现有报告/FlameGraph 工具链对接
- 常驻 19Hz 采样，适合做“低开销常驻 + 按需导出”

两者选一个做主方案，另一个做备选；不要在 MVP 同时接两套。

### 2.4 bpftime 可以作为高开销 uprobe 的替代

`eunomia-bpf/bpftime`：

- 用户态 eBPF runtime，ARM64 构建支持
- uprobe 比内核 uprobe 最多快约 10 倍（官方口径）
- 支持用户态 map、与内核 eBPF 共存
- 可注入运行中进程，但也意味着更强的进程内侵入性

适合场景：高频 Python/C++ 函数 uprobe、内核 uprobe 开销不可接受时。

不适合场景：默认生产模式；需要稳定、可审计、最小侵入时，仍用内核 uprobe + PID 过滤。

## 3. 新架构

```text
┌──────────────────────────────────────────────────────────────────────┐
│ cann-ebpf report                                                    │
│ unified HTML + raw artifacts + SHA256 + availability matrix         │
└───────────────────────────────▲──────────────────────────────────────┘
                                │
┌───────────────────────────────┴──────────────────────────────────────┐
│ join / normalize layer                                               │
│ request_id / boundary_id / pid / tid / time window / confidence      │
└───────────────▲──────────────────────────────────▲───────────────────┘
                │                                  │
┌───────────────┴──────────────────┐  ┌────────────┴───────────────────┐
│ 核心：当前进程零重启              │  │ 可选：需启动预置或维护窗口      │
│ nginx/网关日志 + 已有 /metrics    │  │ msserviceprofiler              │
│ cann-ebpf host span / off-CPU     │  │ vLLM-Ascend profiler           │
│ OTel/Parca 混合栈                 │  │ msPTI fallback                 │
│ DCMI/npu-smi/HCCS/Prometheus      │  │ msprof PMU                     │
└───────────────────────────────────┘  └────────────────────────────────┘
```

### 3.1 数据源矩阵

定位只有两类：**核心**是当前进程零重启即可采集，默认必须开启；**可选**是需要启动前预置或维护窗口，默认关闭，缺失时只标注 `unavailable`，不阻断采集。

| 层 | 定位 | 数据源 | 采集方式 | 对当前进程零重启 | 典型产物 | 缺失时报告 |
|---|---|---|---|---|---|---|
| 请求边界 | 核心 | nginx/网关 access log、已有 `/metrics` | 只读日志 / HTTP 拉取 | 是 | boundary JSONL、metrics JSONL | `unavailable: 未找到日志或指标端点` |
| Host API | 核心 | `cann-ebpf attach` | uprobe / uretprobe | 是 | `spans.jsonl` | `unavailable: attach 失败（见原因）` |
| Host 阻塞 | 核心 | `cann-ebpf attach --blocking on` | sched tracepoint | 是 | off-CPU 事件 | `unavailable: 未开启 blocking` |
| Host 栈 | 核心 | OTel eBPF profiler 或 Parca | perf_event 采样 | 是 | pprof / folded | `unavailable: profiler 未运行` |
| 硬件状态 | 核心 | DCMI / npu-smi / HCCS / Prometheus | 只读采样 | 是 | metrics JSONL | `unavailable: 设备接口不可读` |
| 服务语义 | 可选 | `msserviceprofiler` | 启动前预置 + `enable` 开关 | 否（需预置） | `request.csv`、`batch.csv`、`kvcache.csv` | `unavailable: 未预置 SERVICE_PROF_CONFIG_PATH` |
| 框架/算子 | 可选 | `vLLM-Ascend --profiler-config` | 启动参数预置 + HTTP start/stop | 否（需启动时配置） | `operator_details.csv`、`trace_view.json` | `unavailable: 未设置 --profiler-config` |
| 设备 timeline | 可选 fallback | `msPTI` | LD_PRELOAD / 预置注入 | 否（需启动注入） | kernel/operator timeline | `unavailable: 未注入 msPTI` |
| 设备 PMU | 可选深挖 | `msprof` | `--aic-metrics` 等 | 维护窗口 | PMU CSV/db | `unavailable: 非维护窗口` |

## 4. 关联设计

### 4.1 统一事件 schema

新增 `internal/schema/event.go` 或等价包，统一下列字段：

```json
{
  "ts_ns": 0,
  "end_ns": 0,
  "source": "boundary|ebpf|msservice|vllm_profiler|mspti|dcmi|prometheus",
  "kind": "boundary|request|batch|step|host_span|offcpu|op|kernel|sample|metric",
  "pid": 0,
  "tid": 0,
  "request_id": "",
  "batch_id": "",
  "op_name": "",
  "duration_ns": 0,
  "attrs": {}
}
```

规则：

- 原始时间戳保留，统一转换到 ns；不要覆盖原值。
- 设备时间线与 host 时间线的时钟偏移单独记录，报告标注“对齐误差 ±X ms”。
- 没有 request_id 的事件只做 PID/TID/时间窗关联，不强行归属。
- 关联置信度分 `exact`（request_id 相同）、`high`（PID+TID+时间窗）、`low`（仅时间窗重叠）。

### 4.2 具体 join key

| 数据源 | join key | 说明 |
|---|---|---|
| nginx/网关 access log | request_id / connection id + 时间戳 | 默认请求边界；无 request_id 时用连接号与时间窗生成 `boundary_id` |
| 已有 `/metrics` | 进程/实例 + 采样时间窗 | 只做背景和健康检查，不当作 per-request 主键 |
| `cann-ebpf spans.jsonl` | host PID/TID + 函数名 + 时间窗 | 默认 host API 时间线 |
| `off-CPU` | host PID/TID + 时间窗 | 阻塞归因 |
| OTel/Parca pprof | PID + 采样时间窗 | 按进程聚合，不做 per-request 强关联 |
| `msserviceprofiler request.csv`（可选） | `http_rid` / request id + 时间窗 | 仅预置实例可用时使用 |
| vLLM profiler `trace_view.json`（可选） | step/op 时间戳 + PID/TID；若 trace 内有 request id 则优先 | 仅预置 profiler 时使用 |
| `msPTI`（可选） | host PID/TID + API/KERNEL 时间戳；需要 pidmap | 仅作为设备 timeline fallback |
| DCMI/HCCS | 设备/卡号 + 采样时间窗 | 只做背景，不做 per-op 归属 |

### 4.3 请求关联的现实边界

- 默认请求关联来自 nginx/网关日志；网关没有 request_id 时，只能用连接号/时间窗生成 `boundary_id`，并按实际可关联性标为 `high` 或 `low`。
- `msserviceprofiler` 有 request/batch 语义，但只在预置实例可用时作为增强，不是默认依赖。
- eBPF host span 未必能直接拿到 request_id；只能通过 PID/TID + 时间窗关联。
- vLLM profiler 的 step/op 时间线可能没有 request_id；不要假设一一对应。
- 报告里必须显示关联方法和置信度，不能把“时间窗重叠”写成“精确归属”。

## 5. 报告设计

扩展现有 `report.html`，新增/调整以下视图：

| 视图 | 数据源 | 可用性 | 说明 |
|---|---|---|---|
| Request boundary timeline | nginx/网关、已有 `/metrics` | 核心，默认 | 请求到达、状态码、延迟、并发；无网关日志时显示 `unavailable` |
| Host causal timeline | `cann-ebpf` | 核心，默认 | ACL/HCCL span、off-CPU、driver ioctl |
| Host flame graph | OTel/Parca | 核心，默认 | 用户态 + 内核态混合栈；Python 帧可见 |
| Hardware small multiples | DCMI/HCCS/Prometheus | 核心，默认 | HBM、AICore、HCCS lane、温度、功耗 |
| Service Gantt | `msserviceprofiler` | 可选；缺失显示 `unavailable` | request → batch → model execute → communication，仅预置实例 |
| Device operator timeline | vLLM profiler | 可选；缺失显示 `unavailable` | operator/kernel 时长、队列间隙、HCCL，仅预置 profiler |
| Operator treemap | vLLM profiler `op_statistic.csv` | 可选；缺失显示 `unavailable` | 按算子类型/耗时聚合，无官方产物不生成 |
| Gap analysis | host + device | 可选；仅当真实 device timeline 存在 | host dispatch 与 device kernel 的时间差 Top N；低置信度只标时间窗重叠 |
| Data quality | manifest | 核心 | 每个数据源是否缺失、时间偏移、截断、采样率、版本 |

没有真实官方产物时，Service Gantt、Device operator timeline、Operator treemap、Gap analysis 必须统一显示 `unavailable` 和具体原因（例如 `msserviceprofiler not preconfigured`、`--profiler-config absent`）。不得用 host span、DCMI 指标或时间窗推断生成这些视图，也不得补零。

明确不做：

- 设备内核栈火焰图（没有 stack sampler）。
- 用 eBPF 读 AI Core/Vector/Cube PMU。
- 把 DCMI/HCCS 背景指标当成 per-op 归因。

## 6. 分阶段落地

### Phase 0：只读验证（1–2 天）

目标：确认目标机上有哪些现成数据源，不改服务、不重启进程。**核心源不可用才算问题；可选源缺失只记录 `unavailable`，不阻断后续阶段。**

核心零重启检查项：

```bash
# 1. 进程与启动参数（只读）
tr '\0' ' ' < /proc/<pid>/cmdline
ls -l /proc/<pid>/exe 2>/dev/null || true

# 2. nginx/网关日志与已有 /metrics
nginx -T 2>/dev/null | grep -E 'access_log|log_format' || true
ls -l /var/log/nginx/ /var/log/*gateway*/ 2>/dev/null || true
curl -sS -m 3 http://127.0.0.1:<port>/metrics | head -40 || true

# 3. 现有 cann-ebpf 能力与内核
cann-ebpf --help
cann-ebpf attach --help 2>/dev/null | head -60 || true
uname -a; cat /etc/os-release

# 4. 硬件接口（只读）
npu-smi info 2>/dev/null | head -40 || true
```

可选深挖源检查（只记录，不把缺失当失败）：

```bash
# msserviceprofiler 是否已预置（需要启动前接入，不能在当前进程热插）
python3 -c "import ms_service_profiler; print(ms_service_profiler.__file__)" 2>/dev/null || true
msserviceprofiler --help 2>/dev/null | head -40 || true

# vLLM profiler 是否已预置；缺失只记录 unavailable
tr '\0' ' ' < /proc/<vllm_pid>/cmdline | grep -o -- '--profiler-config[^ ]*' || true

# msPTI / msprof 能力
ls /usr/local/Ascend/ascend-toolkit/latest/tools/profiler/bin 2>/dev/null || true
msprof --help 2>/dev/null | head -40 || true
```

产出：`docs/OPEN-SOURCE-VALIDATION.md`，记录核心源可用性、可选源可用性、版本、缺失项。`--profiler-config` 缺失时只记 `unavailable: --profiler-config not set`，**不能作为 Phase 0 的阻断或失败条件**。

### Phase 1：核心零重启采集闭环（约 1–2 周）

目标：不修改服务、不依赖官方 profiler，先把 nginx/网关边界、cann-ebpf host span、hoststack 采样、DCMI/HCCS 硬件背景串起来。

- 新增 `internal/ingest/boundary`：解析 nginx/网关 access log，解析已有 `/metrics`，生成边界请求事件。
- 复用现有 `cann-ebpf attach`：host span、off-CPU、driver ioctl。
- 接入 OTel eBPF profiler 或 Parca 的 hoststack 原始输出（基础采集；火焰图深化放 Phase 2）。
- 新增 `cann-ebpf collect`，默认源固定为 `ebpf,boundary,hoststack,hardware`。
- 报告先出 Request boundary timeline、Host causal timeline、Hardware small multiples、Data quality。

验收：对运行中的 vLLM 或 Orinfer 执行一条命令即可采集，全程不重启、不改配置；`manifest.json` 显示四个核心源状态；`service,torch` 未启用时报告显示 `unavailable` 而不是报错退出。

### Phase 2：Host 混合栈火焰图（约 1 周）

目标：把“内核栈火焰图”升级为“用户态 + 内核态混合栈”，优先覆盖 Python。

候选：

1. OTel eBPF profiler：ARM64/Python 支持，输出 OTLP/pprof；需要一个小型转换器。
2. Parca Agent：ARM64/Python 支持，pprof 输出；需要确认一键导出路径。
3. py-spy：仅 Python，非 eBPF，但最简单；作为 fallback。

选择标准：目标机内核 5.10 + arm64 能否稳定运行；Python 帧是否可见；10 分钟开销；产物能否转 folded。

验收：对 vLLM/Orinfer 进程生成包含 Python 帧的火焰图；与现有内核栈火焰图对比，说明差异。

### Phase 3：编排与关联（约 1–2 周）

目标：一条命令完成多源采集与报告。

```bash
# 默认：核心源，零重启；不需要 service/torch 目录
cann-ebpf collect \
  --profile vllm-ascend \
  --pid auto \
  --duration 5m \
  --sources ebpf,boundary,hoststack,hardware \
  --out runs/...

# 可选增强：仅当测试实例已经预置 msserviceprofiler / vLLM profiler
cann-ebpf collect \
  --profile vllm-ascend \
  --pid auto \
  --duration 5m \
  --sources ebpf,boundary,hoststack,hardware,service,torch \
  --service-dir /path/to/ms_service_profiler \
  --torch-dir /path/to/vllm_profile \
  --out runs/...
```

要求：

- `--sources` 默认值固定为 `ebpf,boundary,hoststack,hardware`；`service,torch` 只能显式追加，不能作为默认值。
- 每个 source 独立成功/失败，不因一个源失败中断全部；`service/torch` 缺失时在 `manifest` 标 `unavailable`，不返回失败。
- 保存原始文件，不覆盖；计算 SHA256。
- `manifest.json` 记录版本、参数、时间偏移、缺失项。
- 采集前、采集过程中都不修改目标服务配置；不自动重启，不自动开启任何需要启动参数的 profiler。

验收：一条命令产出 HTML；报告顶部显示数据源覆盖矩阵。

### Phase 4：Orinfer / msPTI 可选 fallback（按需，不阻塞主线）

当前 V4.1 是 Orinfer，不适用 vLLM profiler。保留：

- `cann-ebpf` host span + off-CPU + DCMI/HCCS；
- `msPTI` 仅在已有启动预置或维护窗口可用时，作为 Orinfer 设备 timeline 的 fallback；
- 不把 msPTI 当主路径，不为了 msPTI 重启生产。

如果 Orinfer 后续提供 profiler 接口，再替换 fallback。

## 7. 代码改动建议（最小集）

新增：

- `internal/ingest/boundary/`：nginx/网关 access log 与已有 `/metrics` 解析
- `internal/schema/event.go`：统一事件
- `internal/join/`：request/PID/时间窗关联
- `cmd/cann-ebpf/collect.go`：多源编排，默认 `ebpf,boundary,hoststack,hardware`
- `internal/ingest/msservice/`（可选）：CSV/JSON 解析 + schema 转换
- `internal/ingest/vllmprof/`（可选）：CSV/JSON 解析 + schema 转换

修改：

- `cmd/cann-ebpf/report.go`：新增 `--service-profile`、`--torch-profile`、`--profile-flame` 参数；这些参数缺失时只影响可选视图
- `internal/report/`：新增 Request boundary timeline、Host causal timeline、Hardware small multiples、Data quality；Service Gantt、Device timeline 仅在真实产物存在时展示
- `docs/USAGE.md`：默认路线写零重启核心采集；官方 profiler 与 msPTI 单独写成“可选深度模式”

不新增：

- 不新增 device-side eBPF runtime
- 不新增 NPU 栈采样器
- 不新增自定义 msPTI collector 主路径
- 不新增 Prometheus 服务端（已有 exporter；msserviceprofiler 也有 Prometheus）

## 8. 风险与边界

| 风险 | 处理 |
|---|---|
| `msserviceprofiler` 版本/接口变化 | 固定版本；解析失败时保留原始文件并显式标注；默认不依赖 |
| vLLM `--profiler-config` 只在启动时注册 | 当前进程不采集；报告显示 `unavailable`；只在测试实例/维护窗口预置，不重启生产 |
| `msserviceprofiler` 需要启动前接入 | 默认不依赖；未预置时 Service Gantt 显示 `unavailable`，只作为可选增强 |
| 把时间窗重叠误写成精确归因 | 报告强制显示 `exact/high/low` 和 join key；低置信度不写“精确归属” |
| 核心边界日志缺失 | nginx/网关日志不存在时 Request boundary timeline 显示 `unavailable`，退回 `/metrics` 背景；不伪造请求 |
| OTel/Parca 在 openEuler 5.10 上不稳定 | Phase 2 做 A/B；失败则退回 py-spy 或现有内核栈 |
| 时钟偏移导致错配 | 记录偏移和置信度；不做 ns 级承诺 |
| profiler 自身开销 | 每个源单独测；核心源默认低采样；可选 profiler 只在短窗口开启 |
| Orinfer 不支持官方 profiler | 保留 eBPF 核心；msPTI 仅在已有预置或维护窗口可用时作为设备 timeline fallback |

## 9. 与论文结论的一致性

- ProfInfer 证明 host/engine 边界可以用 uprobe 追，但 NPU 设备时间线只有 graph 级；所以设备 timeline 只在预置官方 profiler 或 msPTI fallback 时展示，不自己造，也不当成默认能力。
- eInfer 证明 per-request 关联很难，且需要用户态 CKP；所以先做 request_id + PID/TID + 时间窗的分级关联，不承诺全透明。
- Eliminating eBPF Overhead 证明 hook 越多、未跟踪进程开销越大；所以收敛 uprobe、PID/cgroup 过滤、默认只开必要探针。
- gpu_ext/eGPU 证明设备侧 eBPF 需要厂商运行时；Ascend 没有，直接放弃 device-side eBPF。
- NPUMeter 证明 Ascend 算子建模需要复杂标定；只作为离线 expected baseline，不作为运行时依赖。
- NCCLbpf 证明进程内 eBPF 可用于通信策略，但仅限有 plugin ABI 的场景；HCCL 先调研，不纳入 MVP。

## 10. 参考项目

- Ascend msserviceprofiler: https://github.com/Ascend/msserviceprofiler
- vLLM-Ascend: https://github.com/vllm-project/vllm-ascend
- OpenTelemetry eBPF profiler: https://github.com/open-telemetry/opentelemetry-ebpf-profiler
- Parca Agent: https://github.com/parca-dev/parca-agent
- bpftime: https://github.com/eunomia-bpf/bpftime
- NCCLbpf: https://github.com/eunomia-bpf/nccl-eBPF
- Google pprof: https://github.com/google/pprof
- FlameGraph: https://github.com/brendangregg/FlameGraph
- Pyroscope: https://github.com/grafana/pyroscope
