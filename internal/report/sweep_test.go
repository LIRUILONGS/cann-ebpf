package report

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cann-ebpf/internal/metrics"
)

// Every fixture in this file is synthetic and exists only to exercise the
// builder contract. No real vLLM evidence is read or written.

func writeSyntheticSweep(t *testing.T, manifest SweepManifest) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sweep.json")
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func syntheticSeries(name, unit, kind string, values ...float64) MetricSeries {
	timestamps := make([]float64, len(values))
	for i := range values {
		timestamps[i] = float64(i) * 1000
	}
	return MetricSeries{
		ID:             name + "/" + kind,
		Name:           name,
		Kind:           kind,
		Unit:           unit,
		T:              timestamps,
		V:              append([]float64(nil), values...),
		Count:          len(values),
		OriginalPoints: len(values),
	}
}

func syntheticPercentileSeries(name, unit string, percentile int, values ...float64) MetricSeries {
	return syntheticSeries(fmt.Sprintf("%s:p%d", name, percentile), unit, "histogram", values...)
}

func syntheticTierMetrics(ttftP95Seconds, latencyP95Seconds float64) []MetricSeries {
	return []MetricSeries{
		syntheticPercentileSeries("vllm:time_to_first_token_seconds", "seconds", 50, ttftP95Seconds*0.80),
		syntheticPercentileSeries("vllm:time_to_first_token_seconds", "seconds", 95, ttftP95Seconds),
		syntheticPercentileSeries("vllm:e2e_request_latency_seconds", "seconds", 50, latencyP95Seconds*0.60),
		syntheticPercentileSeries("vllm:e2e_request_latency_seconds", "seconds", 95, latencyP95Seconds),
		syntheticPercentileSeries("vllm:e2e_request_latency_seconds", "seconds", 99, latencyP95Seconds*1.20),
		syntheticSeries("vllm:num_requests_running", "count", "gauge", 0, 1),
		syntheticSeries("vllm:num_requests_waiting", "count", "gauge", 0, 1),
		syntheticSeries("vllm:kv_cache_usage_perc", "ratio", "gauge", 0.20, 0.30),
		syntheticSeries("npu_aicore_utilization_ratio", "ratio", "gauge", 0.40, 0.60),
		// Prometheus creation timestamps are not request latency observations.
		syntheticSeries("vllm:time_to_first_token_seconds_created", "seconds", "gauge", 1_700_000_000, 1_700_000_001),
		syntheticSeries("vllm:e2e_request_latency_seconds_created", "seconds", "gauge", 1_700_000_000, 1_700_000_001),
	}
}

func syntheticLoader(fixtures map[string][]MetricSeries) func(string) ([]MetricSeries, error) {
	return func(runDir string) ([]MetricSeries, error) {
		series, ok := fixtures[runDir]
		if !ok {
			return nil, fmt.Errorf("unknown synthetic run dir %s", runDir)
		}
		return series, nil
	}
}

func TestBuildLoadLatencySyntheticValidFourPlusTiers(t *testing.T) {
	root := t.TempDir()
	runs := []SweepRun{
		{Label: "c1", Dir: filepath.Join(root, "c1"), Concurrency: 1, QPS: 0},
		{Label: "c2", Dir: filepath.Join(root, "c2"), Concurrency: 2, QPS: 15},
		{Label: "c4", Dir: filepath.Join(root, "c4"), Concurrency: 4, QPS: 25},
		{Label: "c8", Dir: filepath.Join(root, "c8"), Concurrency: 8, QPS: 30},
		{Label: "c16", Dir: filepath.Join(root, "c16"), Concurrency: 16, QPS: 32},
	}
	manifestPath := writeSyntheticSweep(t, SweepManifest{
		WorkloadID: "synthetic-vllm-chat-128in-128out-v1",
		Engine:     "vllm-ascend",
		Model:      "synthetic-model",
		Runs:       runs,
	})

	fixtures := map[string][]MetricSeries{
		runs[0].Dir: append(syntheticTierMetrics(0.10, 0.50),
			syntheticSeries("vllm:request_success_total", "count", "counter", 0, 5, 10, 15)),
		runs[1].Dir: syntheticTierMetrics(0.11, 0.55),
		runs[2].Dir: syntheticTierMetrics(0.16, 0.90),
		runs[3].Dir: syntheticTierMetrics(0.30, 1.80),
		runs[4].Dir: syntheticTierMetrics(0.60, 4.00),
	}
	data, err := BuildLoadLatency(manifestPath, syntheticLoader(fixtures))
	if err != nil {
		t.Fatal(err)
	}
	if !data.Status.Available {
		t.Fatalf("valid synthetic sweep is unavailable: %+v", data.Status)
	}
	if data.WorkloadID != "synthetic-vllm-chat-128in-128out-v1" {
		t.Fatalf("workload_id = %q", data.WorkloadID)
	}
	if len(data.Points) != 5 {
		t.Fatalf("points = %d, want 5", len(data.Points))
	}
	concurrencies := []int{1, 2, 4, 8, 16}
	for i, want := range concurrencies {
		if data.Points[i].Concurrency != want {
			t.Fatalf("point %d concurrency = %d, want %d", i, data.Points[i].Concurrency, want)
		}
	}
	first := data.Points[0]
	if !syntheticAlmostEqual(first.TTFTP50MS, 80) || !syntheticAlmostEqual(first.TTFTP95MS, 100) {
		t.Fatalf("ttft percentiles = %.2f/%.2f, want 80/100 ms", first.TTFTP50MS, first.TTFTP95MS)
	}
	if !syntheticAlmostEqual(first.LatencyP50MS, 300) || !syntheticAlmostEqual(first.LatencyP95MS, 500) || !syntheticAlmostEqual(first.LatencyP99MS, 600) {
		t.Fatalf("latency percentiles = %.2f/%.2f/%.2f", first.LatencyP50MS, first.LatencyP95MS, first.LatencyP99MS)
	}
	if first.RunningMax != 1 || first.WaitingMax != 1 {
		t.Fatalf("running/waiting max = %d/%d", first.RunningMax, first.WaitingMax)
	}
	if !syntheticAlmostEqual(first.KVCacheMax, 0.30) || !syntheticAlmostEqual(first.NPUAICoreMean, 0.50) {
		t.Fatalf("kv/npu = %.2f/%.2f", first.KVCacheMax, first.NPUAICoreMean)
	}
	if !syntheticAlmostEqual(first.QPS, 5.0) {
		t.Fatalf("counter-derived qps = %.2f, want 5", first.QPS)
	}
	if data.KneeConcurrency != 4 {
		t.Fatalf("knee = %d, want 4", data.KneeConcurrency)
	}
	if data.Status.Source != "sweep.json+metrics" || data.Status.Confidence != "exact" {
		t.Fatalf("status = %+v", data.Status)
	}
}

func TestLoadSweepRunSeriesUsesLoadClientWindow(t *testing.T) {
	dir := t.TempDir()
	base := time.Unix(1_800_000_000, 0).UTC()
	loadMeta := fmt.Sprintf(`{"started_at":%q,"finished_at":%q}`,
		base.Add(2*time.Second).Format(time.RFC3339Nano),
		base.Add(4*time.Second).Format(time.RFC3339Nano))
	if err := os.WriteFile(filepath.Join(dir, "load.json"), []byte(loadMeta), 0o600); err != nil {
		t.Fatal(err)
	}
	point := func(second int, value float64) metrics.Point {
		wall := base.Add(time.Duration(second) * time.Second)
		return metrics.Point{
			Schema: metrics.SchemaV1,
			MonoNS: wall.UnixNano(),
			WallNS: wall.UnixNano(),
			Source: "prometheus:vllm",
			Name:   "vllm:num_requests_running",
			Kind:   metrics.KindGauge,
			Value:  metrics.Float(value),
			Unit:   "count",
		}
	}
	writeMetricPoints(t, filepath.Join(dir, "metrics.jsonl"), []metrics.Point{
		point(0, 1), point(2, 2), point(4, 3), point(7, 99),
	})

	series, err := LoadSweepRunSeries(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(series) != 1 || len(series[0].V) != 2 {
		t.Fatalf("windowed series = %+v", series)
	}
	if series[0].V[0] != 2 || series[0].V[1] != 3 {
		t.Fatalf("window values = %v, want [2 3]", series[0].V)
	}
}

func TestSweepBackgroundTiersDetectNoisyRun(t *testing.T) {
	points := []LoadLatencyPoint{{Concurrency: 1, RunningMax: 12}, {Concurrency: 32, RunningMax: 32}}
	background := sweepBackgroundTiers(points)
	if len(background) != 1 || !strings.Contains(background[0], "c1 running_max=12") {
		t.Fatalf("background tiers = %v", background)
	}
}

func TestBuildLoadLatencySyntheticFewerThanFourTiers(t *testing.T) {
	root := t.TempDir()
	runs := []SweepRun{
		{Label: "c1", Dir: filepath.Join(root, "c1"), Concurrency: 1, QPS: 5},
		{Label: "c2", Dir: filepath.Join(root, "c2"), Concurrency: 2, QPS: 10},
		{Label: "c4", Dir: filepath.Join(root, "c4"), Concurrency: 4, QPS: 20},
	}
	manifestPath := writeSyntheticSweep(t, SweepManifest{WorkloadID: "synthetic-three-tier", Runs: runs})
	fixtures := map[string][]MetricSeries{
		runs[0].Dir: syntheticTierMetrics(0.10, 0.50),
		runs[1].Dir: syntheticTierMetrics(0.11, 0.55),
		runs[2].Dir: syntheticTierMetrics(0.16, 0.90),
	}
	data, err := BuildLoadLatency(manifestPath, syntheticLoader(fixtures))
	if err != nil {
		t.Fatal(err)
	}
	if data.Status.Available || data.Points == nil || len(data.Points) != 0 {
		t.Fatalf("three-tier sweep = %+v", data)
	}
	if !strings.Contains(data.Status.Reason, "concurrency levels") {
		t.Fatalf("reason = %q", data.Status.Reason)
	}
}

func TestBuildLoadLatencySyntheticMismatchedWorkloadID(t *testing.T) {
	root := t.TempDir()
	runs := []SweepRun{
		{Label: "c1", Dir: filepath.Join(root, "c1"), Concurrency: 1, QPS: 5},
		{Label: "c2", Dir: filepath.Join(root, "c2"), Concurrency: 2, QPS: 10},
		{Label: "c4", Dir: filepath.Join(root, "c4"), Concurrency: 4, QPS: 20},
		{Label: "c8", Dir: filepath.Join(root, "c8"), Concurrency: 8, QPS: 30, WorkloadID: "synthetic-other-workload"},
	}
	manifestPath := writeSyntheticSweep(t, SweepManifest{WorkloadID: "synthetic-main-workload", Runs: runs})
	fixtures := map[string][]MetricSeries{
		runs[0].Dir: syntheticTierMetrics(0.10, 0.50),
		runs[1].Dir: syntheticTierMetrics(0.11, 0.55),
		runs[2].Dir: syntheticTierMetrics(0.16, 0.90),
		runs[3].Dir: syntheticTierMetrics(0.30, 1.80),
	}
	data, err := BuildLoadLatency(manifestPath, syntheticLoader(fixtures))
	if err != nil {
		t.Fatal(err)
	}
	if data.Status.Available || !strings.Contains(data.Status.Reason, "workload_id") {
		t.Fatalf("mismatched workload status = %+v", data.Status)
	}
}

func TestBuildLoadLatencySyntheticMissingMetrics(t *testing.T) {
	root := t.TempDir()
	runs := []SweepRun{
		{Label: "c1", Dir: filepath.Join(root, "c1"), Concurrency: 1, QPS: 5},
		{Label: "c2", Dir: filepath.Join(root, "c2"), Concurrency: 2, QPS: 10},
		{Label: "c4", Dir: filepath.Join(root, "c4"), Concurrency: 4, QPS: 20},
		{Label: "c8", Dir: filepath.Join(root, "c8"), Concurrency: 8, QPS: 30},
	}
	manifestPath := writeSyntheticSweep(t, SweepManifest{WorkloadID: "synthetic-missing-metrics", Runs: runs})
	fixtures := make(map[string][]MetricSeries, len(runs))
	for _, run := range runs {
		fixtures[run.Dir] = []MetricSeries{
			syntheticSeries("vllm:num_requests_running", "count", "gauge", 1, 2, 3),
		}
	}
	data, err := BuildLoadLatency(manifestPath, syntheticLoader(fixtures))
	if err != nil {
		t.Fatal(err)
	}
	if data.Status.Available || data.Points == nil || len(data.Points) != 0 {
		t.Fatalf("missing metrics sweep = %+v", data)
	}
	if !strings.Contains(data.Status.Reason, "usable ttft/latency metrics") {
		t.Fatalf("reason = %q", data.Status.Reason)
	}
}

func TestBuildLoadLatencySyntheticSkipsOneBrokenTier(t *testing.T) {
	root := t.TempDir()
	runs := []SweepRun{
		{Label: "c1", Dir: filepath.Join(root, "c1"), Concurrency: 1, QPS: 5},
		{Label: "c2", Dir: filepath.Join(root, "c2"), Concurrency: 2, QPS: 10},
		{Label: "c4", Dir: filepath.Join(root, "c4"), Concurrency: 4, QPS: 20},
		{Label: "c8", Dir: filepath.Join(root, "c8"), Concurrency: 8, QPS: 30},
		{Label: "c16", Dir: filepath.Join(root, "c16"), Concurrency: 16, QPS: 32},
	}
	manifestPath := writeSyntheticSweep(t, SweepManifest{WorkloadID: "synthetic-partial", Runs: runs})
	fixtures := map[string][]MetricSeries{
		runs[0].Dir: syntheticTierMetrics(0.10, 0.50),
		runs[1].Dir: syntheticTierMetrics(0.11, 0.55),
		runs[2].Dir: syntheticTierMetrics(0.16, 0.90),
		runs[3].Dir: syntheticTierMetrics(0.30, 1.80),
		runs[4].Dir: {syntheticSeries("vllm:num_requests_running", "count", "gauge", 1, 2)},
	}
	data, err := BuildLoadLatency(manifestPath, syntheticLoader(fixtures))
	if err != nil {
		t.Fatal(err)
	}
	if !data.Status.Available || len(data.Points) != 4 {
		t.Fatalf("partial sweep = available %v points %d", data.Status.Available, len(data.Points))
	}
	if data.Status.Confidence != "partial" {
		t.Fatalf("confidence = %q, want partial", data.Status.Confidence)
	}
}

func TestBuildLoadLatencySyntheticMissingWorkloadID(t *testing.T) {
	root := t.TempDir()
	manifestPath := writeSyntheticSweep(t, SweepManifest{
		Runs: []SweepRun{{Label: "c1", Dir: filepath.Join(root, "c1"), Concurrency: 1}},
	})
	data, err := BuildLoadLatency(manifestPath, func(string) ([]MetricSeries, error) {
		return syntheticTierMetrics(0.10, 0.50), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if data.Status.Available || !strings.Contains(data.Status.Reason, "workload_id") {
		t.Fatalf("missing workload status = %+v", data.Status)
	}
}

func syntheticAlmostEqual(left, right float64) bool {
	return math.Abs(left-right) < 1e-9
}
