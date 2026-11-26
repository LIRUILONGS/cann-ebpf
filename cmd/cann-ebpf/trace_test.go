package main

import (
	"strings"
	"testing"

	"cann-ebpf/internal/runlog"
	"cann-ebpf/internal/tracectx"
)

func TestBuildTraceReportGroupsAndSorts(t *testing.T) {
	spans := []runlog.Span{
		{
			SpanID: "event-1", Function: "A", Category: "sync", PID: 1, TID: 10,
			StartNS: 100, EndNS: 110, DurationNS: 10, RequestIDs: []string{"r-1"},
			Trace: &tracectx.Context{
				TraceID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", SpanID: "1111111111111111",
				ParentSpanID: "aaaaaaaaaaaaaaaa", BatchParentSpanID: "cccccccccccccccc", Source: "vllm_general_plugin",
			},
		},
		{
			SpanID: "event-2", Function: "B", Category: "submit", PID: 1, TID: 11,
			StartNS: 105, EndNS: 140, DurationNS: 35,
			Trace: &tracectx.Context{
				TraceID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", SpanID: "2222222222222222",
				ParentSpanID: "aaaaaaaaaaaaaaaa", Source: "vllm_general_plugin",
			},
		},
		{
			SpanID: "event-3", Function: "C", Category: "sync", PID: 2, TID: 20,
			StartNS: 100, EndNS: 200, DurationNS: 100,
			Trace: &tracectx.Context{
				TraceID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", SpanID: "3333333333333333",
				ParentSpanID: "bbbbbbbbbbbbbbbb", Source: "collector_local_batch",
			},
		},
		{SpanID: "event-4", Function: "D", Category: "sync", PID: 3, TID: 30},
	}
	report, err := buildTraceReport("spans.jsonl", spans, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if report.SpanCount != 4 || report.TracedSpans != 3 || report.Untraced != 1 || report.TraceCount != 2 {
		t.Fatalf("report coverage = %+v", report)
	}
	if report.Traces[0].TraceID != "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" {
		t.Fatalf("trace order = %+v", report.Traces)
	}
	if report.Traces[1].SpanCount != 2 || len(report.Traces[1].BatchSpanIDs) != 1 || len(report.Traces[1].BatchParentSpanIDs) != 1 || len(report.Traces[1].RequestIDs) != 1 {
		t.Fatalf("trace A = %+v", report.Traces[1])
	}
	if report.Traces[1].Spans[0].BatchParentSpanID != "cccccccccccccccc" {
		t.Fatalf("batch upstream parent = %+v", report.Traces[1].Spans[0])
	}
	filtered, err := buildTraceReport("spans.jsonl", spans, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 0)
	if err != nil {
		t.Fatal(err)
	}
	if filtered.TraceCount != 1 || filtered.Traces[0].TraceID != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("filtered report = %+v", filtered)
	}
	limited, err := buildTraceReport("spans.jsonl", spans, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(limited.Traces) != 1 || limited.TraceCount != 2 {
		t.Fatalf("limit report = %+v", limited)
	}
}

func TestBuildTraceReportMissingTrace(t *testing.T) {
	_, err := buildTraceReport("spans.jsonl", nil, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 0)
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected not found error, got %v", err)
	}
}

func TestPrintTraceReport(t *testing.T) {
	report, err := buildTraceReport("spans.jsonl", []runlog.Span{{
		SpanID: "event-1", Function: "A", Category: "sync", PID: 1, TID: 10,
		DurationNS: 10,
		Trace: &tracectx.Context{
			TraceID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", SpanID: "1111111111111111",
			ParentSpanID: "aaaaaaaaaaaaaaaa", Source: "vllm_general_plugin",
		},
	}}, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if report.Traces[0].Spans[0].Function != "A" {
		t.Fatalf("span node = %+v", report.Traces[0].Spans[0])
	}
}
