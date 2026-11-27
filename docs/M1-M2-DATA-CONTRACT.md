# M1/M2 并行开发数据契约

日期：2026-11-27
用途：4 个并行任务的写路径隔离与 JSON 契约。所有新视图都放入 `report.Data.views`；metrics 放入 `report.Data.metrics`。

## 0. 所有权

| 任务 | 只允许写 | 禁止写 |
|---|---|---|
| A metrics 后端 | `internal/metrics/**`、`cmd/cann-ebpf/sample.go`、`cmd/cann-ebpf/sample_test.go`、`docs/METRICS-SCHEMA.md` | `cmd/cann-ebpf/main.go`、`internal/report/**`、`internal/hardware/**` |
| B 热图 | `internal/report/heatmap2d.go`、`internal/report/heatmap2d_test.go` | 其他 `internal/report/*` |
| C 聚合/cohort | `internal/report/cohort.go`、`internal/report/cohort_test.go` | 其他 `internal/report/*` |
| D 前端 | `internal/report/template.html` | 其他 `internal/report/*`、`cmd/**` |
| 集成 | `internal/report/report.go`、`cmd/cann-ebpf/report.go`、`cmd/cann-ebpf/main.go`、README/HANDOFF | 各 agent 的独占文件 |

规则：

- 只改自己名下的文件；不要 `git add -A`；不要提交。
- 新文件优先，避免改现有大文件造成冲突。
- 所有 Go 代码用 `gofmt`；提交前跑 `go test ./...` 和 `go vet ./...`。
- 不要修改 `spans.jsonl`、`device.jsonl`、`hardware.jsonl`。

## 1. `report.Data` 扩展

集成时新增：

```go
Views   ReportViews  `json:"views"`
Metrics *MetricsData `json:"metrics,omitempty"`
```

`ReportViews`：

```go
type ReportViews struct {
    LatencyHeatmap   LatencyHeatmapData   `json:"latency_heatmap"`
    SubsecondHeatmap SubsecondHeatmapData `json:"subsecond_heatmap"`
    AggregateBands   AggregateBandsData   `json:"aggregate_bands"`
    TailCohort       TailCohortData       `json:"tail_cohort"`
}
```

每个视图都有 `available` 和 `reason`。不可用时 `available=false`，`reason` 说明原因；前端不得画空图或补造数据。

## 2. 延迟热图

```go
type LatencyHeatmapData struct {
    Available      bool        `json:"available"`
    Reason         string      `json:"reason,omitempty"`
    BucketMS       float64     `json:"bucket_ms"`
    Columns        int         `json:"columns"`
    LatencyMinMS   float64     `json:"latency_min_ms"`
    LatencyMaxMS   float64     `json:"latency_max_ms"`
    LatencyEdgesMS []float64   `json:"latency_edges_ms"` // len = rows+1，递增
    Counts         [][]int     `json:"counts"`           // rows x columns
    P50            []float64   `json:"p50"`              // columns
    P95            []float64   `json:"p95"`              // columns
    P99            []float64   `json:"p99"`              // columns
    MaxCount       int         `json:"max_count"`
}
```

约定：

- 行是延迟桶，列是时间桶；`Counts[row][col]`。
- 延迟轴对数分桶，默认 20–24 行；最小值 1µs，最大值按 run 的实际 max 向上取整。
- 空列 `p50/p95/p99 = 0`；前端用 `Counts` 判断是否有样本。
- 没有 span 时 `available=false`。

## 3. 次秒偏移热图

```go
type SubsecondHeatmapData struct {
    Available  bool      `json:"available"`
    Reason     string    `json:"reason,omitempty"`
    BucketMS   float64   `json:"bucket_ms"`
    Columns    int       `json:"columns"`
    Rows       int       `json:"rows"`
    OffsetMS   float64   `json:"offset_ms"`
    Counts     [][]int   `json:"counts"` // rows x columns
    MaxCount   int       `json:"max_count"`
    DurationMS float64   `json:"duration_ms"`
}
```

约定：

- `offset = (start_ns - window_start_ns) % 1_000_000_000`；`row = offset / (1e9/rows)`。
- 默认 50 行（20ms/行）；run < 2s 时 `available=false`，原因写 `run too short`。
- 这是事件在秒内的位置，不是请求延迟；tooltip 必须写清楚。

## 4. 函数分位带

```go
type AggregateBand struct {
    Function string    `json:"function"`
    Category string    `json:"category"`
    P50      []float64 `json:"p50"`
    P95      []float64 `json:"p95"`
    P99      []float64 `json:"p99"`
    Count    []int     `json:"count"`
}

type AggregateBandsData struct {
    Available  bool            `json:"available"`
    Reason     string          `json:"reason,omitempty"`
    BucketMS   float64         `json:"bucket_ms"`
    Columns    int             `json:"columns"`
    MaxP95MS   float64         `json:"max_p95_ms"`
    Bands      []AggregateBand `json:"bands"`
}
```

约定：

- 按函数聚合，取总耗时 TopN（默认 12）；每列独立算 p50/p95/p99。
- 空列填 0；`Count` 用于判断样本量。
- 前端画成每函数一行、p50 实线 + p95 阴影 + p99 虚线；不是栈火焰图。

## 5. 尾 cohort

```go
type CohortRow struct {
    Label      string  `json:"label"`
    Kind       string  `json:"kind"` // function|category|wait
    SlowPct    float64 `json:"slow_pct"`
    TypicalPct float64 `json:"typical_pct"`
    DeltaPct   float64 `json:"delta_pct"`
}

type TailCohortData struct {
    Available   bool        `json:"available"`
    Reason      string      `json:"reason,omitempty"`
    ThresholdMS float64     `json:"threshold_ms"`
    SlowCount   int         `json:"slow_count"`
    TypicalCount int        `json:"typical_count"`
    Rows        []CohortRow `json:"rows"`
    SlowOffCPUMS float64    `json:"slow_offcpu_ms"`
    TypicalOffCPUMS float64 `json:"typical_offcpu_ms"`
    SlowFutexMS float64     `json:"slow_futex_ms"`
    TypicalFutexMS float64  `json:"typical_futex_ms"`
    Notes       []string    `json:"notes"`
}
```

约定：

- `slow` = duration >= p99（至少 30 个样本；不足则 `available=false` 或 `notes` 标注）。
- `typical` = p50 附近（p45–p55）的 span。
- `Rows` 对比函数占比与类别占比，按 `abs(delta)` 降序取 TopN。
- off-CPU/futex 用总耗时/span 数得到平均 ms；没有测量值时保留 0 并在 notes 说明。

## 6. metrics

`report.MetricsData`（集成时实现）：

```go
type MetricsData struct {
    Available    bool           `json:"available"`
    Reason       string         `json:"reason,omitempty"`
    Samples      int            `json:"samples"`
    Sources      []string       `json:"sources"`
    IntervalMS   float64        `json:"interval_ms"`
    Series       []MetricSeries `json:"series"`
    Stats        []MetricStat   `json:"stats"`
    Warnings     []string       `json:"warnings"`
    Coverage     float64        `json:"coverage"`
    ClockQuality string         `json:"clock_quality"`
}

type MetricSeries struct {
    ID             string            `json:"id"`
    Name           string            `json:"name"`
    Kind           string            `json:"kind"` // gauge|counter|histogram
    Unit           string            `json:"unit"`
    Source         string            `json:"source"`
    Device         string            `json:"device,omitempty"`
    Labels         map[string]string `json:"labels,omitempty"`
    T              []float64         `json:"t"` // 相对 ms
    V              []float64         `json:"v"`
    Count          int               `json:"count"`
    Downsampled    bool              `json:"downsampled"`
    OriginalPoints int               `json:"original_points"`
}

type MetricStat struct {
    ID       string  `json:"id"`
    Name     string  `json:"name"`
    Kind     string  `json:"kind"` // gauge|counter|histogram
    Unit     string  `json:"unit"`
    Source   string  `json:"source"`
    Device   string  `json:"device,omitempty"`
    Points   int     `json:"points"`
    First    float64 `json:"first"`
    Last     float64 `json:"last"`
    Min      float64 `json:"min"`
    Max      float64 `json:"max"`
    Mean     float64 `json:"mean"`
    P50      float64 `json:"p50"`
    P95      float64 `json:"p95"`
    P99      float64 `json:"p99"`
    RateLast float64 `json:"rate_last"`
    RateMean float64 `json:"rate_mean"`
}
```

统计层约定：

- 每个成功投影的 series 对应一条 `MetricStat`，顺序与 `series` 一致；被 `MaxSeries` 截断的 series 不产生 stat。
- `points` 是该 series 过滤非法值后的原始点数；统计基于 downsample 之前的原始点，`MaxPoints` 只影响 `series.t/v` 的绘图密度，不影响 min/max/mean/percentiles。
- `first` / `last` 是首末观测值，不是时间戳。缺失采样不插值，也不补造点。
- `p50` / `p95` / `p99` 使用与 span 报告一致的最近秩（nearest-rank）定义。
- histogram 的统计值来自每个采样点的 p95 点值（series 名带 `:p95`），不是从桶重新构造原始分布。
- `rate_last` / `rate_mean` 仅对 counter 有意义，单位是“每秒”；gauge/histogram 恒为 0。counter 重置不会被解释为负速率：`rate_mean` 只累加正增量，`rate_last` 在最后一次增量为负时取 0。
- 这是 series 级统计，不是 request 级统计；不能据此宣称单个请求的关键路径或尾延迟归因。
- `MetricStat` 在 `LoadMetrics` 阶段基于 `metrics.jsonl` 的原始 series 计算；报告后续按 span 窗口裁剪/重排绘图序列时，不重新计算统计值。

指标矩阵按“来源 + 指标名 + 单位”归并；每行一条组均值曲线，纵轴按本行 min–max 归一化，
counter 先转 Δ/s，样本不足（少于 2 个有效点）的组只标注、不画线。默认“按变化排序”，
优先展示变化最大的组；也可切换“按来源排序”。优先展示的指标包括：

- `npu_aicore_utilization_ratio`
- `npu_hbm_used_ratio`
- `npu_power_watts`
- `npu_temperature_celsius`
- `vllm_requests_running`
- `vllm_requests_waiting`
- `vllm_kv_cache_usage_ratio`
- `cann_p95_latency_ms`（span 派生）

## 7. `metrics.jsonl` 行契约

```json
{"schema":"cann.metrics.v1","mono_ns":123456789012345,"wall_ns":1760000000123456789,"source":"dcmi","device":"NPU-0","name":"npu_aicore_utilization_ratio","kind":"gauge","value":0.42,"unit":"ratio","labels":{}}
```

Histogram：

```json
{"schema":"cann.metrics.v1","mono_ns":123456789012345,"wall_ns":1760000000123456789,"source":"prometheus:vllm","name":"vllm_e2e_latency_seconds","kind":"histogram","unit":"seconds","buckets":[0.1,0.5,1,5],"counts":[3,8,10,12],"sum":4.2,"count":12,"cumulative":true,"labels":{}}
```

## 8. 验收

- `go test ./...`、`go vet ./...`、`gofmt -l` 无输出。
- `node --check` 对模板内嵌 JS 通过（主报告与 metrics-only 报告）。
- 合成 fixture：1s 周期尖刺、双峰延迟、空 run、metrics-only；metrics-only 报告不能补造 span。
- metrics 点严格裁剪到 span 窗口：早于起点和晚于终点的点都丢弃并写 warning。
- metrics-only 的 `HardwareAvailable` 仅在存在 `npu_*`/DCMI/npu-smi series 时为真。
- `stats` 在空文件、单点、非法 histogram 和 downsample 场景下不 panic；单点 series 的 min/max/mean/p50/p95/p99 均等于该点值，rate 为 0。
- 不引入外部 JS/CSS/CDN；不修改原始证据。
