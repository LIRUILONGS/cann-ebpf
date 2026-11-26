package cpython

import (
	"encoding/json"
	"testing"
)

func TestStandardSpansMapsBatchWindowToCanonicalSpan(t *testing.T) {
	report := BatchReport{
		Schema:          BatchSchema,
		Boundary:        "scheduler_output_dict_identity",
		WindowSemantics: "observed interval between SchedulerOutput dict identities",
		Layout:          "cpython-3.12.13-aarch64/vllm-0.23.0rc1",
		Batches: []Batch{{
			BatchID:          "batch-0001",
			Dict:             0x1234,
			Object:           0x5678,
			TypeName:         "SchedulerOutput",
			BeginNS:          100,
			EndNS:            250,
			DurationNS:       150,
			Open:             false,
			RequestIDs:       []string{"req-b", "req-a"},
			RequestCount:     2,
			ObservedIDEvents: 3,
			DictUsed:         2,
			PIDs:             []uint32{42},
			TIDs:             []uint32{43},
		}},
	}
	spans, warnings := StandardSpans(42, "/usr/local/lib/libpython3.12.so.1.0", report)
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	if len(spans) != 1 {
		t.Fatalf("spans=%d, want 1", len(spans))
	}
	span := spans[0]
	if span.Function != BatchSpanFunction || span.Category != "batch" {
		t.Fatalf("unexpected function/category: %+v", span)
	}
	if span.BatchID != "pid-42-batch-0001" {
		t.Fatalf("batch_id=%q", span.BatchID)
	}
	if span.TsNS != 100 || span.EndNS != 250 || span.DurationNS != 150 {
		t.Fatalf("window not preserved: %+v", span)
	}
	if span.TID != 43 {
		t.Fatalf("tid=%d, want observed 43", span.TID)
	}
	if span.Attribution == nil || span.Attribution.Level != "batch" ||
		span.Attribution.Source != "ebpf_scheduler_output_dict" {
		t.Fatalf("attribution=%+v", span.Attribution)
	}
	if len(span.RequestIDs) != 2 || span.RequestIDs[0] != "req-a" || span.RequestIDs[1] != "req-b" {
		t.Fatalf("request ids=%v", span.RequestIDs)
	}
	if err := span.Validate(); err == nil {
		t.Fatal("un-normalized span should require run/profile defaults")
	}
	span.Normalize("run-1", "vllm-ascend")
	span.SpanID = "test-span"
	if err := span.Validate(); err != nil {
		t.Fatalf("valid span rejected: %v", err)
	}
	var found bool
	for _, field := range span.UnknownFields {
		if field.Name == "batch_boundary" {
			var value string
			if err := json.Unmarshal(field.Value, &value); err != nil {
				t.Fatal(err)
			}
			if value != report.Boundary {
				t.Fatalf("boundary=%q", value)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("batch_boundary field missing")
	}
}

func TestStandardSpansMarksOpenBatch(t *testing.T) {
	report := BatchReport{
		BatchCount: 1,
		Batches: []Batch{{
			BatchID: "batch-0001", BeginNS: 10, EndNS: 20, DurationNS: 10, Open: true,
			PIDs: []uint32{7}, TypeName: "SchedulerOutput",
		}},
	}
	spans, _ := StandardSpans(7, "libpython.so", report)
	if len(spans) != 1 || spans[0].Attribution.Verdict != "batch_window_open" {
		t.Fatalf("open verdict not set: %+v", spans)
	}
}
