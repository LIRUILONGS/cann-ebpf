# vLLM 十视图多 Agent 实施计划

日期：2026-11-27
目标引擎：vLLM-Ascend
范围：负载-延迟拐点、关键路径瀑布、Summed Delay vs Critical Path、Slack CDF、off-CPU ribbon、算子 Treemap、Roofline、A/B 差分、变化点、服务拓扑。

## 0. 原则

- 所有视图只读原始证据，不修改 `spans.jsonl`、`device.jsonl`、`metrics.jsonl`。
- 每个视图必须有 `status.available` 和 `status.reason`；没有数据就不画空图，不补造数据。
- 严格版和推断版必须显式标记：`confidence = exact | inferred | partial`。
- 共享文件只允许集成 Agent 修改；worker 只写自己名下的文件。
- 每个 Agent 使用独立 `git worktree` 和分支，禁止在同一工作区并行改文件。
- 先冻结 JSON 契约，再并行开发；前端只按契约渲染。

### 0.1 本次决策与边界

- 本次目标引擎固定为 `vLLM-Ascend`；Orinfer 只作为已有基线/回归对照，不再作为本轮实现主线。
- 本轮已在 36 机器确认运行的是 `vLLM-Ascend 0.23.0`（容器 `deepseek-v4-flash-8905`，端口 8905）；Orinfer 只保留为历史基线。未确认版本的采集不能标成当前 vLLM 证据。
- M0-M3 可以在本机用合成 fixture 完成；M4 必须有真实 vLLM run，不能拿 Orinfer 或历史 run 冒充。
- 不修改 `spans.jsonl`、`device.jsonl`、`metrics.jsonl` 及任何原始证据；不脱敏、不补造数据、不覆盖旧 run。
- 没有栈采样就不画火焰图；没有 per-op FLOPs/bytes 就不画 Roofline 假点；没有 stream/event 依赖就把关键路径标成 `inferred`。

## 1. Agent 编制

| Agent | 负责视图/模块 | 只允许写 | 分支 |
|---|---|---|---|
| `integrator` | 契约、`report.Data` 接线、CLI、文档、合并 | `internal/report/views_v2.go`、`internal/report/build_views_v2.go`、`internal/report/report.go`、`internal/report/views.go`、`cmd/cann-ebpf/report.go`、`docs/VIEWS-V2-CONTRACT.md` | `feat/views2-integration` |
| `cp-agent` | 关键路径瀑布、Summed Delay、Slack CDF | `internal/report/criticalpath.go`、`internal/report/criticalpath_test.go`、`internal/report/slack.go`、`internal/report/slack_test.go` | `feat/views2-cp` |
| `sweep-agent` | 负载-延迟拐点、A/B 差分、压测索引 | `internal/report/sweep.go`、`internal/report/sweep_test.go`、`internal/report/compare.go`、`internal/report/compare_test.go`、`scripts/vllm_sweep.py` | `feat/views2-sweep` |
| `ts-agent` | 变化点/异常带 | `internal/report/changepoint.go`、`internal/report/changepoint_test.go` | `feat/views2-changepoint` |
| `blocking-agent` | off-CPU ribbon | `internal/report/offcpu.go`、`internal/report/offcpu_test.go` | `feat/views2-offcpu` |
| `tree-agent` | 算子 Treemap | `internal/report/treemap.go`、`internal/report/treemap_test.go` | `feat/views2-treemap` |
| `roof-topo-agent` | Roofline、服务拓扑 | `internal/report/roofline.go`、`internal/report/roofline_test.go`、`internal/report/topology.go`、`internal/report/topology_test.go`、`scripts/otel_export_example.py` | `feat/views2-roof-topo` |
| `frontend-agent` | 单文件 HTML 图表、tooltip、无数据态 | `internal/report/template.html` | `feat/views2-frontend` |
| `qa-agent` | 合成 fixture、端到端、真实 vLLM 验收 | `internal/report/views_v2_integration_test.go`、`testdata/views2/**`、`scripts/qa_views2.sh`、`.analysis/views2/**` | `feat/views2-qa` |

## 2. 共享契约

`integrator` 在 M0 先写 `internal/report/views_v2.go`，冻结以下类型和 JSON 字段。worker 只能读，不能改；需要新增字段时先在 `docs/VIEWS-V2-CONTRACT.md` 提变更，由 integrator 统一改。

```go
type ViewStatus struct {
    Available  bool   `json:"available"`
    Reason     string `json:"reason,omitempty"`
    Source     string `json:"source,omitempty"`
    Confidence string `json:"confidence,omitempty"` // exact|inferred|partial
}

type ReportViewsV2 struct {
    LoadLatency      LoadLatencyData      `json:"load_latency"`
    CriticalPath     CriticalPathData     `json:"critical_path"`
    SummedDelay      SummedDelayData      `json:"summed_delay"`
    SlackCDF         SlackCDFData         `json:"slack_cdf"`
    OffCPU           OffCPUData           `json:"off_cpu"`
    OperatorTreemap  TreemapData          `json:"operator_treemap"`
    Roofline         RooflineData         `json:"roofline"`
    ABDiff           ABDiffData           `json:"ab_diff"`
    ChangePoint      ChangePointData      `json:"change_point"`
    ServiceTopology  ServiceTopologyData  `json:"service_topology"`
}
```

### 2.1 字段契约

| 视图 | 关键字段 | 数据来源 | 不可用条件 |
|---|---|---|---|
| LoadLatency | `points[]`：concurrency、qps、ttft_p50/p95、latency_p50/p95/p99、running_max、waiting_max、kv_cache_max、npu_aicore_mean；`knee_concurrency`、`workload_id` | `sweep.json` + 每档 run 的 metrics | 少于 4 个并发档位，或 workload_id 不一致 |
| CriticalPath | `batch_id`/`request_id`/`trace_id`、`nodes[]`（id、parent、label、category、kind、start_ms、duration_ms、device、confidence）、`critical_path[]`、`total_ms`、`coverage` | spans + device + batch/request context | 无 batch/request/trace 上下文，或无可关联 device |
| SummedDelay | `sum_ms`、`critical_path_ms`、`overlap_ms`、`parallelism`、`nodes_by_category[]` | 同 CriticalPath | 依赖 CriticalPath.available |
| SlackCDF | `points[]`（slack_ms、count、cumulative）、`min/p50/p95`、`negative_count` | CriticalPath DAG | DAG 不完整；或样本 < 30 |
| OffCPU | `mode`（aggregate/exact）、`rows[]`（lane、start_ms、end_ms、on_cpu_ms、offcpu_ms、futex_ms、segments[]） | spans 的 `offcpu_ns/futex_ns`；exact 需要 off-CPU interval | 无 blocking 采样 |
| OperatorTreemap | `root`（name、category、kind、device、stream、duration_ms、count、children） | device KERNEL/MEMCPY/HCCL；host fallback | 无 device 数据时降级为 host 函数树并标 source=host |
| Roofline | `points[]`（op、category、duration_ms、flops、bytes、arithmetic_intensity、achieved_tflops、achieved_gbps、bound）、`peak_tflops`、`peak_gbps`、`dtype` | `ops.jsonl` + 910B2 峰值参数 | 无 per-op FLOPs/bytes；不得用理论值补造 |
| ABDiff | `a_run`、`b_run`、`workload_id`、`metrics[]`、`functions[]`、`significant[]` | 两个 run 的 spans/metrics | workload_id 不一致，或 run 不完整 |
| ChangePoint | `series[]`、`points[]`（time_ms、metric、before、after、delta_pct、score、method）、`events[]` | 长时序 metrics + `events.jsonl` | run < 5 分钟，或有效点 < 30 |
| ServiceTopology | `nodes[]`、`edges[]`、`coverage`、`root_service` | OTel/W3C trace 或网关/LiteLLM 日志 | 无上游 trace 传播；只能画进程内拓扑时必须标 scope=process |

## 3. 输入文件契约

### 3.1 `sweep.json`

```json
{
  "workload_id": "vllm-chat-128in-128out-v1",
  "engine": "vllm-ascend",
  "model": "deepseek-v4.1",
  "runs": [
    {"label": "c1", "dir": "/root/cann-ebpf/runs/vllm-c1", "concurrency": 1, "warmup_s": 10, "duration_s": 60},
    {"label": "c4", "dir": "/root/cann-ebpf/runs/vllm-c4", "concurrency": 4, "warmup_s": 10, "duration_s": 60}
  ]
}
```

### 3.2 `ops.jsonl`

```json
{"run_id":"run-1","op":"MatMul","category":"gemm","duration_ns":123456,"flops":17179869184,"bytes":1073741824,"dtype":"bf16"}
```

### 3.3 `events.jsonl`

```json
{"ts_ns":1795759440000000000,"label":"deploy vllm 0.30.0rc2","source":"deploy"}
```

### 3.4 `otel.jsonl`

```json
{"trace_id":"...","span_id":"...","parent_span_id":"...","service":"litellm","name":"chat.completions","start_ns":1795759440000000000,"end_ns":1795759441000000000,"status":"OK"}
```

### 3.5 `offcpu.jsonl`（可选，精确 ribbon）

```json
{"pid":123,"tid":124,"span_id":"...","start_ns":1795759440000000000,"end_ns":1795759440005000000,"kind":"offcpu"}
```

## 4. 里程碑

### M0 契约冻结

- integrator 写 `views_v2.go`、`docs/VIEWS-V2-CONTRACT.md` 和 CLI flag 占位。
- 所有视图先返回 `Available=false`、`Reason="not implemented"`，保证能编译、能渲染无数据态。
- 验收：`go test ./...` 通过；模板能显示 10 个“暂无数据”面板。

### M1 数据层并行开发

- `cp-agent`、`sweep-agent`、`ts-agent`、`blocking-agent`、`tree-agent`、`roof-topo-agent` 同时开工。
- 每个 Agent 只写自己的文件；用合成 fixture 做单测，不依赖真实 vLLM。
- 验收：每个 builder 有 `available=false` 和 `available=true` 两类测试；边界值、空数据、单样本都有覆盖。

### M2 前端接入

- `frontend-agent` 按冻结契约实现 10 个图表和无数据态。
- 所有图表用原生 Canvas/SVG，不引入外部 CDN；单文件离线可打开。
- 每个 tooltip 必须显示 `source` 和 `confidence`；`inferred` 必须有可见标识。
- 验收：合成 fixture 生成的 HTML 在 Chrome/Edge 无脚本错误，截图通过。

### M3 集成

- integrator 接线 `ReportViewsV2`、CLI flags、`report.Data.ViewsV2`。
- 合并顺序：M0 契约 -> cp -> sweep -> ts -> blocking -> tree -> roof-topo -> frontend -> qa。
- 验收：`go test ./...`、`go vet ./...`、`gofmt -l`、模板 `node --check` 全部通过。

### M4 真实 vLLM 验收

- 在 vLLM-Ascend 上跑固定 workload：至少 1/2/4/8/16/32 六档，每档 warmup + 稳定窗口。
- 开启 msPTI 和 vLLM batch context（插件或 `--python-batch`），采一组 trace + metrics。
- 验收：关键路径、Summed Delay、Slack CDF、Treemap 至少各有一张来自真实数据的图；Roofline 没有 `ops.jsonl` 就显示不可用，不画假点。

### M5 交付

- 输出 `.analysis/views2/` 报告、截图、SHA-256、验收记录。
- 更新 `docs/HANDOFF.md`、`docs/GAPS.md`、README 的视图列表。

## 5. 合并与防冲突

```bash
git worktree add ../cann-ebpf-wt-cp -b feat/views2-cp
git worktree add ../cann-ebpf-wt-sweep -b feat/views2-sweep
git worktree add ../cann-ebpf-wt-ts -b feat/views2-changepoint
git worktree add ../cann-ebpf-wt-blocking -b feat/views2-offcpu
git worktree add ../cann-ebpf-wt-tree -b feat/views2-treemap
git worktree add ../cann-ebpf-wt-roof-topo -b feat/views2-roof-topo
git worktree add ../cann-ebpf-wt-frontend -b feat/views2-frontend
git worktree add ../cann-ebpf-wt-qa -b feat/views2-qa
```

规则：

- worker 禁止改 `internal/report/report.go`、`views.go`、`views_v2.go`、`template.html`、`cmd/cann-ebpf/report.go`。
- 每个 worker 只 `git add` 自己名下的路径，禁止 `git add -A`。
- integrator 负责 rebase/merge；frontend 在数据契约稳定后再合并。
- 任何视图字段变更先改契约文档，再改 `views_v2.go`，最后通知 worker。

## 6. 验收清单

- 10 个视图都有 `available=false` 的明确原因，且页面不出现空坐标轴。
- `CriticalPath`、`SlackCDF` 的 `confidence` 不得为空；推断版必须标 `inferred`。
- `OffCPU.mode` 必须是 `aggregate` 或 `exact`；aggregate 不能画成精确时间线。
- `OperatorTreemap.source` 必须是 `mspti` 或 `host`；host 版不能叫算子 Treemap。
- `Roofline` 没有 per-op FLOPs/bytes 时必须不可用；不得用模型参数量或理论峰值补造散点。
- `ABDiff` 的 `workload_id` 不一致时拒绝出图。
- `ChangePoint` 的最小样本量、方法和事件标记必须写进报告。
- `ServiceTopology` 无上游 trace 时只能标 `scope=process`，不能宣称端到端。
- HTML 单文件、离线可开，10 个视图在 1600px 宽度下可用。

## 7. 风险

| 风险 | 影响 | 处理 |
|---|---|---|
| vLLM 尚未部署 | M4 无法开始 | 先完成 vLLM-Ascend 部署和 `/metrics` 验证 |
| msPTI 开销/版本不匹配 | 关键路径/设备视图缺少数据 | 先在测试窗口验证 CANN 9.x + msPTI；失败则只出 host 视图并标不可用 |
| vLLM 插件侵入 | 影响推理进程 | 优先 `--python-batch`；插件只在测试窗口安装 |
| stream/event 依赖不完整 | 关键路径不严格 | 先出 `inferred` 版，报告中明确；补齐依赖边后再升级 `exact` |
| 无 per-op FLOPs/bytes | Roofline 无法出图 | 接 torch_npu profiler 或 `ops.jsonl`；否则保持不可用 |
| 无上游 OTel | 服务拓扑只能进程内 | 先做 scope=process；端到端另立网关/LiteLLM 埋点任务 |
| HTML 体积过大 | 报告打开慢 | 每视图 TopN/分桶上限；大量节点折叠；延迟渲染 |

## 8. 启动清单

开始 M1 前需要确认：

- vLLM-Ascend 版本、启动命令、`/metrics` 端口和进程拓扑。
- 固定 prompt 文件、输入/输出 token 分布、warmup 时长、每档时长。
- 压测窗口和最大并发；是否允许影响生产流量。
- msPTI 和插件/`--python-batch` 的授权。
- Roofline 的 `ops.jsonl` 来源；服务拓扑的 OTel/日志来源。

没有这些输入时，M0-M3 仍可继续：先用合成 fixture 把 10 个视图和契约做完，真实数据验收留到 M4。

## 9. 并行执行编排

| 阶段 | 同时运行 | 主要产出 | 退出条件 |
|---|---|---|---|
| M0 | `integrator`；`qa-agent` 只准备 fixture 骨架 | `views_v2.go`、契约文档、10 个 unavailable stub | `go test ./...` 通过；报告能渲染 10 个“暂无数据”面板 |
| M1 | `cp-agent`、`sweep-agent`、`ts-agent`、`blocking-agent`、`tree-agent`、`roof-topo-agent` | 6 个数据层 builder + 单测 | 每个 builder 同时覆盖 `available=true/false`；空数据、单样本、边界值通过 |
| M1.5 | `frontend-agent`、`qa-agent` | 10 个图表、离线 HTML、端到端集成测试 | 合成 fixture 在 Chrome/Edge 无脚本错误；tooltip 显示 `source/confidence` |
| M2 | `integrator` | 合并、CLI、`report.Data.ViewsV2` 接线 | `go test ./...`、`go vet ./...`、`gofmt -l`、模板语法检查通过 |
| M3 | vLLM runtime 探测 + `qa-agent` | 真实 vLLM 的 process/metrics/trace 基线 | `/metrics`、进程拓扑、batch context 三项均确认 |
| M4 | `qa-agent` + `integrator` | 1/2/4/8/16/32 并发验收报告、截图、SHA | 至少关键路径、延迟拐点、Treemap、off-CPU 有真实数据；不可用视图明确说明原因 |

并行上限是 8 个工作树（M1 的 6 个数据层 Agent，加 frontend、QA）。`integrator` 不参与并行实现，只负责契约、接线和合并。任何两个 Agent 不得修改同一个文件。

## 10. Agent 启动单

通用要求：每个 Agent 先读 `docs/MULTI-AGENT-PLAN-VLLM-VIEWS.md`、`docs/VIEWS-V2-CONTRACT.md` 和现有 `internal/report` 数据结构；只修改自己名下的路径；禁止 `git add -A`；完成后必须报告改动文件、测试命令、`available=true/false` 用例、未解决风险。

| Agent | 首个任务 | 完成定义 |
|---|---|---|
| `integrator` | 冻结 `ViewStatus`、`ReportViewsV2`、10 个子类型和 CLI flag；先返回 unavailable stub | M0 全部测试通过；字段变更流程写入契约；不把 builder 逻辑塞进 `report.go` |
| `cp-agent` | 从 span/device/batch context 建 DAG，输出关键路径、Summed Delay、Slack CDF | 没有依赖边时明确 `inferred`；计算过程有单测；不把聚合时长冒充关键路径 |
| `sweep-agent` | 解析 `sweep.json`，生成负载-延迟拐点；实现同日 workload 的 A/B 差分 | 少于 4 档或 workload_id 不一致时拒绝出图；每个点能追溯到 run 目录 |
| `ts-agent` | 对长时序 metrics 做变化点检测并叠加 `events.jsonl` | 记录方法、窗口、阈值和最小样本量；短 run 返回 unavailable |
| `blocking-agent` | 先做 span 内 `offcpu_ns/futex_ns` 聚合 ribbon，再留精确区间输入 | `mode=aggregate` 与 `mode=exact` 不混用；没有区间不画精确时间线 |
| `tree-agent` | 从 msPTI device span 聚合 KERNEL/MEMCPY/HCCL 树；无 device 时降级 host 函数树 | `source=mspti` 或 `source=host` 必须可见；host 版不能标成“算子 Treemap” |
| `roof-topo-agent` | 实现 `ops.jsonl` Roofline 和 `otel.jsonl` 服务拓扑 | 没有 per-op FLOPs/bytes 时 Roofline 不可用；没有上游 trace 时拓扑只能 `scope=process` |
| `frontend-agent` | 按契约画 10 个图表、无数据态、tooltip、`inferred` 标识 | 单文件离线打开；无外部 CDN；不根据空数据自行补点 |
| `qa-agent` | 合成 fixture、端到端测试、真实 vLLM 验收脚本 | fixture 明确标记 synthetic；真实 run 的 SHA、命令、环境写入验收记录 |

## 11. vLLM 预检门禁

进入 M4 前，由 runtime/QA 在目标机执行并记录，不以本机推测代替：

1. 记录 vLLM-Ascend 版本、启动命令、模型名、容器名和 `/metrics` 端口。
2. 记录 `vllm serve`、`VLLM::EngineCor`、`VLLM::Worker_TP` 的 PID/PPID 树，确认 profile 的 `all_matching + process_tree` 能覆盖。
3. `curl -fsS http://127.0.0.1:<port>/metrics`，确认至少出现 `vllm:num_requests_running`、`vllm:num_requests_waiting`、`vllm:time_to_first_token_seconds`。同时在 `internal/report/metrics.go` 的 `metricPriority` 补 `vllm:` 冒号变体并加测试，否则 `--max-series` 可能先截掉关键 vLLM 指标。
4. 检查 `/run/cann-ebpf/vllm-ascend.sock` 是否存在、权限是否允许目标进程连接；没有插件时明确改用 `--python-batch`，并记录 CPython/vLLM 版本。
5. 先跑 30 秒空载和 30 秒轻载，确认 attach/detach 不报错、证据可读、无原始文件覆盖。
6. 任一项不通过，M4 状态写 `blocked` 或 `unavailable`，不得拿 Orinfer 或历史 run 代替。

## 12. 第一步执行顺序

1. `integrator` 完成 M0 契约和 stub；这是唯一的阻塞项。
2. M0 合并后，立即启动 M1 的 6 个数据层 Agent；`frontend-agent` 和 `qa-agent` 同日启动，但只依赖冻结契约和合成 fixture。
3. 数据层完成一个合并一个，合并顺序为 `cp -> sweep -> ts -> blocking -> tree -> roof-topo`；`frontend` 最后接入真实模板。
4. 真实 vLLM 预检通过后，QA 才执行 M4；未通过则交付“代码完成、真实数据待验收”的报告，不伪造成功。

## 13. 交付状态（2026-11-27）

本计划已执行到 M4，结果以 `docs/M4-VIEWS2-VALIDATION.md` 为准：

- M0–M2：10 个视图契约、10 个 builder、CLI/模板集成和单元测试完成。
- M3：真实 vLLM 的进程拓扑、`/metrics` 和 batch context 已确认；8 个 Worker 并行 attach。
- M4：已生成真实 attach 报告和真实六档并发扫描报告。attach run 的 28,905 spans、217 batch spans、8 个 request ID 和原始 SHA 已记录。
- `load_latency` 已提供 P50/P95/P99；histogram 首帧跳过，活动背景负载时不计算容量拐点。
- `critical_path`、`roofline`、`ab_diff`、`change_point`、算子 exact Treemap 和端到端拓扑仍按缺证据显示 unavailable/inferred/partial；后续接入 msPTI、ops、OTel 和 A/B run 后再验收。
