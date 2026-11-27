// Package metrics implements the independent, low-overhead metrics collection
// path for CANN performance analysis.
//
// Metrics are deliberately separate from trace evidence: a failure here must
// never alter spans.jsonl, device.jsonl or hardware.jsonl. Every record is
// written as one JSON object per line using the cann.metrics.v1 schema.
package metrics

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strings"
)

const (
	// SchemaV1 is the JSONL point schema.
	SchemaV1 = "cann.metrics.v1"
	// MetaSchemaV1 is the sidecar metadata schema.
	MetaSchemaV1 = "cann.metrics.meta.v1"
)

// Kind is a metric type. Values outside this set are rejected.
type Kind string

const (
	KindGauge     Kind = "gauge"
	KindCounter   Kind = "counter"
	KindHistogram Kind = "histogram"
)

// Histogram carries a cumulative histogram sample. Buckets are finite upper
// bounds in increasing order; the implicit +Inf bucket is represented by
// Count, not stored in Buckets. Counts[i] is the cumulative count for
// Buckets[i]. This keeps the JSON representation numeric and stable while the
// total count remains exact.
type Histogram struct {
	Buckets    []float64 `json:"buckets"`
	Counts     []int64   `json:"counts"`
	Sum        float64   `json:"sum"`
	Count      int64     `json:"count"`
	Cumulative bool      `json:"cumulative"`
}

// Point is one long-format metrics sample.
//
// The embedded *Histogram is flattened into the enclosing JSON object, so the
// wire format is exactly the one documented in docs/METRICS-SCHEMA.md and the
// M1/M2 data contract.
type Point struct {
	Schema string            `json:"schema"`
	MonoNS int64             `json:"mono_ns"`
	WallNS int64             `json:"wall_ns"`
	Source string            `json:"source"`
	Device string            `json:"device,omitempty"`
	Name   string            `json:"name"`
	Kind   Kind              `json:"kind"`
	Value  *float64          `json:"value,omitempty"`
	Unit   string            `json:"unit"`
	Labels map[string]string `json:"labels,omitempty"`
	*Histogram
}

// Float returns a pointer to v. It is a convenience for Point constructors.
func Float(v float64) *float64 { return &v }

// Int64 returns a pointer to v.
func Int64(v int64) *int64 { return &v }

// Bool returns a pointer to v.
func Bool(v bool) *bool { return &v }

// Gauge builds a gauge point without timestamps. The collector stamps the
// timestamps immediately before writing.
func Gauge(name, unit string, value float64, labels map[string]string) Point {
	return Point{
		Schema: SchemaV1, Name: name, Kind: KindGauge,
		Value: Float(value), Unit: unit, Labels: cloneLabels(labels),
	}
}

// Counter builds a counter point without timestamps.
func Counter(name, unit string, value float64, labels map[string]string) Point {
	return Point{
		Schema: SchemaV1, Name: name, Kind: KindCounter,
		Value: Float(value), Unit: unit, Labels: cloneLabels(labels),
	}
}

// HistogramPoint builds a histogram point without timestamps.
func HistogramPoint(name, unit string, h Histogram, labels map[string]string) Point {
	return Point{
		Schema: SchemaV1, Name: name, Kind: KindHistogram,
		Unit: unit, Labels: cloneLabels(labels), Histogram: &h,
	}
}

// Validate checks the stable wire contract and low-cardinality label policy.
func (p Point) Validate() error {
	if p.Schema != SchemaV1 {
		return fmt.Errorf("schema must be %q, got %q", SchemaV1, p.Schema)
	}
	if p.MonoNS <= 0 {
		return fmt.Errorf("mono_ns must be > 0")
	}
	if p.WallNS <= 0 {
		return fmt.Errorf("wall_ns must be > 0")
	}
	if strings.TrimSpace(p.Source) == "" {
		return fmt.Errorf("source is required")
	}
	if strings.TrimSpace(p.Name) == "" {
		return fmt.Errorf("name is required")
	}
	if strings.TrimSpace(p.Unit) == "" {
		return fmt.Errorf("unit is required")
	}
	switch p.Kind {
	case KindGauge, KindCounter:
		if p.Value == nil {
			return fmt.Errorf("value is required for kind %s", p.Kind)
		}
		if p.Histogram != nil {
			return fmt.Errorf("histogram fields are not valid for kind %s", p.Kind)
		}
		if math.IsNaN(*p.Value) || math.IsInf(*p.Value, 0) {
			return fmt.Errorf("value must be finite")
		}
	case KindHistogram:
		if p.Histogram == nil {
			return fmt.Errorf("histogram fields are required for kind histogram")
		}
		if p.Value != nil {
			return fmt.Errorf("value must be omitted for kind histogram")
		}
		if len(p.Buckets) != len(p.Counts) {
			return fmt.Errorf("histogram buckets/counts length mismatch: %d != %d", len(p.Buckets), len(p.Counts))
		}
		for i, bound := range p.Buckets {
			if math.IsNaN(bound) || math.IsInf(bound, 0) {
				return fmt.Errorf("histogram bucket %d must be finite", i)
			}
			if i > 0 && !(bound > p.Buckets[i-1]) {
				return fmt.Errorf("histogram buckets must be strictly increasing at index %d", i)
			}
		}
		last := int64(0)
		for i, count := range p.Counts {
			if count < 0 {
				return fmt.Errorf("histogram count %d must be non-negative", i)
			}
			if p.Cumulative && i > 0 && count < last {
				return fmt.Errorf("cumulative histogram counts must be non-decreasing at index %d", i)
			}
			last = count
		}
		if p.Count < 0 {
			return fmt.Errorf("histogram count must be non-negative")
		}
		if p.Cumulative && len(p.Counts) > 0 && p.Count < p.Counts[len(p.Counts)-1] {
			return fmt.Errorf("histogram count %d is smaller than last bucket count %d", p.Count, p.Counts[len(p.Counts)-1])
		}
		if math.IsNaN(p.Sum) || math.IsInf(p.Sum, 0) {
			return fmt.Errorf("histogram sum must be finite")
		}
	default:
		return fmt.Errorf("kind must be gauge, counter or histogram, got %q", p.Kind)
	}
	return validateLabels(p.Labels)
}

// forbiddenLabelKeys are high-cardinality or potentially sensitive dimensions.
// Label values must describe stable topology/configuration, never individual
// requests or user content.
var forbiddenLabelKeys = map[string]struct{}{
	"request": {}, "request_id": {}, "requestid": {},
	"prompt": {}, "prompt_id": {}, "promptid": {},
	"trace": {}, "trace_id": {}, "traceid": {},
	"span": {}, "span_id": {}, "spanid": {},
	"user": {}, "user_id": {}, "userid": {},
	"session": {}, "session_id": {}, "sessionid": {},
	"url": {}, "uri": {}, "query": {}, "path": {},
	"input": {}, "output": {}, "text": {},
}

func validateLabels(labels map[string]string) error {
	if len(labels) > 32 {
		return fmt.Errorf("too many labels: %d (max 32)", len(labels))
	}
	for key, value := range labels {
		key = strings.TrimSpace(key)
		if key == "" {
			return fmt.Errorf("label key must not be empty")
		}
		if _, bad := forbiddenLabelKeys[strings.ToLower(key)]; bad {
			return fmt.Errorf("label %q is high-cardinality or sensitive and is not allowed", key)
		}
		if len(value) > 256 {
			return fmt.Errorf("label %q value is too long (%d bytes)", key, len(value))
		}
	}
	return nil
}

func cloneLabels(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// SeriesKey returns a deterministic identity for one metric series.
func SeriesKey(p Point) string {
	keys := make([]string, 0, len(p.Labels))
	for k := range p.Labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(p.Source)
	b.WriteByte('|')
	b.WriteString(p.Device)
	b.WriteByte('|')
	b.WriteString(string(p.Kind))
	b.WriteByte('|')
	b.WriteString(p.Name)
	for _, k := range keys {
		b.WriteByte('|')
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(p.Labels[k])
	}
	return b.String()
}

// ReadFile reads and validates a metrics.jsonl file. An empty file is valid
// and returns an empty slice.
func ReadFile(path string) ([]Point, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return Read(file)
}

// Read reads and validates cann.metrics.v1 JSONL records from r.
func Read(r io.Reader) ([]Point, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	var points []Point
	line := 0
	for scanner.Scan() {
		line++
		text := strings.TrimSpace(scanner.Text())
		if text == "" {
			continue
		}
		var p Point
		if err := json.Unmarshal([]byte(text), &p); err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		if err := p.Validate(); err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		points = append(points, p)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return points, nil
}

// ErrNoPoints is returned when a completed collection wrote no usable point.
var ErrNoPoints = errors.New("metrics: no points collected")
