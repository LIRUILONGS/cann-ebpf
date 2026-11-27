### 真实报告图表（全部来自报告 HTML）

**浏览器直接看全部图：** [`docs/demo/all-charts.html`](docs/demo/all-charts.html)。页面内嵌四份真实报告，报告本身内嵌 `DATA` JSON，不依赖 CDN；Windows 可直接运行 `start docs/demo/all-charts.html`，Linux 可运行 `xdg-open docs/demo/all-charts.html`。`iframe` 内也能继续滚动、悬停和切换报告控件。

| 图 | 数据来源 | 指标 / `attach` 字段 | 覆盖的图表与功能 | 证据边界 |
|---|---|---|---|---|
| 图 1 | 真实 vLLM-Ascend attach：`vllm-ascend-20261127T072159.325022000Z`，16.37 s | `start_ns`/`end_ns` 计算的 duration、`pid`/`tid`、`function`/`category`、`manifest` | KPI、线程时间轴、延迟散点 | 28,905 spans、18 TID / 9 PID、dropped 0；没有 `device.jsonl` 时设备 KPI 显示 `--`，没有 trace 时不宣称 request 下钻 |
| 图 2 | 同一真实 attach run | duration、时间桶、函数分位、`offcpu_ns`/`futex_ns` | 时间热力图、TopN、延迟热图、次秒偏移热图、函数分位带、尾 cohort | off-CPU 是 `spans` 聚合的 `partial` 模式；不是调用栈火焰图，也不把 host 时间冒充设备执行 |
| 图 3 | 36 机器真实 Orinfer 8 请求轻载 metrics-only 报告，30 s | `metrics.jsonl` 的 gauge/counter/histogram；DCMI、proc、system、`prometheus:orinfer` | 多指标小倍数、指标统计表、直方图窗口分位 | 16,006 点、541 series；没有 `spans.jsonl`，因此函数级时间轴、关键路径和设备四象限不可用 |
| 图 4 | final7 M3 device join 验证样本 | `device.jsonl` 的 `API`/`KERNEL`/`MEMCPY`/`HCCL`、`correlationId`、`streamId`，以及 host span 时间窗 | 延迟 CDF、设备四象限、设备忙率 | 11,859 host spans + 148,466 device spans、慢 Sync 305/305、可匹配率 100%、设备 busy 405.6 ms；设备侧是**合成 ACL**，只验证 join/UI 路径，不是生产性能结论 |
| 图 5 | 真实 vLLM attach run | `views_v2`、`batch_id`/`request_ids`、`offcpu_ns`/`futex_ns`、可选 device/ops/OTel 证据 | vLLM 十视图及每张卡的 `available`/`source`/`confidence` | `off_cpu` = available/partial；`operator_treemap` = available/inferred（host fallback）；`service_topology` = available/partial（process scope）；无 sweep/device/ops/A-B 的视图明确 unavailable |
| 图 6 | 真实六档并发扫描：c1/c2/c4/c8/c16/c32 | `sweep.json`、每档 metrics、TTFT/E2E、running/waiting、KV cache | 负载-延迟拐点，以及其余九张视图的证据门禁 | `load_latency` 保留真实观测但为 `partial`：背景 running 高于客户端并发，拒绝计算容量 knee；其余视图没有对应证据 |
| 图 7 | final7 报告的无 metrics 样本 | metrics 不可用、`manifest.warnings`、`dropped_events`、`notes` | 空态、采集告警、不可判定原因 | 这里展示的是“没有 metrics 就不画空曲线”的真实空态；空值不填 0，降级原因原样保留 |

#### 图 1：KPI、线程时间轴与延迟散点

[![图 1：真实 vLLM attach 的 KPI、线程时间轴与延迟散点](docs/images/readme/fig-01-host-overview.png)](docs/demo/all-charts.html#attach)

*真实 attach run；耗时、TID 泳道和离群点直接来自 `spans.jsonl`。设备关联和 trace 缺失时，报告显示不可用而不是补造。*

#### 图 2：时间热力图、TopN、延迟热图、次秒热图、函数分位带与尾 cohort

[![图 2：真实 vLLM attach 的时间分布和函数分析](docs/images/readme/fig-02-host-analysis.png)](docs/demo/all-charts.html#attach)

*同一真实 attach run；`duration`、时间桶、函数名和函数分位驱动。尾 cohort 对比 slow（p99 以上）与 typical（p45–p55），不是平均值截图。*

#### 图 3：真实 36 机器 metrics 小倍数与统计表

[![图 3：36 机器真实 Orinfer 轻载 metrics 小倍数与统计表](docs/images/readme/fig-03-real36-metrics.png)](docs/demo/all-charts.html#metrics)

*8 请求轻载、16,006 个采样点、541 series；展示 `orinfer_requests_running`、TTFT/E2E histogram、token counter、8 卡 AICore/HBM 等真实序列。该报告是 metrics-only，不提供 request 级关键路径。*

#### 图 4：延迟 CDF、设备四象限与设备忙率

[![图 4：设备 join 的 CDF、四象限与忙率](docs/images/readme/fig-04-device-join.png)](docs/demo/all-charts.html#device)

*final7 M3 join 验证样本；每个点是与 host span 匹配的设备区间，busy 为区间并集。因为来自合成 ACL，图只证明 join 计算和报告渲染正确，不能作为 V4 生产设备利用率结论。*

#### 图 5：vLLM 十视图与证据门禁

[![图 5：真实 vLLM attach 的十视图可用性](docs/images/readme/fig-05-vllm-attach.png)](docs/demo/all-charts.html#attach)

*真实 attach run；`load_latency` 缺 sweep，`critical_path`/`summed_delay`/`slack_cdf` 缺 device 关联，`roofline` 缺 per-op FLOPs/bytes，`ab_diff` 缺两次 run，`change_point` 缺长时序 metrics。卡片显示的不是空白占位，而是带原因的 `unavailable`。*

#### 图 6：真实六档并发扫描

[![图 6：真实 c1 到 c32 并发扫描](docs/images/readme/fig-06-vllm-sweep.png)](docs/demo/all-charts.html#sweep)

*真实六档扫描；`load_latency` 有真实曲线但为 `partial`。背景请求使 `running` 高于客户端并发，因此报告保留观测值、拒绝生成容量 knee；其他视图继续按证据门禁不可用。*

#### 图 7：无 metrics 的空态、告警和边界

[![图 7：无 metrics 时的空态和采集边界](docs/images/readme/fig-07-evidence-boundaries.png)](docs/demo/all-charts.html#device)

*final7 没有 metrics 输入时，报告在 metrics 面板显示“未采集”，同时保留采集告警、dropped/error、边界说明和不可判定原因；没有用 0 或示意曲线填补。*