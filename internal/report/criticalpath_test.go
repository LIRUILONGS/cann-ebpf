package report

import (
	"math"
	"strings"
	"testing"

	"cann-ebpf/internal/device"
	"cann-ebpf/internal/runlog"
	"cann-ebpf/internal/tracectx"
)

// All fixtures in this file are synthetic. They do not read real run evidence.
const syntheticMillisecondNS int64 = 1_000_000

func syntheticHostSpan(spanID, batchID, function, category string, pid, tid int, startNS, durationNS int64) runlog.Span {
	return runlog.Span{
		Schema:     runlog.SpanSchemaV1,
		RunID:      "synthetic-run",
		Profile:    "synthetic-vllm",
		TsNS:       startNS,
		StartNS:    startNS,
		EndNS:      startNS + durationNS,
		PID:        pid,
		TID:        tid,
		TGID:       pid,
		Function:   function,
		APIName:    function,
		Library:    "synthetic-lib",
		Category:   category,
		DurationNS: durationNS,
		BatchID:    batchID,
		SpanID:     spanID,
		Seq:        1,
	}
}

func syntheticAPIRecord(name string, pid, tid int, startNS, durationNS, correlationID int64) device.Span {
	return device.Span{
		Schema:        device.SchemaV1,
		RunID:         "synthetic-run",
		Kind:          device.KindAPI,
		Name:          name,
		PID:           pid,
		TID:           tid,
		StartNS:       startNS,
		EndNS:         startNS + durationNS,
		DurationNS:    durationNS,
		CorrelationID: correlationID,
		Source:        "synthetic-mspti",
	}
}

func syntheticKernelRecord(name string, deviceID int, streamID, correlationID, startNS, durationNS int64) device.Span {
	return device.Span{
		Schema:        device.SchemaV1,
		RunID:         "synthetic-run",
		DeviceID:      deviceID,
		StreamID:      streamID,
		Kind:          device.KindKernel,
		Name:          name,
		StartNS:       startNS,
		EndNS:         startNS + durationNS,
		DurationNS:    durationNS,
		CorrelationID: correlationID,
		Source:        "synthetic-mspti",
	}
}

func syntheticTrace(traceID, spanID, parentSpanID string) *tracectx.Context {
	return &tracectx.Context{
		TraceID:      traceID,
		SpanID:       spanID,
		ParentSpanID: parentSpanID,
		Source:       "synthetic",
	}
}

func assertSyntheticClose(t *testing.T, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-6 {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func syntheticNodeByLabel(t *testing.T, nodes []CriticalPathNode, label string) CriticalPathNode {
	t.Helper()
	for _, node := range nodes {
		if node.Label == label {
			return node
		}
	}
	t.Fatalf("node with label %q not found in %+v", label, nodes)
	return CriticalPathNode{}
}

func syntheticNodeByID(nodes []CriticalPathNode, id string) (CriticalPathNode, bool) {
	for _, node := range nodes {
		if node.ID == id {
			return node, true
		}
	}
	return CriticalPathNode{}, false
}

func TestBuildCriticalPathEmptySynthetic(t *testing.T) {
	cp := BuildCriticalPath(nil, nil)
	if cp.Status.Available {
		t.Fatal("empty input must be unavailable")
	}
	if !strings.Contains(cp.Status.Reason, "no host spans") {
		t.Fatalf("reason = %q", cp.Status.Reason)
	}
	if cp.Status.Confidence != "" {
		t.Fatalf("unavailable confidence = %q, want empty", cp.Status.Confidence)
	}
	if cp.Nodes == nil || len(cp.Nodes) != 0 {
		t.Fatalf("nodes = %#v, want empty non-nil slice", cp.Nodes)
	}
	if cp.CriticalPath == nil || len(cp.CriticalPath) != 0 {
		t.Fatalf("critical path = %#v, want empty non-nil slice", cp.CriticalPath)
	}

	summed := BuildSummedDelay(cp)
	if summed.Status.Available || len(summed.NodesByCategory) != 0 || summed.NodesByCategory == nil {
		t.Fatalf("summed delay = %+v", summed)
	}
	slack := BuildSlackCDF(cp)
	if slack.Status.Available || len(slack.Points) != 0 || slack.Points == nil {
		t.Fatalf("slack cdf = %+v", slack)
	}
}

func TestBuildCriticalPathNoContextSynthetic(t *testing.T) {
	start := int64(1_000_000_000)
	span := syntheticHostSpan("synthetic-no-context", "", "aclrtLaunchKernel", "compute", 101, 101, start, 10*syntheticMillisecondNS)
	devices := []device.Span{
		syntheticAPIRecord("aclrtLaunchKernel", 101, 101, start, syntheticMillisecondNS, 1),
		syntheticKernelRecord("kernel-no-context", 0, 1, 1, start, 10*syntheticMillisecondNS),
	}

	cp := BuildCriticalPath([]runlog.Span{span}, devices)
	if cp.Status.Available {
		t.Fatal("spans without batch/request/trace context must be unavailable")
	}
	if !strings.Contains(cp.Status.Reason, "batch/request/trace context") {
		t.Fatalf("reason = %q", cp.Status.Reason)
	}
	if cp.Status.Confidence != "" {
		t.Fatalf("unavailable confidence = %q, want empty", cp.Status.Confidence)
	}
}

func TestBuildCriticalPathNoDeviceCorrelationSynthetic(t *testing.T) {
	start := int64(2_000_000_000)
	span := syntheticHostSpan("synthetic-no-device", "batch-no-device", "aclrtLaunchKernel", "compute", 102, 102, start, 8*syntheticMillisecondNS)

	t.Run("device_spans_absent", func(t *testing.T) {
		cp := BuildCriticalPath([]runlog.Span{span}, nil)
		if cp.Status.Available {
			t.Fatal("missing device spans must be unavailable")
		}
		if !strings.Contains(cp.Status.Reason, "no device correlation") {
			t.Fatalf("reason = %q", cp.Status.Reason)
		}
		if cp.Status.Confidence != "" {
			t.Fatalf("unavailable confidence = %q, want empty", cp.Status.Confidence)
		}
	})

	t.Run("no_matching_device_interval", func(t *testing.T) {
		devices := []device.Span{
			syntheticAPIRecord("aclrtMemcpy", 102, 102, start, syntheticMillisecondNS, 2),
			syntheticKernelRecord("kernel-other", 1, 2, 2, start, 8*syntheticMillisecondNS),
		}
		cp := BuildCriticalPath([]runlog.Span{span}, devices)
		if cp.Status.Available {
			t.Fatal("unmatched device data must be unavailable")
		}
		if !strings.Contains(cp.Status.Reason, "no device correlation") {
			t.Fatalf("reason = %q", cp.Status.Reason)
		}
	})

	t.Run("api_record_without_intervals", func(t *testing.T) {
		devices := []device.Span{
			syntheticAPIRecord("aclrtLaunchKernel", 102, 102, start, syntheticMillisecondNS, 3),
		}
		cp := BuildCriticalPath([]runlog.Span{span}, devices)
		if cp.Status.Available {
			t.Fatal("API-only device data must be unavailable")
		}
		if !strings.Contains(cp.Status.Reason, "no device correlation") {
			t.Fatalf("reason = %q", cp.Status.Reason)
		}
	})
}

func TestBuildCriticalPathSimpleChainSynthetic(t *testing.T) {
	start := int64(3_000_000_000)
	spans := []runlog.Span{
		syntheticHostSpan("synthetic-chain-1", "batch-simple", "aclrtLaunchKernel", "compute", 200, 200, start, 4*syntheticMillisecondNS),
		syntheticHostSpan("synthetic-chain-2", "batch-simple", "aclrtLaunchKernel", "compute", 200, 200, start+4*syntheticMillisecondNS, 3*syntheticMillisecondNS),
		syntheticHostSpan("synthetic-chain-3", "batch-simple", "aclrtLaunchKernel", "compute", 200, 200, start+7*syntheticMillisecondNS, 3*syntheticMillisecondNS),
	}
	devices := []device.Span{
		syntheticAPIRecord("aclrtLaunchKernel", 200, 200, start, syntheticMillisecondNS, 10),
		syntheticKernelRecord("kernel-simple-1", 0, 7, 10, start, 4*syntheticMillisecondNS),
		syntheticAPIRecord("aclrtLaunchKernel", 200, 200, start+4*syntheticMillisecondNS, syntheticMillisecondNS, 11),
		syntheticKernelRecord("kernel-simple-2", 0, 7, 11, start+4*syntheticMillisecondNS, 3*syntheticMillisecondNS),
		syntheticAPIRecord("aclrtLaunchKernel", 200, 200, start+7*syntheticMillisecondNS, syntheticMillisecondNS, 12),
		syntheticKernelRecord("kernel-simple-3", 0, 7, 12, start+7*syntheticMillisecondNS, 3*syntheticMillisecondNS),
	}

	cp := BuildCriticalPath(spans, devices)
	if !cp.Status.Available {
		t.Fatalf("status = %+v", cp.Status)
	}
	if cp.BatchID != "batch-simple" || cp.TraceID != "" || cp.RequestID != "" {
		t.Fatalf("context = batch=%q trace=%q request=%q", cp.BatchID, cp.TraceID, cp.RequestID)
	}
	if cp.Status.Confidence != criticalPathConfidenceInferred {
		t.Fatalf("confidence = %q", cp.Status.Confidence)
	}
	if !strings.Contains(cp.Status.Reason, "inferred") || !strings.Contains(cp.Status.Reason, "stream/event") {
		t.Fatalf("reason = %q", cp.Status.Reason)
	}
	if len(cp.Nodes) != 6 {
		t.Fatalf("nodes = %d, want 6", len(cp.Nodes))
	}
	if len(cp.CriticalPath) != 3 {
		t.Fatalf("critical path = %v, want 3 nodes", cp.CriticalPath)
	}
	assertSyntheticClose(t, cp.TotalMS, 10)
	assertSyntheticClose(t, cp.Coverage, 1)
	for _, node := range cp.Nodes {
		if node.Confidence != criticalPathConfidenceInferred {
			t.Fatalf("node %q confidence = %q", node.ID, node.Confidence)
		}
	}
	for _, id := range cp.CriticalPath {
		node, ok := syntheticNodeByID(cp.Nodes, id)
		if !ok || node.Kind != "KERNEL" {
			t.Fatalf("critical path node %q = %+v", id, node)
		}
	}

	summed := BuildSummedDelay(cp)
	if !summed.Status.Available {
		t.Fatalf("summed status = %+v", summed.Status)
	}
	assertSyntheticClose(t, summed.SumMS, 10)
	assertSyntheticClose(t, summed.CriticalPathMS, 10)
	assertSyntheticClose(t, summed.OverlapMS, 0)
	assertSyntheticClose(t, summed.Parallelism, 1)
	if len(summed.NodesByCategory) != 1 || summed.NodesByCategory[0].Category != "kernel" ||
		summed.NodesByCategory[0].Count != 3 {
		t.Fatalf("categories = %+v", summed.NodesByCategory)
	}

	slack := BuildSlackCDF(cp)
	if slack.Status.Available {
		t.Fatal("six-node synthetic chain must not produce a 30-sample slack CDF")
	}
	if !strings.Contains(slack.Status.Reason, "sample") || !strings.Contains(slack.Status.Reason, "30") {
		t.Fatalf("reason = %q", slack.Status.Reason)
	}
}

func TestBuildCriticalPathSingleSpanSynthetic(t *testing.T) {
	start := int64(4_000_000_000)
	span := syntheticHostSpan("synthetic-single", "batch-single", "aclrtLaunchKernel", "compute", 201, 201, start, 6*syntheticMillisecondNS)
	devices := []device.Span{
		syntheticAPIRecord("aclrtLaunchKernel", 201, 201, start, syntheticMillisecondNS, 20),
		syntheticKernelRecord("kernel-single", 0, 3, 20, start, 6*syntheticMillisecondNS),
	}
	cp := BuildCriticalPath([]runlog.Span{span}, devices)
	if !cp.Status.Available {
		t.Fatalf("status = %+v", cp.Status)
	}
	if len(cp.CriticalPath) != 1 || len(cp.Nodes) != 2 {
		t.Fatalf("critical path=%v nodes=%d", cp.CriticalPath, len(cp.Nodes))
	}
	assertSyntheticClose(t, cp.TotalMS, 6)
	assertSyntheticClose(t, cp.Coverage, 1)
}

func TestBuildCriticalPathParallelBranchesSynthetic(t *testing.T) {
	start := int64(5_000_000_000)
	spans := []runlog.Span{
		syntheticHostSpan("synthetic-parallel-a", "batch-parallel", "aclrtLaunchKernel", "compute", 300, 300, start, 12*syntheticMillisecondNS),
		syntheticHostSpan("synthetic-parallel-b", "batch-parallel", "aclrtLaunchKernel", "compute", 300, 301, start, 5*syntheticMillisecondNS),
	}
	devices := []device.Span{
		syntheticAPIRecord("aclrtLaunchKernel", 300, 300, start, syntheticMillisecondNS, 30),
		syntheticKernelRecord("kernel-parallel-a", 0, 1, 30, start, 12*syntheticMillisecondNS),
		syntheticAPIRecord("aclrtLaunchKernel", 300, 301, start, syntheticMillisecondNS, 31),
		syntheticKernelRecord("kernel-parallel-b", 0, 2, 31, start, 5*syntheticMillisecondNS),
	}

	cp := BuildCriticalPath(spans, devices)
	if !cp.Status.Available {
		t.Fatalf("status = %+v", cp.Status)
	}
	if len(cp.Nodes) != 4 || len(cp.CriticalPath) != 1 {
		t.Fatalf("nodes=%d critical path=%v", len(cp.Nodes), cp.CriticalPath)
	}
	assertSyntheticClose(t, cp.TotalMS, 12)
	assertSyntheticClose(t, cp.Coverage, 1)
	criticalNode, ok := syntheticNodeByID(cp.Nodes, cp.CriticalPath[0])
	if !ok || criticalNode.Label != "kernel-parallel-a" {
		t.Fatalf("critical node = %+v", criticalNode)
	}

	summed := BuildSummedDelay(cp)
	if !summed.Status.Available {
		t.Fatalf("summed status = %+v", summed.Status)
	}
	assertSyntheticClose(t, summed.SumMS, 17)
	assertSyntheticClose(t, summed.CriticalPathMS, 12)
	assertSyntheticClose(t, summed.OverlapMS, 5)
	assertSyntheticClose(t, summed.Parallelism, 17.0/12.0)
}

func TestBuildCriticalPathRequestContextSynthetic(t *testing.T) {
	start := int64(6_000_000_000)
	span := syntheticHostSpan("synthetic-request", "", "aclrtLaunchKernel", "compute", 400, 400, start, 7*syntheticMillisecondNS)
	span.RequestIDs = []string{"req-42"}
	devices := []device.Span{
		syntheticAPIRecord("aclrtLaunchKernel", 400, 400, start, syntheticMillisecondNS, 40),
		syntheticKernelRecord("kernel-request", 0, 4, 40, start, 7*syntheticMillisecondNS),
	}
	cp := BuildCriticalPath([]runlog.Span{span}, devices)
	if !cp.Status.Available {
		t.Fatalf("status = %+v", cp.Status)
	}
	if cp.RequestID != "req-42" || cp.BatchID != "" || cp.TraceID != "" {
		t.Fatalf("context = batch=%q trace=%q request=%q", cp.BatchID, cp.TraceID, cp.RequestID)
	}
}

func TestBuildCriticalPathTraceContextInferredSynthetic(t *testing.T) {
	start := int64(7_000_000_000)
	traceID := strings.Repeat("a", 32)
	spanOneID := strings.Repeat("b", 16)
	spanTwoID := strings.Repeat("c", 16)
	spanOne := syntheticHostSpan("synthetic-trace-1", "", "aclrtLaunchKernel", "compute", 500, 500, start, 4*syntheticMillisecondNS)
	spanOne.Trace = syntheticTrace(traceID, spanOneID, strings.Repeat("d", 16))
	spanTwo := syntheticHostSpan("synthetic-trace-2", "", "aclrtLaunchKernel", "compute", 500, 500, start+4*syntheticMillisecondNS, 6*syntheticMillisecondNS)
	spanTwo.Trace = syntheticTrace(traceID, spanTwoID, spanOneID)
	devices := []device.Span{
		syntheticAPIRecord("aclrtLaunchKernel", 500, 500, start, syntheticMillisecondNS, 50),
		syntheticKernelRecord("kernel-trace-1", 0, 5, 50, start, 4*syntheticMillisecondNS),
		syntheticAPIRecord("aclrtLaunchKernel", 500, 500, start+4*syntheticMillisecondNS, syntheticMillisecondNS, 51),
		syntheticKernelRecord("kernel-trace-2", 0, 5, 51, start+4*syntheticMillisecondNS, 6*syntheticMillisecondNS),
	}

	cp := BuildCriticalPath([]runlog.Span{spanOne, spanTwo}, devices)
	if !cp.Status.Available {
		t.Fatalf("status = %+v", cp.Status)
	}
	if cp.TraceID != traceID || cp.BatchID != "" {
		t.Fatalf("context = batch=%q trace=%q", cp.BatchID, cp.TraceID)
	}
	if cp.Status.Confidence != criticalPathConfidenceInferred || cp.Status.Confidence == "exact" {
		t.Fatalf("confidence = %q", cp.Status.Confidence)
	}
	if !strings.Contains(cp.Status.Reason, "stream/event") {
		t.Fatalf("reason = %q", cp.Status.Reason)
	}
	firstHost, ok := syntheticNodeByID(cp.Nodes, "host:"+spanOne.SpanID)
	if !ok {
		t.Fatalf("missing host node for span %q", spanOne.SpanID)
	}
	_ = firstHost
	var secondHost *CriticalPathNode
	for i := range cp.Nodes {
		if cp.Nodes[i].ID == "host:"+spanTwo.SpanID {
			secondHost = &cp.Nodes[i]
		}
	}
	if secondHost == nil || secondHost.Parent != "host:"+spanOne.SpanID {
		t.Fatalf("second host parent = %+v", secondHost)
	}
}

func TestBuildSummedDelaySynthetic(t *testing.T) {
	cp := CriticalPathData{
		Status: ViewStatus{
			Available:  true,
			Reason:     "synthetic inferred DAG",
			Source:     "synthetic",
			Confidence: criticalPathConfidenceInferred,
		},
		Nodes: []CriticalPathNode{
			{ID: "host-a", Label: "host-a", Category: "compute", Kind: "host", StartMS: 0, DurationMS: 10},
			{ID: "kernel-a1", Parent: "host-a", Label: "kernel-a1", Category: "kernel", Kind: "KERNEL", StartMS: 0, DurationMS: 6},
			{ID: "kernel-a2", Parent: "host-a", Label: "kernel-a2", Category: "kernel", Kind: "KERNEL", StartMS: 6, DurationMS: 4},
			{ID: "host-b", Label: "host-b", Category: "sync", Kind: "host", StartMS: 0, DurationMS: 5},
		},
		CriticalPath: []string{"kernel-a1", "kernel-a2"},
		TotalMS:      10,
	}
	summed := BuildSummedDelay(cp)
	if !summed.Status.Available {
		t.Fatalf("status = %+v", summed.Status)
	}
	assertSyntheticClose(t, summed.SumMS, 15)
	assertSyntheticClose(t, summed.CriticalPathMS, 10)
	assertSyntheticClose(t, summed.OverlapMS, 5)
	assertSyntheticClose(t, summed.Parallelism, 1.5)
	if len(summed.NodesByCategory) != 2 {
		t.Fatalf("categories = %+v", summed.NodesByCategory)
	}
	if summed.NodesByCategory[0].Category != "kernel" || summed.NodesByCategory[0].Count != 2 {
		t.Fatalf("first category = %+v", summed.NodesByCategory[0])
	}
	assertSyntheticClose(t, summed.NodesByCategory[0].TotalMS, 10)
	if summed.NodesByCategory[1].Category != "sync" || summed.NodesByCategory[1].Count != 1 {
		t.Fatalf("second category = %+v", summed.NodesByCategory[1])
	}
}

func TestBuildSummedDelayUnavailableSynthetic(t *testing.T) {
	cp := CriticalPathData{
		Status: ViewStatus{Available: false, Reason: "not implemented", Source: "none"},
		Nodes:  []CriticalPathNode{{ID: "ignored", DurationMS: 100}},
	}
	summed := BuildSummedDelay(cp)
	if summed.Status.Available {
		t.Fatal("unavailable critical path must make summed delay unavailable")
	}
	if !strings.Contains(summed.Status.Reason, "critical path unavailable") {
		t.Fatalf("reason = %q", summed.Status.Reason)
	}
	if len(summed.NodesByCategory) != 0 || summed.NodesByCategory == nil {
		t.Fatalf("categories = %#v", summed.NodesByCategory)
	}
}

func TestBuildCriticalPathSyntheticNodeLabelsAreStable(t *testing.T) {
	start := int64(8_000_000_000)
	span := syntheticHostSpan("synthetic-labels", "batch-labels", "aclrtLaunchKernel", "compute", 600, 600, start, 5*syntheticMillisecondNS)
	devices := []device.Span{
		syntheticAPIRecord("aclrtLaunchKernel", 600, 600, start, syntheticMillisecondNS, 60),
		syntheticKernelRecord("kernel-labels", 2, 6, 60, start, 5*syntheticMillisecondNS),
	}
	cp := BuildCriticalPath([]runlog.Span{span}, devices)
	if !cp.Status.Available {
		t.Fatalf("status = %+v", cp.Status)
	}
	host := syntheticNodeByLabel(t, cp.Nodes, "aclrtLaunchKernel")
	if host.Kind != "host" || host.Category != "compute" {
		t.Fatalf("host node = %+v", host)
	}
	kernel := syntheticNodeByLabel(t, cp.Nodes, "kernel-labels")
	if kernel.Kind != "KERNEL" || kernel.Category != "kernel" || kernel.Device != "NPU-2" {
		t.Fatalf("kernel node = %+v", kernel)
	}
	if kernel.Parent != host.ID {
		t.Fatalf("kernel parent = %q, host id = %q", kernel.Parent, host.ID)
	}
}

func TestBuildCriticalPathMultipleBatchesSynthetic(t *testing.T) {
	start := int64(9_000_000_000)
	matched := syntheticHostSpan("synthetic-batch-matched", "batch-matched", "aclrtLaunchKernel", "compute", 700, 700, start, 4*syntheticMillisecondNS)
	unmatched := syntheticHostSpan("synthetic-batch-unmatched", "batch-unmatched", "aclrtLaunchKernel", "compute", 701, 701, start, 20*syntheticMillisecondNS)
	devices := []device.Span{
		syntheticAPIRecord("aclrtLaunchKernel", 700, 700, start, syntheticMillisecondNS, 70),
		syntheticKernelRecord("kernel-matched", 0, 7, 70, start, 4*syntheticMillisecondNS),
	}
	cp := BuildCriticalPath([]runlog.Span{unmatched, matched}, devices)
	if !cp.Status.Available {
		t.Fatalf("status = %+v", cp.Status)
	}
	if cp.BatchID != "batch-matched" {
		t.Fatalf("batch = %q, want batch-matched", cp.BatchID)
	}
	if len(cp.Nodes) != 2 {
		t.Fatalf("nodes = %d, want only the correlated batch", len(cp.Nodes))
	}
}
