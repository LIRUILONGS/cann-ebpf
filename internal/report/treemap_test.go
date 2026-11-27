package report

import (
	"fmt"
	"math"
	"strings"
	"testing"

	"cann-ebpf/internal/device"
	"cann-ebpf/internal/runlog"
)

func newTreemapHostFixture(function, category string, startNS, durationNS int64) runlog.Span {
	return runlog.Span{
		Schema:     runlog.SpanSchemaV1,
		RunID:      "run-treemap-test",
		Profile:    "test",
		TsNS:       startNS,
		StartNS:    startNS,
		EndNS:      startNS + durationNS,
		PID:        100,
		TID:        100,
		TGID:       100,
		Function:   function,
		Category:   category,
		DurationNS: durationNS,
		SpanID:     fmt.Sprintf("span-%d-%s", startNS, function),
	}
}

func newTreemapDeviceFixture(kind, name string, deviceID int, streamID int64, startNS, durationNS int64) device.Span {
	return device.Span{
		Schema:     device.SchemaV1,
		RunID:      "run-treemap-test",
		Kind:       kind,
		Name:       name,
		DeviceID:   deviceID,
		StreamID:   streamID,
		StartNS:    startNS,
		EndNS:      startNS + durationNS,
		DurationNS: durationNS,
	}
}

func treemapChildByName(node *TreemapNode, name string) *TreemapNode {
	if node == nil {
		return nil
	}
	for i := range node.Children {
		if node.Children[i].Name == name {
			return &node.Children[i]
		}
	}
	return nil
}

func treemapChildVariant(node *TreemapNode, kind, deviceLabel, streamLabel string) *TreemapNode {
	if node == nil {
		return nil
	}
	for i := range node.Children {
		child := &node.Children[i]
		if child.Kind == kind && child.Device == deviceLabel && child.Stream == streamLabel {
			return child
		}
	}
	return nil
}

func treemapAlmostEqual(a, b float64) bool {
	return math.Abs(a-b) < 1e-9
}

func TestBuildOperatorTreemapEmpty(t *testing.T) {
	got := BuildOperatorTreemap(nil, nil)
	if got.Status.Available {
		t.Fatal("empty input must be unavailable")
	}
	if got.Status.Reason == "" {
		t.Fatal("empty input must carry a reason")
	}
	if got.Status.Source != "none" {
		t.Fatalf("empty source = %q, want none", got.Status.Source)
	}
	if got.Status.Confidence != "" {
		t.Fatalf("unavailable view must not claim confidence: %q", got.Status.Confidence)
	}
	if got.Root != nil {
		t.Fatal("empty input must not fabricate a root node")
	}

	apiOnly := []device.Span{
		newTreemapDeviceFixture(device.KindAPI, "aclrtSynchronizeStream", 0, 1, 1_000_000, 10_000_000),
	}
	got = BuildOperatorTreemap(nil, apiOnly)
	if got.Status.Available || got.Root != nil {
		t.Fatalf("API-only device input must stay unavailable: %+v", got)
	}
	if got.Status.Reason == "" {
		t.Fatal("API-only device input must explain why it is unavailable")
	}
}

func TestBuildOperatorTreemapDeviceOnly(t *testing.T) {
	devices := []device.Span{
		newTreemapDeviceFixture(device.KindKernel, "MatMul", 0, 7, 1_000_000, 1_500_000),
		newTreemapDeviceFixture(device.KindKernel, "MatMul", 0, 7, 3_000_000, 2_500_000),
		newTreemapDeviceFixture(device.KindKernel, "MatMul", 0, 8, 6_000_000, 2_000_000),
		newTreemapDeviceFixture(device.KindHCCL, "AllReduce", 1, 3, 9_000_000, 1_000_000),
		newTreemapDeviceFixture(device.KindMemcpy, "H2D", 0, 7, 11_000_000, 500_000),
		newTreemapDeviceFixture(device.KindAPI, "aclrtSynchronizeStream", 0, 7, 12_000_000, 999_000_000),
	}
	spans := []runlog.Span{
		newTreemapHostFixture("ignoredHostFunction", "sync", 1_000_000, 100_000_000),
	}
	got := BuildOperatorTreemap(spans, devices)
	if !got.Status.Available {
		t.Fatalf("device treemap unavailable: %s", got.Status.Reason)
	}
	if got.Status.Source != "mspti" {
		t.Fatalf("source = %q, want mspti", got.Status.Source)
	}
	if got.Status.Confidence != "exact" {
		t.Fatalf("confidence = %q, want exact", got.Status.Confidence)
	}
	if got.Root == nil {
		t.Fatal("device treemap root is nil")
	}
	if got.Root.Category != "device" {
		t.Fatalf("root category = %q, want device", got.Root.Category)
	}
	if !strings.Contains(strings.ToLower(got.Root.Name), "operator") {
		t.Fatalf("device root name %q should identify operator data", got.Root.Name)
	}
	if !treemapAlmostEqual(got.Root.DurationMS, 7.5) {
		t.Fatalf("root duration = %v, want 7.5ms (API and host spans must be ignored)", got.Root.DurationMS)
	}
	if got.Root.Count != 5 {
		t.Fatalf("root count = %d, want 5", got.Root.Count)
	}
	wantOrder := []string{"MatMul", "AllReduce", "H2D"}
	if len(got.Root.Children) != len(wantOrder) {
		t.Fatalf("root children = %d, want %d", len(got.Root.Children), len(wantOrder))
	}
	for i, want := range wantOrder {
		if got.Root.Children[i].Name != want {
			t.Fatalf("root child[%d] = %q, want %q", i, got.Root.Children[i].Name, want)
		}
	}
	matmul := treemapChildByName(got.Root, "MatMul")
	if matmul == nil {
		t.Fatal("MatMul node missing")
	}
	if matmul.Kind != device.KindKernel || matmul.Category != device.KindKernel {
		t.Fatalf("MatMul kind/category = %q/%q", matmul.Kind, matmul.Category)
	}
	if matmul.Count != 3 || !treemapAlmostEqual(matmul.DurationMS, 6) {
		t.Fatalf("MatMul count/duration = %d/%v, want 3/6ms", matmul.Count, matmul.DurationMS)
	}
	if len(matmul.Children) != 2 {
		t.Fatalf("MatMul nested children = %d, want 2 stream groups", len(matmul.Children))
	}
	stream7 := treemapChildVariant(matmul, device.KindKernel, "NPU 0", "stream 7")
	if stream7 == nil {
		t.Fatalf("MatMul stream 7 variant missing: %+v", matmul.Children)
	}
	if stream7.Count != 2 || !treemapAlmostEqual(stream7.DurationMS, 4) {
		t.Fatalf("stream 7 count/duration = %d/%v, want 2/4ms", stream7.Count, stream7.DurationMS)
	}
	stream8 := treemapChildVariant(matmul, device.KindKernel, "NPU 0", "stream 8")
	if stream8 == nil {
		t.Fatalf("MatMul stream 8 variant missing: %+v", matmul.Children)
	}
	if stream8.Count != 1 || !treemapAlmostEqual(stream8.DurationMS, 2) {
		t.Fatalf("stream 8 count/duration = %d/%v, want 1/2ms", stream8.Count, stream8.DurationMS)
	}
}

func TestBuildOperatorTreemapHostFallback(t *testing.T) {
	spans := []runlog.Span{
		newTreemapHostFixture("aclrtSynchronizeStream", "sync", 1_000_000, 2_000_000),
		newTreemapHostFixture("aclrtSynchronizeStream", "sync", 4_000_000, 3_000_000),
		newTreemapHostFixture("aclrtMemcpy", "memory", 8_000_000, 1_500_000),
		newTreemapHostFixture("", "", 10_000_000, 500_000),
	}
	devices := []device.Span{
		newTreemapDeviceFixture(device.KindAPI, "aclrtSynchronizeStream", 0, 1, 1_000_000, 99_000_000),
		newTreemapDeviceFixture(device.KindKernel, "ZeroDuration", 0, 0, 2_000_000, 0),
	}
	got := BuildOperatorTreemap(spans, devices)
	if !got.Status.Available {
		t.Fatalf("host fallback unavailable: %s", got.Status.Reason)
	}
	if got.Status.Source != "host" {
		t.Fatalf("source = %q, want host", got.Status.Source)
	}
	if got.Status.Confidence == "" || got.Status.Confidence == "exact" {
		t.Fatalf("host fallback confidence = %q, must be non-exact", got.Status.Confidence)
	}
	if !strings.Contains(strings.ToLower(got.Status.Reason), "not operator-level exact") {
		t.Fatalf("host fallback reason does not disclaim operator-level exactness: %q", got.Status.Reason)
	}
	if got.Root == nil {
		t.Fatal("host fallback root is nil")
	}
	if !strings.Contains(strings.ToLower(got.Root.Name), "host") {
		t.Fatalf("host root name = %q, want host wording", got.Root.Name)
	}
	if strings.Contains(strings.ToLower(got.Root.Name), "operator") {
		t.Fatalf("host root must not be labeled as an operator treemap: %q", got.Root.Name)
	}
	if !treemapAlmostEqual(got.Root.DurationMS, 7) {
		t.Fatalf("root duration = %v, want 7ms", got.Root.DurationMS)
	}
	if got.Root.Count != 4 {
		t.Fatalf("root count = %d, want 4", got.Root.Count)
	}
	wantOrder := []string{"aclrtSynchronizeStream", "aclrtMemcpy", "(unknown)"}
	if len(got.Root.Children) != len(wantOrder) {
		t.Fatalf("root children = %d, want %d", len(got.Root.Children), len(wantOrder))
	}
	for i, want := range wantOrder {
		if got.Root.Children[i].Name != want {
			t.Fatalf("root child[%d] = %q, want %q", i, got.Root.Children[i].Name, want)
		}
	}
	sync := treemapChildByName(got.Root, "aclrtSynchronizeStream")
	if sync == nil {
		t.Fatal("host sync function node missing")
	}
	if sync.Category != "sync" || sync.Count != 2 || !treemapAlmostEqual(sync.DurationMS, 5) {
		t.Fatalf("sync node = category %q count %d duration %v", sync.Category, sync.Count, sync.DurationMS)
	}
	if len(sync.Children) != 1 || sync.Children[0].Category != "sync" || sync.Children[0].Count != 2 {
		t.Fatalf("sync category children = %+v", sync.Children)
	}
	unknown := treemapChildByName(got.Root, "(unknown)")
	if unknown == nil || unknown.Category != "other" {
		t.Fatalf("unknown function must fall back to category other: %+v", unknown)
	}
}

func TestBuildOperatorTreemapNestedChildren(t *testing.T) {
	devices := []device.Span{
		newTreemapDeviceFixture(device.KindKernel, "Grouped", 0, 1, 1_000_000, 1_000_000),
		newTreemapDeviceFixture(device.KindKernel, "Grouped", 0, 1, 3_000_000, 2_000_000),
		newTreemapDeviceFixture(device.KindMemcpy, "Grouped", 0, 2, 6_000_000, 1_500_000),
		newTreemapDeviceFixture(device.KindKernel, "Grouped", 1, 1, 8_000_000, 500_000),
	}
	got := BuildOperatorTreemap(nil, devices)
	if !got.Status.Available || got.Root == nil {
		t.Fatalf("nested treemap unavailable: %+v", got.Status)
	}
	if len(got.Root.Children) != 1 {
		t.Fatalf("root children = %d, want one operator", len(got.Root.Children))
	}
	group := &got.Root.Children[0]
	if group.Name != "Grouped" || group.Count != 4 || !treemapAlmostEqual(group.DurationMS, 5) {
		t.Fatalf("group node = %+v", group)
	}
	if group.Kind != "" {
		t.Fatalf("mixed-kind group kind = %q, want empty", group.Kind)
	}
	if len(group.Children) != 3 {
		t.Fatalf("group children = %d, want 3 kind/device/stream variants", len(group.Children))
	}
	want := []struct {
		kind   string
		device string
		stream string
		count  int
		ms     float64
	}{
		{device.KindKernel, "NPU 0", "stream 1", 2, 3},
		{device.KindMemcpy, "NPU 0", "stream 2", 1, 1.5},
		{device.KindKernel, "NPU 1", "stream 1", 1, 0.5},
	}
	for i, expected := range want {
		child := group.Children[i]
		if child.Kind != expected.kind || child.Device != expected.device || child.Stream != expected.stream {
			t.Fatalf("child[%d] = kind %q device %q stream %q", i, child.Kind, child.Device, child.Stream)
		}
		if child.Count != expected.count || !treemapAlmostEqual(child.DurationMS, expected.ms) {
			t.Fatalf("child[%d] count/duration = %d/%v, want %d/%v", i, child.Count, child.DurationMS, expected.count, expected.ms)
		}
		if len(child.Children) != 0 {
			t.Fatalf("leaf child[%d] unexpectedly has nested children", i)
		}
	}
}

func TestBuildOperatorTreemapCapsTopLevelBreadth(t *testing.T) {
	total := treemapMaxTopNodes + 5
	devices := make([]device.Span, 0, total)
	var wantTotalNS int64
	for i := 0; i < total; i++ {
		durationNS := int64(total-i) * 1_000_000
		devices = append(devices, newTreemapDeviceFixture(
			device.KindKernel,
			fmt.Sprintf("op-%02d", i),
			0,
			1,
			int64(i+1)*10_000_000,
			durationNS,
		))
		wantTotalNS += durationNS
	}
	got := BuildOperatorTreemap(nil, devices)
	if !got.Status.Available || got.Root == nil {
		t.Fatalf("capped treemap unavailable: %+v", got.Status)
	}
	if len(got.Root.Children) != treemapMaxTopNodes {
		t.Fatalf("root breadth = %d, want cap %d", len(got.Root.Children), treemapMaxTopNodes)
	}
	last := got.Root.Children[len(got.Root.Children)-1]
	if last.Name != treemapOtherName {
		t.Fatalf("last child = %q, want %q", last.Name, treemapOtherName)
	}
	var tailNS int64
	for i := treemapMaxTopNodes - 1; i < total; i++ {
		tailNS += int64(total-i) * 1_000_000
	}
	tailCount := total - (treemapMaxTopNodes - 1)
	if !treemapAlmostEqual(last.DurationMS, float64(tailNS)/1e6) {
		t.Fatalf("other duration = %v, want %vms", last.DurationMS, float64(tailNS)/1e6)
	}
	if last.Count != tailCount {
		t.Fatalf("other count = %d, want %d", last.Count, tailCount)
	}
	if len(last.Children) != 0 {
		t.Fatal("other node must be a flat aggregate")
	}
	if got.Root.Children[0].Name != "op-00" {
		t.Fatalf("first child = %q, want op-00", got.Root.Children[0].Name)
	}
	if !treemapAlmostEqual(got.Root.DurationMS, float64(wantTotalNS)/1e6) {
		t.Fatalf("root duration = %v, want %vms", got.Root.DurationMS, float64(wantTotalNS)/1e6)
	}
	if got.Root.Count != total {
		t.Fatalf("root count = %d, want %d", got.Root.Count, total)
	}
	var childTotalMS float64
	for _, child := range got.Root.Children {
		childTotalMS += child.DurationMS
	}
	if !treemapAlmostEqual(childTotalMS, got.Root.DurationMS) {
		t.Fatalf("children sum = %v, root = %v", childTotalMS, got.Root.DurationMS)
	}
}
