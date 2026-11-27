package report

import (
	"math"
	"sort"

	"cann-ebpf/internal/runlog"
)

const (
	defaultHeatmap2DColumns   = 160
	defaultLatencyHeatmapRows = 22
	defaultSubsecondRows      = 50
	minLatencyHeatmapNS       = int64(1_000)
	nanosecondsPerSecond      = int64(1_000_000_000)
)

type Heatmap2DOptions struct {
	Columns         int
	LatencyRows     int
	OffsetRows      int
	SlowThresholdNS int64
}

type LatencyHeatmapData struct {
	Available      bool      `json:"available"`
	Reason         string    `json:"reason,omitempty"`
	BucketMS       float64   `json:"bucket_ms"`
	Columns        int       `json:"columns"`
	LatencyMinMS   float64   `json:"latency_min_ms"`
	LatencyMaxMS   float64   `json:"latency_max_ms"`
	LatencyEdgesMS []float64 `json:"latency_edges_ms"`
	Counts         [][]int   `json:"counts"`
	P50            []float64 `json:"p50"`
	P95            []float64 `json:"p95"`
	P99            []float64 `json:"p99"`
	MaxCount       int       `json:"max_count"`
}

type SubsecondHeatmapData struct {
	Available  bool    `json:"available"`
	Reason     string  `json:"reason,omitempty"`
	BucketMS   float64 `json:"bucket_ms"`
	Columns    int     `json:"columns"`
	Rows       int     `json:"rows"`
	OffsetMS   float64 `json:"offset_ms"`
	Counts     [][]int `json:"counts"`
	MaxCount   int     `json:"max_count"`
	DurationMS float64 `json:"duration_ms"`
}

func BuildLatencyHeatmap(spans []runlog.Span, windowStart, windowNS int64, opts Heatmap2DOptions) LatencyHeatmapData {
	opts = normalizeHeatmap2DOptions(opts)
	windowStart, windowNS = normalizeHeatmapWindow(spans, windowStart, windowNS)
	bucketMS := float64(windowNS) / float64(opts.Columns) / 1e6

	data := LatencyHeatmapData{
		BucketMS:     bucketMS,
		Columns:      opts.Columns,
		LatencyMinMS: float64(minLatencyHeatmapNS) / 1e6,
		Counts:       makeHeatmapMatrix(opts.LatencyRows, opts.Columns),
		P50:          make([]float64, opts.Columns),
		P95:          make([]float64, opts.Columns),
		P99:          make([]float64, opts.Columns),
	}
	if len(spans) == 0 {
		data.Reason = "no spans"
		data.LatencyMaxMS = data.LatencyMinMS
		data.LatencyEdgesMS = buildLatencyEdges(opts.LatencyRows, minLatencyHeatmapNS)
		return data
	}

	type sample struct {
		column   int
		duration int64
	}
	samples := make([]sample, 0, len(spans))
	maxDurationNS := minLatencyHeatmapNS
	for _, span := range spans {
		duration := heatmapSpanDurationNS(span)
		if duration > maxDurationNS {
			maxDurationNS = duration
		}
		samples = append(samples, sample{
			column:   heatmapColumnIndex(heatmapSpanStartNS(span), windowStart, windowNS, opts.Columns),
			duration: duration,
		})
	}

	maxDurationNS = ceilLatencyMaxNS(maxDurationNS)
	data.LatencyEdgesMS = buildLatencyEdges(opts.LatencyRows, maxDurationNS)
	data.LatencyMaxMS = data.LatencyEdgesMS[len(data.LatencyEdgesMS)-1]
	valuesByColumn := make([][]int64, opts.Columns)

	for _, item := range samples {
		row := latencyHeatmapRow(item.duration, data.LatencyEdgesMS)
		data.Counts[row][item.column]++
		if data.Counts[row][item.column] > data.MaxCount {
			data.MaxCount = data.Counts[row][item.column]
		}
		valuesByColumn[item.column] = append(valuesByColumn[item.column], item.duration)
	}

	for column, values := range valuesByColumn {
		if len(values) == 0 {
			continue
		}
		sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
		data.P50[column] = nearestRankMilliseconds(values, 0.50)
		data.P95[column] = nearestRankMilliseconds(values, 0.95)
		data.P99[column] = nearestRankMilliseconds(values, 0.99)
	}
	data.Available = true
	return data
}

func BuildSubsecondHeatmap(spans []runlog.Span, windowStart, windowNS int64, opts Heatmap2DOptions) SubsecondHeatmapData {
	opts = normalizeHeatmap2DOptions(opts)
	windowStart, windowNS = normalizeHeatmapWindow(spans, windowStart, windowNS)
	data := SubsecondHeatmapData{
		BucketMS:   float64(windowNS) / float64(opts.Columns) / 1e6,
		Columns:    opts.Columns,
		Rows:       opts.OffsetRows,
		OffsetMS:   1000.0 / float64(opts.OffsetRows),
		Counts:     makeHeatmapMatrix(opts.OffsetRows, opts.Columns),
		DurationMS: float64(windowNS) / 1e6,
	}
	if len(spans) == 0 {
		data.Reason = "no spans"
		return data
	}
	if windowNS < 2*nanosecondsPerSecond {
		data.Reason = "run too short"
		return data
	}

	for _, span := range spans {
		start := heatmapSpanStartNS(span)
		column := heatmapColumnIndex(start, windowStart, windowNS, opts.Columns)
		row := subsecondOffsetRow(start, windowStart, windowNS, opts.OffsetRows)
		data.Counts[row][column]++
		if data.Counts[row][column] > data.MaxCount {
			data.MaxCount = data.Counts[row][column]
		}
	}
	data.Available = true
	return data
}

func normalizeHeatmap2DOptions(opts Heatmap2DOptions) Heatmap2DOptions {
	if opts.Columns <= 0 {
		opts.Columns = defaultHeatmap2DColumns
	}
	if opts.LatencyRows <= 0 {
		opts.LatencyRows = defaultLatencyHeatmapRows
	}
	if opts.OffsetRows <= 0 {
		opts.OffsetRows = defaultSubsecondRows
	}
	return opts
}

func normalizeHeatmapWindow(spans []runlog.Span, windowStart, windowNS int64) (int64, int64) {
	if windowNS > 0 {
		return windowStart, windowNS
	}
	if len(spans) == 0 {
		return windowStart, 1
	}
	first := heatmapSpanStartNS(spans[0])
	last := heatmapSpanEndNS(spans[0])
	for _, span := range spans[1:] {
		start := heatmapSpanStartNS(span)
		end := heatmapSpanEndNS(span)
		if start < first {
			first = start
		}
		if end > last {
			last = end
		}
	}
	if last <= first {
		last = first + 1
	}
	return first, last - first
}

func heatmapSpanStartNS(span runlog.Span) int64 {
	if span.StartNS > 0 {
		return span.StartNS
	}
	return span.TsNS
}

func heatmapSpanEndNS(span runlog.Span) int64 {
	if span.EndNS > 0 {
		return span.EndNS
	}
	duration := heatmapSpanDurationNS(span)
	if duration <= 0 {
		return heatmapSpanStartNS(span)
	}
	return heatmapSpanStartNS(span) + duration
}

func heatmapSpanDurationNS(span runlog.Span) int64 {
	if span.DurationNS > 0 {
		return span.DurationNS
	}
	start := heatmapSpanStartNS(span)
	if start > 0 && span.EndNS > start {
		duration := span.EndNS - start
		if duration > 0 {
			return duration
		}
	}
	return 0
}

func makeHeatmapMatrix(rows, columns int) [][]int {
	matrix := make([][]int, rows)
	for row := range matrix {
		matrix[row] = make([]int, columns)
	}
	return matrix
}

func heatmapColumnIndex(start, windowStart, windowNS int64, columns int) int {
	if columns <= 1 || windowNS <= 0 {
		return 0
	}
	relative := start - windowStart
	if relative <= 0 {
		return 0
	}
	if relative >= windowNS {
		return columns - 1
	}
	column := int(float64(relative) / float64(windowNS) * float64(columns))
	if column < 0 {
		return 0
	}
	if column >= columns {
		return columns - 1
	}
	return column
}

func ceilLatencyMaxNS(maxNS int64) int64 {
	if maxNS <= minLatencyHeatmapNS {
		return minLatencyHeatmapNS
	}
	exponent := math.Pow(10, math.Floor(math.Log10(float64(maxNS))))
	for _, multiplier := range []float64{1, 2, 5, 10} {
		candidate := multiplier * exponent
		if float64(maxNS) <= candidate {
			return int64(math.Ceil(candidate))
		}
	}
	return int64(math.Ceil(10 * exponent))
}

func buildLatencyEdges(rows int, maxNS int64) []float64 {
	edges := make([]float64, rows+1)
	minNS := float64(minLatencyHeatmapNS)
	maxValue := float64(maxNS)
	if maxValue < minNS {
		maxValue = minNS
	}
	for index := 0; index <= rows; index++ {
		if index == 0 {
			edges[index] = minNS / 1e6
			continue
		}
		fraction := float64(index) / float64(rows)
		edges[index] = minNS * math.Pow(maxValue/minNS, fraction) / 1e6
		if edges[index] <= edges[index-1] {
			edges[index] = edges[index-1] + 0.000001
		}
	}
	edges[rows] = maxValue / 1e6
	if rows > 0 && edges[rows] <= edges[rows-1] {
		edges[rows] = edges[rows-1] + 0.000001
	}
	return edges
}

func latencyHeatmapRow(durationNS int64, edgesMS []float64) int {
	rows := len(edgesMS) - 1
	if rows <= 1 || durationNS <= 0 {
		return 0
	}
	durationMS := float64(durationNS) / 1e6
	row := sort.Search(rows, func(index int) bool {
		return durationMS < edgesMS[index+1]
	})
	if row >= rows {
		return rows - 1
	}
	return row
}

func nearestRankMilliseconds(sorted []int64, percentile float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	rank := int(math.Ceil(percentile * float64(len(sorted))))
	if rank < 1 {
		rank = 1
	}
	if rank > len(sorted) {
		rank = len(sorted)
	}
	return float64(sorted[rank-1]) / 1e6
}

func subsecondOffsetRow(start, windowStart, windowNS int64, rows int) int {
	if rows <= 1 {
		return 0
	}
	relative := start - windowStart
	if relative < 0 {
		relative = 0
	} else if relative >= windowNS {
		relative = windowNS - 1
	}
	offset := relative % nanosecondsPerSecond
	row := int(float64(offset) / (float64(nanosecondsPerSecond) / float64(rows)))
	if row < 0 {
		return 0
	}
	if row >= rows {
		return rows - 1
	}
	return row
}
