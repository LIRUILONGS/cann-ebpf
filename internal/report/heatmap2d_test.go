package report

import (
	"testing"

	"cann-ebpf/internal/runlog"
)

func heatmapTestSpan(start, duration int64) runlog.Span {
	return runlog.Span{
		Schema:     runlog.SpanSchemaV1,
		StartNS:    start,
		EndNS:      start + duration,
		DurationNS: duration,
	}
}

func countHeatmapSamples(matrix [][]int) int {
	total := 0
	for _, row := range matrix {
		for _, count := range row {
			total += count
		}
	}
	return total
}

func TestBuildLatencyHeatmapDualPeak(t *testing.T) {
	spans := []runlog.Span{
		heatmapTestSpan(100_000_000, 100_000),
		heatmapTestSpan(200_000_000, 120_000),
		heatmapTestSpan(300_000_000, 90_000),
		heatmapTestSpan(4_000_000_000, 10_000_000),
		heatmapTestSpan(5_000_000_000, 9_000_000),
	}
	data := BuildLatencyHeatmap(spans, 0, 10_000_000_000, Heatmap2DOptions{Columns: 20})

	if !data.Available {
		t.Fatalf("available=false: %s", data.Reason)
	}
	if len(data.Counts) != 22 || len(data.Counts[0]) != 20 {
		t.Fatalf("counts shape = %dx%d", len(data.Counts), len(data.Counts[0]))
	}
	if total := countHeatmapSamples(data.Counts); total != len(spans) {
		t.Fatalf("heatmap total = %d, want %d", total, len(spans))
	}
	nonZeroRows := 0
	for _, row := range data.Counts {
		for _, count := range row {
			if count > 0 {
				nonZeroRows++
				break
			}
		}
	}
	if nonZeroRows < 2 {
		t.Fatalf("dual-peak durations occupied %d latency rows, want at least 2", nonZeroRows)
	}
	if data.MaxCount == 0 {
		t.Fatal("max_count = 0")
	}
}

func TestBuildLatencyHeatmapEdgesAndEmptyInput(t *testing.T) {
	data := BuildLatencyHeatmap(nil, 0, 1_000_000_000, Heatmap2DOptions{LatencyRows: 24, Columns: 32})

	if data.Available || data.Reason != "no spans" {
		t.Fatalf("empty heatmap = available=%v reason=%q", data.Available, data.Reason)
	}
	if len(data.Counts) != 24 || len(data.Counts[0]) != 32 {
		t.Fatalf("empty counts shape = %dx%d", len(data.Counts), len(data.Counts[0]))
	}
	if len(data.P50) != 32 || len(data.P95) != 32 || len(data.P99) != 32 {
		t.Fatalf("percentile lengths = %d/%d/%d", len(data.P50), len(data.P95), len(data.P99))
	}
	if len(data.LatencyEdgesMS) != 25 {
		t.Fatalf("edge length = %d, want 25", len(data.LatencyEdgesMS))
	}
	for i := 1; i < len(data.LatencyEdgesMS); i++ {
		if data.LatencyEdgesMS[i] <= data.LatencyEdgesMS[i-1] {
			t.Fatalf("edges not increasing at %d: %v", i, data.LatencyEdgesMS)
		}
	}
	if data.LatencyEdgesMS[0] != 0.001 {
		t.Fatalf("minimum edge = %v, want 0.001 ms", data.LatencyEdgesMS[0])
	}
}

func TestBuildLatencyHeatmapCountsEverySpan(t *testing.T) {
	spans := []runlog.Span{
		heatmapTestSpan(0, 1_000),
		heatmapTestSpan(2_000_000_000, 2_000_000),
		heatmapTestSpan(20_000_000_000, 500),
		heatmapTestSpan(5_000_000_000, 0),
	}
	data := BuildLatencyHeatmap(spans, 0, 10_000_000_000, Heatmap2DOptions{Columns: 10, LatencyRows: 12})
	if total := countHeatmapSamples(data.Counts); total != len(spans) {
		t.Fatalf("heatmap total = %d, want %d", total, len(spans))
	}
}

func TestBuildSubsecondHeatmapPeriodicEvents(t *testing.T) {
	spans := []runlog.Span{
		heatmapTestSpan(0, 10_000_000),
		heatmapTestSpan(1_000_000_000, 10_000_000),
		heatmapTestSpan(2_000_000_000, 10_000_000),
		heatmapTestSpan(3_000_000_000, 10_000_000),
		heatmapTestSpan(4_000_000_000, 10_000_000),
		heatmapTestSpan(5_000_000_000, 10_000_000),
	}
	data := BuildSubsecondHeatmap(spans, 0, 6_000_000_000, Heatmap2DOptions{Columns: 12})

	if !data.Available {
		t.Fatalf("available=false: %s", data.Reason)
	}
	if len(data.Counts) != 50 || len(data.Counts[0]) != 12 {
		t.Fatalf("counts shape = %dx%d", len(data.Counts), len(data.Counts[0]))
	}
	if data.OffsetMS != 20 {
		t.Fatalf("offset_ms = %v, want 20", data.OffsetMS)
	}
	if total := countHeatmapSamples(data.Counts); total != len(spans) {
		t.Fatalf("subsecond total = %d, want %d", total, len(spans))
	}
	rowZeroTotal := 0
	for _, count := range data.Counts[0] {
		rowZeroTotal += count
	}
	if rowZeroTotal != len(spans) {
		t.Fatalf("periodic events not concentrated in offset row 0: %v", data.Counts[0])
	}
	for row := 1; row < len(data.Counts); row++ {
		for _, count := range data.Counts[row] {
			if count != 0 {
				t.Fatalf("unexpected count outside offset row 0 at row %d", row)
			}
		}
	}
}

func TestBuildSubsecondHeatmapUnavailable(t *testing.T) {
	empty := BuildSubsecondHeatmap(nil, 0, 10_000_000_000, Heatmap2DOptions{})
	if empty.Available || empty.Reason != "no spans" {
		t.Fatalf("empty subsecond = available=%v reason=%q", empty.Available, empty.Reason)
	}
	if len(empty.Counts) != 50 || len(empty.Counts[0]) != 160 {
		t.Fatalf("empty counts shape = %dx%d", len(empty.Counts), len(empty.Counts[0]))
	}

	short := BuildSubsecondHeatmap(
		[]runlog.Span{heatmapTestSpan(0, 10_000_000)},
		0,
		1_500_000_000,
		Heatmap2DOptions{Columns: 20, OffsetRows: 25},
	)
	if short.Available || short.Reason != "run too short" {
		t.Fatalf("short subsecond = available=%v reason=%q", short.Available, short.Reason)
	}
	if len(short.Counts) != 25 || len(short.Counts[0]) != 20 {
		t.Fatalf("short counts shape = %dx%d", len(short.Counts), len(short.Counts[0]))
	}
}

func TestHeatmapSpanFallbacks(t *testing.T) {
	span := runlog.Span{
		TsNS:    2_000_000_000,
		StartNS: 0,
		EndNS:   2_500_000_000,
	}
	data := BuildLatencyHeatmap([]runlog.Span{span}, 2_000_000_000, 1_000_000_000, Heatmap2DOptions{})
	if total := countHeatmapSamples(data.Counts); total != 1 {
		t.Fatalf("fallback span not counted: total=%d", total)
	}
	if data.Available && data.P50[0] <= 0 {
		t.Fatal("expected duration from end_ns minus fallback start")
	}
}
