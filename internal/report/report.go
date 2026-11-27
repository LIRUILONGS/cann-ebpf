// Package report renders a read-only performance dashboard from one completed
// cann-ebpf run. It never attaches probes and never rewrites raw evidence.
package report

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"cann-ebpf/internal/device"
	"cann-ebpf/internal/diagnose"
	"cann-ebpf/internal/hardware"
	"cann-ebpf/internal/runlog"
)

const SchemaV1 = "cann.report.html.v1"

// Options controls how much data is embedded in the standalone report.
type Options struct {
	Title               string
	MinDurationNS       int64
	SlowThresholdNS     int64
	BucketCount         int
	TopN                int
	MaxLanes            int
	MaxHeatmapRows      int
	DeviceMode          string
	DeviceContainerID   string
	SweepPath           string
	OpsPath             string
	EventsPath          string
	OffCPUPath          string
	OTelPath            string
	ABAPath             string
	ABBRunPath          string
	ABWorkloadID        string
	RooflinePeakTFLOPS  float64
	RooflinePeakGBPS    float64
	RooflineDType       string
	TopologyRootService string
}

func (o *Options) normalize() {
	if o.MinDurationNS <= 0 {
		o.MinDurationNS = 50_000
	}
	if o.SlowThresholdNS <= 0 {
		o.SlowThresholdNS = 1_000_000
	}
	if o.BucketCount <= 0 {
		o.BucketCount = 160
	}
	if o.TopN <= 0 {
		o.TopN = 15
	}
	if o.MaxLanes <= 0 {
		o.MaxLanes = 64
	}
	if o.MaxHeatmapRows <= 0 {
		o.MaxHeatmapRows = 32
	}
	if o.DeviceMode == "" {
		o.DeviceMode = "matched"
	}
}

type Data struct {
	Schema      string        `json:"schema"`
	GeneratedAt string        `json:"generated_at"`
	Input       string        `json:"input"`
	Title       string        `json:"title"`
	Run         RunInfo       `json:"run"`
	Window      Window        `json:"window"`
	Summary     Summary       `json:"summary"`
	Categories  []CategoryRow `json:"categories"`
	Functions   []FunctionRow `json:"functions"`
	Spans       []SpanPoint   `json:"spans"`
	Timeline    TimelineData  `json:"timeline"`
	Heatmap     HeatmapData   `json:"heatmap"`
	CDF         []CDFPoint    `json:"cdf"`
	Views       ReportViews   `json:"views"`
	ViewsV2     ReportViewsV2 `json:"views_v2"`
	Device      *DeviceData   `json:"device,omitempty"`
	Hardware    HardwareData  `json:"hardware"`
	Metrics     *MetricsData  `json:"metrics,omitempty"`
	Warnings    []string      `json:"warnings"`
	Notes       []string      `json:"notes"`
}

type RunInfo struct {
	RunID       string `json:"run_id,omitempty"`
	Profile     string `json:"profile,omitempty"`
	ToolVersion string `json:"tool_version,omitempty"`
	Host        string `json:"host,omitempty"`
	Kernel      string `json:"kernel,omitempty"`
	Status      string `json:"status,omitempty"`
	StartedAt   string `json:"started_at,omitempty"`
	EndedAt     string `json:"ended_at,omitempty"`
	TargetPIDs  []int  `json:"target_pids,omitempty"`
}

type Window struct {
	StartNS    string  `json:"start_ns"`
	EndNS      string  `json:"end_ns"`
	DurationMS float64 `json:"duration_ms"`
}

type Summary struct {
	Spans              int     `json:"spans"`
	PIDs               int     `json:"pids"`
	TIDs               int     `json:"tids"`
	TotalMS            float64 `json:"total_ms"`
	P50MS              float64 `json:"p50_ms"`
	P95MS              float64 `json:"p95_ms"`
	P99MS              float64 `json:"p99_ms"`
	MaxMS              float64 `json:"max_ms"`
	SlowSpans          int     `json:"slow_spans"`
	SlowThresholdMS    float64 `json:"slow_threshold_ms"`
	DroppedEvents      uint64  `json:"dropped_events"`
	TraceSpans         int     `json:"trace_spans"`
	DeviceSpans        int     `json:"device_spans"`
	DeviceMatchRate    float64 `json:"device_match_rate"`
	DeviceSlowSyncRate float64 `json:"device_slow_sync_rate"`
	DeviceBusyMS       float64 `json:"device_busy_ms"`
	HardwareAvailable  bool    `json:"hardware_available"`
}

type CategoryRow struct {
	Category string  `json:"category"`
	Count    int     `json:"count"`
	TotalMS  float64 `json:"total_ms"`
	SpanPct  float64 `json:"span_pct"`
}

type FunctionRow struct {
	Function string  `json:"function"`
	Category string  `json:"category"`
	Count    int     `json:"count"`
	TotalMS  float64 `json:"total_ms"`
	P50MS    float64 `json:"p50_ms"`
	P95MS    float64 `json:"p95_ms"`
	MaxMS    float64 `json:"max_ms"`
	Slow     int     `json:"slow"`
}

type SpanPoint struct {
	T        float64 `json:"t"`
	D        float64 `json:"d"`
	PID      int     `json:"p"`
	TID      int     `json:"q"`
	Row      string  `json:"r"`
	Function string  `json:"f"`
	Category string  `json:"c"`
	SpanID   string  `json:"s,omitempty"`
	TraceID  string  `json:"tr,omitempty"`
	BatchID  string  `json:"b,omitempty"`
	OffCPUMS float64 `json:"o,omitempty"`
	FutexMS  float64 `json:"x,omitempty"`
}

type LaneRow struct {
	ID       string  `json:"id"`
	Label    string  `json:"label"`
	PID      int     `json:"pid,omitempty"`
	TID      int     `json:"tid,omitempty"`
	DeviceID int     `json:"device_id,omitempty"`
	StreamID int64   `json:"stream_id,omitempty"`
	Count    int     `json:"count"`
	TotalMS  float64 `json:"total_ms"`
}

type TimelineData struct {
	HostRows      []LaneRow `json:"host_rows"`
	DeviceRows    []LaneRow `json:"device_rows"`
	MinDurationMS float64   `json:"min_duration_ms"`
}

type HeatmapData struct {
	Dim      string    `json:"dim"`
	BucketMS float64   `json:"bucket_ms"`
	Columns  int       `json:"columns"`
	Rows     []HeatRow `json:"rows"`
	MaxP95MS float64   `json:"max_p95_ms"`
	MaxSlow  int       `json:"max_slow"`
}

type HeatRow struct {
	Label string    `json:"label"`
	P95   []float64 `json:"p95"`
	Slow  []int     `json:"slow"`
	Count []int     `json:"count"`
}

type CDFPoint struct {
	P float64 `json:"p"`
	D float64 `json:"d"`
}

type DeviceData struct {
	Available    bool              `json:"available"`
	Spans        int               `json:"spans"`
	Kinds        string            `json:"kinds"`
	MatchRate    float64           `json:"match_rate"`
	SlowSyncRate float64           `json:"slow_sync_rate"`
	BusyMS       float64           `json:"busy_ms"`
	Intervals    []DeviceInterval  `json:"intervals"`
	Hosts        []DeviceHostPoint `json:"hosts"`
	Rows         []LaneRow         `json:"rows"`
	Busy         []BusyPoint       `json:"busy"`
	Verdicts     map[string]int    `json:"verdicts"`
}

type DeviceInterval struct {
	T      float64 `json:"t"`
	D      float64 `json:"d"`
	Kind   string  `json:"k"`
	Name   string  `json:"n,omitempty"`
	Device int     `json:"dev"`
	Stream int64   `json:"st,omitempty"`
	Corr   int64   `json:"c,omitempty"`
	Row    string  `json:"r"`
	Host   string  `json:"h,omitempty"`
}

type DeviceHostPoint struct {
	SpanID     string  `json:"s"`
	Function   string  `json:"f"`
	T          float64 `json:"t"`
	D          float64 `json:"d"`
	BusyPct    float64 `json:"b"`
	Verdicts   string  `json:"v,omitempty"`
	DeviceBusy float64 `json:"db"`
	Kernel     float64 `json:"kn"`
	Memcpy     float64 `json:"mc"`
	HCCL       float64 `json:"hc"`
}

type BusyPoint struct {
	T      float64 `json:"t"`
	Busy   float64 `json:"busy"`
	Kernel float64 `json:"kernel"`
	Memcpy float64 `json:"memcpy"`
	HCCL   float64 `json:"hccl"`
	Count  int     `json:"count"`
}

type HardwareData struct {
	Available     bool     `json:"available"`
	Samples       int      `json:"samples"`
	Devices       int      `json:"devices"`
	HBMFreeMinPct *float64 `json:"hbm_free_min_pct,omitempty"`
	AICoreMaxPct  float64  `json:"aicore_max_pct,omitempty"`
}

type funcAgg struct {
	category  string
	durations []int64
	totalNS   int64
	slow      int
}

type laneAgg struct {
	pid     int
	tid     int
	name    string
	count   int
	totalNS int64
}

type deviceRowAgg struct {
	deviceID int
	streamID int64
	count    int
	totalNS  int64
}

type cellAgg struct {
	durations []int64
	slow      int
}

type busyInterval struct {
	start int64
	end   int64
	kind  string
}

// Build derives all dashboard data from one loaded run. It is deterministic
// for the same input and options, and never calls attach or writes evidence.
func Build(in *diagnose.Input, opts Options) (Data, error) {
	opts.normalize()
	if in == nil {
		return Data{}, fmt.Errorf("input is nil")
	}
	spans := make([]runlog.Span, 0, len(in.Spans))
	for _, span := range in.Spans {
		if spanStart(span) <= 0 {
			continue
		}
		spans = append(spans, span)
	}
	if len(spans) == 0 {
		return Data{}, fmt.Errorf("no spans with a positive start_ns/ts_ns")
	}
	sort.SliceStable(spans, func(i, j int) bool { return spanStart(spans[i]) < spanStart(spans[j]) })
	startNS := spanStart(spans[0])
	endNS := spanEnd(spans[0])
	for _, span := range spans[1:] {
		if s := spanStart(span); s < startNS {
			startNS = s
		}
		if e := spanEnd(span); e > endNS {
			endNS = e
		}
	}
	if endNS <= startNS {
		endNS = startNS + 1
	}
	windowNS := endNS - startNS

	data := Data{
		Schema:      SchemaV1,
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Input:       in.Path,
		Title:       strings.TrimSpace(opts.Title),
		Run:         buildRunInfo(in, spans),
		Window: Window{
			StartNS:    strconv.FormatInt(startNS, 10),
			EndNS:      strconv.FormatInt(endNS, 10),
			DurationMS: float64(windowNS) / 1e6,
		},
		Categories: []CategoryRow{},
		Functions:  []FunctionRow{},
		Spans:      []SpanPoint{},
		Timeline: TimelineData{
			HostRows:      []LaneRow{},
			DeviceRows:    []LaneRow{},
			MinDurationMS: float64(opts.MinDurationNS) / 1e6,
		},
		Heatmap:  HeatmapData{Rows: []HeatRow{}},
		CDF:      []CDFPoint{},
		Views:    ReportViews{},
		Warnings: append([]string{}, in.Warnings...),
		Notes:    defaultNotes(),
	}
	if data.Title == "" {
		data.Title = "CANN eBPF 性能报告"
	}

	points, durations := buildSpanPoints(spans, startNS, opts)
	data.Spans = points
	data.Summary = buildSummary(spans, durations, in, opts)
	data.Categories = buildCategories(spans)
	functions, functionAgg := buildFunctions(spans, opts)
	data.Functions = functions
	data.Timeline.HostRows, data.Warnings = buildHostLanes(spans, opts, data.Warnings)
	data.Heatmap, data.Warnings = buildHeatmap(spans, startNS, windowNS, functionAgg, data.Timeline.HostRows, opts, data.Warnings)
	data.CDF = buildCDF(durations)
	data.Views = buildReportViews(spans, startNS, windowNS, opts)
	data.Hardware = buildHardware(in.Hardware)
	data.Summary.HardwareAvailable = data.Hardware.Available

	if opts.DeviceMode != "none" && len(in.DeviceSpans) > 0 {
		dev, warnings := buildDevice(in, spans, startNS, windowNS, opts)
		data.Device = dev
		data.Warnings = append(data.Warnings, warnings...)
		if dev != nil {
			data.Summary.DeviceSpans = dev.Spans
			data.Summary.DeviceMatchRate = dev.MatchRate
			data.Summary.DeviceSlowSyncRate = dev.SlowSyncRate
			data.Summary.DeviceBusyMS = dev.BusyMS
			data.Timeline.DeviceRows = dev.Rows
		}
	}
	if data.Summary.TraceSpans == 0 {
		data.Notes = append(data.Notes, "spans 中没有 trace 上下文：本报告不能按 request/batch 做瀑布下钻。")
	}
	if data.Device == nil {
		data.Notes = append(data.Notes, "没有嵌入设备时间线：device.jsonl 缺失，或使用了 --device-mode none。")
	}
	data.ViewsV2 = buildViewsV2ForData(&data, in, nil, opts)
	return data, nil
}

func buildReportViews(spans []runlog.Span, windowStart, windowNS int64, opts Options) ReportViews {
	return ReportViews{
		LatencyHeatmap: BuildLatencyHeatmap(spans, windowStart, windowNS, Heatmap2DOptions{
			Columns:         opts.BucketCount,
			LatencyRows:     22,
			OffsetRows:      50,
			SlowThresholdNS: opts.SlowThresholdNS,
		}),
		SubsecondHeatmap: BuildSubsecondHeatmap(spans, windowStart, windowNS, Heatmap2DOptions{
			Columns:     opts.BucketCount,
			LatencyRows: 22,
			OffsetRows:  50,
		}),
		AggregateBands: BuildAggregateBands(spans, windowStart, windowNS, AggregateOptions{
			BucketCount:  opts.BucketCount,
			TopFunctions: 12,
		}),
		TailCohort: BuildTailCohort(spans, CohortOptions{}),
	}
}

func defaultNotes() []string {
	return []string{
		"本报告只读派生，不修改 spans.jsonl、device.jsonl 或 hardware.jsonl。",
		"没有调用栈采样：这里的图表不是按栈聚合的火焰图，不能做代码行级归因。",
		"时间轴按 TID 泳道展示；设备区间按 correlationId 关联，只包含匹配到的记录。",
		"设备 busy 为区间并集；kernel/memcpy/HCCL 为区间时长之和，可能有重叠。",
	}
}

func spanStart(span runlog.Span) int64 {
	if span.StartNS > 0 {
		return span.StartNS
	}
	return span.TsNS
}

func spanEnd(span runlog.Span) int64 {
	if span.EndNS > 0 {
		return span.EndNS
	}
	return spanStart(span) + spanDurationNS(span)
}

func spanDurationNS(span runlog.Span) int64 {
	if span.DurationNS > 0 {
		return span.DurationNS
	}
	duration := spanEnd(span) - spanStart(span)
	if duration < 0 {
		return 0
	}
	return duration
}

func buildRunInfo(in *diagnose.Input, spans []runlog.Span) RunInfo {
	info := RunInfo{}
	if in.Manifest != nil {
		info.RunID = in.Manifest.RunID
		info.Profile = in.Manifest.Profile
		info.ToolVersion = in.Manifest.ToolVersion
		info.Host = in.Manifest.Host
		info.Kernel = in.Manifest.Kernel
		info.Status = in.Manifest.Status
		info.TargetPIDs = append([]int(nil), in.Manifest.TargetPIDs...)
		if !in.Manifest.StartedAt.IsZero() {
			info.StartedAt = in.Manifest.StartedAt.Format(time.RFC3339Nano)
		}
		if in.Manifest.EndedAt != nil {
			info.EndedAt = in.Manifest.EndedAt.Format(time.RFC3339Nano)
		}
	}
	if len(spans) > 0 {
		if info.RunID == "" {
			info.RunID = spans[0].RunID
		}
		if info.Profile == "" {
			info.Profile = spans[0].Profile
		}
	}
	return info
}

func buildSpanPoints(spans []runlog.Span, windowStart int64, opts Options) ([]SpanPoint, []int64) {
	points := make([]SpanPoint, 0, len(spans))
	durations := make([]int64, 0, len(spans))
	for _, span := range spans {
		duration := spanDurationNS(span)
		durations = append(durations, duration)
		start := spanStart(span)
		t := float64(start-windowStart) / 1e6
		if t < 0 {
			t = 0
		}
		point := SpanPoint{
			T:        t,
			D:        float64(duration) / 1e6,
			PID:      span.PID,
			TID:      span.TID,
			Row:      rowKey(span.PID, span.TID),
			Function: span.Function,
			Category: span.Category,
			SpanID:   span.SpanID,
			BatchID:  span.BatchID,
		}
		if point.Function == "" {
			point.Function = "(unknown)"
		}
		if point.Category == "" {
			point.Category = "other"
		}
		if span.Trace != nil {
			point.TraceID = span.Trace.TraceID
		}
		if span.OffCPUNS != nil {
			point.OffCPUMS = float64(*span.OffCPUNS) / 1e6
		}
		if span.FutexNS != nil {
			point.FutexMS = float64(*span.FutexNS) / 1e6
		}
		points = append(points, point)
	}
	return points, durations
}

func buildSummary(spans []runlog.Span, durations []int64, in *diagnose.Input, opts Options) Summary {
	sorted := append([]int64(nil), durations...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	pids := map[int]struct{}{}
	tids := map[int]struct{}{}
	traces := 0
	var totalNS int64
	for _, span := range spans {
		pids[span.PID] = struct{}{}
		tids[span.TID] = struct{}{}
		if span.Trace != nil && span.Trace.TraceID != "" {
			traces++
		}
		totalNS += spanDurationNS(span)
	}
	slow := 0
	for _, duration := range sorted {
		if duration >= opts.SlowThresholdNS {
			slow++
		}
	}
	dropped := uint64(0)
	if in.Manifest != nil {
		dropped = in.Manifest.DroppedEvents
	}
	return Summary{
		Spans:           len(spans),
		PIDs:            len(pids),
		TIDs:            len(tids),
		TotalMS:         float64(totalNS) / 1e6,
		P50MS:           float64(percentile(sorted, 0.50)) / 1e6,
		P95MS:           float64(percentile(sorted, 0.95)) / 1e6,
		P99MS:           float64(percentile(sorted, 0.99)) / 1e6,
		MaxMS:           float64(percentile(sorted, 1.00)) / 1e6,
		SlowSpans:       slow,
		SlowThresholdMS: float64(opts.SlowThresholdNS) / 1e6,
		DroppedEvents:   dropped,
		TraceSpans:      traces,
	}
}

func percentile(sorted []int64, p float64) int64 {
	if len(sorted) == 0 {
		return 0
	}
	if p <= 0 {
		return sorted[0]
	}
	if p >= 1 {
		return sorted[len(sorted)-1]
	}
	index := int(math.Ceil(p*float64(len(sorted)))) - 1
	if index < 0 {
		index = 0
	}
	if index >= len(sorted) {
		index = len(sorted) - 1
	}
	return sorted[index]
}

func buildCategories(spans []runlog.Span) []CategoryRow {
	type agg struct {
		count   int
		totalNS int64
	}
	byCategory := map[string]*agg{}
	for _, span := range spans {
		category := span.Category
		if category == "" {
			category = "other"
		}
		entry := byCategory[category]
		if entry == nil {
			entry = &agg{}
			byCategory[category] = entry
		}
		entry.count++
		entry.totalNS += spanDurationNS(span)
	}
	rows := make([]CategoryRow, 0, len(byCategory))
	for category, entry := range byCategory {
		rows = append(rows, CategoryRow{
			Category: category,
			Count:    entry.count,
			TotalMS:  float64(entry.totalNS) / 1e6,
			SpanPct:  float64(entry.count) * 100 / float64(len(spans)),
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].TotalMS == rows[j].TotalMS {
			return rows[i].Category < rows[j].Category
		}
		return rows[i].TotalMS > rows[j].TotalMS
	})
	return rows
}

func buildFunctions(spans []runlog.Span, opts Options) ([]FunctionRow, map[string]*funcAgg) {
	aggs := map[string]*funcAgg{}
	for _, span := range spans {
		name := span.Function
		if name == "" {
			name = "(unknown)"
		}
		entry := aggs[name]
		if entry == nil {
			entry = &funcAgg{category: span.Category}
			aggs[name] = entry
		}
		if entry.category == "" {
			entry.category = span.Category
		}
		duration := spanDurationNS(span)
		entry.durations = append(entry.durations, duration)
		entry.totalNS += duration
		if duration >= opts.SlowThresholdNS {
			entry.slow++
		}
	}
	rows := make([]FunctionRow, 0, len(aggs))
	for name, entry := range aggs {
		sorted := append([]int64(nil), entry.durations...)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
		category := entry.category
		if category == "" {
			category = "other"
		}
		rows = append(rows, FunctionRow{
			Function: name,
			Category: category,
			Count:    len(entry.durations),
			TotalMS:  float64(entry.totalNS) / 1e6,
			P50MS:    float64(percentile(sorted, 0.50)) / 1e6,
			P95MS:    float64(percentile(sorted, 0.95)) / 1e6,
			MaxMS:    float64(percentile(sorted, 1.00)) / 1e6,
			Slow:     entry.slow,
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].TotalMS == rows[j].TotalMS {
			return rows[i].Function < rows[j].Function
		}
		return rows[i].TotalMS > rows[j].TotalMS
	})
	if len(rows) > opts.TopN {
		rows = rows[:opts.TopN]
	}
	return rows, aggs
}

func buildHostLanes(spans []runlog.Span, opts Options, warnings []string) ([]LaneRow, []string) {
	byRow := map[string]*laneAgg{}
	for _, span := range spans {
		key := rowKey(span.PID, span.TID)
		entry := byRow[key]
		if entry == nil {
			entry = &laneAgg{pid: span.PID, tid: span.TID, name: span.ThreadName}
			byRow[key] = entry
		}
		if entry.name == "" {
			entry.name = span.ThreadName
		}
		entry.count++
		entry.totalNS += spanDurationNS(span)
	}
	rows := make([]LaneRow, 0, len(byRow))
	for key, entry := range byRow {
		label := fmt.Sprintf("TID %d", entry.tid)
		if entry.name != "" {
			label = fmt.Sprintf("%s · TID %d", entry.name, entry.tid)
		}
		rows = append(rows, LaneRow{
			ID: key, Label: label, PID: entry.pid, TID: entry.tid,
			Count: entry.count, TotalMS: float64(entry.totalNS) / 1e6,
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].PID == rows[j].PID {
			return rows[i].TID < rows[j].TID
		}
		return rows[i].PID < rows[j].PID
	})
	if len(rows) > opts.MaxLanes {
		sortedByCount := append([]LaneRow(nil), rows...)
		sort.Slice(sortedByCount, func(i, j int) bool {
			if sortedByCount[i].Count == sortedByCount[j].Count {
				return sortedByCount[i].ID < sortedByCount[j].ID
			}
			return sortedByCount[i].Count > sortedByCount[j].Count
		})
		kept := map[string]struct{}{}
		for _, row := range sortedByCount[:opts.MaxLanes] {
			kept[row.ID] = struct{}{}
		}
		filtered := rows[:0]
		for _, row := range rows {
			if _, ok := kept[row.ID]; ok {
				filtered = append(filtered, row)
			}
		}
		warnings = append(warnings, fmt.Sprintf("TID 泳道超过 %d 条，仅保留 span 数最多的 %d 条；原始 spans.jsonl 未改动。", opts.MaxLanes, opts.MaxLanes))
		rows = filtered
	}
	return rows, warnings
}

func buildHeatmap(spans []runlog.Span, windowStart, windowNS int64, functions map[string]*funcAgg, lanes []LaneRow, opts Options, warnings []string) (HeatmapData, []string) {
	dim := "tid"
	tidCount := len(lanes)
	if tidCount <= 4 {
		dim = "function"
	}
	type rowInfo struct {
		key   string
		label string
		total int64
	}
	var rowInfos []rowInfo
	if dim == "function" {
		for name, entry := range functions {
			rowInfos = append(rowInfos, rowInfo{key: name, label: name, total: entry.totalNS})
		}
		sort.Slice(rowInfos, func(i, j int) bool {
			if rowInfos[i].total == rowInfos[j].total {
				return rowInfos[i].key < rowInfos[j].key
			}
			return rowInfos[i].total > rowInfos[j].total
		})
	} else {
		for _, lane := range lanes {
			rowInfos = append(rowInfos, rowInfo{
				key: rowKey(lane.PID, lane.TID), label: lane.Label,
				total: int64(lane.TotalMS * 1e6),
			})
		}
	}
	if len(rowInfos) > opts.MaxHeatmapRows {
		rowInfos = rowInfos[:opts.MaxHeatmapRows]
		warnings = append(warnings, fmt.Sprintf("热力图行数超过 %d，仅保留前 %d 行。", opts.MaxHeatmapRows, opts.MaxHeatmapRows))
	}
	bucketNS := int64(math.Ceil(float64(windowNS) / float64(opts.BucketCount)))
	if bucketNS < 100_000 {
		bucketNS = 100_000
	}
	columns := int(math.Ceil(float64(windowNS) / float64(bucketNS)))
	if columns < 1 {
		columns = 1
	}
	rowIndex := map[string]int{}
	for i, row := range rowInfos {
		rowIndex[row.key] = i
	}
	cells := make([][]cellAgg, len(rowInfos))
	for i := range cells {
		cells[i] = make([]cellAgg, columns)
	}
	for _, span := range spans {
		key := span.Function
		if key == "" {
			key = "(unknown)"
		}
		if dim == "tid" {
			key = rowKey(span.PID, span.TID)
		}
		row, ok := rowIndex[key]
		if !ok {
			continue
		}
		offsetNS := spanStart(span) - windowStart
		column := int(offsetNS / bucketNS)
		if column < 0 {
			column = 0
		}
		if column >= columns {
			column = columns - 1
		}
		duration := spanDurationNS(span)
		cells[row][column].durations = append(cells[row][column].durations, duration)
		if duration >= opts.SlowThresholdNS {
			cells[row][column].slow++
		}
	}
	heat := HeatmapData{
		Dim:      dim,
		BucketMS: float64(bucketNS) / 1e6,
		Columns:  columns,
		Rows:     make([]HeatRow, 0, len(rowInfos)),
	}
	for i, row := range rowInfos {
		heatRow := HeatRow{
			Label: row.label,
			P95:   make([]float64, columns),
			Slow:  make([]int, columns),
			Count: make([]int, columns),
		}
		for column := 0; column < columns; column++ {
			cell := cells[i][column]
			heatRow.Count[column] = len(cell.durations)
			heatRow.Slow[column] = cell.slow
			if len(cell.durations) > 0 {
				sort.Slice(cell.durations, func(a, b int) bool { return cell.durations[a] < cell.durations[b] })
				value := float64(percentile(cell.durations, 0.95)) / 1e6
				heatRow.P95[column] = value
				if value > heat.MaxP95MS {
					heat.MaxP95MS = value
				}
			}
			if cell.slow > heat.MaxSlow {
				heat.MaxSlow = cell.slow
			}
		}
		heat.Rows = append(heat.Rows, heatRow)
	}
	return heat, warnings
}

func buildCDF(durations []int64) []CDFPoint {
	if len(durations) == 0 {
		return []CDFPoint{}
	}
	sorted := append([]int64(nil), durations...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	points := make([]CDFPoint, 0, 101)
	for i := 0; i <= 100; i++ {
		points = append(points, CDFPoint{
			P: float64(i),
			D: float64(percentile(sorted, float64(i)/100)) / 1e6,
		})
	}
	return points
}

func buildHardware(samples []hardware.Sample) HardwareData {
	data := HardwareData{Available: len(samples) > 0, Samples: len(samples)}
	devices := map[int]struct{}{}
	names := map[int]string{}
	var hbmMin *float64
	for _, sample := range samples {
		devices[sample.DeviceID] = struct{}{}
		if sample.Name != "" {
			names[sample.DeviceID] = sample.Name
		}
		if sample.AICorePct != nil && *sample.AICorePct > data.AICoreMaxPct {
			data.AICoreMaxPct = *sample.AICorePct
		}
		if value, ok := sample.HBMFreePct(); ok {
			if hbmMin == nil || value < *hbmMin {
				v := value
				hbmMin = &v
			}
		}
	}
	data.Devices = len(devices)
	data.HBMFreeMinPct = hbmMin
	return data
}

func buildDevice(in *diagnose.Input, spans []runlog.Span, windowStart, windowNS int64, opts Options) (*DeviceData, []string) {
	pidMap := map[int]int(nil)
	tidMap := map[int]int(nil)
	if in.PIDMap != nil {
		pidMap = in.PIDMap.PIDMap
		tidMap = in.PIDMap.TIDMap
	}
	if len(pidMap) == 0 && opts.DeviceContainerID != "" {
		var err error
		pidMap, tidMap, err = device.ResolveContainerPIDMap(opts.DeviceContainerID)
		if err != nil {
			pidMap, tidMap = nil, nil
			return nil, []string{fmt.Sprintf("device pid 映射失败（%s）：%v", opts.DeviceContainerID, err)}
		}
	}
	joined := device.Join(spans, in.DeviceSpans, device.Options{
		TimeToleranceNS: 100_000,
		SlowThresholdNS: opts.SlowThresholdNS,
		PIDMap:          pidMap,
		TIDMap:          tidMap,
	})
	data := &DeviceData{
		Available:    true,
		Spans:        len(in.DeviceSpans),
		Kinds:        joined.DeviceKinds,
		MatchRate:    joined.MatchRate,
		SlowSyncRate: joined.SlowSyncMatchRate,
		Intervals:    []DeviceInterval{},
		Hosts:        []DeviceHostPoint{},
		Rows:         []LaneRow{},
		Busy:         []BusyPoint{},
		Verdicts:     map[string]int{},
	}
	for _, verdict := range joined.Verdicts {
		data.Verdicts[verdict.Verdict] = int(verdict.Count)
	}
	for _, summary := range joined.Devices {
		data.BusyMS += float64(summary.BusyNS) / 1e6
	}
	warnings := append([]string{}, joined.Warnings...)
	deviceRows := map[string]*deviceRowAgg{}
	seen := map[string]struct{}{}
	for _, host := range joined.Hosts {
		if !host.Matched || len(host.Matches) == 0 {
			continue
		}
		duration := host.DurationNS
		if duration <= 0 {
			duration = host.EndNS - host.StartNS
		}
		data.Hosts = append(data.Hosts, DeviceHostPoint{
			SpanID:     host.SpanID,
			Function:   host.Function,
			T:          float64(host.StartNS-windowStart) / 1e6,
			D:          float64(duration) / 1e6,
			BusyPct:    host.BusyPct * 100,
			Verdicts:   strings.Join(host.Verdicts, ","),
			DeviceBusy: float64(host.DeviceBusyNS) / 1e6,
			Kernel:     float64(host.KernelNS) / 1e6,
			Memcpy:     float64(host.MemcpyNS) / 1e6,
			HCCL:       float64(host.HCCLNS) / 1e6,
		})
		for _, match := range host.Matches {
			startNS, endNS := match.StartNS, match.EndNS
			if endNS <= startNS {
				startNS = host.StartNS
				endNS = startNS + match.DurationNS
			}
			if startNS <= 0 || endNS <= startNS {
				continue
			}
			key := fmt.Sprintf("%s|%d|%d|%d|%d", match.Kind, startNS, endNS, match.CorrelationID, match.StreamID)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			row := deviceRowKey(match.DeviceID, match.StreamID)
			entry := deviceRows[row]
			if entry == nil {
				entry = &deviceRowAgg{deviceID: match.DeviceID, streamID: match.StreamID}
				deviceRows[row] = entry
			}
			entry.count++
			entry.totalNS += endNS - startNS
			data.Intervals = append(data.Intervals, DeviceInterval{
				T:      float64(startNS-windowStart) / 1e6,
				D:      float64(endNS-startNS) / 1e6,
				Kind:   match.Kind,
				Name:   match.Name,
				Device: match.DeviceID,
				Stream: match.StreamID,
				Corr:   match.CorrelationID,
				Row:    row,
				Host:   host.SpanID,
			})
		}
	}
	sort.Slice(data.Intervals, func(i, j int) bool {
		if data.Intervals[i].T == data.Intervals[j].T {
			return data.Intervals[i].Kind < data.Intervals[j].Kind
		}
		return data.Intervals[i].T < data.Intervals[j].T
	})
	for row, entry := range deviceRows {
		label := fmt.Sprintf("NPU %d", entry.deviceID)
		if entry.streamID != 0 {
			label = fmt.Sprintf("NPU %d · stream %d", entry.deviceID, entry.streamID)
		}
		data.Rows = append(data.Rows, LaneRow{
			ID: row, Label: label, DeviceID: entry.deviceID, StreamID: entry.streamID,
			Count: entry.count, TotalMS: float64(entry.totalNS) / 1e6,
		})
	}
	sort.Slice(data.Rows, func(i, j int) bool {
		if data.Rows[i].DeviceID == data.Rows[j].DeviceID {
			return data.Rows[i].StreamID < data.Rows[j].StreamID
		}
		return data.Rows[i].DeviceID < data.Rows[j].DeviceID
	})
	data.Busy = buildBusyBuckets(data.Intervals, windowStart, windowNS, opts.BucketCount)
	if len(data.Intervals) == 0 {
		warnings = append(warnings, "device.jsonl 存在，但没有找到与 host span 匹配的设备区间；四象限和设备忙率图为空。")
	}
	return data, warnings
}

func buildBusyBuckets(intervals []DeviceInterval, windowStart, windowNS int64, bucketCount int) []BusyPoint {
	if len(intervals) == 0 {
		return []BusyPoint{}
	}
	bucketNS := int64(math.Ceil(float64(windowNS) / float64(bucketCount)))
	if bucketNS < 100_000 {
		bucketNS = 100_000
	}
	columns := int(math.Ceil(float64(windowNS) / float64(bucketNS)))
	if columns < 1 {
		columns = 1
	}
	converted := make([]busyInterval, 0, len(intervals))
	for _, item := range intervals {
		start := windowStart + int64(math.Round(item.T*1e6))
		end := start + int64(math.Round(item.D*1e6))
		if end > start {
			converted = append(converted, busyInterval{start: start, end: end, kind: item.Kind})
		}
	}
	points := make([]BusyPoint, 0, columns)
	for i := 0; i < columns; i++ {
		start := windowStart + int64(i)*bucketNS
		end := start + bucketNS
		if end > windowStart+windowNS {
			end = windowStart + windowNS
		}
		bucketNSActual := end - start
		if bucketNSActual <= 0 {
			continue
		}
		var clipped []busyInterval
		var kernelNS, memcpyNS, hcclNS int64
		for _, item := range converted {
			clipStart := item.start
			if clipStart < start {
				clipStart = start
			}
			clipEnd := item.end
			if clipEnd > end {
				clipEnd = end
			}
			if clipEnd <= clipStart {
				continue
			}
			clipped = append(clipped, busyInterval{start: clipStart, end: clipEnd, kind: item.kind})
			switch item.kind {
			case device.KindKernel:
				kernelNS += clipEnd - clipStart
			case device.KindMemcpy:
				memcpyNS += clipEnd - clipStart
			case device.KindHCCL:
				hcclNS += clipEnd - clipStart
			}
		}
		busyNS := unionDuration(clipped)
		points = append(points, BusyPoint{
			T:      float64(int64(i)*bucketNS) / 1e6,
			Busy:   math.Min(100, float64(busyNS)*100/float64(bucketNSActual)),
			Kernel: math.Min(100, float64(kernelNS)*100/float64(bucketNSActual)),
			Memcpy: math.Min(100, float64(memcpyNS)*100/float64(bucketNSActual)),
			HCCL:   math.Min(100, float64(hcclNS)*100/float64(bucketNSActual)),
			Count:  len(clipped),
		})
	}
	return points
}

func unionDuration(items []busyInterval) int64 {
	if len(items) == 0 {
		return 0
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].start == items[j].start {
			return items[i].end < items[j].end
		}
		return items[i].start < items[j].start
	})
	var total int64
	curStart, curEnd := items[0].start, items[0].end
	for _, item := range items[1:] {
		if item.start > curEnd {
			total += curEnd - curStart
			curStart, curEnd = item.start, item.end
			continue
		}
		if item.end > curEnd {
			curEnd = item.end
		}
	}
	total += curEnd - curStart
	return total
}

func rowKey(pid, tid int) string {
	return strconv.Itoa(pid) + ":" + strconv.Itoa(tid)
}

func deviceRowKey(deviceID int, streamID int64) string {
	return "dev:" + strconv.Itoa(deviceID) + ":stream:" + strconv.FormatInt(streamID, 10)
}
