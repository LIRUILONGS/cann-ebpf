package report

import (
	"math"
	"strings"
	"testing"
)

func changePointTestSeries(name string, intervalMS float64, values []float64) MetricSeries {
	times := make([]float64, len(values))
	for i := range values {
		times[i] = float64(i) * intervalMS
	}
	return MetricSeries{
		ID:     name,
		Name:   name,
		Kind:   "gauge",
		Source: "synthetic",
		T:      times,
		V:      append([]float64(nil), values...),
		Count:  len(values),
	}
}

func changePointTestNoise(index int) float64 {
	return 0.7*math.Sin(float64(index)*0.73) + 0.35*math.Sin(float64(index)*1.91)
}

func TestBuildChangePointEmpty(t *testing.T) {
	data := BuildChangePoint(nil, nil, ChangePointOptions{})
	if data.Status.Available {
		t.Fatal("empty input must be unavailable")
	}
	if data.Status.Reason == "" || data.Status.Source != "none" || data.Status.Confidence != "" {
		t.Fatalf("unexpected unavailable status: %+v", data.Status)
	}
	if data.Series == nil || data.Points == nil || data.Events == nil {
		t.Fatal("unavailable result must use empty arrays, not nil")
	}
	if len(data.Series) != 0 || len(data.Points) != 0 || len(data.Events) != 0 {
		t.Fatal("empty input must not fabricate data")
	}
	if data.MinSamples != DefaultChangePointMinSamples {
		t.Fatalf("min_samples = %d, want %d", data.MinSamples, DefaultChangePointMinSamples)
	}
	if !strings.Contains(data.Method, "pelt-like") || !strings.Contains(data.Method, "window=30") ||
		!strings.Contains(data.Method, "threshold=5") {
		t.Fatalf("method does not record parameters: %q", data.Method)
	}
}

func TestBuildChangePointShortInputs(t *testing.T) {
	t.Run("too few valid points", func(t *testing.T) {
		values := make([]float64, 20)
		for i := range values {
			values[i] = 100
		}
		data := BuildChangePoint([]MetricSeries{
			changePointTestSeries("requests", 20_000, values),
		}, nil, ChangePointOptions{})
		if data.Status.Available {
			t.Fatal("short series must be unavailable")
		}
		if !strings.Contains(data.Status.Reason, "insufficient valid points") {
			t.Fatalf("reason = %q", data.Status.Reason)
		}
	})

	t.Run("run under five minutes", func(t *testing.T) {
		values := make([]float64, 90)
		for i := range values {
			values[i] = 100
		}
		data := BuildChangePoint([]MetricSeries{
			changePointTestSeries("requests", 2_000, values),
		}, nil, ChangePointOptions{})
		if data.Status.Available {
			t.Fatal("run under five minutes must be unavailable")
		}
		if !strings.Contains(data.Status.Reason, "run too short") {
			t.Fatalf("reason = %q", data.Status.Reason)
		}
	})
}

func TestBuildChangePointStableSeries(t *testing.T) {
	values := make([]float64, 180)
	for i := range values {
		values[i] = 100 + changePointTestNoise(i)
	}
	data := BuildChangePoint([]MetricSeries{
		changePointTestSeries("vllm:num_requests_running", 10_000, values),
	}, nil, ChangePointOptions{})
	if !data.Status.Available {
		t.Fatalf("stable long series should be available: %+v", data.Status)
	}
	if data.Status.Source != "metrics" || data.Status.Confidence != "inferred" {
		t.Fatalf("unexpected status: %+v", data.Status)
	}
	if len(data.Points) != 0 {
		t.Fatalf("stable series produced %d fabricated change points: %+v", len(data.Points), data.Points)
	}
	if len(data.Series) != 1 || len(data.Series[0].Values) != len(values) {
		t.Fatalf("series projection = %+v", data.Series)
	}
}

func TestBuildChangePointSingleStep(t *testing.T) {
	values := make([]float64, 150)
	for i := range values {
		values[i] = 100 + changePointTestNoise(i)*0.25
		if i >= 75 {
			values[i] += 40
		}
	}
	data := BuildChangePoint([]MetricSeries{
		changePointTestSeries("vllm:num_requests_running", 10_000, values),
	}, nil, ChangePointOptions{})
	if !data.Status.Available {
		t.Fatalf("step series should be available: %+v", data.Status)
	}
	if len(data.Points) != 1 {
		t.Fatalf("change points = %d, want 1: %+v", len(data.Points), data.Points)
	}
	point := data.Points[0]
	if math.Abs(point.TimeMS-750_000) > 10_000 {
		t.Fatalf("change time = %.0f ms, want near 750000 ms", point.TimeMS)
	}
	if point.Before < 99 || point.Before > 101 || point.After < 139 || point.After > 141 {
		t.Fatalf("before/after = %.2f/%.2f, want about 100/140", point.Before, point.After)
	}
	if point.DeltaPct < 39 || point.DeltaPct > 41 {
		t.Fatalf("delta_pct = %.2f, want about 40", point.DeltaPct)
	}
	if point.Score <= 0 || point.Method != data.Method {
		t.Fatalf("unexpected point metadata: %+v", point)
	}
}

func TestBuildChangePointNoisySeries(t *testing.T) {
	t.Run("stable noise", func(t *testing.T) {
		values := make([]float64, 240)
		for i := range values {
			values[i] = 250 + 8*changePointTestNoise(i)
		}
		data := BuildChangePoint([]MetricSeries{
			changePointTestSeries("npu_aicore", 5_000, values),
		}, nil, ChangePointOptions{})
		if !data.Status.Available {
			t.Fatalf("noisy long series should be available: %+v", data.Status)
		}
		if len(data.Points) != 0 {
			t.Fatalf("stable noisy series produced false positives: %+v", data.Points)
		}
	})

	t.Run("noisy step", func(t *testing.T) {
		values := make([]float64, 240)
		for i := range values {
			values[i] = 250 + 8*changePointTestNoise(i)
			if i >= 140 {
				values[i] += 25
			}
		}
		data := BuildChangePoint([]MetricSeries{
			changePointTestSeries("npu_aicore", 5_000, values),
		}, nil, ChangePointOptions{})
		if !data.Status.Available {
			t.Fatalf("noisy step series should be available: %+v", data.Status)
		}
		if len(data.Points) != 1 {
			t.Fatalf("noisy step produced %d points, want 1: %+v", len(data.Points), data.Points)
		}
		if math.Abs(data.Points[0].TimeMS-700_000) > 10_000 {
			t.Fatalf("change time = %.0f ms, want near 700000 ms", data.Points[0].TimeMS)
		}
		if data.Points[0].After-data.Points[0].Before < 20 {
			t.Fatalf("step magnitude not preserved: %+v", data.Points[0])
		}
	})
}

func TestBuildChangePointEventAnnotationOnly(t *testing.T) {
	values := make([]float64, 180)
	for i := range values {
		values[i] = 100 + changePointTestNoise(i)
	}
	data := BuildChangePoint([]MetricSeries{
		changePointTestSeries("vllm:num_requests_waiting", 10_000, values),
	}, []ChangeEvent{
		{TimeMS: 120_000, Label: "deploy", Source: "events.jsonl"},
		{TimeMS: 60_000, Label: "scale-out", Source: "events.jsonl"},
	}, ChangePointOptions{})
	if !data.Status.Available {
		t.Fatalf("series should be available: %+v", data.Status)
	}
	if len(data.Points) != 0 {
		t.Fatalf("events must not create change points: %+v", data.Points)
	}
	if len(data.Events) != 2 {
		t.Fatalf("events = %d, want 2", len(data.Events))
	}
	if data.Events[0].TimeMS != 60_000 || data.Events[1].TimeMS != 120_000 {
		t.Fatalf("events are not sorted annotations: %+v", data.Events)
	}
	if data.Events[0].Label != "scale-out" || data.Events[1].Label != "deploy" {
		t.Fatalf("event labels changed: %+v", data.Events)
	}
}

func TestBuildChangePointCustomParametersRecorded(t *testing.T) {
	values := make([]float64, 120)
	for i := range values {
		values[i] = 50
	}
	data := BuildChangePoint([]MetricSeries{
		changePointTestSeries("metric", 4_000, values),
	}, nil, ChangePointOptions{
		MinSamples:       40,
		Window:           20,
		Threshold:        4.5,
		MinRunDurationMS: 120_000,
	})
	if !data.Status.Available {
		t.Fatalf("custom parameter series should be available: %+v", data.Status)
	}
	if data.MinSamples != 40 {
		t.Fatalf("min_samples = %d, want 40", data.MinSamples)
	}
	want := "pelt-like(window=20,threshold=4.5,min_samples=40,min_run_ms=120000)"
	if data.Method != want {
		t.Fatalf("method = %q, want %q", data.Method, want)
	}
}

func TestBuildChangePointMultipleSteps(t *testing.T) {
	values := make([]float64, 240)
	for i := range values {
		switch {
		case i < 80:
			values[i] = 100
		case i < 160:
			values[i] = 135
		default:
			values[i] = 95
		}
	}
	data := BuildChangePoint([]MetricSeries{
		changePointTestSeries("kv_cache", 5_000, values),
	}, nil, ChangePointOptions{})
	if !data.Status.Available {
		t.Fatalf("multi-step series should be available: %+v", data.Status)
	}
	if len(data.Points) != 2 {
		t.Fatalf("change points = %d, want 2: %+v", len(data.Points), data.Points)
	}
	if math.Abs(data.Points[0].TimeMS-400_000) > 5_000 || math.Abs(data.Points[1].TimeMS-800_000) > 5_000 {
		t.Fatalf("change times = %.0f, %.0f; want 400000, 800000", data.Points[0].TimeMS, data.Points[1].TimeMS)
	}
}
