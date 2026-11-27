# 评审与修复记录：server-live-batch 报告（2026-11-27）

对象：`.analysis/server-live-batch-20261127/report.html`（修复前）与
`report-review-v2.html`（修复后）。
生成时间：2026-11-27T09:41:02Z  
原始 run：`vllm-ascend-20261127T094020.474635795Z`

> 事实更正：报告里的主机是 `bms-84865758`，路径是
> `/root/cann-ebpf/runs/ebpf-metrics-batch-20261127T094018Z`，不是早期评审里写的
> `target-host` / `/opt/cann-ebpf`。这些是真实证据的一部分；本项目不做默认脱敏，
> 也不会为了“看起来干净”修改原始证据。

## 1. 这份报告实际包含什么

| 项 | 值 |
|---|---|
| run_id | `vllm-ascend-20261127T094020.474635795Z` |
| 采集窗口 | 约 35 秒（17:40:20–17:40:56） |
| spans | **46,717** |
| PID / TID | 9 / 18 |
| 慢 span（>1ms） | **1,391** |
| profile | `vllm-ascend` |
| manifest 状态 | `complete` |
| 修复前 HTML | 18,078,918 bytes |
| 修复后 HTML | 8,649,046 bytes（默认 20k 散点上限） |

指标计数必须按下面这条链读，不能再把“原始 series”和“图表候选”混成一个数：

| 阶段 | 字段 | 数量 | 说明 |
|---|---|---|---|
| 采集端原始 series | `meta_series` | **961** | `metrics.meta.json` 里登记的 series |
| 报告投影后 | `projected_series` | **991** | 按窗口重建，histogram 展开成 p50/p95/p99，所以会多于 961 |
| 上下文指标过滤 | `filtered_series` | **41** | `*_created` / `*_info` 只留在原始 `metrics.jsonl`，不画图 |
| 可绘制候选 | `eligible_series` | **950** | 991 − 41 |
| 实际上限 | `series_limit` | **256** | `--max-series` |
| 实际绘制 | `rendered_series` | **256** | 报告里真正有曲线的 series |
| 未绘制 | `truncated_series` | **694** | 950 − 256，warning 会写清楚 |

修复前的 warning 只写“991 超过上限 256，已截断”，没有说明 991 里包含 histogram
分位展开、也没有说明 `_created` 被过滤；这是本次修复的重点之一。

## 2. 图表覆盖：视图 3/10 可用，不是“所有指标都有图”

| 视图 | 状态 | 说明 |
|---|---|---|
| `off_cpu` | **available / partial** | 有真实阻塞采样，但只覆盖外层 span |
| `operator_treemap` | **available / inferred** | 基于 host span 推断，没有 msPTI 算子级数据 |
| `service_topology` | **available / partial** | 只到进程范围，缺 trace 传播 |
| `load_latency` | **unavailable** | 缺 `sweep.json`（至少 4 档并发） |
| `critical_path` | **unavailable** | 无 device correlation |
| `summed_delay` | **unavailable** | 依赖 critical path |
| `slack_cdf` | **unavailable** | 依赖 critical path |
| `roofline` | **unavailable** | 缺 `ops.jsonl` |
| `ab_diff` | **unavailable** | 缺 A/B 两次 run |
| `change_point` | **unavailable** | 本次窗口 29.5s < 300s |

结论：

- **视图层：3/10 有图，7/10 是“不可用 + 原因”**；
- **指标层：950 个可绘制候选里画了 256 个**，其余 694 个是主动限流，不是采集缺失；
- 没有伪造 unavailable 数据，这一点保持不变。

## 3. 已修复项（含验证）

| 项 | 状态 | 验证 |
|---|---|---|
| `*_created` / `*_info` 被画成误导性平线 | **已修复** | 投影阶段过滤；v2 报告 `DATA.metrics.series` 中 `_created` / `_info` 名称计数均为 0；原始 `metrics.jsonl` 未改动 |
| 991→256 截断在页面顶部不可见 | **已修复** | 顶部新增“视图覆盖 / 指标覆盖 / 证据边界”三张卡；指标卡显示“已绘制 / 候选、投影后、过滤、未绘制、上限” |
| 18MB 页面 + 46,717 spans 太重 | **已修复（默认值）** | `--report-max-spans` 与 `--max-spans` 默认从 100000/0 改为 **20000**；`0` 仍表示全量。v2 报告 8.6MB，原始 `spans.jsonl` 完整保留 |
| series 计数口径混乱 | **已修复** | 报告写入 `meta_series`、`projected_series`、`filtered_series`、`eligible_series`、`rendered_series`、`truncated_series`、`series_limit` |
| 三个 JSON 的 `</script>` 转义 | **已有覆盖，无需改** | 当前实现只有一个 `DATA` payload，`Render` 统一做 `</` → `<\/`；`report_test.go` 已用 `</script>` 注入用例覆盖 span 名和标题。仓库中不存在旧评审提到的 `RenderBundle` |
| 两个画廊重复 | **已不存在** | 当前 `docs/demo/` 只有 13KB 的 `all-charts.html` 索引和 4 个 `report-*.html`，没有 17MB 的 `all-reports.html`；无需再删 |
| 默认脱敏 host / 绝对路径 | **不采纳为默认** | 报告必须保留真实 host 和真实路径作为证据；如未来对外发布，再加**可选** `--redact`，不改变默认行为 |

## 4. 仍然缺的图：缺的是证据，不是代码

| 缺的视图 | 缺什么数据 | 为什么这次没有 |
|---|---|---|
| `load_latency` | `sweep.json`，至少 4 档并发 | 本次只跑了一次 attach，没有并发扫描 |
| `critical_path` | host span + device 时间线同窗口 | 只采了 host，没有 `device.jsonl`；V4 不能停，collector 没注入 |
| `summed_delay` | 依赖 critical path | 上游 critical path 不可用 |
| `slack_cdf` | 依赖 critical path | 上游 critical path 不可用 |
| `roofline` | `ops.jsonl`（每算子 FLOPs/bytes） | 当前没有采集算子级算力/带宽数据 |
| `ab_diff` | A/B 两次同 workload run | 只有一次 run，没有对照组 |
| `change_point` | ≥5 分钟稳定序列 | 本次窗口 29.5 秒，被判定太短 |

另外，HBM 带宽、ECC、频率、HCCS/PCIe、逐 request TTFT/ITL 这些指标采集端还没有
对应数据源；报告不会用推断值补齐。

## 5. 按时间分批的采集计划

原则：**短窗口采 host + device，长窗口只采 metrics**。长时间 attach 会产生巨量
spans，既拖慢报告也干扰业务；metrics-only 的代价小得多。

| 批次 | 窗口 | 方式 | 解锁 | 状态 |
|---|---|---|---|---|
| A | 30–60s | `attach` + `--metrics` | host spans、batch、device、基础指标、off-CPU、TopN、拓扑 | 已完成（本报告） |
| B | 6 分钟 | `sample --duration 360s` | `change_point`（≥300s、≥30 点） | 本次执行 |
| C | 20–30 分钟 | `sample --duration 1200s/1800s` | KV cache、HBM/内存、队列漂移、长稳基线 | 待执行 |
| D | 4–6 档 × 30–60s | `sweep` + 固定 workload | `load_latency` 拐点 | 需单独压测窗口 |
| E | 与 D 同窗口 | host + msPTI `device.jsonl` | `critical_path`、`summed_delay`、`slack_cdf` | 需维护窗口注入 collector |
| F | 2 × 60s | 两次相同 workload | `ab_diff` | 需固定模型/prompt/max_tokens |
| G | 单次 | `ops.jsonl` | `roofline` | 需算子级 FLOPs/bytes |

批次 B/C 使用 `scripts/run_metrics_window.sh`，每次都写新目录，拒绝覆盖：

```bash
cd /root/cann-ebpf
OUT=/root/cann-ebpf/runs/metrics-window-b-<UTC>
DURATION=360s sh run_metrics_window.sh
```

采集完成后生成 metrics-only 报告：

```bash
./cann-ebpf-review report \
  --metrics /root/cann-ebpf/runs/metrics-window-b-<UTC>/metrics.jsonl \
  --out /root/cann-ebpf/runs/metrics-window-b-<UTC>/report.html
```

### 5.1 批次 B 实测（2026-11-27 18:06–18:12）

| 项 | 值 |
|---|---|
| 窗口 | **359.0 s**（`sample --duration 360s`） |
| 采样点 | **292,104** |
| metrics 文件 | 73,224,623 bytes，SHA-256 `d25f642a...b4912f` |
| exit code | `0` |
| 服务健康 | 采集前后 `/health` 均为 `200` |
| `change_point` | **available**；256 series、1,222 个变化点、PELT-like 方法 |
| 并发请求 | `num_requests_running` 5–16，`num_requests_waiting` 0–1 |
| KV cache | `vllm:kv_cache_usage_perc` 4.4%–41.0%，说明窗口内有真实推理流量 |
| NPU 显存 | 8 卡 `npu_hbm_used_ratio` 均为 93.7%，窗口内基本恒定 |
| Worker RSS | 各 Worker 约 5.5–6.15 GB，窗口内波动约 1%–3% |

这份数据证明长窗口 metrics-only 采集不影响正在运行的 V4，并且能拿到真实的
队列、KV cache 和内存漂移；但它是采样观测，不是逐 request 关键路径。

## 6. 本次修复后的结论

- 这份 live 报告证明了采集、投影、报告链路可用；
- `_created` 误导、覆盖不可见、默认 18MB 三个交付问题已修；
- 视图仍是 **3/10**，指标仍是 **256/950 可绘制候选**，缺的图都明确写了证据依赖；
- 报告保留真实 host、真实路径和原始 JSONL，不做默认脱敏，不修改原始证据；
- 下一步按第 5 节 B→C→D→E 补数据，其中 B 只读 metrics，不影响正在运行的 V4。
