package report

import (
	"strings"
	"testing"

	"cann-ebpf/internal/device"
	"cann-ebpf/internal/diagnose"
	"cann-ebpf/internal/runlog"
)

func testSpan(pid, tid int, start, duration int64, function, category, spanID string) runlog.Span {
	return runlog.Span{
		Schema:     runlog.SpanSchemaV1,
		RunID:      "run-test",
		Profile:    "test",
		TsNS:       start,
		StartNS:    start,
		EndNS:      start + duration,
		PID:        pid,
		TID:        tid,
		TGID:       pid,
		Function:   function,
		Category:   category,
		DurationNS: duration,
		SpanID:     spanID,
	}
}

func TestBuildTimelineHeatmapAndCDF(t *testing.T) {
	spans := []runlog.Span{
		testSpan(100, 100, 1_000_000, 2_000_000, "aclrtSynchronizeStream", "sync", "span-a"),
		testSpan(100, 100, 4_000_000, 50_000, "aclrtGetDevice", "handle", "span-b"),
		testSpan(100, 101, 5_000_000, 5_000_000, "aclrtSynchronizeStream", "sync", "span-c"),
	}
	data, err := Build(&diagnose.Input{Path: "test-run", Spans: spans}, Options{SlowThresholdNS: 1_000_000})
	if err != nil {
		t.Fatal(err)
	}
	if data.Summary.Spans != 3 || data.Summary.TIDs != 2 || data.Summary.SlowSpans != 2 {
		t.Fatalf("summary = %+v", data.Summary)
	}
	if len(data.Spans) != 3 || len(data.Timeline.HostRows) != 2 {
		t.Fatalf("spans=%d lanes=%d", len(data.Spans), len(data.Timeline.HostRows))
	}
	if data.Heatmap.Dim != "function" || data.Heatmap.Columns < 1 {
		t.Fatalf("heatmap = %+v", data.Heatmap)
	}
	if len(data.CDF) != 101 {
		t.Fatalf("cdf points = %d", len(data.CDF))
	}
	if data.Functions[0].Function != "aclrtSynchronizeStream" {
		t.Fatalf("top function = %+v", data.Functions[0])
	}
	if data.Summary.P50MS <= 0 || data.Summary.MaxMS < data.Summary.P95MS {
		t.Fatalf("percentiles = %+v", data.Summary)
	}
}

func TestBuildDeviceJoinAndQuadrant(t *testing.T) {
	host := testSpan(200, 200, 1_000_000, 1_500_000, "aclrtSynchronizeStream", "sync", "span-host")
	dev := []device.Span{
		{
			Schema: device.SchemaV1, RunID: "run-test", Kind: device.KindAPI,
			Name: "StreamSynchronize", PID: 200, TID: 200,
			StartNS: 1_000_000, EndNS: 1_500_000, DurationNS: 500_000, CorrelationID: 7,
		},
		{
			Schema: device.SchemaV1, RunID: "run-test", Kind: device.KindKernel,
			Name: "Add_test", PID: 200, TID: 200, DeviceID: 0, StreamID: 92,
			StartNS: 1_100_000, EndNS: 1_450_000, DurationNS: 350_000, CorrelationID: 7,
		},
	}
	data, err := Build(&diagnose.Input{Path: "test-run", Spans: []runlog.Span{host}, DeviceSpans: dev}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if data.Device == nil || !data.Device.Available {
		t.Fatal("device data missing")
	}
	if len(data.Device.Intervals) != 1 || len(data.Device.Hosts) != 1 {
		t.Fatalf("intervals=%d hosts=%d", len(data.Device.Intervals), len(data.Device.Hosts))
	}
	if data.Device.Hosts[0].BusyPct <= 0 || data.Device.Hosts[0].Kernel <= 0 {
		t.Fatalf("host points = %+v", data.Device.Hosts[0])
	}
	if len(data.Device.Busy) == 0 || len(data.Device.Rows) != 1 {
		t.Fatalf("device rows=%d busy=%d", len(data.Device.Rows), len(data.Device.Busy))
	}
}

func TestRenderEscapesScriptPayload(t *testing.T) {
	data, err := Build(&diagnose.Input{
		Path: "test-run",
		Spans: []runlog.Span{
			testSpan(1, 1, 1, 1, "</script><script>alert(1)</script>", "sync", "x"),
		},
	}, Options{Title: "</title><script>alert(1)</script>"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := Render(data)
	if err != nil {
		t.Fatal(err)
	}
	text := string(out)
	if strings.Contains(text, "</title><script>alert(1)</script>") {
		t.Fatal("title was not escaped")
	}
	if strings.Contains(text, "</script><script>alert(1)</script>") {
		t.Fatal("function payload can terminate the data script")
	}
	if !strings.Contains(text, "cann.report.html.v1") {
		t.Fatal("schema missing from rendered report")
	}
}

func TestBuildMaxSpanPointsKeepsSlowestAndFullSummary(t *testing.T) {
	spans := make([]runlog.Span, 0, 1000)
	for i := 0; i < 1000; i++ {
		duration := int64(1_000_000)
		spanID := "span"
		if i == 777 {
			duration = 10_000_000_000
			spanID = "slowest"
		}
		spans = append(spans, testSpan(1, 1, int64(i+1)*1_000_000, duration, "aclrtSynchronizeStream", "sync", spanID))
	}
	data, err := Build(&diagnose.Input{Path: "test-run", Spans: spans}, Options{MaxSpanPoints: 100})
	if err != nil {
		t.Fatal(err)
	}
	if data.Summary.Spans != 1000 {
		t.Fatalf("summary spans = %d, want 1000", data.Summary.Spans)
	}
	if len(data.Spans) != 100 {
		t.Fatalf("embedded spans = %d, want 100", len(data.Spans))
	}
	foundSlowest := false
	for _, point := range data.Spans {
		if point.SpanID == "slowest" {
			foundSlowest = true
		}
	}
	if !foundSlowest {
		t.Fatal("downsample dropped the slowest span")
	}
	if !strings.Contains(strings.Join(data.Warnings, " "), "抽样") {
		t.Fatalf("missing downsample warning: %v", data.Warnings)
	}
}

func TestBuildMaxSpanPointsCapsOffCPURows(t *testing.T) {
	spans := make([]runlog.Span, 0, 50)
	for i := 0; i < 50; i++ {
		span := testSpan(1, 1, int64(i+1)*1_000_000, 1_000_000, "aclrtSynchronizeStream", "sync", "span")
		offCPU := int64(200_000)
		span.OffCPUNS = &offCPU
		spans = append(spans, span)
	}
	data, err := Build(&diagnose.Input{Path: "test-run", Spans: spans}, Options{MaxSpanPoints: 10})
	if err != nil {
		t.Fatal(err)
	}
	if !data.ViewsV2.OffCPU.Truncated || data.ViewsV2.OffCPU.TotalRows != 50 || len(data.ViewsV2.OffCPU.Rows) != 10 {
		t.Fatalf("offcpu rows = %+v", data.ViewsV2.OffCPU)
	}
	if !strings.Contains(strings.Join(data.Warnings, " "), "off-CPU ribbon") {
		t.Fatalf("missing off-CPU truncation warning: %v", data.Warnings)
	}
}
