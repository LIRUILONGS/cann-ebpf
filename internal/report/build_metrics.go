package report

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"cann-ebpf/internal/diagnose"
)

// BuildWithMetrics builds a normal span-based report and optionally attaches
// a metrics projection. When there are no spans but metrics are available it
// returns a degraded, metrics-only report instead of fabricating a timeline.
func BuildWithMetrics(in *diagnose.Input, m *MetricsData, opts Options) (Data, error) {
	if in != nil && len(in.Spans) > 0 {
		data, err := Build(in, opts)
		if err != nil {
			return Data{}, err
		}
		if m != nil {
			startNS, parseErr := strconv.ParseInt(data.Window.StartNS, 10, 64)
			if parseErr == nil {
				endNS, endErr := strconv.ParseInt(data.Window.EndNS, 10, 64)
				if endErr != nil {
					endNS = 0
				}
				m = rebaseMetrics(m, startNS, endNS)
			}
			data.Metrics = m
			data.Warnings = append(data.Warnings, m.Warnings...)
		}
		return data, nil
	}
	if m == nil || !m.Available {
		return Data{}, fmt.Errorf("no spans and no available metrics")
	}
	opts.normalize()
	startNS, endNS := m.StartNS, m.EndNS
	if startNS <= 0 {
		startNS = 1
	}
	if endNS <= startNS {
		endNS = startNS + 1
	}
	data := Data{
		Schema:      SchemaV1,
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Input:       m.Path,
		Title:       opts.Title,
		Run:         RunInfo{},
		Window: Window{
			StartNS:    strconv.FormatInt(startNS, 10),
			EndNS:      strconv.FormatInt(endNS, 10),
			DurationMS: float64(endNS-startNS) / 1e6,
		},
		Summary: Summary{
			SlowThresholdMS:   float64(opts.SlowThresholdNS) / 1e6,
			HardwareAvailable: hasNPUMetricSeries(m),
		},
		Categories: []CategoryRow{},
		Functions:  []FunctionRow{},
		Spans:      []SpanPoint{},
		Timeline:   TimelineData{HostRows: []LaneRow{}, DeviceRows: []LaneRow{}, MinDurationMS: float64(opts.MinDurationNS) / 1e6},
		Heatmap:    HeatmapData{Rows: []HeatRow{}},
		CDF:        []CDFPoint{},
		Views: ReportViews{
			LatencyHeatmap:   LatencyHeatmapData{Available: false, Reason: "no span evidence"},
			SubsecondHeatmap: SubsecondHeatmapData{Available: false, Reason: "no span evidence"},
			AggregateBands:   AggregateBandsData{Available: false, Reason: "no span evidence", Bands: []AggregateBand{}},
			TailCohort:       TailCohortData{Available: false, Reason: "no span evidence", Rows: []CohortRow{}, Notes: []string{"指标模式没有 span，无法计算 cohort。"}},
		},
		Metrics:  m,
		Warnings: append([]string{}, m.Warnings...),
		Notes:    defaultNotes(),
	}
	if data.Title == "" {
		data.Title = "CANN eBPF 指标报告"
	}
	data.Notes = append(data.Notes,
		"指标模式：没有 spans.jsonl 证据，函数级归因、时间轴和设备四象限不可用。",
		"指标值与采样间隔来自 metrics.jsonl / metrics.meta.json；缺失区间不插值。",
		"histogram 的 p95 按相邻采样点的增量计算；首个采样点为累计快照，无新增请求的区间不绘制。",
	)
	return data, nil
}

// rebaseMetrics shifts each series from metrics-start time to the span-run
// start and drops points outside the run window rather than clamping them, so
// the report does not pretend they happened inside the measured window.
func rebaseMetrics(m *MetricsData, windowStartNS, windowEndNS int64) *MetricsData {
	if m == nil || windowStartNS <= 0 {
		return m
	}
	shiftMS := float64(m.StartNS-windowStartNS) / 1e6
	windowMS := float64(0)
	if windowEndNS > windowStartNS {
		windowMS = float64(windowEndNS-windowStartNS) / 1e6
	}
	if shiftMS == 0 && windowMS == 0 {
		return m
	}
	out := *m
	out.Series = make([]MetricSeries, 0, len(m.Series))
	out.Warnings = append([]string{}, m.Warnings...)
	droppedBefore, droppedAfter := 0, 0
	for _, series := range m.Series {
		next := series
		next.T = make([]float64, 0, len(series.T))
		next.V = make([]float64, 0, len(series.V))
		for i, t := range series.T {
			shifted := t + shiftMS
			if shifted < 0 {
				droppedBefore++
				continue
			}
			if windowMS > 0 && shifted > windowMS {
				droppedAfter++
				continue
			}
			next.T = append(next.T, shifted)
			if i < len(series.V) {
				next.V = append(next.V, series.V[i])
			}
		}
		next.Count = len(next.T)
		if len(next.T) > 0 {
			out.Series = append(out.Series, next)
		}
	}
	if droppedBefore > 0 {
		out.Warnings = append(out.Warnings, fmt.Sprintf("metrics 有 %d 个点早于 span 窗口起点，已丢弃", droppedBefore))
	}
	if droppedAfter > 0 {
		out.Warnings = append(out.Warnings, fmt.Sprintf("metrics 有 %d 个点晚于 span 窗口终点，已丢弃", droppedAfter))
	}
	out.StartNS = windowStartNS
	if windowEndNS > windowStartNS {
		out.EndNS = windowEndNS
	}
	if len(out.Series) == 0 {
		out.Available = false
		out.Reason = "no metrics points inside span window"
	}
	return &out
}

func hasNPUMetricSeries(m *MetricsData) bool {
	if m == nil {
		return false
	}
	for _, series := range m.Series {
		if strings.HasPrefix(strings.ToLower(series.Name), "npu_") {
			return true
		}
		switch strings.ToLower(series.Source) {
		case "dcmi", "npu-smi", "npu_smi", "hardware":
			return true
		}
	}
	return false
}
