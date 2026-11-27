package probes

import (
	"context"
	"errors"
	"testing"
	"time"

	"cann-ebpf/internal/metrics"
)

type namedMetricSource string

func (s namedMetricSource) Name() string            { return string(s) }
func (s namedMetricSource) Interval() time.Duration { return time.Second }
func (s namedMetricSource) Timeout() time.Duration  { return time.Second }
func (s namedMetricSource) Collect(context.Context) ([]metrics.Point, []string, error) {
	return nil, nil, nil
}

func TestCompactMetricsResult(t *testing.T) {
	run := metrics.Result{
		Status: "completed", Points: 42, MetricsPath: "run/metrics.jsonl",
		MetaPath: "run/metrics.meta.json", Bytes: 1234, SHA256: "abc",
		Warnings: []string{"source warning"},
		Meta: metrics.Meta{
			Sources: []string{"dcmi", "prometheus:vllm"},
			Series:  []metrics.SeriesSummary{{Name: "npu_aicore_utilization_ratio"}},
		},
	}
	got := compactMetricsResult(run, errors.New("partial"), []metrics.Source{namedMetricSource("fallback")})
	if !got.Enabled || got.Status != "completed" || got.Points != 42 || got.Series != 1 {
		t.Fatalf("result=%+v", got)
	}
	if len(got.Sources) != 2 || got.Sources[0] != "dcmi" || got.Sources[1] != "prometheus:vllm" {
		t.Fatalf("sources=%v", got.Sources)
	}
	if got.Error != "partial" || len(got.Warnings) != 1 {
		t.Fatalf("error/warnings=%+v", got)
	}
}

func TestCompactMetricsResultFallsBackToSourceNames(t *testing.T) {
	got := compactMetricsResult(metrics.Result{}, nil, []metrics.Source{
		namedMetricSource("dcmi"), namedMetricSource("dcmi"), namedMetricSource("system"),
	})
	if !got.Enabled || len(got.Sources) != 2 || got.Sources[0] != "dcmi" || got.Sources[1] != "system" {
		t.Fatalf("result=%+v", got)
	}
}
