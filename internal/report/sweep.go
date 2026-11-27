package report

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	defaultSweepMinConcurrencyLevels = 4
	defaultSweepKneeLatencyGrowth    = 0.20
	defaultSweepKneeMinDeltaMS       = 1.0
)

// SweepRun is one concurrency tier in sweep.json. QPS is an optional
// client-measured throughput; when it is absent the builder derives QPS from
// a vLLM request-success counter when one is available.
type SweepRun struct {
	Label       string  `json:"label,omitempty"`
	Dir         string  `json:"dir"`
	Concurrency int     `json:"concurrency"`
	WarmupS     float64 `json:"warmup_s,omitempty"`
	DurationS   float64 `json:"duration_s,omitempty"`
	WorkloadID  string  `json:"workload_id,omitempty"`
	QPS         float64 `json:"qps,omitempty"`
}

// SweepManifest mirrors the vLLM sweep index documented in
// docs/MULTI-AGENT-PLAN-VLLM-VIEWS.md. Each run points at a directory that
// contains metrics.jsonl; this type is read-only and never writes evidence.
type SweepManifest struct {
	WorkloadID string     `json:"workload_id"`
	Engine     string     `json:"engine,omitempty"`
	Model      string     `json:"model,omitempty"`
	Runs       []SweepRun `json:"runs"`
}

// SweepOptions controls load-latency knee detection. A tier is a knee
// candidate when its p95 latency grows by at least KneeLatencyGrowth over the
// previous tier and by at least KneeMinDeltaMS. The minimum concurrency count
// is clamped to the contract-required four levels.
type SweepOptions struct {
	MinConcurrencyLevels int
	KneeLatencyGrowth    float64
	KneeMinDeltaMS       float64
	MinMetricSamples     int
}

func (o SweepOptions) normalized() SweepOptions {
	if o.MinConcurrencyLevels < defaultSweepMinConcurrencyLevels {
		o.MinConcurrencyLevels = defaultSweepMinConcurrencyLevels
	}
	if o.KneeLatencyGrowth <= 0 {
		o.KneeLatencyGrowth = defaultSweepKneeLatencyGrowth
	}
	if o.KneeMinDeltaMS <= 0 {
		o.KneeMinDeltaMS = defaultSweepKneeMinDeltaMS
	}
	if o.MinMetricSamples <= 0 {
		o.MinMetricSamples = 1
	}
	return o
}

// BuildLoadLatency reads a sweep manifest and derives the load-latency curve
// from per-tier metric series. The load function receives the resolved run
// directory and must be read-only. Missing or malformed evidence is reported
// through Status (available=false with a reason) so a report can render an
// explicit unavailable panel; an error is reserved for invalid builder usage.
func BuildLoadLatency(sweepPath string, load func(runDir string) ([]MetricSeries, error)) (LoadLatencyData, error) {
	return BuildLoadLatencyWithOptions(sweepPath, load, SweepOptions{})
}

// BuildLoadLatencyWithOptions is BuildLoadLatency with explicit knee and
// sample options. A sweep with fewer than four distinct concurrency levels or
// without a single workload_id is rejected before any curve points are built.
func BuildLoadLatencyWithOptions(sweepPath string, load func(runDir string) ([]MetricSeries, error), opts SweepOptions) (LoadLatencyData, error) {
	opts = opts.normalized()
	if load == nil {
		data := LoadLatencyData{
			Status: ViewStatus{Available: false, Reason: "metric loader is nil", Source: "none"},
			Points: []LoadLatencyPoint{},
		}
		return data, fmt.Errorf("load-latency builder: metric loader is nil")
	}

	manifest, baseDir, err := readSweepManifest(sweepPath)
	if err != nil {
		return LoadLatencyData{
			Status: ViewStatus{Available: false, Reason: err.Error(), Source: "none"},
			Points: []LoadLatencyPoint{},
		}, nil
	}

	data := LoadLatencyData{
		Status:     ViewStatus{Available: false, Source: "sweep.json+metrics"},
		WorkloadID: strings.TrimSpace(manifest.WorkloadID),
		Points:     []LoadLatencyPoint{},
	}
	if data.WorkloadID == "" {
		data.Status.Reason = "sweep.json is missing workload_id"
		return data, nil
	}

	var skipped []string
	validRuns := make([]SweepRun, 0, len(manifest.Runs))
	distinct := make(map[int]struct{})
	for i, run := range manifest.Runs {
		label := sweepRunLabel(run, i)
		if id := strings.TrimSpace(run.WorkloadID); id != "" && id != data.WorkloadID {
			data.Status.Reason = fmt.Sprintf("run %q workload_id %q does not match sweep workload_id %q", label, id, data.WorkloadID)
			return data, nil
		}
		if run.Concurrency <= 0 {
			skipped = append(skipped, fmt.Sprintf("run %q has invalid concurrency %d", label, run.Concurrency))
			continue
		}
		if strings.TrimSpace(run.Dir) == "" {
			skipped = append(skipped, fmt.Sprintf("run %q has no dir", label))
			continue
		}
		validRuns = append(validRuns, run)
		distinct[run.Concurrency] = struct{}{}
	}
	if len(distinct) < opts.MinConcurrencyLevels {
		data.Status.Reason = fmt.Sprintf("sweep %q has %d distinct concurrency levels; need at least %d", data.WorkloadID, len(distinct), opts.MinConcurrencyLevels)
		return data, nil
	}

	grouped := make(map[int][]sweepRunPoint)
	for i, run := range validRuns {
		label := sweepRunLabel(run, i)
		dir := resolveSweepRunDir(baseDir, run.Dir)
		series, loadErr := load(dir)
		if loadErr != nil {
			skipped = append(skipped, fmt.Sprintf("run %q (%s): load metrics: %v", label, dir, loadErr))
			continue
		}
		point, ok := buildSweepRunPoint(run, series, opts)
		if !ok {
			skipped = append(skipped, fmt.Sprintf("run %q (%s): missing ttft/latency metrics", label, dir))
			continue
		}
		grouped[run.Concurrency] = append(grouped[run.Concurrency], point)
	}

	concurrencies := make([]int, 0, len(grouped))
	for concurrency := range grouped {
		concurrencies = append(concurrencies, concurrency)
	}
	sort.Ints(concurrencies)
	points := make([]LoadLatencyPoint, 0, len(concurrencies))
	complete := true
	for _, concurrency := range concurrencies {
		point, pointComplete := aggregateSweepPoints(concurrency, grouped[concurrency])
		points = append(points, point)
		if !pointComplete {
			complete = false
		}
	}
	data.Points = points
	if len(points) < opts.MinConcurrencyLevels {
		data.Status.Reason = fmt.Sprintf("only %d concurrency levels have usable ttft/latency metrics; need at least %d", len(points), opts.MinConcurrencyLevels)
		if len(skipped) > 0 {
			data.Status.Reason += ": " + strings.Join(sweepLimitStrings(skipped, 3), "; ")
		}
		return data, nil
	}

	data.KneeConcurrency = detectSweepKnee(points, opts)
	data.Status.Available = true
	data.Status.Confidence = "exact"
	if !complete || len(skipped) > 0 {
		data.Status.Confidence = "partial"
	}
	if background := sweepBackgroundTiers(points); len(background) > 0 {
		data.Status.Confidence = "partial"
		data.KneeConcurrency = 0
		data.Status.Reason = "检测到背景负载：运行中请求数高于客户端并发（" + strings.Join(background, ", ") + "）；曲线保留为真实观测，但不能计算容量拐点"
	}
	return data, nil
}

func sweepBackgroundTiers(points []LoadLatencyPoint) []string {
	var background []string
	for _, point := range points {
		if point.RunningMax > point.Concurrency {
			background = append(background, fmt.Sprintf("c%d running_max=%d", point.Concurrency, point.RunningMax))
		}
	}
	return background
}

// LoadSweepRunSeries is the standard loader for BuildLoadLatency. It reads a
// run directory's metrics.jsonl through LoadMetrics and never writes it.
func LoadSweepRunSeries(runDir string) ([]MetricSeries, error) {
	startWallNS, endWallNS := loadSweepRunWindow(runDir)
	data, err := LoadMetrics(filepath.Join(runDir, "metrics.jsonl"), MetricsOptions{
		WindowStartWallNS: startWallNS,
		WindowEndWallNS:   endWallNS,
	})
	if err != nil {
		return nil, err
	}
	if !data.Available {
		reason := data.Reason
		if reason == "" {
			reason = "no usable metric series"
		}
		return nil, fmt.Errorf("metrics unavailable: %s", reason)
	}
	return data.Series, nil
}

// loadSweepRunWindow uses the load client's own wall-clock start/end to keep
// the tier metrics aligned with the requests that tier actually issued. The
// histogram loader still reads the last pre-window cumulative snapshot as a
// baseline, so the first in-window delta is not discarded.
func loadSweepRunWindow(runDir string) (startNS, endNS int64) {
	raw, err := os.ReadFile(filepath.Join(runDir, "load.json"))
	if err != nil {
		return 0, 0
	}
	var load struct {
		StartedAt  string `json:"started_at"`
		FinishedAt string `json:"finished_at"`
	}
	if err := json.Unmarshal(raw, &load); err != nil {
		return 0, 0
	}
	if started, err := time.Parse(time.RFC3339Nano, load.StartedAt); err == nil {
		startNS = started.UnixNano()
	}
	if finished, err := time.Parse(time.RFC3339Nano, load.FinishedAt); err == nil {
		// Allow one final scrape after the client has joined all workers.
		endNS = finished.Add(2 * time.Second).UnixNano()
	}
	return startNS, endNS
}

type sweepRunPoint struct {
	point      LoadLatencyPoint
	qps        bool
	ttftP50    bool
	ttftP95    bool
	latencyP50 bool
	latencyP95 bool
	latencyP99 bool
	running    bool
	waiting    bool
	kvCache    bool
	aicore     bool
}

func buildSweepRunPoint(run SweepRun, series []MetricSeries, opts SweepOptions) (sweepRunPoint, bool) {
	ttft := sweepSummarizeMetricFamily(series, "ttft")
	latency := sweepSummarizeMetricFamily(series, "latency")
	if ttft.Samples < opts.MinMetricSamples || latency.Samples < opts.MinMetricSamples {
		return sweepRunPoint{}, false
	}
	if !ttft.P95Present || !latency.P95Present {
		return sweepRunPoint{}, false
	}

	running := sweepSummarizeMetricFamily(series, "running")
	waiting := sweepSummarizeMetricFamily(series, "waiting")
	kvCache := sweepSummarizeMetricFamily(series, "kv_cache")
	aicore := sweepSummarizeMetricFamily(series, "aicore")
	qps := run.QPS
	qpsPresent := qps > 0
	if !qpsPresent {
		qps = sweepCounterQPS(series)
		qpsPresent = qps > 0
	}

	return sweepRunPoint{
		point: LoadLatencyPoint{
			Concurrency:   run.Concurrency,
			QPS:           qps,
			TTFTP50MS:     ttft.P50,
			TTFTP95MS:     ttft.P95,
			LatencyP50MS:  latency.P50,
			LatencyP95MS:  latency.P95,
			LatencyP99MS:  latency.P99,
			RunningMax:    int(math.Round(running.Max)),
			WaitingMax:    int(math.Round(waiting.Max)),
			KVCacheMax:    kvCache.Max,
			NPUAICoreMean: aicore.Mean,
		},
		qps:        qpsPresent,
		ttftP50:    ttft.P50Present,
		ttftP95:    ttft.P95Present,
		latencyP50: latency.P50Present,
		latencyP95: latency.P95Present,
		latencyP99: latency.P99Present,
		running:    running.Samples > 0,
		waiting:    waiting.Samples > 0,
		kvCache:    kvCache.Samples > 0,
		aicore:     aicore.Samples > 0,
	}, true
}

func aggregateSweepPoints(concurrency int, runs []sweepRunPoint) (LoadLatencyPoint, bool) {
	point := LoadLatencyPoint{Concurrency: concurrency}
	var qpsSum float64
	var qpsCount int
	var ttft50Sum, ttft95Sum float64
	var ttft50Count, ttft95Count int
	var latency50Sum, latency95Sum, latency99Sum float64
	var latency50Count, latency95Count, latency99Count int
	var aicoreSum float64
	var aicoreCount int
	runningSeen, waitingSeen, kvSeen, aicoreSeen := false, false, false, false

	for _, run := range runs {
		if run.qps {
			qpsSum += run.point.QPS
			qpsCount++
		}
		if run.ttftP50 {
			ttft50Sum += run.point.TTFTP50MS
			ttft50Count++
		}
		if run.ttftP95 {
			ttft95Sum += run.point.TTFTP95MS
			ttft95Count++
		}
		if run.latencyP50 {
			latency50Sum += run.point.LatencyP50MS
			latency50Count++
		}
		if run.latencyP95 {
			latency95Sum += run.point.LatencyP95MS
			latency95Count++
		}
		if run.latencyP99 {
			latency99Sum += run.point.LatencyP99MS
			latency99Count++
		}
		if run.running {
			if !runningSeen || run.point.RunningMax > point.RunningMax {
				point.RunningMax = run.point.RunningMax
			}
			runningSeen = true
		}
		if run.waiting {
			if !waitingSeen || run.point.WaitingMax > point.WaitingMax {
				point.WaitingMax = run.point.WaitingMax
			}
			waitingSeen = true
		}
		if run.kvCache {
			if !kvSeen || run.point.KVCacheMax > point.KVCacheMax {
				point.KVCacheMax = run.point.KVCacheMax
			}
			kvSeen = true
		}
		if run.aicore {
			aicoreSum += run.point.NPUAICoreMean
			aicoreCount++
			aicoreSeen = true
		}
	}
	if qpsCount > 0 {
		point.QPS = qpsSum / float64(qpsCount)
	}
	if ttft50Count > 0 {
		point.TTFTP50MS = ttft50Sum / float64(ttft50Count)
	}
	if ttft95Count > 0 {
		point.TTFTP95MS = ttft95Sum / float64(ttft95Count)
	}
	if latency50Count > 0 {
		point.LatencyP50MS = latency50Sum / float64(latency50Count)
	}
	if latency95Count > 0 {
		point.LatencyP95MS = latency95Sum / float64(latency95Count)
	}
	if latency99Count > 0 {
		point.LatencyP99MS = latency99Sum / float64(latency99Count)
	}
	if aicoreCount > 0 {
		point.NPUAICoreMean = aicoreSum / float64(aicoreCount)
	}
	complete := qpsCount == len(runs) &&
		ttft50Count == len(runs) && ttft95Count == len(runs) &&
		latency50Count == len(runs) && latency95Count == len(runs) && latency99Count == len(runs) &&
		runningSeen && waitingSeen && kvSeen && aicoreSeen
	for _, run := range runs {
		if !run.running || !run.waiting || !run.kvCache || !run.aicore {
			complete = false
			break
		}
	}
	return point, complete
}

func detectSweepKnee(points []LoadLatencyPoint, opts SweepOptions) int {
	if len(points) < 2 {
		return 0
	}
	for i := 1; i < len(points); i++ {
		previous := sweepKneeLatency(points[i-1])
		current := sweepKneeLatency(points[i])
		if previous <= 0 || current <= 0 {
			continue
		}
		growth := (current - previous) / previous
		if growth >= opts.KneeLatencyGrowth && current-previous >= opts.KneeMinDeltaMS {
			return points[i].Concurrency
		}
	}
	return 0
}

func sweepKneeLatency(point LoadLatencyPoint) float64 {
	if point.LatencyP95MS > 0 {
		return point.LatencyP95MS
	}
	return point.LatencyP50MS
}

func readSweepManifest(path string) (SweepManifest, string, error) {
	cleaned := strings.TrimSpace(path)
	if cleaned == "" {
		return SweepManifest{}, "", fmt.Errorf("sweep path is empty")
	}
	if info, err := os.Stat(cleaned); err == nil && info.IsDir() {
		cleaned = filepath.Join(cleaned, "sweep.json")
	}
	raw, err := os.ReadFile(cleaned)
	if err != nil {
		if os.IsNotExist(err) {
			return SweepManifest{}, "", fmt.Errorf("sweep manifest not found: %s", cleaned)
		}
		return SweepManifest{}, "", fmt.Errorf("read sweep manifest: %w", err)
	}
	var manifest SweepManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return SweepManifest{}, "", fmt.Errorf("parse sweep manifest %s: %w", cleaned, err)
	}
	return manifest, filepath.Dir(cleaned), nil
}

func resolveSweepRunDir(baseDir, dir string) string {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return ""
	}
	if filepath.IsAbs(dir) {
		return filepath.Clean(dir)
	}
	return filepath.Clean(filepath.Join(baseDir, dir))
}

func sweepRunLabel(run SweepRun, index int) string {
	if label := strings.TrimSpace(run.Label); label != "" {
		return label
	}
	if run.Concurrency > 0 {
		return fmt.Sprintf("c%d", run.Concurrency)
	}
	return fmt.Sprintf("run-%d", index)
}

func sweepLimitStrings(values []string, limit int) []string {
	if len(values) <= limit {
		return values
	}
	return values[:limit]
}

type sweepFamilySummary struct {
	P50        float64
	P95        float64
	P99        float64
	P50Present bool
	P95Present bool
	P99Present bool
	Max        float64
	Mean       float64
	Samples    int
}

func sweepSummarizeMetricFamily(series []MetricSeries, family string) sweepFamilySummary {
	var summary sweepFamilySummary
	var allValues []float64
	var distributionValues []float64
	percentileValues := make(map[float64][]float64)
	for _, item := range series {
		if sweepMetricFamily(item.Name) != family {
			continue
		}
		values := sweepSeriesValues(item)
		if len(values) == 0 {
			continue
		}
		allValues = append(allValues, values...)
		if percentile, ok := sweepSeriesPercentile(item); ok {
			percentileValues[percentile] = append(percentileValues[percentile], values...)
		} else {
			distributionValues = append(distributionValues, values...)
		}
	}
	if len(allValues) == 0 {
		return summary
	}
	summary.Samples = len(allValues)
	summary.Mean = sweepMean(allValues)
	summary.Max = allValues[0]
	for _, value := range allValues[1:] {
		if value > summary.Max {
			summary.Max = value
		}
	}
	setPercentile := func(percentile float64, destination *float64, present *bool) {
		if values := percentileValues[percentile]; len(values) > 0 {
			*destination = sweepMean(values)
			*present = true
			return
		}
		if len(distributionValues) == 0 {
			return
		}
		sorted := append([]float64(nil), distributionValues...)
		sort.Float64s(sorted)
		*destination = metricPercentile(sorted, percentile)
		*present = true
	}
	setPercentile(0.50, &summary.P50, &summary.P50Present)
	setPercentile(0.95, &summary.P95, &summary.P95Present)
	setPercentile(0.99, &summary.P99, &summary.P99Present)
	return summary
}

func sweepMetricFamily(name string) string {
	lower := strings.ToLower(strings.TrimSpace(name))
	normalized := strings.NewReplacer(":", "_", ".", "_", "-", "_").Replace(lower)
	// Prometheus *_created gauges expose a Unix creation timestamp, not a
	// latency. Treating them as request observations produces astronomical
	// percentile values after unit conversion.
	if strings.HasSuffix(normalized, "_created") {
		return ""
	}
	switch {
	case strings.Contains(normalized, "time_to_first_token") || strings.Contains(normalized, "ttft"):
		return "ttft"
	case strings.Contains(normalized, "e2e_request_latency") || strings.Contains(normalized, "request_latency") || strings.Contains(normalized, "e2e_latency"):
		return "latency"
	case strings.Contains(normalized, "num_requests_running") || strings.Contains(normalized, "requests_running"):
		return "running"
	case strings.Contains(normalized, "num_requests_waiting") || strings.Contains(normalized, "requests_waiting"):
		return "waiting"
	case strings.Contains(normalized, "kv_cache_usage") || strings.Contains(normalized, "gpu_cache_usage") || strings.Contains(normalized, "kv_cache"):
		return "kv_cache"
	case strings.Contains(normalized, "npu_aicore_utilization") || strings.Contains(normalized, "aicore_utilization") || strings.Contains(normalized, "npu_aicore"):
		return "aicore"
	case strings.Contains(normalized, "request_success") || strings.Contains(normalized, "requests_total") || strings.Contains(normalized, "completed_requests"):
		return "request_counter"
	default:
		return ""
	}
}

func sweepSeriesPercentile(series MetricSeries) (float64, bool) {
	if raw, ok := series.Labels["quantile"]; ok {
		if value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64); err == nil {
			if value > 1 {
				value /= 100
			}
			if value > 0 && value < 1 {
				return value, true
			}
		}
	}
	lower := strings.ToLower(strings.TrimSpace(series.Name))
	for _, candidate := range []struct {
		suffix     string
		percentile float64
	}{
		{":p50", 0.50}, {":p95", 0.95}, {":p99", 0.99},
		{"_p50", 0.50}, {"_p95", 0.95}, {"_p99", 0.99},
	} {
		if strings.HasSuffix(lower, candidate.suffix) {
			return candidate.percentile, true
		}
	}
	return 0, false
}

func sweepSeriesValues(series MetricSeries) []float64 {
	scale := sweepMetricScale(series)
	values := make([]float64, 0, len(series.V))
	for _, raw := range series.V {
		if math.IsNaN(raw) || math.IsInf(raw, 0) {
			continue
		}
		values = append(values, raw*scale)
	}
	return values
}

func sweepMetricScale(series MetricSeries) float64 {
	unit := strings.ToLower(strings.TrimSpace(series.Unit))
	switch unit {
	case "seconds", "second", "s":
		return 1000
	case "milliseconds", "millisecond", "ms":
		return 1
	case "microseconds", "microsecond", "us", "µs":
		return 0.001
	case "nanoseconds", "nanosecond", "ns":
		return 1e-6
	}
	name := strings.ToLower(series.Name)
	for _, suffix := range []string{"_bucket", "_count", "_sum", "_total"} {
		name = strings.TrimSuffix(name, suffix)
	}
	switch {
	case strings.HasSuffix(name, "_seconds") || strings.HasSuffix(name, "_second"):
		return 1000
	case strings.HasSuffix(name, "_milliseconds") || strings.HasSuffix(name, "_millisecond") || strings.HasSuffix(name, "_ms"):
		return 1
	case strings.HasSuffix(name, "_microseconds") || strings.HasSuffix(name, "_microsecond") || strings.HasSuffix(name, "_us"):
		return 0.001
	case strings.HasSuffix(name, "_nanoseconds") || strings.HasSuffix(name, "_nanosecond") || strings.HasSuffix(name, "_ns"):
		return 1e-6
	default:
		return 1
	}
}

func sweepCounterQPS(series []MetricSeries) float64 {
	var total float64
	for _, item := range series {
		if sweepMetricFamily(item.Name) != "request_counter" || !strings.EqualFold(strings.TrimSpace(item.Kind), "counter") {
			continue
		}
		total += sweepCounterRate(item)
	}
	return total
}

func sweepCounterRate(series MetricSeries) float64 {
	if len(series.T) < 2 || len(series.V) < 2 {
		return 0
	}
	count := len(series.T)
	if len(series.V) < count {
		count = len(series.V)
	}
	var positiveDelta float64
	var elapsedSeconds float64
	for i := 1; i < count; i++ {
		deltaSeconds := (series.T[i] - series.T[i-1]) / 1000
		if deltaSeconds <= 0 {
			continue
		}
		elapsedSeconds += deltaSeconds
		if delta := series.V[i] - series.V[i-1]; delta > 0 {
			positiveDelta += delta
		}
	}
	if elapsedSeconds <= 0 {
		return 0
	}
	return positiveDelta / elapsedSeconds
}

func sweepMean(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	var sum float64
	for _, value := range values {
		sum += value
	}
	return sum / float64(len(values))
}
