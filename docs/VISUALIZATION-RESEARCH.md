# CANN eBPF 性能可视化调研与下一阶段方案

日期：2026-11-27
状态：调研完成；M1/M2 已落地（`sample`、metrics 叠加、延迟热图、次秒偏移热图、函数分位带、尾 cohort、多指标小倍数）
范围：只读性能报告 + 独立指标采集；不修改 `spans.jsonl`、`device.jsonl`、`hardware.jsonl`

## 0. 结论摘要

现有 `cann-ebpf report` 已经覆盖了线程时间轴、延迟散点、TID/函数时间热图、TopN、CDF、设备四象限和设备忙率。下一阶段不建议继续堆同质图表，而是补三类缺口：

1. **分布形状**：延迟热图、次秒偏移热图，回答“长尾、双峰、周期性尖刺在哪里”。
2. **聚合上下文**：聚合感知时间轴、尾 cohort 对比，回答“这条慢调用相对同函数/同批次到底慢多少，慢在哪些操作组合”。
3. **因果关系**：关键路径瀑布、Summed Delay vs Critical Path、Slack CDF，回答“真正决定 batch 完成时间的是哪一段，host 再慢多少才会影响完成”。

指标必须能独立采集。trace 是高频、事件驱动、可能丢事件的证据；metrics 是低频、轮询、可长期运行的证据。两者用单调时钟和低基数标签只读对齐，任何一侧缺失都不补造。

本轮已实现 **M1 + M2**（约一周的并行开发已完成）：

- M1：`cann-ebpf sample`、`cann.metrics.v1` schema、report 接入、多指标小倍数、metrics-only 降级报告。
- M2：延迟热图、次秒偏移热图、聚合感知时间轴、尾 cohort 对比。

暂不做：没有栈采样却叫“火焰图”、没有 per-op FLOPs/bytes 却画 Roofline、没有 request ID 却宣称 request 级关键路径、双 Y 轴/雷达图/3D 图。实现状态与证据见 `docs/HANDOFF.md` 和 `.analysis/report-samples/QA-REPORT.md`。

## 1. 调研方法与范围

检索时间：2026-11-27。

来源：

- OpenAlex、Crossref、arXiv、USENIX/ACM 论文页面；
- 工业实践：Netflix FlameScope、Brendan Gregg 的 latency heat map/off-CPU/USE、Perfetto、Vampir、Paraver、Scalasca、Meta Holistic Trace Analysis。

说明：

- 下表中的“论文”指可核验的会议/期刊/arXiv 文献；“实践”指工业博客或工具文档，不作为学术结论引用。
- 这里关注的是**表达方式**，不是具体绘图库。所有图都应能在单文件离线 HTML 中用 Canvas/CSS/JS 实现，不引入 CDN。

## 2. 论文与实践给出的 9 条结论

### 2.1 单条 trace 不够，必须给聚合上下文

TraVista 指出，单条 trace 的 Gantt 图只能回答“这次请求发生了什么”，不能回答“这次请求相对典型请求慢在哪里”。它把 metric、temporal、structure 三类聚合数据叠加到单条 trace 上。[8]

对本项目：时间轴不能只画 span 条；要叠加同函数/同类别的 p50/p95/p99 包络，并高亮离群 span。

### 2.2 大 trace 的交互要靠分层数据结构和缓存

ESeMan 用分层数据结构和缓存，让并行事件时间轴在缩放、平移、过滤时保持亚 100ms 的取数时间，同时保持像素级准确。[10]

对本项目：报告里的时间轴不能一次性把全部 span 画出来；要按可视窗口做多分辨率聚合，只渲染当前窗口。

### 2.3 尾延迟要单独看，不要只看全局 CDF

The Tail at Scale 说明大规模服务的尾部延迟会显著影响整体体验；PonyTail 进一步把“慢请求”与典型请求对照，寻找尾服务时间对应的控制流和微架构模式。[5][11]

对本项目：全局 CDF 只能说明“有长尾”，不能说明“长尾由什么组成”。需要 top 1%/阈值以上 cohort 与中位 cohort 的函数、设备操作、off-CPU、资源状态对比。

### 2.4 关键路径不是各组件耗时求和

Mystery Machine 从低层 trace 中自动提取端到端关键路径，并归因延迟；Canopy 提供端到端 tracing 与分析。[6][7]

对本项目：host 入队、排队、kernel、memcpy、HCCL 可能并发。把各段耗时直接相加会高估并发组件的重要性；必须区分“summed delay”和“critical path”。

### 2.5 热图是发现长尾和周期性的低成本方法

Gregg 的 latency heat map 用时间 × 延迟 × 颜色展示延迟分布，能直接看到长尾、双峰和随时间的形态变化；FlameScope 的 subsecond offset 视图把时间按秒内偏移折叠，能暴露“每秒一次”“每 15 分钟一次”的周期性抖动。[1][2][3]

对本项目：这两张图只需要现有 span 时间戳，不需要新内核探针，性价比最高。

### 2.6 Roofline 需要真实的 per-op 计算量/访存量

Roofline 用 arithmetic intensity 和 achieved performance 判断计算受限/访存受限；面向 ML accelerator 的 roofline 工作进一步讨论了不同执行区间和能效边界。[12][13]

对本项目：没有每个算子的 FLOPs/bytes，就不能用模型参数量硬估。拿不到数据时，Roofline 必须显示“不可用”，不能画一条看似专业的假散点。

### 2.7 差分视图适合 A/B 和回归定位

Differential flame graphs 用两次 profile 的差异定位性能回归。[14]

对本项目：Orinfer vs vLLM-Ascend、版本升级前后、参数调整前后，适合同步缩放的时间轴 + CDF 位移 + TopN delta + 热图差值。

### 2.8 多指标用小倍数和共享时间轴，不要双 Y 轴

Horizon graph 的研究表明，多时间序列可以用更紧凑的编码在有限空间内保持可读性；工业实践更常用 small multiples（每个指标一行、各自 Y 轴、共享 X 轴）。[15]

对本项目：HBM、AICore、功耗、温度、队列深度、p95 应各占一行；不要把它们塞进一张双 Y 轴图，避免视觉上制造假相关。

### 2.9 off-CPU 等待是一等公民

Gregg 的 off-CPU 分析和 wPerf 都把“线程没有在 CPU 上运行”的等待事件作为独立瓶颈来源。[16][17]

对本项目：已有 `offcpu_ns`、`futex_ns` 字段。应把等待时间做成独立 ribbon/堆叠面积，而不是只放在 span tooltip 里。

## 3. 候选表达方式清单

优先级：

- P0：现有数据即可做，直接回答高频问题；
- P1：需要额外因果边/设备 join/负载数据，但价值高；
- P2：需要 per-op 计数或多次 run，条件满足再做；
- P3：探索项，不进入近期交付。

| # | 表达方式 | 回答什么问题 | 数据要求 | 依据 | 优先级 |
|---|---|---|---|---|---|
| 1 | 延迟热图（时间 × 对数延迟 × 次数） | 长尾、双峰、周期性尖刺在哪里 | span duration；或 latency histogram | Gregg 2010；FlameScope [1][2] | P0 |
| 2 | 次秒偏移热图（时间 × 秒内偏移 × 次数/耗时） | 是否每秒/每分钟/每 15 分钟规律抖动 | span start_ns；run 时长 ≥ 数秒 | FlameScope [2] | P0 |
| 3 | 聚合感知时间轴（span + p50/p95/p99 包络） | 这条 span 相对同函数/同类别是否异常 | spans 按函数与时间桶聚合 | TraVista；微服务 trace 聚合 [8][9] | P0 |
| 4 | 尾 cohort 对比（slow vs typical） | 慢调用多出了哪些函数/设备操作/等待 | spans + device + metrics；足够慢样本 | Tail at Scale；PonyTail [5][11] | P0/P1 |
| 5 | 多指标小倍数（共享 X 轴、十字光标） | HBM、AICore、功耗、队列、p95 是否同步变化 | `metrics.jsonl`；可选 span 派生 p95 | Horizon graph；USE/RED [15][25] | P0 |
| 6 | 负载-延迟拐点图 | 并发升到多少时 p99 开始抬升、吞吐何时饱和 | 多轮固定 workload + 并发梯度 + metrics | Little's Law；queueing [18] | P1 |
| 7 | 关键路径瀑布 | 一个 batch 的完成时间由哪条路径决定 | spans + device + 因果边/batch 归属 | Mystery Machine；Canopy [6][7] | P1 |
| 8 | Summed Delay vs Critical Path | 各组件直接相加是否高估了并发组件 | 同 7 | Mystery Machine [6] | P1 |
| 9 | Slack CDF | host 再慢多少才会影响 batch 完成 | 同 7，需关键路径与时间窗 | 关键路径分析派生 | P1 |
| 10 | off-CPU 等待 ribbon | host 阻塞、futex、runqueue 等待占了多少 | offcpu_ns/futex_ns；可选 sched 事件 | Gregg off-CPU；wPerf [16][17] | P1 |
| 11 | 算子 Treemap | 大量算子中时间集中在哪一层 | spans/device 的 category→op→stream 层级 | Shneiderman treemap [19] | P2 |
| 12 | Roofline 散点 | 计算受限还是访存受限，离屋顶多远 | 每算子 FLOPs/bytes、峰值算力/带宽 | Roofline；ML accelerator roofline [12][13] | P2（条件） |
| 13 | A/B 差分视图 | 两次 run 的延迟/热点/设备行为差异 | 两次同 workload run | differential flame graphs [14] | P1/P2 |
| 14 | 变化点/异常带 | 行为何时发生变化，是否与配置/部署相关 | 指标或 trace 时间序列 | change point survey；Chimbuko [20][21] | P2/P3 |
| 15 | 服务拓扑/依赖图 | 网关→LiteLLM→vLLM 的跨服务延迟 | 分布式 trace/span 关联 | Dapper；Canopy [22][7] | P3（当前范围外） |

补充：

- 真正的 flame graph 需要栈采样；当前工具只有 TID/函数/算子级数据，不能把时间轴或 Treemap 叫作 flame graph。[23]
- Treemap/Icicle 是层级耗时视图，不是栈火焰图；面积表示总耗时，颜色表示类别或 p95。
- Roofline 只能使用实测或可核验的 per-op 计数；模型参数量、理论峰值不能替代。

## 4. 推荐的首批视图

如果只能先做两张：**延迟热图 + 多指标小倍数**，前者解决“慢在哪”，后者解决“当时资源/队列是什么状态”。

如果能做四张：再加 **次秒偏移热图 + 尾 cohort 对比**。聚合感知时间轴是对现有时间轴的结构性增强，不单独占一个页面。

### 4.1 页面布局

报告仍然保持单文件、离线、只读。建议顺序：

1. KPI 条：span 数、p95/p99、device busy、HBM 最低余量、队列峰值、数据覆盖率。
2. 聚合感知时间轴：TID/stream 泳道 + 同函数分位包络 + 离群 span 高亮。
3. 两张热图并排：延迟热图、次秒偏移热图。
4. 多指标小倍数：HBM/AICore/功耗/温度/队列/KV cache/off-CPU/p95。
5. 尾 cohort + TopN delta：慢 cohort 与典型 cohort 的差异。
6. 设备/关键路径区：有 device 证据才显示；没有就明确写“不可用”。
7. 覆盖率和警告：缺样本、时钟、join、降采样、数据源失败。

每张图都要有 `available / reason / source / quality` 元信息。不可用时显示原因，不画空图，也不补造。

### 4.2 延迟热图

- X 轴：run 内时间桶；Y 轴：对数延迟桶（例如 10µs 到 10s，16–24 桶）；颜色：调用次数。
- 叠加 p50/p95/p99 折线；tooltip 显示时间范围、延迟范围、count、p95。
- 用途：长尾、双峰、周期性尖刺、阶段切换。
- 数据：span duration；没有 span 时可用 vLLM latency histogram 的窗口 delta。
- 实现：Canvas 绘制 2D 数组；按可视宽度分桶；颜色用顺序色板，不用 rainbow/jet。
- 与现有热图区分：当前 `HeatmapData` 是“TID/函数 × 时间”的 p95/慢调用数；这里新增的是“时间 × 延迟桶 × 次数”，两者回答的问题不同。

### 4.3 次秒偏移热图

- X 轴：run 内时间（秒）；Y 轴：秒内偏移（例如 50 行 × 20ms）；颜色：count 或 p95。
- 用途：每秒一次的调度抖动、定时器对齐、周期性锁竞争、GC/刷新尖刺。
- 数据：span start_ns；run 时长至少数秒。
- 实现：`offset = (start_ns - window_start_ns) % 1s`；按 offset 行与时间列聚合。
- 注意：这是“事件在秒内的位置”，不是请求延迟；tooltip 必须写清楚。

### 4.4 聚合感知时间轴

- 保留现有 TID/stream 泳道，在背景增加同函数或同类别的 p50/p95/p99 包络。
- 离群 span 用描边或标记高亮；点击后过滤到该函数/该时间段。
- 顶部提供 `show aggregate`、`only slow cohort`、`min duration` 三个开关。
- 数据：spans 按函数/类别与时间桶聚合；p95 用 nearest-rank，样本不足时标注 `n<30`。
- 依据：TraVista 的 metric/temporal/structure 聚合思想；微服务 trace 聚合分析。[8][9]
- 交互参考：Perfetto/Vampir/Paraver 的多泳道、同步光标与缩放；PyTorch trace 可参考 Meta HTA 的 temporal/kernel breakdown。[4][27]

### 4.5 多指标小倍数

默认行：

| 行 | 指标 | 单位 | 来源 | 说明 |
|---|---|---|---|---|
| 1 | AICore 利用率 | % | DCMI/npu-smi | 每设备或最大值 |
| 2 | HBM 使用率 | % | DCMI/npu-smi | 每设备或最大值 |
| 3 | 功耗 | W | DCMI/npu-smi | 观察降频/功耗墙 |
| 4 | 温度 | °C | DCMI/npu-smi | 观察热限 |
| 5 | 请求队列 | count | vLLM/Prometheus | running/waiting |
| 6 | KV cache 使用率 | % | vLLM/Prometheus | 观察显存压力 |
| 7 | off-CPU/futex 比例 | % | spans | host 等待 |
| 8 | p95/p99 延迟 | ms | spans/histogram | 与资源指标对照 |

规则：

- 指标选择遵循 USE（utilization/saturation/errors）与 RED（rate/errors/duration）口径，避免“什么都画”。[25]
- 共享 X 轴与十字光标；每个指标独立 Y 轴，但**不把两条不同单位的线画在同一坐标系**。
- 多设备时默认显示“最大值”并在 tooltip 列出各设备；不要静默平均。
- 指标间隔 > 2s 时，图上标 `coarse`，并禁止用它做毫秒级因果判断。
- 指标缺失时画断线/灰区，不插值填平。

### 4.6 尾 cohort 对比

- 定义：`slow` = top 1% 或超过阈值的 span/batch；`typical` = 中位附近 cohort。
- 对比维度：
  - 函数/类别占比与总耗时 delta；
  - 设备操作组合（kernel/memcpy/HCCL）占比；
  - off-CPU/futex 比例；
  - 同时段 HBM/AICore/队列/KV cache 分布。
- 展示：左右并排 bar + delta bar；必要时加一张 cohort fingerprint 热图。
- 统计：slow 样本 < 30 时标 `insufficient sample`；p99 要显示 N，不能只给一个数。
- 依据：PonyTail 的尾服务时间 outlier 分析；The Tail at Scale。[5][11]

## 5. 指标独立采集设计

### 5.1 为什么独立采集

- trace 采集需要 uprobe/msPTI，开销高、可能丢事件、通常只在窗口内运行。
- metrics 是轮询型证据，开销低，可以 1Hz 连续跑数小时，适合看趋势、容量和资源压力。
- 两者失败模式不同：trace 丢事件不应导致 metrics 缺失；metrics 源失败也不应影响 trace。
- 独立采集后，报告可以做三种模式：trace-only、metrics-only、trace+metrics。任何模式都不修改原始证据。

### 5.2 CLI

```bash
# 独立采集 10 分钟，1Hz；DCMI 优先，npu-smi 回退
cann-ebpf sample \
  --config metrics.yaml \
  --out runs/metrics-20261127T101500Z

# 同时抓 vLLM 和 LiteLLM 的 Prometheus 端点
cann-ebpf sample \
  --interval 1s --duration 10m \
  --dcmi \
  --prometheus http://127.0.0.1:8000/metrics \
  --prometheus http://127.0.0.1:4000/metrics \
  --out runs/metrics-20261127T101500Z

# 报告：trace + metrics
cann-ebpf report \
  --input runs/ebpf-20261127 \
  --metrics runs/metrics-20261127T101500Z/metrics.jsonl \
  --out report.html

# 报告：只有 metrics（降级模式）
cann-ebpf report \
  --metrics runs/metrics-20261127T101500Z/metrics.jsonl \
  --out metrics-only.html
```

### 5.3 目录与文件

```text
runs/metrics-20261127T101500Z/
  metrics.jsonl          # 原始长格式样本，一行一个点/一个 histogram
  metrics.meta.json      # 采集元数据、源、窗口、时钟、覆盖率、SHA256
```

`metrics.meta.json` 建议字段：

```json
{
  "schema": "cann.metrics.meta.v1",
  "tool_version": "0.6.0",
  "host": "node-1",
  "kernel": "5.10.0",
  "started_at": "2026-11-27T10:15:00+08:00",
  "ended_at": "2026-11-27T10:25:00+08:00",
  "interval_ms": 1000,
  "sources": ["dcmi", "proc", "cgroup", "prometheus:vllm"],
  "clock": {
    "mono_start_ns": 123456789000000,
    "wall_start_ns": 1760000000000000000,
    "offset_ns": 1759876543211000000,
    "drift_ns": 1200
  },
  "series": [
    {"name": "npu_aicore_utilization_ratio", "kind": "gauge", "unit": "ratio", "source": "dcmi", "labels": {"device": "NPU-0"}}
  ],
  "warnings": [],
  "artifacts": {
    "metrics_jsonl": {"bytes": 1234567, "sha256": "..."}
  }
}
```

### 5.4 `metrics.jsonl` 行格式

```json
{"schema":"cann.metrics.v1","mono_ns":123456789012345,"wall_ns":1760000000123456789,"source":"dcmi","device":"NPU-0","name":"npu_aicore_utilization_ratio","kind":"gauge","value":0.42,"unit":"ratio","labels":{}}
```

约定：

- `mono_ns`：与 span 同一主机单调时钟域，报告对齐的首选键。
- `wall_ns`：人类可读时间；NTP 跳变时不能替代 `mono_ns`。
- `source`：`dcmi`、`npu-smi`、`proc`、`cgroup`、`prometheus:vllm`、`prometheus:litellm`。
- `device`：`NPU-0` 或 `host`；没有设备维度时省略。
- `kind`：`gauge`、`counter`、`histogram`；不要用 `counter` 表示瞬时值。
- `unit`：`ratio`、`percent`、`bytes`、`watts`、`celsius`、`seconds`、`count`、`tokens_per_second`。
- `labels`：只放低基数维度（device、model、engine、pid、cgroup、status）；禁止 request_id、prompt、trace_id 等高基数/敏感内容。
- `histogram`：一行一个采样，包含 `buckets`（递增上界，最后 `+Inf`）、`counts`（与 Prometheus 一致的累计计数）、`sum`、`count`、`cumulative:true`。

### 5.5 采集源

| 源 | 默认间隔 | 指标 | 说明 |
|---|---:|---|---|
| DCMI | 1s | AICore、HBM、功耗、温度、health | 优先；`dcmi_probe` 已存在 |
| npu-smi | 5s | 同上 | 进程 spawn 开销高，不建议 1s |
| `/proc/<pid>` | 1s | utime/stime、RSS、threads、io、schedstat run_delay | 目标 vLLM/Orinfer 进程 |
| cgroup v2 | 1s | cpu.stat、memory.current/max/events、io.stat、pids.current | 容器/服务边界 |
| Prometheus | 1s 或 scrape 间隔 | vLLM/LiteLLM 的 gauge/counter/histogram | 通用文本解析，按 `# TYPE` 判型 |
| npu-exporter | 1s | NPU 指标 | 走 Prometheus 适配器 |

默认 vLLM 关注项（名称随版本可能变化，用 allowlist + 正则匹配）：

- `vllm:num_requests_running`
- `vllm:num_requests_waiting`
- `vllm:gpu_cache_usage_perc`
- `vllm:avg_prompt_throughput_toks_per_s`
- `vllm:avg_generation_throughput_toks_per_s`
- `vllm:e2e_request_latency_seconds` histogram
- `vllm:time_to_first_token_seconds` histogram
- `vllm:inter_token_latency_seconds` histogram
- `vllm:request_prompt_tokens` / `request_generation_tokens` histogram
- `vllm:request_success_total` / `request_failure_total` counter

### 5.6 配置文件

建议支持 `metrics.yaml`，避免每次拼长命令：

```yaml
interval: 1s
duration: 10m
out: runs/metrics-20261127T101500Z
sources:
  dcmi:
    enabled: true
  npu_smi:
    enabled: false
  proc:
    enabled: true
    pids: [auto]
  cgroup:
    enabled: true
    path: /sys/fs/cgroup/system.slice/vllm.service
  prometheus:
    - url: http://127.0.0.1:8000/metrics
      source: vllm
      include:
        - "vllm:num_requests_*"
        - "vllm:gpu_cache_*"
        - "vllm:*_latency_seconds*"
    - url: http://127.0.0.1:4000/metrics
      source: litellm
      include:
        - "litellm_*"
```

### 5.7 开销与可靠性

- 目标：1Hz、32 条序列、1 小时，CPU <1%，RSS <50MB；DCMI 子进程超时 2s，Prometheus 超时 2s。
- 单个源失败只记 warning，不终止其他源；连续失败 3 次后降频并提示。
- 每个源独立 ticker/超时，避免 `npu-smi` 卡住拖垮整个采样循环。
- 缓冲区按行 flush，结束/异常时 fsync；meta 记录 bytes/SHA256。
- 磁盘余量低于阈值时停止采样，写入 `disk_low` warning，不留下半个 JSON 行。
- 支持 SIGINT/SIGTERM 优雅退出；meta 写入 `ended_at` 和 `status`。
- 长期低开销采样可参考 Google-Wide Profiling 的 continuous profiling 经验；本工具先做到 1Hz/小时级，不追求全量。[24]

## 6. 报告接入与数据契约

### 6.1 三种模式

| 模式 | 输入 | 显示 | 明确隐藏/标注 |
|---|---|---|---|
| trace-only | `spans.jsonl` | 现有全部视图 | 指标区显示“未采集 metrics” |
| metrics-only | `metrics.jsonl` | 多指标小倍数、histogram 延迟热图、覆盖率 | 时间轴/设备区显示“无 span 证据” |
| trace+metrics | 两者 | 全部可用视图 + 联合十字光标 | 显示 join 覆盖率与时钟质量 |

### 6.2 时间对齐

- 统一用 `mono_ns` 对齐；`wall_ns` 只用于展示。
- 每条 metric 样本落到包含其 `mono_ns` 的采样区间；不跨 gap 插值。
- histogram 是累计值：报告先做窗口 delta，再算分位数；进程重启/计数器回绕要检测并断开。
- 设备指标按 device 对齐；跨设备汇总只允许 `max`/`sum` 并显式标注。
- 时钟偏移校准：采集开始/结束各记录一次 `wall-mono`；漂移超过阈值（建议 10ms）时标 `clock_drift`，降低因果结论置信度。

### 6.3 数据规模控制

- 每序列最多嵌入 2× 画布宽度的点；按像素列做 min/max/avg 聚合，保留尖峰。
- 总序列数上限默认 64；超过时按优先级截断并显示 `series_truncated`。
- 使用列式数组嵌入，减少 HTML 体积：

```json
{"name":"npu_hbm_used_ratio","unit":"ratio","t":[0,1000,2000],"v":[0.61,0.63,0.62]}
```

- HTML 目标 <10MB；超过时自动降低分辨率并提示，不静默丢失。
- 大 trace 时间轴按可视窗口做多分辨率聚合；参考 ESeMan 的分层/缓存思路。[10]

### 6.4 质量标记

每张图/每个数据源至少暴露：

- `source`：来自哪个采集器；
- `coverage`：期望样本数 vs 实际样本数；
- `gaps`：缺失区间；
- `clock_quality`：`mono` / `wall_only` / `drift`；
- `join_coverage`：成功对齐到 span/device 的比例；
- `downsampled`：是否降采样；
- `insufficient_sample`：样本量不足。

这些标记应出现在图上或图旁，而不是只写进日志。

## 7. 分期计划与验收

### 7.1 总览

| 阶段 | 内容 | 工期 | 验收 |
|---|---|---:|---|
| M1 | metrics schema、`sample` 采集器、report 接入、多指标小倍数、metrics-only 降级 | 2–3 天 | 1Hz 采 1 小时无丢点；CPU <1%、RSS <50MB；无 span 也能出报告 |
| M2 | 延迟热图、次秒偏移热图、聚合感知时间轴、尾 cohort 对比 | 3–4 天 | 合成周期尖刺/双峰可识别；final7/M4 各出一版样本；HTML 无外部依赖 |
| M3 | 关键路径瀑布、Summed vs Critical Path、Slack CDF | 4–6 天 | batch 级路径可解释；join 覆盖率 ≥95%；不重复计数；置信度标注 |
| M4 | 负载-延迟拐点、算子 Treemap、Roofline（条件） | 3–5 天 | 有并发梯度和 metrics 才画拐点；有 per-op FLOPs/bytes 才画 Roofline |
| M5 | A/B 差分、变化点标注、UI 打磨 | 2–3 天 | 两次 run 可并排同步缩放；HTML 仍单文件离线 |

建议先做 **M1 + M2**。M3 开工前先做 1 天可行性 spike，确认 `batch_id` 覆盖率和 device join 质量；M4 的 Roofline 先确认 msPTI/CANN 是否能提供 per-op 计数，否则只做 Treemap。

### 7.2 代码落点（预估）

```text
cmd/cann-ebpf/sample.go                 # 新命令
internal/metrics/schema.go              # cann.metrics.v1 类型与校验
internal/metrics/collector.go           # 调度、超时、落盘、meta
internal/metrics/source_dcmi.go         # 复用 hardware.DCMI
internal/metrics/source_npusmi.go       # 复用 hardware.ParseNPUSmiInfo
internal/metrics/source_proc.go         # /proc
internal/metrics/source_cgroup.go       # cgroup v2
internal/metrics/promparse.go           # Prometheus text -> metrics
internal/report/metrics.go              # 读取/对齐/降采样
internal/report/heatmap2d.go            # 延迟热图/次秒偏移
internal/report/cohort.go               # tail cohort
internal/report/template.html           # 新面板
```

不修改现有 `spans.jsonl` / `device.jsonl` / `hardware.jsonl` 的写入路径；`sample` 只新增 `metrics.jsonl` 与 `metrics.meta.json`。

### 7.3 测试与验收

**单元测试**

- metrics schema 校验：gauge/counter/histogram、单位、labels 基数、非法值。
- Prometheus 解析：`# TYPE`、counter reset、histogram `_bucket/_sum/_count`、NaN/Inf。
- 时间对齐：mono/wall、gap、drift、窗口 delta。
- 热图分桶：对数延迟桶边界、秒内偏移、空桶。
- 降采样：保留 min/max、尖峰不丢、总点数上限。
- cohort：样本不足、阈值、delta 计算。

**合成 fixtures**

- 1 秒周期尖刺：次秒偏移热图应出现固定行。
- 双峰延迟：延迟热图应出现两条水平带。
- metrics gap：图上断线 + `gaps` 标记，不插值。
- histogram reset：报告断开并标 `counter_reset`。
- 无 device：设备区显示“无 device 证据”，不显示 0。

**真实样本**

- `.analysis/final7/ebpf`：11,859 host spans、305 device intervals；
- `dist/cann-ebpf-m4-delivery-20261126/evidence/runs/ebpf-auto-10s-final-20261126`：6,991 spans、无 device；
- 生成 HTML 后用 `node --check` 检查内嵌 JS，Chrome headless 截图检查布局。

**性能与体积**

- 1Hz、32 序列、1 小时：CPU <1%、RSS <50MB。
- 默认报告 HTML <10MB；超过时降低分辨率并显示 `downsampled`。
- 时间轴缩放/平移保持可交互；必要时用多分辨率聚合，参考 ESeMan。[10]

### 7.4 第一周排期（建议）

| 天 | 工作 | 产出 |
|---|---|---|
| D1 | 冻结 `cann.metrics.v1` schema；`sample` CLI 骨架；单测夹具 | `docs/METRICS-SCHEMA.md`、schema 校验测试 |
| D2 | DCMI/npu-smi/proc/cgroup 适配；meta/时钟/落盘 | 1Hz 本地采集可运行 |
| D3 | Prometheus 解析；report 读取/对齐/降采样；metrics-only 报告 | 三种模式都能打开 |
| D4 | 多指标小倍数；延迟热图；次秒偏移热图 | 新面板 + 截图 |
| D5 | 聚合感知时间轴；尾 cohort；final7/M4/metrics-only 样本 | 可评审 HTML + 验收记录 |

产出物：

- `docs/METRICS-SCHEMA.md`：schema、单位、label 基数、示例、兼容规则；
- `docs/VISUALIZATION.md`：每张图的读法、数据来源、不可用条件；
- `README.md` / `docs/HANDOFF.md` / `docs/GAPS.md` 更新；
- `report-final7.html`、`report-m4.html`、`report-metrics-only.html` 三个样本；
- `go test ./...`、`go vet ./...`、`gofmt -l`、`node --check`、Chrome headless 截图记录。

## 8. 边界与防误读

### 8.1 不能做的事

- 不修改、不脱敏、不重写原始证据；报告只读派生。
- 没有栈采样，不叫 flame graph；现有数据只能做 TID/函数/算子级视图。[23]
- 没有 request ID，不宣称 request 级关键路径；只能做 batch/window 级，并标注 `attribution.level` 和置信度。
- 没有 per-op FLOPs/bytes，不画 Roofline；不能用模型参数量或理论峰值硬估。[12][13]
- 不把相关性写成因果；多指标图只提示“同时变化”，原因需要关键路径/实验验证。
- 不用双 Y 轴、雷达图、3D 饼图；它们会制造视觉假相关或掩盖样本量。
- 不隐藏 gap、丢事件、低覆盖率和降采样；这些必须出现在报告里。
- AI Flame Graphs 这类 AI 辅助解释可以作为“生成假设/导航”的入口，但每个结论必须能回到原始 span/device/metric 证据，不能把模型生成文本当证据。[26]

### 8.2 统计口径

- p99 需要样本量；N 很小时显示 N 和置信区间（bootstrap），不要只给一个“漂亮”的数。
- 比较两次 run 的 p99，要同 workload、同并发、同 warmup；否则标 `not comparable`。
- 尾 cohort 至少 30 个慢样本；不足时只给描述，不给结论。
- 负载测试用开环/固定到达率，避免 coordinated omission；不能只统计“已完成请求”的延迟。
- device busy 是区间并集；kernel/memcpy/HCCL 是区间时长之和，可能重叠，不能直接相加当忙率。

### 8.3 视觉编码

- 位置/长度优先于颜色；颜色只做第三维（count、p95、delta）。
- 顺序数据用顺序色板，A/B 差值用发散色板；不用 rainbow/jet。
- 对数延迟轴要标刻度（10µs、100µs、1ms、10ms、100ms、1s、10s）。
- 每个面板显示单位、来源、样本量、时间窗；tooltip 显示精确值和原始单位。
- 交叉光标共享 X 轴；不要在多指标图里共用 Y 轴。

## 9. 待确认的决策

建议默认值如下，确认后即可开工：

| 决策 | 建议默认 | 影响 |
|---|---|---|
| 是否先做 M1+M2 | 是 | 约一周，能覆盖大部分日常排障 |
| DCMI/npu-smi 默认 | DCMI 1s；npu-smi 5s 回退 | 避免 `npu-smi` 进程开销 |
| vLLM 指标名 | allowlist + 正则，不硬编码版本 | 兼容不同 vLLM 版本 |
| metrics 配置 | `metrics.yaml` + CLI 覆盖 | 可复现、可交接 |
| HTML 体积上限 | 10MB，超过降采样并提示 | 保持单文件可用 |
| 最大序列数 | 64 | 控制报告体积和浏览器内存 |
| M3 是否做 | 先 1 天 spike 再决定 | 依赖 batch_id/join 质量 |
| Roofline 是否做 | 先确认 per-op FLOPs/bytes | 没数据就不做，避免假图 |
| A/B 是否现在做 | M5 | 需要两次同 workload run |

## 10. 参考来源

编号按“论文/会议”和“工业实践/工具”分组，不按正文出现顺序。

### 论文/会议

[1] Brendan Gregg. Visualizing System Latency. Communications of the ACM 53(7):48–54, 2010. DOI: 10.1145/1785414.1785435.

[5] Jeffrey Dean, Luiz André Barroso. The Tail at Scale. Communications of the ACM 56(2):74–80, 2013. DOI: 10.1145/2408776.2408794.

[6] Michael Chow, David Meisner, Jason Flinn, Daniel Peek, Thomas F. Wenisch. The Mystery Machine: End-to-end Performance Analysis of Large-scale Internet Services. OSDI 2014. https://www.usenix.org/conference/osdi14/technical-sessions/presentation/chow

[7] Jonathan Kaldor, et al. Canopy: An End-to-End Performance Tracing and Analysis System. SOSP 2017:34–50. DOI: 10.1145/3132747.3132749.

[8] TraVista: Aggregate-Driven Trace Visualizations for Performance Debugging. arXiv:2010.13681, 2020. https://arxiv.org/abs/2010.13681

[9] Enhancing Trace Visualizations for Microservices Performance Analysis. arXiv:2302.12734, 2023. https://arxiv.org/abs/2302.12734

[10] ESeMan: Managing Data for Scalable and Interactive Event Sequence Visualization. arXiv:2508.03974, 2025. https://arxiv.org/abs/2508.03974

[11] PonyTail: Profiling and Improving Service Tail Latency. arXiv:2610.04615, 2026. https://arxiv.org/abs/2610.04615

[12] Samuel Williams, Andrew Waterman, David Patterson. Roofline: An Insightful Visual Performance Model for Multicore Architectures. Communications of the ACM 52(4):65–76, 2009. DOI: 10.1145/1498765.1498785.

[13] How to keep pushing ML accelerator performance? Know your rooflines! arXiv:2505.16346, 2025. https://arxiv.org/abs/2505.16346

[14] Understanding Software Performance Regressions Using Differential Flame Graphs. SANER 2015:535–539. DOI: 10.1109/SANER.2015.7081872.

[15] Interactive Horizon Graphs: Improving the Compact Visualization of Multiple Time Series. CHI 2013:3217–3226. DOI: 10.1145/2470654.2466441.

[17] wPerf: Generic Off-CPU Analysis to Identify Bottleneck Waiting Events. OSDI 2018. DOI: 10.5555/3291168.3291207.

[18] John D. C. Little. A Proof for the Queuing Formula: L = λW. Operations Research 9(3):383–387, 1961. DOI: 10.1287/opre.9.3.383.

[19] Ben Shneiderman. Tree Visualization with Tree-Maps: 2-D Space-Filling Approach. ACM Transactions on Graphics 11(1):92–99, 1992. DOI: 10.1145/102377.115768.

[20] A Survey of Methods for Time Series Change Point Detection. Knowledge and Information Systems 51(2):339–367, 2016. DOI: 10.1007/s10115-016-0987-z.

[21] Chimbuko: A Workflow-Level Scalable Performance Trace Analysis Tool. arXiv:2008.13742, 2020. https://arxiv.org/abs/2008.13742

[22] Benjamin H. Sigelman, et al. Dapper, a Large-Scale Distributed Systems Tracing Infrastructure. Google Technical Report dapper-2010-1, 2010. https://research.google/pubs/pub36356/

[23] Brendan Gregg. The Flame Graph. Communications of the ACM 59(6):48–57, 2016. DOI: 10.1145/2909476.

[24] Google-Wide Profiling: A Continuous Profiling Infrastructure for Data Centers. IEEE Micro 30(4):65–79, 2010. DOI: 10.1109/MM.2010.68.

### 工业实践/工具

[2] Brendan Gregg. FlameScope Pattern Recognition. 2018. https://www.brendangregg.com/blog/2018-11-08/flamescope-pattern-recognition.html

[3] Netflix FlameScope. https://github.com/Netflix/flamescope

[4] Perfetto Tracing Docs；Vampir；Paraver；Scalasca. https://perfetto.dev/docs/ ; https://tools.bsc.es/paraver ; https://www.scalasca.org/

[16] Brendan Gregg. Off-CPU Analysis. https://www.brendangregg.com/offcpuanalysis.html

[25] Brendan Gregg. The USE Method. https://www.brendangregg.com/usemethod.html

[26] Brendan Gregg. AI Flame Graphs. 2024. https://www.brendangregg.com/blog/2024-10-29/ai-flame-graphs.html

[27] Meta Holistic Trace Analysis. https://github.com/facebookresearch/HolisticTraceAnalysis
