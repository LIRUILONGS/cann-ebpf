package main

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"cann-ebpf/internal/runlog"
)

const traceReportSchema = "cann.trace.report.v1"

type traceSpanNode struct {
	SpanID            string   `json:"span_id"`
	TraceSpanID       string   `json:"trace_span_id"`
	ParentSpanID      string   `json:"parent_span_id,omitempty"`
	BatchParentSpanID string   `json:"batch_parent_span_id,omitempty"`
	Function          string   `json:"function"`
	Category          string   `json:"category"`
	PID               int      `json:"pid"`
	TID               int      `json:"tid"`
	StartNS           int64    `json:"start_ns,omitempty"`
	EndNS             int64    `json:"end_ns,omitempty"`
	DurationNS        int64    `json:"duration_ns"`
	RequestIDs        []string `json:"request_ids,omitempty"`
}

type traceNode struct {
	TraceID            string          `json:"trace_id"`
	BatchSpanIDs       []string        `json:"batch_span_ids,omitempty"`
	BatchParentSpanIDs []string        `json:"batch_parent_span_ids,omitempty"`
	SpanCount          int             `json:"span_count"`
	TotalNS            int64           `json:"total_ns"`
	RequestIDs         []string        `json:"request_ids,omitempty"`
	PIDs               []int           `json:"pids,omitempty"`
	Sources            []string        `json:"sources,omitempty"`
	Spans              []traceSpanNode `json:"spans"`
}

type traceReport struct {
	Schema      string      `json:"schema"`
	Input       string      `json:"input"`
	TraceCount  int         `json:"trace_count"`
	SpanCount   int         `json:"span_count"`
	TracedSpans int         `json:"traced_spans"`
	Untraced    int         `json:"untraced_spans"`
	Traces      []traceNode `json:"traces"`
}

func cmdTrace(args []string) int {
	fs := flag.NewFlagSet("trace", flag.ContinueOnError)
	input := fs.String("input", "", "spans.jsonl path")
	traceID := fs.String("trace-id", "", "only show one trace_id")
	limit := fs.Int("limit", 20, "maximum traces to show (0 = all)")
	jsonOut := fs.Bool("json", false, "json output")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if strings.TrimSpace(*input) == "" {
		fmt.Fprintln(os.Stderr, "trace: --input is required")
		return 2
	}
	file, err := os.Open(*input)
	if err != nil {
		fmt.Fprintf(os.Stderr, "trace: open %s: %v\n", *input, err)
		return 4
	}
	defer file.Close()
	spans, err := runlog.ReadSpans(file)
	if err != nil {
		fmt.Fprintf(os.Stderr, "trace: read %s: %v\n", *input, err)
		return 4
	}
	report, err := buildTraceReport(*input, spans, *traceID, *limit)
	if err != nil {
		fmt.Fprintf(os.Stderr, "trace: %v\n", err)
		return 3
	}
	if *jsonOut {
		writeJSON(report)
		return 0
	}
	printTraceReport(report)
	return 0
}

func buildTraceReport(input string, spans []runlog.Span, onlyTraceID string, limit int) (traceReport, error) {
	report := traceReport{Schema: traceReportSchema, Input: input, SpanCount: len(spans)}
	groups := map[string]*traceNode{}
	for _, span := range spans {
		if span.Trace == nil || span.Trace.TraceID == "" {
			report.Untraced++
			continue
		}
		if onlyTraceID != "" && span.Trace.TraceID != onlyTraceID {
			continue
		}
		report.TracedSpans++
		node := groups[span.Trace.TraceID]
		if node == nil {
			node = &traceNode{TraceID: span.Trace.TraceID}
			groups[span.Trace.TraceID] = node
		}
		node.SpanCount++
		node.TotalNS += span.DurationNS
		if span.Trace.ParentSpanID != "" {
			node.BatchSpanIDs = appendUnique(node.BatchSpanIDs, span.Trace.ParentSpanID)
		}
		if span.Trace.BatchParentSpanID != "" {
			node.BatchParentSpanIDs = appendUnique(node.BatchParentSpanIDs, span.Trace.BatchParentSpanID)
		}
		if span.Trace.Source != "" {
			node.Sources = appendUnique(node.Sources, span.Trace.Source)
		}
		for _, requestID := range span.RequestIDs {
			node.RequestIDs = appendUnique(node.RequestIDs, requestID)
		}
		node.PIDs = appendUniqueInt(node.PIDs, span.PID)
		node.Spans = append(node.Spans, traceSpanNode{
			SpanID: span.SpanID, TraceSpanID: span.Trace.SpanID,
			ParentSpanID: span.Trace.ParentSpanID, BatchParentSpanID: span.Trace.BatchParentSpanID, Function: span.Function,
			Category: span.Category, PID: span.PID, TID: span.TID,
			StartNS: span.StartNS, EndNS: span.EndNS, DurationNS: span.DurationNS,
			RequestIDs: append([]string(nil), span.RequestIDs...),
		})
	}
	if onlyTraceID != "" && len(groups) == 0 {
		return traceReport{}, fmt.Errorf("trace_id %s not found", onlyTraceID)
	}
	for _, node := range groups {
		sort.Strings(node.BatchSpanIDs)
		sort.Strings(node.BatchParentSpanIDs)
		sort.Strings(node.RequestIDs)
		sort.Strings(node.Sources)
		sort.Ints(node.PIDs)
		sort.Slice(node.Spans, func(i, j int) bool {
			if node.Spans[i].StartNS == node.Spans[j].StartNS {
				return node.Spans[i].SpanID < node.Spans[j].SpanID
			}
			return node.Spans[i].StartNS < node.Spans[j].StartNS
		})
		report.Traces = append(report.Traces, *node)
	}
	report.TraceCount = len(report.Traces)
	sort.Slice(report.Traces, func(i, j int) bool {
		if report.Traces[i].TotalNS == report.Traces[j].TotalNS {
			return report.Traces[i].TraceID < report.Traces[j].TraceID
		}
		return report.Traces[i].TotalNS > report.Traces[j].TotalNS
	})
	if limit > 0 && len(report.Traces) > limit {
		report.Traces = report.Traces[:limit]
	}
	return report, nil
}

func printTraceReport(report traceReport) {
	fmt.Printf("trace report input=%s traces=%d traced_spans=%d untraced=%d\n",
		report.Input, report.TraceCount, report.TracedSpans, report.Untraced)
	for _, node := range report.Traces {
		fmt.Printf("TRACE %s spans=%d total_ns=%d requests=%d pids=%s\n",
			node.TraceID, node.SpanCount, node.TotalNS, len(node.RequestIDs), joinTraceInts(node.PIDs))
		if len(node.Sources) > 0 {
			fmt.Printf("  sources=%s\n", strings.Join(node.Sources, ","))
		}
		byParent := map[string][]traceSpanNode{}
		upstreamByParent := map[string][]string{}
		for _, span := range node.Spans {
			byParent[span.ParentSpanID] = append(byParent[span.ParentSpanID], span)
			if span.BatchParentSpanID != "" {
				upstreamByParent[span.ParentSpanID] = appendUnique(upstreamByParent[span.ParentSpanID], span.BatchParentSpanID)
			}
		}
		parents := make([]string, 0, len(byParent))
		for parent := range byParent {
			parents = append(parents, parent)
		}
		sort.Strings(parents)
		for _, parent := range parents {
			label := parent
			if label == "" {
				label = "(local root)"
			}
			upstreams := append([]string(nil), upstreamByParent[parent]...)
			sort.Strings(upstreams)
			for _, upstream := range upstreams {
				fmt.Printf("  upstream %s (not exported as a CANN span)\n", upstream)
			}
			fmt.Printf("    batch %s (not exported as a CANN span)\n", label)
			for _, span := range byParent[parent] {
				fmt.Printf("      %s pid=%d tid=%d duration_ns=%d trace_span=%s event_span=%s\n",
					span.Function, span.PID, span.TID, span.DurationNS, span.TraceSpanID, span.SpanID)
			}
		}
	}
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func appendUniqueInt(values []int, value int) []int {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func joinTraceInts(values []int) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, fmt.Sprintf("%d", value))
	}
	return strings.Join(parts, ",")
}
