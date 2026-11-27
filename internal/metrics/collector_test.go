package metrics

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeSource struct {
	name     string
	every    time.Duration
	timeout  time.Duration
	points   []Point
	warnings []string
	err      error
}

func (f *fakeSource) Name() string            { return f.name }
func (f *fakeSource) Interval() time.Duration { return f.every }
func (f *fakeSource) Timeout() time.Duration  { return f.timeout }
func (f *fakeSource) Collect(context.Context) ([]Point, []string, error) {
	return f.points, f.warnings, f.err
}

func fixedNow() NowFunc {
	tick := int64(1000)
	return func() (int64, int64) {
		tick += int64(time.Millisecond)
		return tick, 1_700_000_000_000_000_000 + tick
	}
}

func TestCollectorWritesMetricsAndMeta(t *testing.T) {
	dir := t.TempDir()
	source := &fakeSource{
		name:    "test",
		every:   5 * time.Millisecond,
		timeout: time.Second,
		points: []Point{
			Gauge("test_gauge", "count", 7, map[string]string{"kind": "unit"}),
			Counter("test_counter", "count", 9, nil),
		},
	}
	result, err := Run(context.Background(), Options{
		OutDir: dir, Interval: 5 * time.Millisecond, Duration: 25 * time.Millisecond,
		Sources: []Source{source}, Now: fixedNow(), ToolVersion: "test",
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Points < 2 {
		t.Fatalf("points=%d, want >=2", result.Points)
	}
	points, err := ReadFile(result.MetricsPath)
	if err != nil {
		t.Fatalf("read metrics: %v", err)
	}
	if len(points) != result.Points {
		t.Fatalf("read points=%d result=%d", len(points), result.Points)
	}
	for _, point := range points {
		if point.MonoNS <= 0 || point.WallNS <= 0 {
			t.Fatalf("unstamped point: %+v", point)
		}
		if point.Source != "test" || point.Schema != SchemaV1 {
			t.Fatalf("identity: %+v", point)
		}
	}
	data, err := os.ReadFile(result.MetaPath)
	if err != nil {
		t.Fatal(err)
	}
	var meta Meta
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatal(err)
	}
	if meta.Schema != MetaSchemaV1 || meta.Points != result.Points || meta.Status != "completed" {
		t.Fatalf("meta=%+v", meta)
	}
	bytes, sum, err := digestFile(result.MetricsPath)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Artifacts.MetricsJSONL.Bytes != bytes || meta.Artifacts.MetricsJSONL.SHA256 != sum {
		t.Fatalf("artifact=%+v, file=%d/%s", meta.Artifacts.MetricsJSONL, bytes, sum)
	}
	if meta.Clock.MonoStartNS <= 0 || meta.Clock.WallStartNS <= 0 {
		t.Fatalf("clock=%+v", meta.Clock)
	}
}

func TestCollectorPartialFailureKeepsData(t *testing.T) {
	dir := t.TempDir()
	good := &fakeSource{name: "good", every: 5 * time.Millisecond, points: []Point{Gauge("ok", "count", 1, nil)}}
	bad := &fakeSource{name: "bad", every: 5 * time.Millisecond, err: errors.New("source down")}
	result, err := Run(context.Background(), Options{
		OutDir: dir, Interval: 5 * time.Millisecond, Duration: 15 * time.Millisecond,
		Sources: []Source{good, bad}, Now: fixedNow(),
	})
	if err != nil {
		t.Fatalf("partial failure should not fail run: %v", err)
	}
	if result.Points == 0 {
		t.Fatal("expected good source points")
	}
	if !containsWarning(result.Warnings, "source bad") {
		t.Fatalf("warnings=%v", result.Warnings)
	}
}

func TestCollectorNoPoints(t *testing.T) {
	dir := t.TempDir()
	source := &fakeSource{name: "empty", every: 5 * time.Millisecond}
	result, err := Run(context.Background(), Options{
		OutDir: dir, Interval: 5 * time.Millisecond, Duration: 12 * time.Millisecond,
		Sources: []Source{source}, Now: fixedNow(),
	})
	if !errors.Is(err, ErrNoPoints) {
		t.Fatalf("err=%v, want ErrNoPoints", err)
	}
	if result.Status != "no_points" {
		t.Fatalf("status=%q", result.Status)
	}
	var meta Meta
	data, readErr := os.ReadFile(result.MetaPath)
	if readErr != nil {
		t.Fatalf("meta not written: %v", readErr)
	}
	if json.Unmarshal(data, &meta) != nil || meta.Points != 0 || meta.Status != "no_points" {
		t.Fatalf("meta=%+v", meta)
	}
}

func TestCollectorRefusesOverwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "metrics.jsonl")
	if err := os.WriteFile(path, []byte("existing\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Run(context.Background(), Options{
		OutDir: dir, Sources: []Source{&fakeSource{name: "x", points: []Point{Gauge("x", "count", 1, nil)}}},
	})
	if err == nil || !strings.Contains(err.Error(), "refusing to overwrite") {
		t.Fatalf("err=%v", err)
	}
}

func containsWarning(warnings []string, fragment string) bool {
	for _, warning := range warnings {
		if strings.Contains(warning, fragment) {
			return true
		}
	}
	return false
}
