package report

// ViewStatus is the common availability contract for the ten vLLM views.
// Builders must set Available=false and explain why when the evidence needed
// by a view is absent. Confidence is intentionally empty for unavailable views.
type ViewStatus struct {
	Available  bool   `json:"available"`
	Reason     string `json:"reason,omitempty"`
	Source     string `json:"source,omitempty"`
	Confidence string `json:"confidence,omitempty"` // exact|inferred|partial
}

// ReportViewsV2 is the stable JSON boundary for the vLLM ten-view report.
// The M0 implementation returns unavailable statuses; data-layer builders
// fill these structures without changing their JSON shape.
type ReportViewsV2 struct {
	LoadLatency     LoadLatencyData     `json:"load_latency"`
	CriticalPath    CriticalPathData    `json:"critical_path"`
	SummedDelay     SummedDelayData     `json:"summed_delay"`
	SlackCDF        SlackCDFData        `json:"slack_cdf"`
	OffCPU          OffCPUData          `json:"off_cpu"`
	OperatorTreemap TreemapData         `json:"operator_treemap"`
	Roofline        RooflineData        `json:"roofline"`
	ABDiff          ABDiffData          `json:"ab_diff"`
	ChangePoint     ChangePointData     `json:"change_point"`
	ServiceTopology ServiceTopologyData `json:"service_topology"`
}

type LoadLatencyPoint struct {
	Concurrency   int     `json:"concurrency"`
	QPS           float64 `json:"qps"`
	TTFTP50MS     float64 `json:"ttft_p50_ms"`
	TTFTP95MS     float64 `json:"ttft_p95_ms"`
	LatencyP50MS  float64 `json:"latency_p50_ms"`
	LatencyP95MS  float64 `json:"latency_p95_ms"`
	LatencyP99MS  float64 `json:"latency_p99_ms"`
	RunningMax    int     `json:"running_max"`
	WaitingMax    int     `json:"waiting_max"`
	KVCacheMax    float64 `json:"kv_cache_max"`
	NPUAICoreMean float64 `json:"npu_aicore_mean"`
}

type LoadLatencyData struct {
	Status          ViewStatus         `json:"status"`
	WorkloadID      string             `json:"workload_id,omitempty"`
	KneeConcurrency int                `json:"knee_concurrency,omitempty"`
	Points          []LoadLatencyPoint `json:"points"`
}

type CriticalPathNode struct {
	ID         string  `json:"id"`
	Parent     string  `json:"parent,omitempty"`
	Label      string  `json:"label"`
	Category   string  `json:"category,omitempty"`
	Kind       string  `json:"kind,omitempty"`
	StartMS    float64 `json:"start_ms"`
	DurationMS float64 `json:"duration_ms"`
	Device     string  `json:"device,omitempty"`
	Confidence string  `json:"confidence,omitempty"`
}

type CriticalPathData struct {
	Status       ViewStatus         `json:"status"`
	BatchID      string             `json:"batch_id,omitempty"`
	RequestID    string             `json:"request_id,omitempty"`
	TraceID      string             `json:"trace_id,omitempty"`
	Nodes        []CriticalPathNode `json:"nodes"`
	CriticalPath []string           `json:"critical_path"`
	TotalMS      float64            `json:"total_ms"`
	Coverage     float64            `json:"coverage"`
}

type CategoryDelay struct {
	Category string  `json:"category"`
	TotalMS  float64 `json:"total_ms"`
	Count    int     `json:"count"`
}

type SummedDelayData struct {
	Status          ViewStatus      `json:"status"`
	SumMS           float64         `json:"sum_ms"`
	CriticalPathMS  float64         `json:"critical_path_ms"`
	OverlapMS       float64         `json:"overlap_ms"`
	Parallelism     float64         `json:"parallelism"`
	NodesByCategory []CategoryDelay `json:"nodes_by_category"`
}

type SlackPoint struct {
	SlackMS    float64 `json:"slack_ms"`
	Count      int     `json:"count"`
	Cumulative int     `json:"cumulative"`
}

type SlackCDFData struct {
	Status        ViewStatus   `json:"status"`
	Points        []SlackPoint `json:"points"`
	MinMS         float64      `json:"min_ms"`
	P50MS         float64      `json:"p50_ms"`
	P95MS         float64      `json:"p95_ms"`
	NegativeCount int          `json:"negative_count"`
}

type OffCPUSegment struct {
	StartMS float64 `json:"start_ms"`
	EndMS   float64 `json:"end_ms"`
	Kind    string  `json:"kind"`
}

type OffCPURow struct {
	Lane     string          `json:"lane"`
	StartMS  float64         `json:"start_ms"`
	EndMS    float64         `json:"end_ms"`
	OnCPUMS  float64         `json:"on_cpu_ms"`
	OffCPUMS float64         `json:"off_cpu_ms"`
	FutexMS  float64         `json:"futex_ms"`
	Segments []OffCPUSegment `json:"segments"`
}

type OffCPUData struct {
	Status    ViewStatus  `json:"status"`
	Mode      string      `json:"mode,omitempty"` // aggregate|exact
	Rows      []OffCPURow `json:"rows"`
	TotalRows int         `json:"total_rows,omitempty"`
	Truncated bool        `json:"truncated,omitempty"`
}

type TreemapNode struct {
	Name       string        `json:"name"`
	Category   string        `json:"category,omitempty"`
	Kind       string        `json:"kind,omitempty"`
	Device     string        `json:"device,omitempty"`
	Stream     string        `json:"stream,omitempty"`
	DurationMS float64       `json:"duration_ms"`
	Count      int           `json:"count"`
	Children   []TreemapNode `json:"children,omitempty"`
}

type TreemapData struct {
	Status ViewStatus   `json:"status"`
	Root   *TreemapNode `json:"root,omitempty"`
}

type RooflinePoint struct {
	Op                  string  `json:"op"`
	Category            string  `json:"category,omitempty"`
	DurationMS          float64 `json:"duration_ms"`
	Flops               float64 `json:"flops"`
	Bytes               float64 `json:"bytes"`
	ArithmeticIntensity float64 `json:"arithmetic_intensity"`
	AchievedTFLOPS      float64 `json:"achieved_tflops"`
	AchievedGBPS        float64 `json:"achieved_gbps"`
	Bound               string  `json:"bound,omitempty"`
}

type RooflineData struct {
	Status     ViewStatus      `json:"status"`
	PeakTFLOPS float64         `json:"peak_tflops"`
	PeakGBPS   float64         `json:"peak_gbps"`
	DType      string          `json:"dtype,omitempty"`
	Points     []RooflinePoint `json:"points"`
}

type ABMetricDelta struct {
	Name       string  `json:"name"`
	ADeltaPct  float64 `json:"a_delta_pct"`
	BDeltaPct  float64 `json:"b_delta_pct"`
	Difference float64 `json:"difference"`
}

type ABFunctionDelta struct {
	Function   string  `json:"function"`
	ADeltaPct  float64 `json:"a_delta_pct"`
	BDeltaPct  float64 `json:"b_delta_pct"`
	Difference float64 `json:"difference"`
}

type ABDiffData struct {
	Status      ViewStatus        `json:"status"`
	ARun        string            `json:"a_run,omitempty"`
	BRun        string            `json:"b_run,omitempty"`
	WorkloadID  string            `json:"workload_id,omitempty"`
	Metrics     []ABMetricDelta   `json:"metrics"`
	Functions   []ABFunctionDelta `json:"functions"`
	Significant []string          `json:"significant"`
}

type ChangeSeries struct {
	Name   string    `json:"name"`
	Values []float64 `json:"values"`
}

type ChangePoint struct {
	TimeMS   float64 `json:"time_ms"`
	Metric   string  `json:"metric"`
	Before   float64 `json:"before"`
	After    float64 `json:"after"`
	DeltaPct float64 `json:"delta_pct"`
	Score    float64 `json:"score"`
	Method   string  `json:"method,omitempty"`
}

type ChangeEvent struct {
	TimeMS float64 `json:"time_ms"`
	Label  string  `json:"label"`
	Source string  `json:"source,omitempty"`
}

type ChangePointData struct {
	Status     ViewStatus     `json:"status"`
	Series     []ChangeSeries `json:"series"`
	Points     []ChangePoint  `json:"points"`
	Events     []ChangeEvent  `json:"events"`
	Method     string         `json:"method,omitempty"`
	MinSamples int            `json:"min_samples,omitempty"`
}

type TopologyNode struct {
	ID      string `json:"id"`
	Service string `json:"service"`
	Name    string `json:"name,omitempty"`
	Kind    string `json:"kind,omitempty"`
	Status  string `json:"status,omitempty"`
}

type TopologyEdge struct {
	From      string  `json:"from"`
	To        string  `json:"to"`
	Calls     int     `json:"calls"`
	LatencyMS float64 `json:"latency_ms"`
}

type ServiceTopologyData struct {
	Status      ViewStatus     `json:"status"`
	Scope       string         `json:"scope,omitempty"` // process|end_to_end
	RootService string         `json:"root_service,omitempty"`
	Coverage    float64        `json:"coverage"`
	Nodes       []TopologyNode `json:"nodes"`
	Edges       []TopologyEdge `json:"edges"`
}
