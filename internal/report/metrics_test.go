package report

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"

	"cann-ebpf/internal/metrics"
)

func TestLoadMetricsProjectsSeriesAndHistogramP95(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "metrics.jsonl")
	lines := "" +
		`{"schema":"cann.metrics.v1","mono_ns":1000,"wall_ns":1000,"source":"dcmi","device":"NPU-0","name":"npu_aicore_utilization_ratio","kind":"gauge","value":0.4,"unit":"ratio"}` + "\n" +
		`{"schema":"cann.metrics.v1","mono_ns":2000,"wall_ns":2000,"source":"dcmi","device":"NPU-0","name":"npu_aicore_utilization_ratio","kind":"gauge","value":0.6,"unit":"ratio"}` + "\n" +
		`{"schema":"cann.metrics.v1","mono_ns":3000,"wall_ns":3000,"source":"dcmi","device":"NPU-0","name":"npu_aicore_utilization_ratio","kind":"gauge","value":0.5,"unit":"ratio"}` + "\n" +
		`{"schema":"cann.metrics.v1","mono_ns":1000,"wall_ns":1000,"source":"prometheus:vllm","name":"vllm_requests_running","kind":"counter","value":7,"unit":"count"}` + "\n" +
		`{"schema":"cann.metrics.v1","mono_ns":2000,"wall_ns":2000,"source":"prometheus:vllm","name":"vllm_requests_running","kind":"counter","value":9,"unit":"count"}` + "\n" +
		`{"schema":"cann.metrics.v1","mono_ns":1000,"wall_ns":1000,"source":"prometheus:vllm","name":"vllm_e2e_latency_seconds","kind":"histogram","buckets":[0.1,0.5,1],"counts":[5,9,10],"sum":3.2,"count":10,"cumulative":true,"unit":"seconds"}` + "\n"
	if err := os.WriteFile(path, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	meta := `{"schema":"cann.metrics.meta.v1","interval_ms":1000,"sources":["dcmi","prometheus:vllm"],"status":"complete"}`
	if err := os.WriteFile(filepath.Join(dir, "metrics.meta.json"), []byte(meta), 0o600); err != nil {
		t.Fatal(err)
	}

	data, err := LoadMetrics(path, MetricsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !data.Available || data.Samples != 6 {
		t.Fatalf("metrics = %+v", data)
	}
	if data.IntervalMS != 1000 || data.ClockQuality != "mono" {
		t.Fatalf("interval/clock = %v/%s", data.IntervalMS, data.ClockQuality)
	}
	if len(data.Series) != 3 {
		t.Fatalf("series = %+v", data.Series)
	}
	var hist *MetricSeries
	for i := range data.Series {
		if data.Series[i].Kind == "histogram" {
			hist = &data.Series[i]
			break
		}
	}
	if hist == nil || hist.Name != "vllm_e2e_latency_seconds:p95" {
		t.Fatalf("histogram series = %+v", hist)
	}
	if len(hist.V) != 1 || hist.V[0] < 0.7 || hist.V[0] > 0.8 {
		t.Fatalf("p95 = %v", hist.V)
	}
}

func TestLoadMetricsDownsamplesAndEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "metrics.jsonl")
	lines := ""
	for i := 0; i < 20; i++ {
		lines += `{"schema":"cann.metrics.v1","mono_ns":` + itoa(int64(1000+i*1000)) + `,"wall_ns":1000,"source":"proc","name":"proc_rss_bytes","kind":"gauge","value":` + itoa(int64(i)) + `,"unit":"bytes"}` + "\n"
	}
	if err := os.WriteFile(path, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := LoadMetrics(path, MetricsOptions{MaxPoints: 4})
	if err != nil {
		t.Fatal(err)
	}
	if len(data.Series) != 1 || !data.Series[0].Downsampled {
		t.Fatalf("downsample = %+v", data.Series)
	}
	if len(data.Series[0].T) > 4 {
		t.Fatalf("points = %d", len(data.Series[0].T))
	}
	if len(data.Stats) != 1 {
		t.Fatalf("stats = %+v", data.Stats)
	}
	stat := data.Stats[0]
	if stat.Points != 20 || stat.First != 0 || stat.Last != 19 {
		t.Fatalf("stats should use raw points before downsample: %+v", stat)
	}
	if stat.Min != 0 || stat.Max != 19 {
		t.Fatalf("raw min/max = %v/%v", stat.Min, stat.Max)
	}
	assertClose(t, stat.Mean, 9.5)
	if stat.P50 != 9 || stat.P95 != 18 || stat.P99 != 19 {
		t.Fatalf("raw percentiles = p50 %v p95 %v p99 %v", stat.P50, stat.P95, stat.P99)
	}

	empty := filepath.Join(dir, "empty.jsonl")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	data, err = LoadMetrics(empty, MetricsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if data.Available || data.Reason == "" {
		t.Fatalf("empty metrics = %+v", data)
	}
	if data.Stats == nil || len(data.Stats) != 0 {
		t.Fatalf("empty stats must be an empty slice: %+v", data.Stats)
	}
}

func TestLoadMetricsStatsGaugeCounterHistogram(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "metrics.jsonl")
	writeMetricPoints(t, path, []metrics.Point{
		{Schema: metrics.SchemaV1, MonoNS: 1_000_000_000, WallNS: 1_000_000_000, Source: "dcmi", Device: "NPU-0", Name: "npu_temperature_celsius", Kind: metrics.KindGauge, Value: metrics.Float(40), Unit: "celsius"},
		{Schema: metrics.SchemaV1, MonoNS: 2_000_000_000, WallNS: 2_000_000_000, Source: "dcmi", Device: "NPU-0", Name: "npu_temperature_celsius", Kind: metrics.KindGauge, Value: metrics.Float(50), Unit: "celsius"},
		{Schema: metrics.SchemaV1, MonoNS: 4_000_000_000, WallNS: 4_000_000_000, Source: "dcmi", Device: "NPU-0", Name: "npu_temperature_celsius", Kind: metrics.KindGauge, Value: metrics.Float(45), Unit: "celsius"},
		{Schema: metrics.SchemaV1, MonoNS: 1_000_000_000, WallNS: 1_000_000_000, Source: "prometheus:vllm", Name: "vllm_requests_total", Kind: metrics.KindCounter, Value: metrics.Float(100), Unit: "count"},
		{Schema: metrics.SchemaV1, MonoNS: 2_000_000_000, WallNS: 2_000_000_000, Source: "prometheus:vllm", Name: "vllm_requests_total", Kind: metrics.KindCounter, Value: metrics.Float(160), Unit: "count"},
		{Schema: metrics.SchemaV1, MonoNS: 3_000_000_000, WallNS: 3_000_000_000, Source: "prometheus:vllm", Name: "vllm_requests_total", Kind: metrics.KindCounter, Value: metrics.Float(20), Unit: "count"},
		{Schema: metrics.SchemaV1, MonoNS: 4_000_000_000, WallNS: 4_000_000_000, Source: "prometheus:vllm", Name: "vllm_requests_total", Kind: metrics.KindCounter, Value: metrics.Float(70), Unit: "count"},
		{Schema: metrics.SchemaV1, MonoNS: 1_000_000_000, WallNS: 1_000_000_000, Source: "prometheus:vllm", Name: "vllm_e2e_latency_seconds", Kind: metrics.KindHistogram, Unit: "seconds", Histogram: &metrics.Histogram{Buckets: []float64{0.1, 0.5, 1}, Counts: []int64{10, 10, 10}, Sum: 2.0, Count: 10, Cumulative: true}},
		{Schema: metrics.SchemaV1, MonoNS: 2_000_000_000, WallNS: 2_000_000_000, Source: "prometheus:vllm", Name: "vllm_e2e_latency_seconds", Kind: metrics.KindHistogram, Unit: "seconds", Histogram: &metrics.Histogram{Buckets: []float64{0.1, 0.5, 1}, Counts: []int64{9, 10, 10}, Sum: 2.0, Count: 10, Cumulative: true}},
		{Schema: metrics.SchemaV1, MonoNS: 3_000_000_000, WallNS: 3_000_000_000, Source: "prometheus:vllm", Name: "vllm_e2e_latency_seconds", Kind: metrics.KindHistogram, Unit: "seconds", Histogram: &metrics.Histogram{Buckets: []float64{0.1, 0.5, 1}, Counts: []int64{5, 9, 10}, Sum: 2.0, Count: 10, Cumulative: true}},
	})

	data, err := LoadMetrics(path, MetricsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(data.Series) != 3 || len(data.Stats) != len(data.Series) {
		t.Fatalf("series/stats = %d/%d (%+v)", len(data.Series), len(data.Stats), data.Stats)
	}
	for i := range data.Series {
		series, stat := data.Series[i], data.Stats[i]
		if stat.ID != series.ID || stat.Name != series.Name || stat.Kind != series.Kind || stat.Source != series.Source || stat.Device != series.Device {
			t.Fatalf("stat %d metadata mismatch: series=%+v stat=%+v", i, series, stat)
		}
	}

	gauge := findMetricStat(data.Stats, "npu_temperature_celsius")
	if gauge == nil {
		t.Fatalf("missing gauge stat: %+v", data.Stats)
	}
	if gauge.Points != 3 || gauge.First != 40 || gauge.Last != 45 {
		t.Fatalf("gauge point span = %+v", gauge)
	}
	if gauge.Min != 40 || gauge.Max != 50 {
		t.Fatalf("gauge min/max = %+v", gauge)
	}
	assertClose(t, gauge.Mean, 45)
	assertClose(t, gauge.P50, 45)
	assertClose(t, gauge.P95, 50)
	assertClose(t, gauge.P99, 50)
	if gauge.RateLast != 0 || gauge.RateMean != 0 {
		t.Fatalf("gauge rates should be zero: %+v", gauge)
	}

	counter := findMetricStat(data.Stats, "vllm_requests_total")
	if counter == nil {
		t.Fatalf("missing counter stat: %+v", data.Stats)
	}
	if counter.Points != 4 || counter.First != 100 || counter.Last != 70 {
		t.Fatalf("counter point span = %+v", counter)
	}
	if counter.Min != 20 || counter.Max != 160 {
		t.Fatalf("counter min/max = %+v", counter)
	}
	assertClose(t, counter.Mean, 87.5)
	assertClose(t, counter.P50, 70)
	assertClose(t, counter.P95, 160)
	assertClose(t, counter.P99, 160)
	assertClose(t, counter.RateLast, 50)
	assertClose(t, counter.RateMean, 110.0/3)

	histogram := findMetricStat(data.Stats, "vllm_e2e_latency_seconds:p95")
	if histogram == nil {
		t.Fatalf("missing histogram stat: %+v", data.Stats)
	}
	if histogram.Kind != "histogram" || histogram.Points != 3 || histogram.First != 0.095 || histogram.Last != 0.75 {
		t.Fatalf("histogram identity/span = %+v", histogram)
	}
	assertClose(t, histogram.Min, 0.095)
	assertClose(t, histogram.Max, 0.75)
	assertClose(t, histogram.Mean, (0.095+0.3+0.75)/3)
	assertClose(t, histogram.P50, 0.3)
	assertClose(t, histogram.P95, 0.75)
	assertClose(t, histogram.P99, 0.75)
	if histogram.RateLast != 0 || histogram.RateMean != 0 {
		t.Fatalf("histogram rates should be zero: %+v", histogram)
	}
}

func TestLoadMetricsStatsEmptyAndSinglePoint(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "invalid.jsonl")
	writeMetricPoints(t, path, []metrics.Point{
		{Schema: metrics.SchemaV1, MonoNS: 1000, WallNS: 1000, Source: "x", Name: "empty_histogram", Kind: metrics.KindHistogram, Unit: "seconds", Histogram: &metrics.Histogram{Buckets: []float64{}, Counts: []int64{}, Sum: 0, Count: 0, Cumulative: true}},
	})
	data, err := LoadMetrics(path, MetricsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if data.Available || data.Stats == nil || len(data.Stats) != 0 {
		t.Fatalf("empty series = %+v", data)
	}

	single := filepath.Join(dir, "single.jsonl")
	writeMetricPoints(t, single, []metrics.Point{
		{Schema: metrics.SchemaV1, MonoNS: 1000, WallNS: 1000, Source: "proc", Name: "proc_threads", Kind: metrics.KindGauge, Value: metrics.Float(12), Unit: "count"},
	})
	data, err = LoadMetrics(single, MetricsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(data.Stats) != 1 {
		t.Fatalf("single stats = %+v", data.Stats)
	}
	stat := data.Stats[0]
	if stat.Points != 1 || stat.First != 12 || stat.Last != 12 {
		t.Fatalf("single point span = %+v", stat)
	}
	if stat.Min != 12 || stat.Max != 12 || stat.Mean != 12 || stat.P50 != 12 || stat.P95 != 12 || stat.P99 != 12 {
		t.Fatalf("single point stat = %+v", stat)
	}
	if stat.RateLast != 0 || stat.RateMean != 0 {
		t.Fatalf("single point rates = %+v", stat)
	}
}

func TestMetricStatJSONFields(t *testing.T) {
	stat := MetricStat{
		ID: "series-1", Name: "npu_temperature_celsius", Kind: "gauge",
		Unit: "celsius", Source: "dcmi", Device: "NPU-0", Points: 2,
		First: 0, Last: 1000, Min: 40, Max: 50, Mean: 45,
		P50: 45, P95: 50, P99: 50,
	}
	raw, err := json.Marshal(stat)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"id", "name", "kind", "unit", "source", "device", "points",
		"first", "last", "min", "max", "mean", "p50", "p95", "p99",
		"rate_last", "rate_mean",
	}
	if len(got) != len(want) {
		t.Fatalf("json fields = %v", got)
	}
	for _, key := range want {
		if _, ok := got[key]; !ok {
			t.Fatalf("missing json field %q in %s", key, raw)
		}
	}
}

func TestCounterRatesHandleReset(t *testing.T) {
	tests := []struct {
		name     string
		points   []metricPoint
		wantLast float64
		wantMean float64
	}{
		{
			name: "reset in middle",
			points: []metricPoint{
				{T: 0, V: 100},
				{T: 1000, V: 160},
				{T: 2000, V: 20},
				{T: 3000, V: 70},
			},
			wantLast: 50,
			wantMean: 110.0 / 3,
		},
		{
			name: "reset on final segment",
			points: []metricPoint{
				{T: 0, V: 100},
				{T: 1000, V: 160},
				{T: 2000, V: 20},
			},
			wantLast: 0,
			wantMean: 30,
		},
		{
			name: "flat final segment",
			points: []metricPoint{
				{T: 0, V: 100},
				{T: 1000, V: 160},
				{T: 2000, V: 160},
			},
			wantLast: 0,
			wantMean: 30,
		},
		{
			name:   "single point",
			points: []metricPoint{{T: 0, V: 100}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			last, mean := counterRates(tt.points)
			if last < 0 || mean < 0 {
				t.Fatalf("rates must not be negative: last=%v mean=%v", last, mean)
			}
			assertClose(t, last, tt.wantLast)
			assertClose(t, mean, tt.wantMean)
		})
	}
}

func writeMetricPoints(t *testing.T, path string, points []metrics.Point) {
	t.Helper()
	var buf []byte
	for _, point := range points {
		raw, err := json.Marshal(point)
		if err != nil {
			t.Fatal(err)
		}
		buf = append(buf, raw...)
		buf = append(buf, byte(10))
	}
	if err := os.WriteFile(path, buf, 0o600); err != nil {
		t.Fatal(err)
	}
}

func findMetricStat(stats []MetricStat, name string) *MetricStat {
	for i := range stats {
		if stats[i].Name == name {
			return &stats[i]
		}
	}
	return nil
}

func TestMetricPriorityKeepsOrinferCriticalSeries(t *testing.T) {
	generic := metricPriority("orinfer_admission_seconds_total")
	for _, name := range []string{
		"orinfer_requests_running",
		"orinfer_requests_waiting",
		"orinfer_request_time_to_first_token_seconds:p95",
		"orinfer_request_latency_seconds:p95",
		"orinfer_generated_tokens_total",
	} {
		if metricPriority(name) >= generic {
			t.Fatalf("metricPriority(%q) = %d, want < %d", name, metricPriority(name), generic)
		}
	}
}

func TestLoadMetricsHistogramUsesWindowDelta(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "metrics.jsonl")
	writeMetricPoints(t, path, []metrics.Point{
		{Schema: metrics.SchemaV1, MonoNS: 1_000_000_000, WallNS: 1_000_000_000, Source: "prometheus:vllm", Name: "vllm_e2e_latency_seconds", Kind: metrics.KindHistogram, Unit: "seconds", Histogram: &metrics.Histogram{Buckets: []float64{0.1, 0.5, 1}, Counts: []int64{10, 10, 10}, Sum: 0.75, Count: 10, Cumulative: true}},
		{Schema: metrics.SchemaV1, MonoNS: 2_000_000_000, WallNS: 2_000_000_000, Source: "prometheus:vllm", Name: "vllm_e2e_latency_seconds", Kind: metrics.KindHistogram, Unit: "seconds", Histogram: &metrics.Histogram{Buckets: []float64{0.1, 0.5, 1}, Counts: []int64{10, 20, 20}, Sum: 3.75, Count: 20, Cumulative: true}},
	})

	data, err := LoadMetrics(path, MetricsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var hist *MetricSeries
	for i := range data.Series {
		if data.Series[i].Kind == "histogram" {
			hist = &data.Series[i]
			break
		}
	}
	if hist == nil || len(hist.V) != 2 {
		t.Fatalf("histogram series = %+v", hist)
	}
	if hist.V[0] < 0.09 || hist.V[0] > 0.1 {
		t.Fatalf("first cumulative p95 = %v", hist.V[0])
	}
	if hist.V[1] < 0.45 || hist.V[1] > 0.5 {
		t.Fatalf("window delta p95 = %v", hist.V[1])
	}
}

func assertClose(t *testing.T, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func itoa(value int64) string {
	if value == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	for value > 0 {
		pos--
		buf[pos] = byte('0' + value%10)
		value /= 10
	}
	return string(buf[pos:])
}
