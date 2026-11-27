package report

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func TestBuildServiceTopologyEmpty(t *testing.T) {
	data := BuildServiceTopology(nil, TopologyOptions{})
	if data.Status.Available {
		t.Fatal("empty topology must be unavailable")
	}
	if data.Status.Reason == "" {
		t.Fatal("unavailable topology needs a reason")
	}
	if data.Status.Source != "none" {
		t.Fatalf("empty topology source = %q, want none", data.Status.Source)
	}
	if data.Status.Confidence != "" {
		t.Fatalf("unavailable topology must not claim confidence: %q", data.Status.Confidence)
	}
	if data.Nodes == nil || len(data.Nodes) != 0 || data.Edges == nil || len(data.Edges) != 0 {
		t.Fatalf("empty topology arrays = %#v / %#v", data.Nodes, data.Edges)
	}
}

func TestBuildServiceTopologyProcessOnly(t *testing.T) {
	spans := []TopologySpan{
		{Service: "vllm", Name: "serve", StartNS: 1, EndNS: 2, Status: "OK"},
		{Service: "vllm", Name: "worker", StartNS: 2, EndNS: 3},
	}
	data := BuildServiceTopology(spans, TopologyOptions{})
	if !data.Status.Available {
		t.Fatalf("process-only topology unavailable: %+v", data.Status)
	}
	if data.Scope != "process" {
		t.Fatalf("scope = %q, want process", data.Scope)
	}
	if data.Status.Confidence != "partial" {
		t.Fatalf("confidence = %q, want partial", data.Status.Confidence)
	}
	if !strings.Contains(data.Status.Reason, "process scope") || !strings.Contains(data.Status.Reason, "end-to-end topology is not claimed") {
		t.Fatalf("reason = %q", data.Status.Reason)
	}
	if data.RootService != "vllm" {
		t.Fatalf("root_service = %q, want vllm", data.RootService)
	}
	if data.Coverage != 0 {
		t.Fatalf("coverage = %v, want 0 without trace IDs", data.Coverage)
	}
	if len(data.Nodes) != 1 || data.Nodes[0].ID != "vllm" || data.Nodes[0].Status != "OK" {
		t.Fatalf("nodes = %+v", data.Nodes)
	}
	if len(data.Edges) != 0 {
		t.Fatalf("edges = %+v, want none without trace IDs", data.Edges)
	}
}

func TestBuildServiceTopologyProcessOnlyWithLocalTrace(t *testing.T) {
	spans := []TopologySpan{
		{TraceID: "t1", SpanID: "s1", Service: "vllm", Name: "serve", StartNS: 1, EndNS: 5},
		{TraceID: "t1", SpanID: "s2", ParentSpanID: "s1", Service: "vllm", Name: "worker", StartNS: 2, EndNS: 3},
	}
	data := BuildServiceTopology(spans, TopologyOptions{})
	if !data.Status.Available || data.Scope != "process" {
		t.Fatalf("local trace scope = %q, status = %+v", data.Scope, data.Status)
	}
	if len(data.Edges) != 0 {
		t.Fatalf("same-service trace produced service edges: %+v", data.Edges)
	}
	if data.Coverage != 1 {
		t.Fatalf("coverage = %v, want 1", data.Coverage)
	}
	if !strings.Contains(data.Status.Reason, "no cross-service parent/child edge") {
		t.Fatalf("reason = %q", data.Status.Reason)
	}
}

func TestBuildServiceTopologyEndToEnd(t *testing.T) {
	spans := []TopologySpan{
		{TraceID: "trace-a", SpanID: "span-1", Service: "gateway", Name: "POST /v1/chat/completions", StartNS: 1_000_000, EndNS: 11_000_000, Status: "OK"},
		{TraceID: "trace-a", SpanID: "span-2", ParentSpanID: "span-1", Service: "litellm", Name: "chat.completions", StartNS: 2_000_000, EndNS: 8_000_000, Status: "OK"},
		{TraceID: "trace-a", SpanID: "span-3", ParentSpanID: "span-2", Service: "vllm", Name: "vllm.generate", StartNS: 3_000_000, EndNS: 7_000_000, Status: "OK"},
	}
	data := BuildServiceTopology(spans, TopologyOptions{})
	if !data.Status.Available {
		t.Fatalf("end-to-end topology unavailable: %+v", data.Status)
	}
	if data.Scope != "end_to_end" {
		t.Fatalf("scope = %q, want end_to_end", data.Scope)
	}
	if data.Status.Confidence != "exact" || data.Status.Reason != "" || data.Status.Source != "otel.jsonl" {
		t.Fatalf("status = %+v", data.Status)
	}
	if data.RootService != "gateway" {
		t.Fatalf("root_service = %q, want gateway", data.RootService)
	}
	if data.Coverage != 1 {
		t.Fatalf("coverage = %v, want 1", data.Coverage)
	}
	if len(data.Nodes) != 3 {
		t.Fatalf("nodes = %+v, want 3", data.Nodes)
	}
	if len(data.Edges) != 2 {
		t.Fatalf("edges = %+v, want 2", data.Edges)
	}
	edges := make(map[string]TopologyEdge, len(data.Edges))
	for _, edge := range data.Edges {
		edges[edge.From+"->"+edge.To] = edge
	}
	gatewayToLiteLLM := edges["gateway->litellm"]
	if gatewayToLiteLLM.Calls != 1 || !topologyCloseEnough(gatewayToLiteLLM.LatencyMS, 6) {
		t.Fatalf("gateway->litellm = %+v", gatewayToLiteLLM)
	}
	litellmToVLLM := edges["litellm->vllm"]
	if litellmToVLLM.Calls != 1 || !topologyCloseEnough(litellmToVLLM.LatencyMS, 4) {
		t.Fatalf("litellm->vllm = %+v", litellmToVLLM)
	}

	payload, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(payload), `"nodes":[{`) || !strings.Contains(string(payload), `"edges":[{`) {
		t.Fatalf("serialized topology arrays missing: %s", payload)
	}
}

func TestBuildServiceTopologyDoesNotInferEdgesFromTiming(t *testing.T) {
	spans := []TopologySpan{
		{Service: "gateway", StartNS: 1, EndNS: 10},
		{Service: "vllm", StartNS: 2, EndNS: 9},
	}
	data := BuildServiceTopology(spans, TopologyOptions{})
	if !data.Status.Available || data.Scope != "process" {
		t.Fatalf("scope = %q status = %+v", data.Scope, data.Status)
	}
	if len(data.Edges) != 0 {
		t.Fatalf("timing co-location produced edges: %+v", data.Edges)
	}
	if len(data.Nodes) != 2 {
		t.Fatalf("nodes = %+v", data.Nodes)
	}
}

func topologyCloseEnough(got, want float64) bool {
	return math.Abs(got-want) <= 1e-9
}
