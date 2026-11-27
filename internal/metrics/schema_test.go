package metrics

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPointValidateGauge(t *testing.T) {
	valid := Point{
		Schema: SchemaV1, MonoNS: 10, WallNS: 20, Source: "test",
		Name: "npu_power_watts", Kind: KindGauge, Value: Float(1.5), Unit: "watts",
		Labels: map[string]string{"device": "NPU-0"},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid gauge: %v", err)
	}
	cases := map[string]Point{
		"missing schema":  {MonoNS: 1, WallNS: 1, Source: "test", Name: "x", Kind: KindGauge, Value: Float(1), Unit: "count"},
		"zero mono":       {Schema: SchemaV1, WallNS: 1, Source: "test", Name: "x", Kind: KindGauge, Value: Float(1), Unit: "count"},
		"zero wall":       {Schema: SchemaV1, MonoNS: 1, Source: "test", Name: "x", Kind: KindGauge, Value: Float(1), Unit: "count"},
		"missing source":  {Schema: SchemaV1, MonoNS: 1, WallNS: 1, Name: "x", Kind: KindGauge, Value: Float(1), Unit: "count"},
		"missing name":    {Schema: SchemaV1, MonoNS: 1, WallNS: 1, Source: "test", Kind: KindGauge, Value: Float(1), Unit: "count"},
		"missing unit":    {Schema: SchemaV1, MonoNS: 1, WallNS: 1, Source: "test", Name: "x", Kind: KindGauge, Value: Float(1)},
		"bad kind":        {Schema: SchemaV1, MonoNS: 1, WallNS: 1, Source: "test", Name: "x", Kind: "histogram2", Value: Float(1), Unit: "count"},
		"missing value":   {Schema: SchemaV1, MonoNS: 1, WallNS: 1, Source: "test", Name: "x", Kind: KindGauge, Unit: "count"},
		"forbidden label": {Schema: SchemaV1, MonoNS: 1, WallNS: 1, Source: "test", Name: "x", Kind: KindGauge, Value: Float(1), Unit: "count", Labels: map[string]string{"request_id": "abc"}},
	}
	for name, point := range cases {
		t.Run(name, func(t *testing.T) {
			if err := point.Validate(); err == nil {
				t.Fatalf("expected validation error")
			}
		})
	}
}

func TestPointValidateHistogram(t *testing.T) {
	valid := Point{
		Schema: SchemaV1, MonoNS: 1, WallNS: 2, Source: "test", Name: "latency",
		Kind: KindHistogram, Unit: "seconds",
		Histogram: &Histogram{Buckets: []float64{0.1, 0.5, 1}, Counts: []int64{1, 5, 9}, Sum: 3.2, Count: 9, Cumulative: true},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid histogram: %v", err)
	}
	bad := []Point{
		{Schema: SchemaV1, MonoNS: 1, WallNS: 2, Source: "test", Name: "latency", Kind: KindHistogram, Unit: "seconds", Histogram: &Histogram{Buckets: []float64{0.1, 0.5}, Counts: []int64{1}, Sum: 1, Count: 1, Cumulative: true}},
		{Schema: SchemaV1, MonoNS: 1, WallNS: 2, Source: "test", Name: "latency", Kind: KindHistogram, Unit: "seconds", Histogram: &Histogram{Buckets: []float64{0.5, 0.1}, Counts: []int64{1, 2}, Sum: 1, Count: 2, Cumulative: true}},
		{Schema: SchemaV1, MonoNS: 1, WallNS: 2, Source: "test", Name: "latency", Kind: KindHistogram, Unit: "seconds", Histogram: &Histogram{Buckets: []float64{0.1, 0.5}, Counts: []int64{5, 4}, Sum: 1, Count: 5, Cumulative: true}},
	}
	for i, point := range bad {
		if err := point.Validate(); err == nil {
			t.Fatalf("case %d: expected histogram validation error", i)
		}
	}
}

func TestPointJSONFlattensHistogram(t *testing.T) {
	point := Point{
		Schema: SchemaV1, MonoNS: 1, WallNS: 2, Source: "prometheus:vllm", Name: "latency",
		Kind: KindHistogram, Unit: "seconds",
		Histogram: &Histogram{Buckets: []float64{0.1, 0.5}, Counts: []int64{1, 2}, Sum: 1.2, Count: 2, Cumulative: true},
	}
	data, err := json.Marshal(point)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{`"schema":"cann.metrics.v1"`, `"buckets":[0.1,0.5]`, `"counts":[1,2]`, `"sum":1.2`, `"count":2`, `"cumulative":true`} {
		if !strings.Contains(text, want) {
			t.Fatalf("json %s missing %s", text, want)
		}
	}
	var decoded Point
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if err := decoded.Validate(); err != nil {
		t.Fatalf("round-trip: %v", err)
	}
}

func TestReadEmpty(t *testing.T) {
	points, err := Read(strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 0 {
		t.Fatalf("points=%d", len(points))
	}
}

func TestSeriesKeyStable(t *testing.T) {
	a := Point{Source: "dcmi", Device: "NPU-0", Name: "npu_power_watts", Kind: KindGauge, Labels: map[string]string{"name": "910B2"}}
	b := Point{Source: "dcmi", Device: "NPU-0", Name: "npu_power_watts", Kind: KindGauge, Labels: map[string]string{"name": "910B2"}}
	if SeriesKey(a) != SeriesKey(b) {
		t.Fatalf("keys differ: %q %q", SeriesKey(a), SeriesKey(b))
	}
}
