package runlog

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPercentileNearestRank(t *testing.T) {
	values := []int64{10, 20, 30, 40, 50, 60, 70, 80, 90, 100}
	cases := []struct {
		percentile float64
		want       int64
	}{
		{0.50, 50},
		{0.95, 100},
		{0.99, 100},
	}
	for _, testCase := range cases {
		got, err := percentileNearestRank(values, testCase.percentile)
		if err != nil {
			t.Fatal(err)
		}
		if got != testCase.want {
			t.Fatalf("p%.0f = %d, want %d", testCase.percentile*100, got, testCase.want)
		}
	}
	if _, err := percentileNearestRank(nil, 0.5); !errors.Is(err, ErrNoDurationData) {
		t.Fatalf("empty percentile error = %v", err)
	}
	if _, err := percentileNearestRank(values, 0); err == nil {
		t.Fatal("expected invalid percentile to fail")
	}
}

func TestBuildSummaryFunctionAndCategoryStats(t *testing.T) {
	spans := []Span{
		summarySpan("A", "sync", 20),
		summarySpan("B", "submit", 40),
		summarySpan("A", "sync", 10),
		summarySpan("B", "submit", 30),
	}
	summary, err := BuildSummary(spans)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Status != SummaryStatusOK || summary.SpanCount != 4 {
		t.Fatalf("summary = %+v", summary)
	}
	if len(summary.Functions) != 2 || summary.Functions[0].Function != "B" || summary.Functions[1].Function != "A" {
		t.Fatalf("functions not sorted by total: %+v", summary.Functions)
	}
	b := summary.Functions[0]
	if b.Count != 2 || b.TotalNS != 70 || b.P50NS != 30 || b.P95NS != 40 || b.P99NS != 40 || b.MaxNS != 40 {
		t.Fatalf("B stats = %+v", b)
	}
	a := summary.Functions[1]
	if a.Count != 2 || a.TotalNS != 30 || a.P50NS != 10 || a.P95NS != 20 || a.MaxNS != 20 {
		t.Fatalf("A stats = %+v", a)
	}
	if len(summary.Categories) != 2 || summary.Categories[0].Category != "submit" || summary.Categories[1].Category != "sync" {
		t.Fatalf("categories = %+v", summary.Categories)
	}
	if summary.Categories[0].SpanPct != 50 || summary.Categories[0].TotalNS != 70 {
		t.Fatalf("submit category = %+v", summary.Categories[0])
	}

	markdown := summary.Markdown()
	for _, wanted := range []string{"| `B` | 2 | 30 | 40 | 40 | 40 | 70 |", "| `submit` | 2 | 50.00% | 30 | 40 | 40 | 40 | 70 |"} {
		if !strings.Contains(markdown, wanted) {
			t.Fatalf("markdown missing %q:\n%s", wanted, markdown)
		}
	}
}

func TestBuildSummaryEmptyDoesNotFabricateStats(t *testing.T) {
	summary, err := BuildSummary(nil)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Status != SummaryStatusEmpty || summary.SpanCount != 0 {
		t.Fatalf("empty summary = %+v", summary)
	}
	if summary.Functions == nil || summary.Categories == nil {
		t.Fatalf("empty slices must be non-nil: %+v", summary)
	}
	if len(summary.Functions) != 0 || len(summary.Categories) != 0 {
		t.Fatalf("empty summary has rows: %+v", summary)
	}
	markdown := summary.Markdown()
	if !strings.Contains(markdown, "未生成任何") || strings.Contains(markdown, "| function |") {
		t.Fatalf("empty markdown fabricated a table:\n%s", markdown)
	}

	dir := t.TempDir()
	jsonPath := filepath.Join(dir, "nested", "summary.json")
	markdownPath := filepath.Join(dir, "nested", "summary.md")
	if err := summary.WriteJSON(jsonPath); err != nil {
		t.Fatal(err)
	}
	if err := summary.WriteMarkdown(markdownPath); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(jsonPath)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if string(raw["functions"]) != "[]" || string(raw["categories"]) != "[]" || string(raw["span_count"]) != "0" {
		t.Fatalf("empty JSON fabricated stats: %s", data)
	}
	markdownData, err := os.ReadFile(markdownPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(markdownData), "未发现 span 数据") {
		t.Fatalf("empty markdown = %s", markdownData)
	}
}

func TestBuildSummaryFromReaderAndValidation(t *testing.T) {
	body := `{"function":"A","category":"sync","duration_ns":7,"extra":"kept"}
{"function":"A","category":"sync","duration_ns":9}
`
	summary, err := BuildSummaryFromReader(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if summary.SpanCount != 2 || len(summary.Functions) != 1 || summary.Functions[0].TotalNS != 16 {
		t.Fatalf("summary = %+v", summary)
	}
	if _, err := BuildSummary([]Span{{Function: "", Category: "sync", DurationNS: 1}}); err == nil {
		t.Fatal("expected empty function to fail")
	}
	if _, err := BuildSummary([]Span{{Function: "A", Category: "sync", DurationNS: -1}}); err == nil {
		t.Fatal("expected negative duration to fail")
	}

	path := filepath.Join(t.TempDir(), "spans.jsonl")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	fromFile, err := BuildSummaryFromFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if fromFile.SpanCount != 2 || fromFile.Functions[0].TotalNS != 16 {
		t.Fatalf("summary from file = %+v", fromFile)
	}
}

func TestBuildSummaryContextCoverageAndBatches(t *testing.T) {
	batchAttribution := &Attribution{Level: "batch", Source: "test", Confidence: 1}
	threadAttribution := &Attribution{Level: "thread", Source: "ebpf_tid", Confidence: 0.3}
	spans := []Span{
		{Function: "A", Category: "sync", DurationNS: 10, PID: 1, BatchID: "b-1", RequestIDs: []string{"r-2", "r-1"}, Attribution: batchAttribution, RunID: "run-1", SpanID: "s-1"},
		{Function: "A", Category: "sync", DurationNS: 20, PID: 1, BatchID: "b-1", RequestIDs: []string{"r-3"}, Attribution: batchAttribution, RunID: "run-1", SpanID: "s-2"},
		{Function: "B", Category: "submit", DurationNS: 30, PID: 2, Attribution: threadAttribution, RunID: "run-1", SpanID: "s-3"},
	}
	summary, err := BuildSummary(spans)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Coverage.BatchSpans != 2 || summary.Coverage.ThreadSpans != 1 || summary.Coverage.UniqueBatches != 1 || summary.Coverage.UniqueRequests != 3 {
		t.Fatalf("coverage = %+v", summary.Coverage)
	}
	if len(summary.PIDs) != 2 || summary.PIDs[0].PID != 1 || summary.PIDs[0].BatchSpans != 2 || summary.PIDs[1].ThreadSpans != 1 {
		t.Fatalf("pids = %+v", summary.PIDs)
	}
	if len(summary.Attribution) != 2 {
		t.Fatalf("attribution = %+v", summary.Attribution)
	}
	if len(summary.TopSlowBatches) != 1 {
		t.Fatalf("batches = %+v", summary.TopSlowBatches)
	}
	batch := summary.TopSlowBatches[0]
	if batch.BatchID != "b-1" || batch.Count != 2 || batch.TotalNS != 30 || len(batch.RequestIDs) != 3 || len(batch.PIDs) != 1 || batch.PIDs[0] != 1 {
		t.Fatalf("batch = %+v", batch)
	}
	markdown := summary.Markdown()
	for _, wanted := range []string{"上下文与归因覆盖", "Top slow batch", "| `b-1` |"} {
		if !strings.Contains(markdown, wanted) {
			t.Fatalf("markdown missing %q:\n%s", wanted, markdown)
		}
	}
}

func summarySpan(function, category string, duration int64) Span {
	return Span{Function: function, Category: category, DurationNS: duration}
}
