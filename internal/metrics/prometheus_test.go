package metrics

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const prometheusFixture = `# HELP vllm:num_requests_running Running requests.
# TYPE vllm:num_requests_running gauge
vllm:num_requests_running{model="qwen3"} 3
# TYPE vllm:e2e_request_latency_seconds histogram
vllm:e2e_request_latency_seconds_bucket{model="qwen3",le="0.1"} 3
vllm:e2e_request_latency_seconds_bucket{model="qwen3",le="0.5"} 8
vllm:e2e_request_latency_seconds_bucket{model="qwen3",le="1"} 10
vllm:e2e_request_latency_seconds_bucket{model="qwen3",le="5"} 12
vllm:e2e_request_latency_seconds_bucket{model="qwen3",le="+Inf"} 12
vllm:e2e_request_latency_seconds_sum{model="qwen3"} 4.2
vllm:e2e_request_latency_seconds_count{model="qwen3"} 12
# TYPE vllm:request_success_total counter
vllm:request_success_total{model="qwen3"} 42
escaped_metric{label="a\"b"} 1
nan_metric NaN
`

func TestParsePrometheusText(t *testing.T) {
	samples, warnings, err := ParsePrometheusText([]byte(prometheusFixture), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) == 0 || !strings.Contains(strings.Join(warnings, "\n"), "non-finite") {
		t.Fatalf("warnings=%v", warnings)
	}
	gauge := findParsedSample(samples, "vllm:num_requests_running")
	if gauge == nil || gauge.Kind != KindGauge || gauge.Value == nil || *gauge.Value != 3 {
		t.Fatalf("gauge=%+v", gauge)
	}
	if gauge.Labels["model"] != "qwen3" {
		t.Fatalf("gauge labels=%v", gauge.Labels)
	}
	histogram := findParsedSample(samples, "vllm:e2e_request_latency_seconds")
	if histogram == nil || histogram.Kind != KindHistogram || histogram.Histogram == nil {
		t.Fatalf("histogram=%+v", histogram)
	}
	if got, want := histogram.Histogram.Buckets, []float64{0.1, 0.5, 1, 5}; !equalFloatSlices(got, want) {
		t.Fatalf("buckets=%v want %v", got, want)
	}
	if got, want := histogram.Histogram.Counts, []int64{3, 8, 10, 12}; !equalIntSlices(got, want) {
		t.Fatalf("counts=%v want %v", got, want)
	}
	if histogram.Histogram.Count != 12 || histogram.Histogram.Sum != 4.2 || !histogram.Histogram.Cumulative {
		t.Fatalf("histogram=%+v", histogram.Histogram)
	}
	if _, ok := histogram.Labels["le"]; ok {
		t.Fatalf("le label leaked: %v", histogram.Labels)
	}
	counter := findParsedSample(samples, "vllm:request_success_total")
	if counter == nil || counter.Kind != KindCounter || counter.Value == nil || *counter.Value != 42 {
		t.Fatalf("counter=%+v", counter)
	}
	escaped := findParsedSample(samples, "escaped_metric")
	if escaped == nil || escaped.Labels["label"] != `a"b` {
		t.Fatalf("escaped=%+v", escaped)
	}
	if findParsedSample(samples, "nan_metric") != nil {
		t.Fatal("NaN metric should be skipped")
	}
}

func TestParsePrometheusTextEmpty(t *testing.T) {
	samples, warnings, err := ParsePrometheusText(nil, nil)
	if err != nil || len(samples) != 0 || len(warnings) != 0 {
		t.Fatalf("samples=%v warnings=%v err=%v", samples, warnings, err)
	}
}

func TestParsePrometheusAllowlist(t *testing.T) {
	include := []*regexp.Regexp{regexp.MustCompile(`^vllm:e2e_`)}
	samples, _, err := ParsePrometheusText([]byte(prometheusFixture), include)
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) != 1 || samples[0].Name != "vllm:e2e_request_latency_seconds" {
		t.Fatalf("samples=%+v", samples)
	}
}

func TestCounterTrackerDetectsReset(t *testing.T) {
	tracker := NewCounterTracker()
	if tracker.Observe("series", 10) {
		t.Fatal("first observation must not be a reset")
	}
	if tracker.Observe("series", 15) {
		t.Fatal("increase must not be a reset")
	}
	if !tracker.Observe("series", 7) {
		t.Fatal("decrease must be a reset")
	}
}

func TestPrometheusSourceCounterResetWarning(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		if calls.Add(1) == 1 {
			_, _ = w.Write([]byte("# TYPE c counter\nc 5\n"))
			return
		}
		_, _ = w.Write([]byte("# TYPE c counter\nc 2\n"))
	}))
	defer server.Close()
	source, err := NewPrometheusSource(PrometheusConfig{Name: "test", URL: server.URL}, time.Second, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if points, warnings, err := source.Collect(context.Background()); err != nil || len(points) != 1 || len(warnings) != 0 {
		t.Fatalf("first collect points=%v warnings=%v err=%v", points, warnings, err)
	}
	points, warnings, err := source.Collect(context.Background())
	if err != nil || len(points) != 1 {
		t.Fatalf("second collect points=%v err=%v", points, err)
	}
	if !strings.Contains(strings.Join(warnings, "\n"), "counter reset") {
		t.Fatalf("warnings=%v", warnings)
	}
}

func findParsedSample(samples []ParsedSample, name string) *ParsedSample {
	for i := range samples {
		if samples[i].Name == name {
			return &samples[i]
		}
	}
	return nil
}

func equalFloatSlices(a, b []float64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalIntSlices(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
