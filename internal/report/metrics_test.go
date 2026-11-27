package report

import (
	"os"
	"path/filepath"
	"testing"
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
