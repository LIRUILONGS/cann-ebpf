# 评审：server-live-batch 报告（2026-11-27）

对象：`.analysis/server-live-batch-20261127/report.html`  
生成时间：2026-11-27T09:41:02Z  
文件：18,078,918 bytes，SHA-256 `c1b0287fdd2ef98e951961b96082e12bcb7bc0363e2ade2232a7812d068b95d7`

## 1. 这份报告实际包含什么

| 项 | 值 |
|---|---|
| run_id | `vllm-ascend-20261127T094020.474635795Z` |
| 采集窗口 | 约 35 秒（17:40:20–17:40:56） |
| spans | **46,717** |
| PID / TID | 9 / 18 |
| 慢 span（>1ms） | **1,391** |
| profile | `vllm-ascend` |
| 状态 | complete |

指标：

| 项 | 值 |
|---|---|
| metrics 原始 series | **991** |
| 报告实际渲染 | **256**（超过上限被截断） |
| 采样点 | 27,102 |
| interval | 1s |
| sources | `proc=163`、`npu-smi=72`、`prometheus:vllm=21` |
| kinds | counter=163、gauge=81、histogram=12 |
| 仍被画成曲线的 `*_created` | **4** 个（TTFT/e2e/prompt/generation 的 created 时间戳） |

## 2. 图表覆盖：不是“所有指标都有图”

10 个视图里只有 **3 个可用**：

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
| `change_point` | **unavailable** | 窗口 29.5s < 300s |

所以：
- **视图层：3/10 有图，7/10 是“不可用 + 原因”**；
- **指标层：991 个 series 只画了 256 个**，不是全部；
- 没有伪造 unavailable 数据，这一点是对的。

## 3. 做得好的地方

- 10 个视图都有明确的 `available/partial/inferred/unavailable` 和 reason；
- `operator_treemap` 明确标 `inferred`，没有冒充设备算子级；
- `off_cpu` 标 `partial`，没有声称覆盖所有线程；
- 46,717 spans、27,102 个指标点都内嵌，报告可离线打开；
- 有 metrics 截断 warning（991→256），没有假装全量；
- 构建、单测通过，报告 schema 完整。

## 4. 必须修的问题

### P0：报告内嵌了主机名/绝对路径

报告 `DATA.input` 和 `DATA.run.host` 仍包含：

- `target-host`（主机名）
- `/opt/cann-ebpf/runs/...`（服务器绝对路径）

如果这份报告要发给别人/公开，必须加**脱敏选项**（默认隐藏 host、input 路径只留 basename），或在生成后做替换。

### P0：`*_created` 时间戳被当指标画

4 个 `*_created` 是 Prometheus 的进程创建时间戳，不是延迟/吞吐：

- `vllm:time_to_first_token_seconds_created`
- `vllm:e2e_request_latency_seconds_created`
- `vllm:request_prompt_tokens_created`
- `vllm:request_generation_tokens_created`

它们会画成巨大的平线，误导读者。应在加载 metrics 时过滤 `_created`（以及 `_info`）。

### P1：991 个指标只画 256 个，但 UI 没突出“已截断”

- warning 只出现在数据里，页面顶部没有“metrics 991→256”的提示；
- 用户会以为所有指标都在图上；
- 建议顶部加“指标覆盖：256/991，按 source 分组，可展开 Top-N”。

### P1：18MB 页面 + 46,717 spans + 最多 256 张动态图

- 浏览器打开、滚动、重绘都会慢；
- `MaxSpanPoints` 默认 100k 太大；
- 建议：默认 Top-N（如 24 张关键图）、按 source 折叠、span 点降到 10k–20k、必要时分页。

### P1：两个画廊重复

- `docs/demo/all-reports.html` 是 17MB 自包含版本；
- `docs/demo/all-charts.html` 只有 13KB，依赖同目录 `report-*.html`；
- 对外只保留一个自包含版本，另一个删除或归档。

### P2：XSS 转义不完整

`RenderBundle` 只转义了 `DOCS` 的 `</`；`REPORTS`/`SUMMARY` 的 JSON 仍是原样插入，manifest 的 label/description 含 `</script>` 时可跳出脚本块。三个 JSON 都要转义。

### P2：缺少关键指标/视图

- 视图缺：sweep、device correlation、ops、A/B、5 分钟序列；
- 指标缺：HBM 带宽、ECC、频率、HCCS/PCIe、逐 request TTFT/ITL（当前只有 batch/局部）。

## 5. 现在能做什么 / 需要什么数据

### 现在就能做（不需要新采集）

1. 过滤 `*_created` / `*_info`；
2. 顶部加“视图覆盖 3/10、指标 256/991”的摘要；
3. 指标按 source 分组，默认只画关键 SLO，支持展开；
4. 报告默认脱敏 host/input 路径；
5. 默认 Top-N 图 + 更低 span 上限，控制 HTML <5MB；
6. 只保留一个自包含画廊；
7. 三个 JSON 全部做 `</` 转义 + 测试；
8. 把 unavailable 的 reason 汇总成一张表，放在报告开头。

### 需要补数据

| 数据 | 解锁 |
|---|---|
| `sweep.json`（≥4 档并发） | `load_latency` |
| 同窗口 host + device | `critical_path`、`summed_delay`、`slack_cdf` |
| `ops.jsonl` | `roofline` |
| A/B 两次 run | `ab_diff` |
| ≥5 分钟稳定 metrics | `change_point` |
| HBM 带宽/ECC/频率 | 硬件图表 |
| request/trace ID 贯通 | 逐 request TTFT/ITL/全链路 |

## 6. 结论

**这份 live 报告证明了采集和报告链路可用，但还不能说“所有指标都有图”：**

- 视图：**3/10 可用**；
- 指标：**256/991 渲染**；
- 关键图（critical path、roofline、A/B、change point）都缺证据；
- 还有 `*_created` 误导、18MB 体积、host/path 泄漏三个交付问题。

**建议他先交 P0/P1：过滤 `_created`、覆盖摘要、默认 Top-N、脱敏、单画廊、XSS 测试。**  
这些做完，这份报告就可以作为“真实 V4 的 host + metrics 报告”对外展示；device/critical path 等再等补齐数据。
