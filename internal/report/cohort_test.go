package report

import (
	"math"
	"strings"
	"testing"

	"cann-ebpf/internal/runlog"
)

func cohortSpan(function, category string, start, duration int64) runlog.Span {
	return runlog.Span{
		Schema:     runlog.SpanSchemaV1,
		RunID:      "run-test",
		Profile:    "test",
		TsNS:       start,
		StartNS:    start,
		EndNS:      start + duration,
		PID:        100,
		TID:        101,
		TGID:       100,
		Function:   function,
		Category:   category,
		DurationNS: duration,
	}
}

func TestBuildAggregateBands(t *testing.T) {
	const windowStart = int64(1_000_000)
	const windowNS = int64(10_000_000_000)
	spans := make([]runlog.Span, 0, 105)
	for i := 0; i < 60; i++ {
		spans = append(spans, cohortSpan("fnA", "catA", windowStart+int64(i)*100_000_000, 2_000_000))
	}
	for i := 0; i < 30; i++ {
		spans = append(spans, cohortSpan("fnB", "catB", windowStart+int64(i)*200_000_000, 5_000_000))
	}
	for i := 0; i < 10; i++ {
		spans = append(spans, cohortSpan("fnC", "catC", windowStart+int64(i)*100_000_000, 1_000_000))
	}
	for i := 0; i < 5; i++ {
		spans = append(spans, cohortSpan("", "", windowStart+int64(i)*100_000_000, 1_000_000))
	}

	got := BuildAggregateBands(spans, windowStart, windowNS, AggregateOptions{BucketCount: 10, TopFunctions: 12})
	if !got.Available {
		t.Fatalf("expected available, reason=%q", got.Reason)
	}
	if got.Columns != 10 || math.Abs(got.BucketMS-1000) > 1e-9 {
		t.Fatalf("columns=%d bucket_ms=%v", got.Columns, got.BucketMS)
	}
	wantOrder := []string{"fnB", "fnA", "fnC", "(unknown)"}
	if len(got.Bands) != len(wantOrder) {
		t.Fatalf("bands=%d want=%d", len(got.Bands), len(wantOrder))
	}
	for i, want := range wantOrder {
		if got.Bands[i].Function != want {
			t.Fatalf("band[%d]=%q want=%q", i, got.Bands[i].Function, want)
		}
	}

	wantCounts := map[string]int{"fnA": 60, "fnB": 30, "fnC": 10, "(unknown)": 5}
	total := 0
	for _, band := range got.Bands {
		if len(band.P50) != got.Columns || len(band.P95) != got.Columns || len(band.P99) != got.Columns || len(band.Count) != got.Columns {
			t.Fatalf("band %q slices do not match columns", band.Function)
		}
		sum := 0
		for column := range band.Count {
			sum += band.Count[column]
			if band.Count[column] == 0 {
				if band.P50[column] != 0 || band.P95[column] != 0 || band.P99[column] != 0 {
					t.Fatalf("band %q empty column %d has non-zero percentiles", band.Function, column)
				}
				continue
			}
			if band.P50[column] > band.P95[column] || band.P95[column] > band.P99[column] {
				t.Fatalf("band %q column %d percentiles not ordered: %v %v %v", band.Function, column, band.P50[column], band.P95[column], band.P99[column])
			}
		}
		if sum != wantCounts[band.Function] {
			t.Fatalf("band %q count=%d want=%d", band.Function, sum, wantCounts[band.Function])
		}
		total += sum
	}
	if total != len(spans) {
		t.Fatalf("count total=%d spans=%d", total, len(spans))
	}
	for _, band := range got.Bands {
		if band.Function == "(unknown)" && band.Category != "other" {
			t.Fatalf("unknown category=%q want other", band.Category)
		}
	}
	wantMaxP95 := 0.0
	for _, band := range got.Bands {
		for _, value := range band.P95 {
			if value > wantMaxP95 {
				wantMaxP95 = value
			}
		}
	}
	if got.MaxP95MS != wantMaxP95 {
		t.Fatalf("max_p95=%v want=%v", got.MaxP95MS, wantMaxP95)
	}
}

func TestBuildAggregateBandsEmpty(t *testing.T) {
	got := BuildAggregateBands(nil, 0, 0, AggregateOptions{})
	if got.Available || got.Reason != "no spans" {
		t.Fatalf("available=%v reason=%q", got.Available, got.Reason)
	}
	if len(got.Bands) != 0 {
		t.Fatalf("bands=%d want=0", len(got.Bands))
	}
}

func TestBuildTailCohort(t *testing.T) {
	spans := make([]runlog.Span, 0, 200)
	start := int64(1_000_000)
	for i := 0; i < 25; i++ {
		span := cohortSpan("sync_wait", "sync", start+int64(len(spans))*1_000_000, 10_000_000)
		offCPU := int64(8_000_000)
		futex := int64(4_000_000)
		span.OffCPUNS = &offCPU
		span.FutexNS = &futex
		spans = append(spans, span)
	}
	for i := 0; i < 15; i++ {
		spans = append(spans, cohortSpan("launch", "launch", start+int64(len(spans))*1_000_000, 10_000_000))
	}
	for i := 0; i < 80; i++ {
		span := cohortSpan("launch", "launch", start+int64(len(spans))*1_000_000, 500_000)
		offCPU := int64(100_000)
		futex := int64(20_000)
		span.OffCPUNS = &offCPU
		span.FutexNS = &futex
		spans = append(spans, span)
	}
	for i := 0; i < 60; i++ {
		span := cohortSpan("memcpy", "memcpy", start+int64(len(spans))*1_000_000, 600_000)
		offCPU := int64(200_000)
		futex := int64(50_000)
		span.OffCPUNS = &offCPU
		span.FutexNS = &futex
		spans = append(spans, span)
	}
	for i := 0; i < 20; i++ {
		span := cohortSpan("sync_wait", "sync", start+int64(len(spans))*1_000_000, 700_000)
		offCPU := int64(300_000)
		futex := int64(80_000)
		span.OffCPUNS = &offCPU
		span.FutexNS = &futex
		spans = append(spans, span)
	}

	got := BuildTailCohort(spans, CohortOptions{})
	if !got.Available {
		t.Fatalf("expected available, reason=%q", got.Reason)
	}
	if got.SlowCount != 40 || got.TypicalCount != 60 {
		t.Fatalf("slow=%d typical=%d", got.SlowCount, got.TypicalCount)
	}
	if math.Abs(got.ThresholdMS-10) > 1e-9 {
		t.Fatalf("threshold=%v want 10", got.ThresholdMS)
	}
	if math.Abs(got.SlowOffCPUMS-8) > 1e-9 || math.Abs(got.TypicalOffCPUMS-0.2) > 1e-9 {
		t.Fatalf("offcpu slow=%v typical=%v", got.SlowOffCPUMS, got.TypicalOffCPUMS)
	}
	if math.Abs(got.SlowFutexMS-4) > 1e-9 || math.Abs(got.TypicalFutexMS-0.05) > 1e-9 {
		t.Fatalf("futex slow=%v typical=%v", got.SlowFutexMS, got.TypicalFutexMS)
	}

	findRow := func(kind, label string) CohortRow {
		t.Helper()
		for _, row := range got.Rows {
			if row.Kind == kind && row.Label == label {
				return row
			}
		}
		t.Fatalf("row %s/%s not found", kind, label)
		return CohortRow{}
	}
	syncRow := findRow("function", "sync_wait")
	if math.Abs(syncRow.SlowPct-62.5) > 1e-9 || syncRow.TypicalPct != 0 || math.Abs(syncRow.DeltaPct-62.5) > 1e-9 {
		t.Fatalf("sync row=%+v", syncRow)
	}
	memcpyRow := findRow("category", "memcpy")
	if memcpyRow.SlowPct != 0 || math.Abs(memcpyRow.TypicalPct-100) > 1e-9 || math.Abs(memcpyRow.DeltaPct+100) > 1e-9 {
		t.Fatalf("memcpy row=%+v", memcpyRow)
	}
	launchRow := findRow("function", "launch")
	if math.Abs(launchRow.DeltaPct-37.5) > 1e-9 {
		t.Fatalf("launch row=%+v", launchRow)
	}
	for i := 1; i < len(got.Rows); i++ {
		previous := math.Abs(got.Rows[i-1].DeltaPct)
		current := math.Abs(got.Rows[i].DeltaPct)
		if previous < current {
			t.Fatalf("rows not sorted by abs delta at %d: %v < %v", i, previous, current)
		}
	}
	joinedNotes := strings.Join(got.Notes, "\n")
	if !strings.Contains(joinedNotes, "不是因果证明") {
		t.Fatalf("correlation caveat missing: %q", joinedNotes)
	}
}

func TestBuildTailCohortInsufficientSamples(t *testing.T) {
	spans := make([]runlog.Span, 0, 50)
	for i := 0; i < 50; i++ {
		spans = append(spans, cohortSpan("fn", "cat", int64(1_000_000+i), 1_000_000))
	}
	got := BuildTailCohort(spans, CohortOptions{})
	if got.Available {
		t.Fatal("expected unavailable")
	}
	if !strings.Contains(got.Reason, "insufficient samples") {
		t.Fatalf("reason=%q", got.Reason)
	}
}

func TestBuildTailCohortSlowCohortTooSmall(t *testing.T) {
	spans := make([]runlog.Span, 0, 200)
	for i := 0; i < 160; i++ {
		spans = append(spans, cohortSpan("fast", "fast", int64(1_000_000+i), 100_000))
	}
	for i := 0; i < 40; i++ {
		spans = append(spans, cohortSpan("slow", "slow", int64(1_000_000+160+i), int64(i+1)*1_000_000))
	}
	got := BuildTailCohort(spans, CohortOptions{})
	if got.Available {
		t.Fatal("expected unavailable")
	}
	if !strings.Contains(got.Reason, "slow cohort too small") {
		t.Fatalf("reason=%q", got.Reason)
	}
}

func TestBuildTailCohortNoBlockingMeasurements(t *testing.T) {
	spans := make([]runlog.Span, 0, 200)
	for i := 0; i < 40; i++ {
		spans = append(spans, cohortSpan("slow", "slow", int64(1_000_000+i), 10_000_000))
	}
	for i := 0; i < 160; i++ {
		spans = append(spans, cohortSpan("fast", "fast", int64(1_000_000+40+i), 500_000))
	}
	got := BuildTailCohort(spans, CohortOptions{})
	if !got.Available {
		t.Fatalf("expected available, reason=%q", got.Reason)
	}
	if got.SlowOffCPUMS != 0 || got.TypicalOffCPUMS != 0 || got.SlowFutexMS != 0 || got.TypicalFutexMS != 0 {
		t.Fatalf("expected zero unmeasured averages: %+v", got)
	}
	joinedNotes := strings.Join(got.Notes, "\n")
	if !strings.Contains(joinedNotes, "没有测量值") {
		t.Fatalf("unmeasured note missing: %q", joinedNotes)
	}
}

func TestBuildTailCohortEmpty(t *testing.T) {
	got := BuildTailCohort(nil, CohortOptions{})
	if got.Available || got.Reason != "no spans" {
		t.Fatalf("available=%v reason=%q", got.Available, got.Reason)
	}
	if len(got.Rows) != 0 {
		t.Fatalf("rows=%d want=0", len(got.Rows))
	}
	if len(got.Notes) == 0 || !strings.Contains(strings.Join(got.Notes, "\n"), "不是因果证明") {
		t.Fatalf("correlation caveat missing: %v", got.Notes)
	}
}
