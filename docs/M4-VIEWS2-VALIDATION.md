# vLLM 十视图 M4 验收记录

日期：2026-11-27
目标：vLLM-Ascend `0.23.0`，DeepSeek-V4-Flash，TP=8，DP=1
结论：十个视图已接入 `report.Data.ViewsV2`；真实 attach run 和真实六档并发扫描均已生成。报告只显示有证据的视图；没有 msPTI、per-op、OTel 或 A/B 证据的视图继续显示 `available=false`，不补点、不插值。

## 1. 代码状态

- `internal/report/views_v2.go`：十个视图的冻结数据类型。
- `internal/report/build_views_v2.go`：自动发现 run 目录下的 `sweep.json`、`ops.jsonl`、`events.jsonl`、`offcpu.jsonl`、`otel.jsonl`，逐个调用 builder。
- `report.Options` / `cmd/cann-ebpf/report.go`：新增
  `--sweep --ops --events --offcpu --otel --ab-a --ab-b --ab-workload-id
  --roofline-peak-tflops --roofline-peak-gbps --roofline-dtype
  --topology-root-service`。
- `internal/report/sweep.go`：读取每档 `load.json` 的真实 wall-clock 窗口；窗口前的最后一个 histogram 累计快照只作基线，窗口内只输出相邻快照差值。
- `internal/report/metrics.go`：histogram 首帧跳过；同一份增量同时生成 `:p50`、`:p95`、`:p99`，不生成 `_created` 时间戳序列。
- `internal/report/template.html`：十个 SVG 面板、状态、来源、置信度和无数据原因；不引入 CDN。

## 2. 真实 attach 证据

服务器 run：

```text
/root/cann-ebpf/runs/views2-attach-load-20261127T072157Z
```

本地只读副本：

```text
.analysis/views2/attach-real/
```

关键事实：

- `spans.jsonl`：18,879,190 bytes，SHA-256 `EBBFDFCF59403B413FD92C398A2CFAF25383A68D3FFE20C52A02050C6308F1FD`。
- 28,905 spans，217 batch spans，8 个唯一 request ID。
- 归因：thread 28,688、batch 217、unattributed 0。
- 目标：8 个 `VLLM::Worker_TP` 均有采集；另含 frontend/EngineCore 进程。
- trace span 0；device span 0；没有 msPTI 设备区间。
- 8 个 Worker 各有 1 个 batch 在采集结束时仍打开，`end_ns` 是最后观测值，不能当成函数返回。

真实 attach 报告：

```text
.analysis/views2/report-views2-attach-integrated.html
.analysis/views2/report-views2-attach-integrated-top.png
```

SHA-256：

```text
attach HTML  E6F85506EEC27711F94948D5A47FE391682980E9DD09906BD41DBD091BD93765
attach PNG   18C003FA2B55769108FEE61E2397151DACE9D81E9257AC15FA7F10CEFC191825
```

| 视图 | 状态 | 证据/限制 |
|---|---|---|
| load_latency | unavailable | 该 run 没有 `sweep.json` |
| critical_path | unavailable | 没有 device correlation |
| summed_delay | unavailable | 依赖 critical path |
| slack_cdf | unavailable | 依赖 critical path |
| off_cpu | available / partial | 28,688 条 aggregate row；不是精确 off-CPU 区间 |
| operator_treemap | available / inferred | host fallback，根节点是 `Host function aggregation (inferred)`；不是 msPTI 算子级 |
| roofline | unavailable | 没有 `ops.jsonl` 的 per-op FLOPs/bytes |
| ab_diff | unavailable | 没有两个同 workload 的完整 run |
| change_point | unavailable | 没有 metrics 长时序 |
| service_topology | available / partial | 9 个进程节点、0 条边；`scope=process`，没有端到端 trace |

## 3. 真实并发扫描

服务器目录：

```text
/root/cann-ebpf/runs/views2-sweep-20261127/
  c1/ c2/ c4/ c8/ c16/ c32/
```

本地副本：

```text
.analysis/views2/sweep-real/
```

manifest：`.analysis/views2/sweep-real/sweep.json`，workload `vllm-chat-64out-30s-20261127`。

客户端实际结果（`load.json`）：

| 并发 | OK | Errors | Client RPS | Client P50 | Client P95 |
|---:|---:|---:|---:|---:|---:|
| 1 | 6 | 0 | 0.171 | 5.5629 s | 7.8378 s |
| 2 | 10 | 0 | 0.309 | 6.6168 s | 6.8591 s |
| 4 | 18 | 0 | 0.499 | 7.3629 s | 9.1329 s |
| 8 | 32 | 0 | 0.965 | 7.8594 s | 9.2719 s |
| 16 | 64 | 0 | 1.822 | 8.5993 s | 9.9461 s |
| 32 | 97 | 0 | 2.239 | 12.5946 s | 19.2343 s |

每档客户端请求均成功，结束时 `/health` 均为 200。`client rps` 是客户端观测值，不是服务端容量结论。

服务端 metrics 原始文件 SHA-256：

```text
c1  c9013fc47160b5133d388b879163a80baf9c407ee8e3adc97d7f36c70a6481c7
c2  36b0749069c6e0e6219673caf46fe11bbf4bb0bbb6cb04fb04e5e244e15bcfc2
c4  a84bd80d613a58867f9e9da68484f877fb98a054ee09bcdaf4ad18b135a978cd
c8  7f08c11a47c8ebb6249b815e061ace678df73ea497412f6e72ead41f2d9b92ef
c16 ac8ca3b76959729864276483641338e585cedb111e1661cfe92b882938a9db29
c32 7f53e7206d8560f70266d438bd1d4b63c9a3bf439f78eaa596a1287c28668fc9
```

### 3.1 这次扫描的限制

在采集时，`/metrics` 显示服务端仍有 8–11 个来自 LiteLLM 的活动请求；六档客户端负载不是唯一流量。报告中的 `running_max` 高于客户端并发（例如 c1=12、c2=13），因此：

- `load_latency` 保留真实曲线，但标记 `confidence=partial`；
- 代码检测到背景负载后不再给出 `knee_concurrency`，避免把污染条件下的拐点当成容量上限；
- 客户端 `load.json` 的成功数、错误数和 P50/P95 是独立可复核的；服务端 histogram 的 P50/P95/P99 只能作为噪声条件下观测，不能当成干净容量测试。

这不是把没有的视图伪装成有数据，而是把证据边界写在报告和验收记录里。需要干净拐点时，应在 `running=0` 的窗口用同一 manifest 重跑六档；重跑前不应停止或重启正在承载其他请求的 vLLM。

交付时复核（2026-11-27 16:05 CST）：`vllm-ds4-8905.service=active`、`/health=200`，
`vllm:num_requests_running` 在 11→10 之间波动，`num_requests_waiting=0`。生产流量仍未清空，
因此没有执行第二次扫描，也没有把现有六档数据改写成“干净容量上限”。

## 4. histogram 语义

Prometheus `vllm:time_to_first_token_seconds` 和 `vllm:e2e_request_latency_seconds` 是进程启动以来的累计 histogram。实现遵循：

1. 第一帧没有前一个边界，不生成窗口点；
2. 相邻累计帧做差，只有正增量且 bucket layout 不变时才生成 P50/P95/P99；
3. 负增量、reset、bucket layout 变化不生成点；
4. `_created` gauge 是 Unix 创建时间，不是延迟，直接排除；
5. 不把直方图插值称为单请求精确值；报告里的值是 bucket 内线性插值。

## 5. 复现命令

构建和测试：

```powershell
go test ./... -count=1
go vet ./...
gofmt -l internal cmd
```

真实 attach 报告：

```powershell
go run ./cmd/cann-ebpf report `
  --input .\.analysis\views2\attach-real `
  --out .\.analysis\views2\report-views2-attach-integrated.html `
  --title "CANN eBPF vLLM 十视图 - 真实 attach run"
```

六档扫描报告：

```powershell
go run ./cmd/cann-ebpf report `
  --metrics .\.analysis\views2\sweep-real\c1\metrics.jsonl `
  --sweep .\.analysis\views2\sweep-real\sweep.json `
  --out .\.analysis\views2\report-views2-sweep-integrated.html `
  --title "CANN eBPF vLLM 十视图 - 真实并发扫描"
```

SHA-256：

```text
sweep HTML   14ED73419D81C6E5BB615A9EFA7C5653825E70C57EF215F3639B6EABB812FAA6
sweep PNG    3D67F169EDAD232DD59321254A4F859D967933C2E0592AD5B7B8903B9D48C30B
```

## 6. 后续证据门禁

| 缺口 | 需要的真实证据 | 当前行为 |
|---|---|---|
| 严格关键路径 | `device.jsonl` + msPTI correlation | 显示 unavailable，不拿 host 时长替代 |
| 算子 Treemap | msPTI `KERNEL/MEMCPY/HCCL` | 只显示 host fallback，标 `inferred` |
| Roofline | `ops.jsonl` 的 FLOPs/bytes | 显示 unavailable，不补理论点 |
| 端到端拓扑 | OTel/W3C trace 或网关日志 | 只显示 `scope=process` |
| 变化点 | ≥5 分钟、≥30 个有效点的 metrics | 短 run 显示 unavailable |
| A/B 差分 | 两个同 workload 完整 run | 缺 run 时显示 unavailable |
| 火焰图 | 调用栈采样 | 当前没有栈采样，不把现有视图叫火焰图 |

## 7. 验收结果

```text
go test ./... -count=1    pass
go vet ./...              pass
gofmt -l internal cmd     no output
Chrome headless           two reports rendered, no page-level JS exception observed
inline script syntax      node --check pass for both reports
```

报告截图：

- attach：`.analysis/views2/report-views2-attach-integrated-top.png`
- sweep：`.analysis/views2/report-views2-sweep-integrated-top.png`
