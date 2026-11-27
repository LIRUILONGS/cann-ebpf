package report

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"cann-ebpf/internal/metrics"
)

// MetricsData is the report-facing, downsampled metrics view. Raw metrics
// remain in metrics.jsonl; this type is derived and read-only.
type MetricsData struct {
	Path         string         `json:"-"`
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
	StartNS      int64          `json:"-"`
	EndNS        int64          `json:"-"`
}

// MetricSeries is a columnar series so embedded HTML stays compact.
type MetricSeries struct {
	ID             string            `json:"id"`
	Name           string            `json:"name"`
	Kind           string            `json:"kind"`
	Unit           string            `json:"unit"`
	Source         string            `json:"source"`
	Device         string            `json:"device,omitempty"`
	Labels         map[string]string `json:"labels,omitempty"`
	T              []float64         `json:"t"`
	V              []float64         `json:"v"`
	Count          int               `json:"count"`
	Downsampled    bool              `json:"downsampled"`
	OriginalPoints int               `json:"original_points"`
}

// MetricStat is a compact statistical summary for one emitted series. It is
// computed from the raw points before downsampling. First and Last are the
// first and last observed values, not timestamps. Rate fields are populated
// for counters only and are expressed per second.
type MetricStat struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Kind     string  `json:"kind"`
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

// MetricsOptions controls the report-side metrics projection. Raw JSONL is
// never modified; downsampling only affects the embedded HTML payload.
type MetricsOptions struct {
	WindowStartNS int64
	MaxPoints     int
	MaxSeries     int
}

func (o *MetricsOptions) normalize() {
	if o.MaxPoints <= 0 {
		o.MaxPoints = 2000
	}
	if o.MaxSeries <= 0 {
		o.MaxSeries = 64
	}
}

type metricPoint struct {
	T float64
	V float64
}

type metricSeriesAgg struct {
	point  metrics.Point
	points []metricPoint
}

// LoadMetrics reads metrics.jsonl plus an optional metrics.meta.json sidecar
// and projects it into the compact, column-oriented report model.
func LoadMetrics(path string, opts MetricsOptions) (*MetricsData, error) {
	opts.normalize()
	points, err := metrics.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read metrics: %w", err)
	}
	data := &MetricsData{
		Path:         path,
		Available:    false,
		Sources:      []string{},
		Series:       []MetricSeries{},
		Stats:        []MetricStat{},
		Warnings:     []string{},
		ClockQuality: "mono",
	}
	if len(points) == 0 {
		data.Reason = "no metrics points"
		return data, nil
	}
	for _, point := range points {
		if point.MonoNS <= 0 {
			data.ClockQuality = "wall_only"
			break
		}
	}
	startNS, endNS := points[0].MonoNS, points[0].MonoNS
	for _, point := range points[1:] {
		if point.MonoNS < startNS {
			startNS = point.MonoNS
		}
		if point.MonoNS > endNS {
			endNS = point.MonoNS
		}
	}
	baseNS := opts.WindowStartNS
	if baseNS <= 0 || baseNS > startNS {
		baseNS = startNS
	}
	data.StartNS, data.EndNS = startNS, endNS
	data.Samples = len(points)

	metaPath := filepath.Join(filepath.Dir(path), "metrics.meta.json")
	if meta, err := readMetricsMeta(metaPath); err == nil {
		data.IntervalMS = meta.IntervalMS
		if len(meta.Sources) > 0 {
			data.Sources = append([]string(nil), meta.Sources...)
		}
		data.Warnings = append(data.Warnings, meta.Warnings...)
	} else if !os.IsNotExist(err) {
		data.Warnings = append(data.Warnings, "metrics.meta.json 读取失败："+err.Error())
	}

	bySeries := map[string]*metricSeriesAgg{}
	for _, point := range points {
		key := metrics.SeriesKey(point)
		agg := bySeries[key]
		if agg == nil {
			agg = &metricSeriesAgg{point: point}
			bySeries[key] = agg
		}
		t := float64(point.MonoNS-baseNS) / 1e6
		if t < 0 {
			t = 0
		}
		switch point.Kind {
		case metrics.KindGauge, metrics.KindCounter:
			if point.Value == nil || math.IsNaN(*point.Value) || math.IsInf(*point.Value, 0) {
				continue
			}
			agg.points = append(agg.points, metricPoint{T: t, V: *point.Value})
		case metrics.KindHistogram:
			if value, ok := histogramP95(point.Histogram); ok {
				agg.points = append(agg.points, metricPoint{T: t, V: value})
			}
		}
	}

	sourceSet := map[string]struct{}{}
	keys := make([]string, 0, len(bySeries))
	for key := range bySeries {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		left, right := bySeries[keys[i]].point, bySeries[keys[j]].point
		if metricPriority(left.Name) != metricPriority(right.Name) {
			return metricPriority(left.Name) < metricPriority(right.Name)
		}
		return keys[i] < keys[j]
	})
	if len(keys) > opts.MaxSeries {
		data.Warnings = append(data.Warnings, fmt.Sprintf("metrics series 数量 %d 超过上限 %d，已截断", len(keys), opts.MaxSeries))
		keys = keys[:opts.MaxSeries]
	}
	for _, key := range keys {
		agg := bySeries[key]
		if len(agg.points) == 0 {
			continue
		}
		sort.Slice(agg.points, func(i, j int) bool { return agg.points[i].T < agg.points[j].T })
		series := MetricSeries{
			ID:             key,
			Name:           agg.point.Name,
			Kind:           string(agg.point.Kind),
			Unit:           agg.point.Unit,
			Source:         agg.point.Source,
			Device:         agg.point.Device,
			Labels:         agg.point.Labels,
			OriginalPoints: len(agg.points),
			Count:          len(agg.points),
		}
		if agg.point.Kind == metrics.KindHistogram {
			series.Name += ":p95"
		}
		stat := computeMetricStat(series, agg.points)
		pointsOut, downsampled := downsampleMetricPoints(agg.points, opts.MaxPoints)
		series.T = make([]float64, len(pointsOut))
		series.V = make([]float64, len(pointsOut))
		for i, point := range pointsOut {
			series.T[i] = point.T
			series.V[i] = point.V
		}
		series.Count = len(series.T)
		series.Downsampled = downsampled
		data.Series = append(data.Series, series)
		data.Stats = append(data.Stats, stat)
		sourceSet[agg.point.Source] = struct{}{}
	}
	if len(data.Sources) == 0 {
		for source := range sourceSet {
			data.Sources = append(data.Sources, source)
		}
		sort.Strings(data.Sources)
	}
	if len(data.Series) == 0 {
		data.Reason = "no usable metric series"
		return data, nil
	}
	data.Available = true
	if data.IntervalMS <= 0 {
		data.IntervalMS = estimateIntervalMS(data.Series)
	}
	data.Coverage = estimateCoverage(data.Series, data.IntervalMS, float64(endNS-startNS)/1e6)
	return data, nil
}

func readMetricsMeta(path string) (metrics.Meta, error) {
	var meta metrics.Meta
	raw, err := os.ReadFile(path)
	if err != nil {
		return meta, err
	}
	if err := json.Unmarshal(raw, &meta); err != nil {
		return meta, err
	}
	if meta.Schema != "" && meta.Schema != metrics.MetaSchemaV1 {
		return meta, fmt.Errorf("unexpected meta schema %q", meta.Schema)
	}
	return meta, nil
}

func histogramP95(h *metrics.Histogram) (float64, bool) {
	if h == nil || h.Count <= 0 || len(h.Buckets) == 0 || len(h.Counts) == 0 {
		return 0, false
	}
	counts := make([]int64, len(h.Counts))
	if h.Cumulative {
		copy(counts, h.Counts)
	} else {
		var running int64
		for i, count := range h.Counts {
			running += count
			counts[i] = running
		}
	}
	target := 0.95 * float64(h.Count)
	prevBound, prevCount := 0.0, int64(0)
	for i, bound := range h.Buckets {
		count := counts[i]
		if float64(count) >= target {
			if count == prevCount {
				return bound, true
			}
			ratio := (target - float64(prevCount)) / float64(count-prevCount)
			return prevBound + ratio*(bound-prevBound), true
		}
		prevBound, prevCount = bound, count
	}
	return h.Buckets[len(h.Buckets)-1], true
}

func downsampleMetricPoints(points []metricPoint, maxPoints int) ([]metricPoint, bool) {
	if len(points) <= maxPoints {
		return points, false
	}
	buckets := maxPoints / 2
	if buckets < 1 {
		buckets = 1
	}
	out := make([]metricPoint, 0, maxPoints)
	for bucket := 0; bucket < buckets; bucket++ {
		start := bucket * len(points) / buckets
		end := (bucket + 1) * len(points) / buckets
		if end <= start {
			continue
		}
		minIndex, maxIndex := start, start
		for i := start + 1; i < end; i++ {
			if points[i].V < points[minIndex].V {
				minIndex = i
			}
			if points[i].V > points[maxIndex].V {
				maxIndex = i
			}
		}
		if minIndex <= maxIndex {
			out = append(out, points[minIndex])
			if maxIndex != minIndex {
				out = append(out, points[maxIndex])
			}
		} else {
			out = append(out, points[maxIndex], points[minIndex])
		}
	}
	if len(out) > maxPoints {
		out = out[:maxPoints]
	}
	return out, true
}

// computeMetricStat derives stats from the raw, sorted series points. The
// caller computes this before downsampling so percentiles are not distorted
// by the display projection.
func computeMetricStat(series MetricSeries, points []metricPoint) MetricStat {
	stat := MetricStat{
		ID:     series.ID,
		Name:   series.Name,
		Kind:   series.Kind,
		Unit:   series.Unit,
		Source: series.Source,
		Device: series.Device,
	}
	if len(points) == 0 {
		return stat
	}
	values := make([]float64, len(points))
	var sum float64
	for i, point := range points {
		values[i] = point.V
		sum += point.V
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	stat.Points = len(points)
	stat.First = points[0].V
	stat.Last = points[len(points)-1].V
	stat.Min = sorted[0]
	stat.Max = sorted[len(sorted)-1]
	stat.Mean = sum / float64(len(points))
	stat.P50 = metricPercentile(sorted, 0.50)
	stat.P95 = metricPercentile(sorted, 0.95)
	stat.P99 = metricPercentile(sorted, 0.99)
	if series.Kind == string(metrics.KindCounter) {
		stat.RateLast, stat.RateMean = counterRates(points)
	}
	return stat
}

// metricPercentile uses the same nearest-rank definition as the span report
// percentiles so metric and trace summaries agree.
func metricPercentile(sorted []float64, p float64) float64 {
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

// counterRates returns per-second rates for a counter. Counter resets are not
// interpreted as negative rates: RateMean sums positive deltas over the valid
// observed time, and RateLast is zero when the final valid delta is not
// positive.
func counterRates(points []metricPoint) (rateLast, rateMean float64) {
	if len(points) < 2 {
		return 0, 0
	}
	var positiveDelta, validSeconds float64
	var lastRate float64
	for i := 1; i < len(points); i++ {
		deltaSeconds := (points[i].T - points[i-1].T) / 1000
		if deltaSeconds <= 0 {
			continue
		}
		validSeconds += deltaSeconds
		delta := points[i].V - points[i-1].V
		if delta > 0 {
			positiveDelta += delta
			lastRate = delta / deltaSeconds
		} else {
			lastRate = 0
		}
	}
	if validSeconds > 0 {
		rateMean = positiveDelta / validSeconds
	}
	return lastRate, rateMean
}

func metricPriority(name string) int {
	lower := strings.ToLower(name)
	prefixes := []string{
		"npu_aicore", "npu_hbm", "npu_power", "npu_temperature",
		"vllm_requests_running", "vllm_requests_waiting", "vllm_kv_cache", "vllm_gpu_cache",
		"cann_p95", "cann_latency",
	}
	for i, prefix := range prefixes {
		if strings.HasPrefix(lower, prefix) {
			return i
		}
	}
	return len(prefixes) + 1
}

func estimateIntervalMS(series []MetricSeries) float64 {
	for _, item := range series {
		if len(item.T) < 2 {
			continue
		}
		diffs := make([]float64, 0, len(item.T)-1)
		for i := 1; i < len(item.T); i++ {
			if delta := item.T[i] - item.T[i-1]; delta > 0 {
				diffs = append(diffs, delta)
			}
		}
		if len(diffs) == 0 {
			continue
		}
		sort.Float64s(diffs)
		return diffs[len(diffs)/2]
	}
	return 0
}

func estimateCoverage(series []MetricSeries, intervalMS, durationMS float64) float64 {
	if intervalMS <= 0 || durationMS <= 0 {
		return 1
	}
	expected := durationMS/intervalMS + 1
	if expected <= 0 {
		return 1
	}
	actual := 0
	for _, item := range series {
		if len(item.T) > actual {
			actual = len(item.T)
		}
	}
	coverage := float64(actual) / expected
	if coverage > 1 {
		return 1
	}
	if coverage < 0 {
		return 0
	}
	return coverage
}
