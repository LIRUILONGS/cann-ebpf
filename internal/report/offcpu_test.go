package report

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cann-ebpf/internal/runlog"
)

func offcpuInt64Ptr(value int64) *int64 { return &value }

func offcpuAlmostEqual(left, right float64) bool {
	return math.Abs(left-right) <= 1e-9
}

func TestBuildOffCPUEmpty(t *testing.T) {
	data := BuildOffCPU(nil, OffCPUOptions{})
	if data.Status.Available {
		t.Fatalf("empty spans must be unavailable: %+v", data)
	}
	if data.Status.Reason == "" {
		t.Fatal("empty spans need an unavailable reason")
	}
	if data.Status.Confidence != "" {
		t.Fatalf("unavailable view must not claim confidence: %q", data.Status.Confidence)
	}
	if data.Mode != offCPUModeAggregate {
		t.Fatalf("mode = %q, want %q", data.Mode, offCPUModeAggregate)
	}
	if data.Rows == nil || len(data.Rows) != 0 {
		t.Fatalf("empty rows must be a non-nil empty slice: %#v", data.Rows)
	}
}

func TestBuildOffCPUUnavailableWithoutBlockingSamples(t *testing.T) {
	spans := []runlog.Span{
		testSpan(10, 11, 1_000_000_000, 5_000_000, "aclrtSynchronizeStream", "sync", "span-a"),
		testSpan(10, 12, 1_010_000_000, 5_000_000, "aclrtMemcpy", "memory", "span-b"),
	}
	data := BuildOffCPU(spans, OffCPUOptions{})
	if data.Status.Available {
		t.Fatalf("spans without offcpu_ns/futex_ns must be unavailable: %+v", data)
	}
	if !strings.Contains(data.Status.Reason, "no blocking samples") {
		t.Fatalf("reason = %q", data.Status.Reason)
	}
	if len(data.Rows) != 0 {
		t.Fatalf("unavailable view must not synthesize rows: %+v", data.Rows)
	}
	if data.Status.Confidence != "" {
		t.Fatalf("unavailable view must not claim confidence: %q", data.Status.Confidence)
	}
}

func TestBuildOffCPUAggregateRows(t *testing.T) {
	base := int64(1_000_000_000)
	first := testSpan(10, 11, base, 10_000_000, "aclrtSynchronizeStream", "sync", "span-a")
	first.OffCPUNS = offcpuInt64Ptr(4_000_000)
	first.FutexNS = offcpuInt64Ptr(1_000_000)
	second := testSpan(10, 12, base+20_000_000, 5_000_000, "aclrtMemcpy", "memory", "span-b")
	second.OffCPUNS = offcpuInt64Ptr(0)
	second.FutexNS = offcpuInt64Ptr(0)

	data := BuildOffCPU([]runlog.Span{second, first}, OffCPUOptions{})
	if !data.Status.Available {
		t.Fatalf("aggregate data must be available: %+v", data.Status)
	}
	if data.Mode != offCPUModeAggregate {
		t.Fatalf("mode = %q, want %q", data.Mode, offCPUModeAggregate)
	}
	if data.Status.Source != "spans" || data.Status.Confidence != "partial" {
		t.Fatalf("status = %+v", data.Status)
	}
	if len(data.Rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(data.Rows))
	}
	firstRow := data.Rows[0]
	if !offcpuAlmostEqual(firstRow.StartMS, 0) || !offcpuAlmostEqual(firstRow.EndMS, 10) {
		t.Fatalf("first row window = %v..%v", firstRow.StartMS, firstRow.EndMS)
	}
	if !offcpuAlmostEqual(firstRow.OnCPUMS, 6) ||
		!offcpuAlmostEqual(firstRow.OffCPUMS, 4) ||
		!offcpuAlmostEqual(firstRow.FutexMS, 1) {
		t.Fatalf("first row = %+v", firstRow)
	}
	if len(firstRow.Segments) != 0 {
		t.Fatalf("aggregate mode must not synthesize segments: %+v", firstRow.Segments)
	}
	secondRow := data.Rows[1]
	if !offcpuAlmostEqual(secondRow.StartMS, 20) || !offcpuAlmostEqual(secondRow.EndMS, 25) {
		t.Fatalf("second row window = %v..%v", secondRow.StartMS, secondRow.EndMS)
	}
	if !offcpuAlmostEqual(secondRow.OnCPUMS, 5) ||
		!offcpuAlmostEqual(secondRow.OffCPUMS, 0) ||
		!offcpuAlmostEqual(secondRow.FutexMS, 0) {
		t.Fatalf("second row = %+v", secondRow)
	}
}

func TestBuildOffCPUZeroOffCPUIsMeasuredEvidence(t *testing.T) {
	span := testSpan(20, 21, 2_000_000_000, 8_000_000, "aclrtSynchronizeStream", "sync", "span-zero")
	span.OffCPUNS = offcpuInt64Ptr(0)

	data := BuildOffCPU([]runlog.Span{span}, OffCPUOptions{})
	if !data.Status.Available {
		t.Fatalf("measured zero off-CPU is evidence: %+v", data.Status)
	}
	if len(data.Rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(data.Rows))
	}
	row := data.Rows[0]
	if !offcpuAlmostEqual(row.OffCPUMS, 0) || !offcpuAlmostEqual(row.OnCPUMS, 8) {
		t.Fatalf("row = %+v", row)
	}
	if row.Segments == nil || len(row.Segments) != 0 {
		t.Fatalf("aggregate segments = %#v", row.Segments)
	}
}

func TestBuildOffCPUFutexOnly(t *testing.T) {
	span := testSpan(30, 31, 3_000_000_000, 7_000_000, "aclrtSynchronizeStream", "sync", "span-futex")
	span.FutexNS = offcpuInt64Ptr(2_000_000)

	data := BuildOffCPU([]runlog.Span{span}, OffCPUOptions{})
	if !data.Status.Available {
		t.Fatalf("futex-only evidence must be available: %+v", data.Status)
	}
	if data.Mode != offCPUModeAggregate {
		t.Fatalf("mode = %q", data.Mode)
	}
	if len(data.Rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(data.Rows))
	}
	row := data.Rows[0]
	if !offcpuAlmostEqual(row.OffCPUMS, 0) || !offcpuAlmostEqual(row.FutexMS, 2) {
		t.Fatalf("futex-only row = %+v", row)
	}
	if !offcpuAlmostEqual(row.OnCPUMS, 0) {
		t.Fatalf("on_cpu_ms must stay unknown (0) when offcpu_ns was not measured: %v", row.OnCPUMS)
	}
	if len(row.Segments) != 0 {
		t.Fatalf("futex-only aggregate must not synthesize intervals: %+v", row.Segments)
	}
}

func TestBuildOffCPUExactIntervals(t *testing.T) {
	base := int64(4_000_000_000)
	span := testSpan(40, 41, base, 10_000_000, "aclrtSynchronizeStream", "sync", "span-exact")
	span.OffCPUNS = offcpuInt64Ptr(9_000_000)
	span.FutexNS = offcpuInt64Ptr(9_000_000)
	baseMS := float64(base) / 1e6

	data := BuildOffCPU([]runlog.Span{span}, OffCPUOptions{
		Intervals: []OffCPUSegment{
			{StartMS: baseMS + 1, EndMS: baseMS + 3, Kind: "offcpu"},
			{StartMS: baseMS + 5, EndMS: baseMS + 6, Kind: "futex"},
		},
	})
	if !data.Status.Available {
		t.Fatalf("exact intervals must be available: %+v", data.Status)
	}
	if data.Mode != offCPUModeExact {
		t.Fatalf("mode = %q, want %q", data.Mode, offCPUModeExact)
	}
	if data.Status.Confidence != "exact" {
		t.Fatalf("confidence = %q, want exact", data.Status.Confidence)
	}
	if len(data.Rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(data.Rows))
	}
	row := data.Rows[0]
	if !offcpuAlmostEqual(row.StartMS, 0) || !offcpuAlmostEqual(row.EndMS, 10) {
		t.Fatalf("row window = %v..%v", row.StartMS, row.EndMS)
	}
	if !offcpuAlmostEqual(row.OffCPUMS, 3) {
		t.Fatalf("exact off_cpu_ms = %v, want 3 (aggregate 9ms must be ignored)", row.OffCPUMS)
	}
	if !offcpuAlmostEqual(row.FutexMS, 1) {
		t.Fatalf("exact futex_ms = %v, want 1", row.FutexMS)
	}
	if !offcpuAlmostEqual(row.OnCPUMS, 7) {
		t.Fatalf("exact on_cpu_ms = %v, want 7", row.OnCPUMS)
	}
	if len(row.Segments) != 2 {
		t.Fatalf("segments = %+v", row.Segments)
	}
	if !offcpuAlmostEqual(row.Segments[0].StartMS, 1) || !offcpuAlmostEqual(row.Segments[0].EndMS, 3) {
		t.Fatalf("first segment = %+v", row.Segments[0])
	}
	if !offcpuAlmostEqual(row.Segments[1].StartMS, 5) || !offcpuAlmostEqual(row.Segments[1].EndMS, 6) ||
		row.Segments[1].Kind != "futex" {
		t.Fatalf("second segment = %+v", row.Segments[1])
	}
}

func TestBuildOffCPUExactIgnoresUnmatchedIntervals(t *testing.T) {
	base := int64(5_000_000_000)
	span := testSpan(50, 51, base, 4_000_000, "aclrtSynchronizeStream", "sync", "span-keep")
	span.OffCPUNS = offcpuInt64Ptr(2_000_000)
	baseMS := float64(base) / 1e6

	data := BuildOffCPU([]runlog.Span{span}, OffCPUOptions{
		Intervals: []OffCPUSegment{
			{StartMS: baseMS + 100, EndMS: baseMS + 101, Kind: "offcpu"},
			{StartMS: baseMS + 1, EndMS: baseMS + 2, Kind: "offcpu"},
		},
	})
	if !data.Status.Available || len(data.Rows) != 1 {
		t.Fatalf("data = %+v", data)
	}
	if len(data.Rows[0].Segments) != 1 || !offcpuAlmostEqual(data.Rows[0].OffCPUMS, 1) {
		t.Fatalf("unmatched interval leaked into row: %+v", data.Rows[0])
	}
}

func TestBuildOffCPUInvalidAggregateSamples(t *testing.T) {
	span := testSpan(60, 61, 6_000_000_000, 3_000_000, "aclrtSynchronizeStream", "sync", "span-bad")
	span.OffCPUNS = offcpuInt64Ptr(4_000_000)

	data := BuildOffCPU([]runlog.Span{span}, OffCPUOptions{})
	if data.Status.Available {
		t.Fatalf("impossible offcpu_ns > duration must not be available: %+v", data)
	}
	if !strings.Contains(data.Status.Reason, "invalid") {
		t.Fatalf("reason = %q", data.Status.Reason)
	}
	if len(data.Rows) != 0 {
		t.Fatalf("invalid sample produced rows: %+v", data.Rows)
	}
}

func TestLoadOffCPUIntervals(t *testing.T) {
	path := filepath.Join(t.TempDir(), "offcpu.jsonl")
	payload := strings.Join([]string{
		`{"pid":1,"tid":2,"span_id":"span-b","start_ns":1003000000,"end_ns":1004000000,"kind":"offcpu"}`,
		``,
		`{"pid":1,"tid":2,"span_id":"span-a","start_ns":1001000000,"end_ns":1002000000,"kind":"offcpu"}`,
	}, "\n")
	if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}

	segments, err := LoadOffCPUIntervals(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(segments) != 2 {
		t.Fatalf("segments = %+v", segments)
	}
	if !offcpuAlmostEqual(segments[0].StartMS, 1001) || !offcpuAlmostEqual(segments[0].EndMS, 1002) {
		t.Fatalf("first segment = %+v", segments[0])
	}
	if !offcpuAlmostEqual(segments[1].StartMS, 1003) || !offcpuAlmostEqual(segments[1].EndMS, 1004) {
		t.Fatalf("second segment = %+v", segments[1])
	}
}

func TestLoadOffCPUIntervalsRejectsMalformedLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "offcpu.jsonl")
	if err := os.WriteFile(path, []byte(`{"start_ns":10,"end_ns":10}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOffCPUIntervals(path); err == nil {
		t.Fatal("invalid interval should return an error")
	}
}

func TestBuildOffCPUExactFromIntervalPath(t *testing.T) {
	base := int64(7_000_000_000)
	span := testSpan(70, 71, base, 5_000_000, "aclrtSynchronizeStream", "sync", "span-file")
	path := filepath.Join(t.TempDir(), "offcpu.jsonl")
	payload := `{"pid":70,"tid":71,"span_id":"span-file","start_ns":7002000000,"end_ns":7003000000,"kind":"offcpu"}` + "\n"
	if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}

	data := BuildOffCPU([]runlog.Span{span}, OffCPUOptions{IntervalPath: path})
	if !data.Status.Available || data.Mode != offCPUModeExact || len(data.Rows) != 1 {
		t.Fatalf("data = %+v", data)
	}
	if data.Status.Source != "offcpu.jsonl" {
		t.Fatalf("source = %q", data.Status.Source)
	}
	row := data.Rows[0]
	if !offcpuAlmostEqual(row.OffCPUMS, 1) || len(row.Segments) != 1 {
		t.Fatalf("row = %+v", row)
	}
	if !offcpuAlmostEqual(row.Segments[0].StartMS, 2) || !offcpuAlmostEqual(row.Segments[0].EndMS, 3) {
		t.Fatalf("segment = %+v", row.Segments[0])
	}
	if !offcpuAlmostEqual(row.OnCPUMS, 4) {
		t.Fatalf("on_cpu_ms = %v, want 4", row.OnCPUMS)
	}
}
