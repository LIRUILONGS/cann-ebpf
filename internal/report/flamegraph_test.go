package report

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cann-ebpf/internal/stacks"
)

func flamegraphChild(t *testing.T, node *FlamegraphNode, name string) *FlamegraphNode {
	t.Helper()
	for _, child := range node.Children {
		if child.Name == name {
			return child
		}
	}
	t.Fatalf("child %q not found under %q", name, node.Name)
	return nil
}

func flamegraphNodeCount(node *FlamegraphNode) int {
	if node == nil {
		return 0
	}
	count := 1
	for _, child := range node.Children {
		count += flamegraphNodeCount(child)
	}
	return count
}

func flamegraphSelfSum(node *FlamegraphNode) uint64 {
	if node == nil {
		return 0
	}
	total := node.Self
	for _, child := range node.Children {
		total += flamegraphSelfSum(child)
	}
	return total
}

func flamegraphCategorySamples(data *FlamegraphData, name string) uint64 {
	for _, category := range data.Categories {
		if category.Name == name {
			return category.Samples
		}
	}
	return 0
}

func TestBuildFlamegraphCountsSelfOrderingAndNPU(t *testing.T) {
	profile := &stacks.Profile{
		Stacks: []stacks.ResolvedStack{
			{Count: 3, Frames: []stacks.Frame{
				{Name: "main", Category: "app"},
				{Name: "sys_read", Category: "io"},
				{Name: "vfs_read", Category: "io"},
			}},
			{Count: 2, Frames: []stacks.Frame{
				{Name: "main", Category: "app"},
				{Name: "sys_read", Category: "io"},
				{Name: "ext4_file_read", Category: "io"},
			}},
			{Count: 5, Frames: []stacks.Frame{
				{Name: "npu_launch", Category: "npu"},
				{Name: "[unknown]", Unknown: true},
			}},
			{Count: 4, Frames: []stacks.Frame{
				{Name: "npu_launch", Category: "npu"},
			}},
		},
	}
	meta := stacks.Meta{
		Host: "host-a", Kernel: "6.6.0", StartedAt: "2026-11-27T00:00:00Z",
		DurationS: 10, FrequencyHz: 99, Samples: 14, SymbolCoverage: 0.97,
	}
	data := BuildFlamegraph(profile, meta, "runs/stacks-test")
	if !data.Status.Available {
		t.Fatalf("expected available flamegraph: %+v", data.Status)
	}
	if data.Status.Source != "runs/stacks-test" || data.Status.Confidence != "high" {
		t.Fatalf("status = %+v", data.Status)
	}
	if data.Tree == nil || data.Tree.Name != "all" || data.Tree.Count != 14 || data.Tree.Self != 0 {
		t.Fatalf("root = %+v", data.Tree)
	}
	if len(data.Tree.Children) != 2 || data.Tree.Children[0].Name != "npu_launch" || data.Tree.Children[0].Count != 9 {
		t.Fatalf("root children = %+v", data.Tree.Children)
	}
	main := flamegraphChild(t, data.Tree, "main")
	if main.Count != 5 || main.Self != 0 || main.Depth != 1 {
		t.Fatalf("main = %+v", main)
	}
	sysRead := flamegraphChild(t, main, "sys_read")
	if sysRead.Count != 5 || sysRead.Self != 0 {
		t.Fatalf("sys_read = %+v", sysRead)
	}
	if vfs := flamegraphChild(t, sysRead, "vfs_read"); vfs.Count != 3 || vfs.Self != 3 {
		t.Fatalf("vfs_read = %+v", vfs)
	}
	if ext4 := flamegraphChild(t, sysRead, "ext4_file_read"); ext4.Count != 2 || ext4.Self != 2 {
		t.Fatalf("ext4_file_read = %+v", ext4)
	}
	npu := flamegraphChild(t, data.Tree, "npu_launch")
	if npu.Count != 9 || npu.Self != 4 {
		t.Fatalf("npu_launch = %+v", npu)
	}
	unknown := flamegraphChild(t, npu, "[unknown]")
	if unknown.Count != 5 || unknown.Self != 5 || unknown.Category != "unknown" || !unknown.Unknown {
		t.Fatalf("unknown node = %+v", unknown)
	}

	if data.TotalSamples != 14 || data.TotalFrames != 29 {
		t.Fatalf("totals = samples %d frames %d", data.TotalSamples, data.TotalFrames)
	}
	if math.Abs(data.NPUFramePct-31.0344827586) > 1e-6 {
		t.Fatalf("npu frame pct = %f", data.NPUFramePct)
	}
	if got := flamegraphCategorySamples(data, "io"); got != 5 {
		t.Fatalf("io self samples = %d", got)
	}
	if got := flamegraphCategorySamples(data, "unknown"); got != 5 {
		t.Fatalf("unknown self samples = %d", got)
	}
	if got := flamegraphCategorySamples(data, "npu"); got != 4 {
		t.Fatalf("npu self samples = %d", got)
	}
	if flamegraphSelfSum(data.Tree) != 14 {
		t.Fatalf("self sum = %d", flamegraphSelfSum(data.Tree))
	}

	if len(data.TopNPU) != 1 {
		t.Fatalf("top npu = %+v", data.TopNPU)
	}
	top := data.TopNPU[0]
	if top.Name != "npu_launch" || top.Samples != 9 || top.Self != 4 || math.Abs(top.SelfPct-28.5714285714) > 1e-6 {
		t.Fatalf("top npu = %+v", top)
	}
	if len(data.Warnings) == 0 || !strings.Contains(strings.Join(data.Warnings, " "), "unknown") {
		t.Fatalf("expected unknown warning: %v", data.Warnings)
	}
}

func TestBuildFlamegraphNodeLimitMergesTruncated(t *testing.T) {
	profile := &stacks.Profile{}
	for i := 0; i < 20; i++ {
		profile.Stacks = append(profile.Stacks, stacks.ResolvedStack{
			Count: uint64(i + 1),
			Frames: []stacks.Frame{{
				Name: "frame_" + string(rune('a'+i)), Category: "other",
			}},
		})
	}
	data := buildFlamegraphWithLimits(profile, stacks.Meta{}, "test", flamegraphBuildLimits{
		maxDepth: 8,
		maxNodes: 8,
	})
	if !data.Status.Available || data.Tree == nil {
		t.Fatalf("expected available graph: %+v", data.Status)
	}
	if !data.Truncated {
		t.Fatal("expected truncation flag")
	}
	if nodes := flamegraphNodeCount(data.Tree); nodes > 8 {
		t.Fatalf("node count = %d, limit 8", nodes)
	}
	if flamegraphSelfSum(data.Tree) != data.TotalSamples {
		t.Fatalf("self sum %d != total %d", flamegraphSelfSum(data.Tree), data.TotalSamples)
	}
	truncated := flamegraphChild(t, data.Tree, "[truncated]")
	if truncated.Self == 0 {
		t.Fatalf("truncated node lost samples: %+v", truncated)
	}
	if !strings.Contains(strings.Join(data.Warnings, " "), "[truncated]") {
		t.Fatalf("missing truncation warning: %v", data.Warnings)
	}
}

func TestBuildFlamegraphDepthLimitMergesTruncated(t *testing.T) {
	profile := &stacks.Profile{Stacks: []stacks.ResolvedStack{{
		Count: 7,
		Frames: []stacks.Frame{
			{Name: "f1"}, {Name: "f2"}, {Name: "f3"}, {Name: "f4"}, {Name: "f5"},
		},
	}}}
	data := buildFlamegraphWithLimits(profile, stacks.Meta{}, "test", flamegraphBuildLimits{
		maxDepth: 2,
		maxNodes: 64,
	})
	if data.Tree == nil || !data.Truncated {
		t.Fatalf("expected truncated tree: %+v", data)
	}
	first := flamegraphChild(t, data.Tree, "f1")
	second := flamegraphChild(t, first, "f2")
	truncated := flamegraphChild(t, second, "[truncated]")
	if truncated.Depth != 3 || truncated.Count != 7 || truncated.Self != 7 {
		t.Fatalf("depth truncated node = %+v", truncated)
	}
	if flamegraphSelfSum(data.Tree) != 7 || data.TotalSamples != 7 {
		t.Fatalf("self sum = %d total = %d", flamegraphSelfSum(data.Tree), data.TotalSamples)
	}
}

func TestBuildFlamegraphUnavailableReasons(t *testing.T) {
	data := BuildFlamegraph(nil, stacks.Meta{Warnings: []string{"meta warning"}}, "runs/missing")
	if data.Status.Available {
		t.Fatal("nil profile must be unavailable")
	}
	if !strings.Contains(data.Status.Reason, "stacks.folded") {
		t.Fatalf("reason = %q", data.Status.Reason)
	}
	if len(data.Warnings) != 1 || data.Warnings[0] != "meta warning" {
		t.Fatalf("warnings = %v", data.Warnings)
	}
	if data.Tree != nil || len(data.Categories) != 0 || len(data.TopNPU) != 0 {
		t.Fatalf("unavailable graph must not contain a tree: %+v", data)
	}

	data = BuildFlamegraph(&stacks.Profile{Stacks: []stacks.ResolvedStack{{Count: 0}}}, stacks.Meta{}, "runs/empty")
	if data.Status.Available || !strings.Contains(data.Status.Reason, "samples=0") {
		t.Fatalf("empty profile = %+v", data.Status)
	}
}

func TestFlamegraphConfidenceNormalizesFractionsAndPercents(t *testing.T) {
	cases := []struct {
		value float64
		want  string
	}{
		{0.97, "high"},
		{85, "medium"},
		{0.5, "low"},
		{0, "unknown"},
	}
	for _, test := range cases {
		if got := flamegraphConfidence(test.value); got != test.want {
			t.Fatalf("confidence(%v) = %q, want %q", test.value, got, test.want)
		}
	}
}

func TestFlamegraphReclassifiesFoldedNames(t *testing.T) {
	cases := []struct {
		frame stacks.Frame
		want  string
	}{
		{stacks.Frame{Name: "devdrv_submit"}, "npu"},
		{stacks.Frame{Name: "vfs_read"}, "vfs"},
		{stacks.Frame{Name: "ext4_file_read"}, "vfs"},
		{stacks.Frame{Name: "io_schedule"}, "block"},
		{stacks.Frame{Name: "schedule"}, "sync"},
		{stacks.Frame{Name: "tcp_sendmsg"}, "net"},
		{stacks.Frame{Name: "[unknown]", Unknown: true}, "unknown"},
	}
	for _, test := range cases {
		if got := flamegraphFrameCategory(test.frame); got != test.want {
			t.Fatalf("category(%q) = %q, want %q", test.frame.Name, got, test.want)
		}
	}
	if !isNPUFrame(stacks.Frame{Name: "devdrv_submit"}) {
		t.Fatal("devdrv_ frame must count as NPU after reclassification")
	}
}

func TestLoadFlamegraphFromRunDirectoryAndFoldedPath(t *testing.T) {
	dir := t.TempDir()
	profile := &stacks.Profile{
		Stacks: []stacks.ResolvedStack{{
			Count: 3,
			Frames: []stacks.Frame{
				{Name: "main"},
				{Name: "npu_launch"},
				{Name: "devdrv_submit"},
			},
		}},
		Total: 3,
	}
	meta := stacks.Meta{
		Schema:         stacks.SchemaV1,
		Host:           "host-a",
		Kernel:         "6.6.0",
		StartedAt:      "2026-11-27T00:00:00Z",
		DurationS:      5,
		FrequencyHz:    99,
		Samples:        3,
		UniqueStacks:   1,
		SymbolCoverage: 1,
	}
	if err := stacks.WriteRun(dir, meta, nil, profile, false); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{dir, filepath.Join(dir, stacks.FoldedFileName)} {
		data, err := LoadFlamegraph(path)
		if err != nil {
			t.Fatalf("LoadFlamegraph(%s): %v", path, err)
		}
		if !data.Status.Available || data.TotalSamples != 3 || data.Tree == nil {
			t.Fatalf("flamegraph(%s) = %+v", path, data)
		}
		if data.Status.Source != path || data.Meta.Host != "host-a" || data.Meta.FrequencyHz != 99 {
			t.Fatalf("meta(%s) = %+v", path, data)
		}
		if len(data.TopNPU) == 0 {
			t.Fatalf("expected npu classification from folded names: %+v", data)
		}
	}
}

func TestRenderFlamegraphPanelEscapesPayload(t *testing.T) {
	profile := &stacks.Profile{Stacks: []stacks.ResolvedStack{{
		Count: 2,
		Frames: []stacks.Frame{{
			Name:     `</script><img src=x onerror=alert(1)>`,
			Module:   `<script>alert(2)</script>`,
			Category: "other",
		}},
	}}}
	flamegraph := BuildFlamegraph(profile, stacks.Meta{
		Host:    `<img src=x onerror=alert(3)>`,
		Samples: 2,
	}, `runs/<script>alert(4)</script>`)
	data := Data{
		Schema:      SchemaV1,
		GeneratedAt: "2026-11-27T00:00:00Z",
		Input:       "test",
		Title:       "test",
		Window:      Window{StartNS: "0", EndNS: "1000000", DurationMS: 1},
		Categories:  []CategoryRow{},
		Functions:   []FunctionRow{},
		Spans:       []SpanPoint{},
		Timeline:    TimelineData{HostRows: []LaneRow{}, DeviceRows: []LaneRow{}},
		Heatmap:     HeatmapData{Rows: []HeatRow{}},
		CDF:         []CDFPoint{},
		Flamegraph:  flamegraph,
		Warnings:    []string{},
		Notes:       []string{},
	}
	out, err := Render(data)
	if err != nil {
		t.Fatal(err)
	}
	text := string(out)
	if !strings.Contains(text, "flamegraph") || !strings.Contains(text, `id="flamegraphPanel"`) || !strings.Contains(text, "const FLAMEGRAPH") {
		t.Fatal("rendered report does not include the flamegraph panel")
	}
	for _, raw := range []string{
		"</script><img src=x onerror=alert(1)>",
		"<script>alert(2)</script>",
		"<img src=x onerror=alert(3)>",
		"runs/<script>alert(4)</script>",
	} {
		if strings.Contains(text, raw) {
			t.Fatalf("raw payload leaked into HTML: %q", raw)
		}
	}
	if !strings.Contains(text, `\u003cscript\u003e`) {
		t.Fatal("expected JSON-escaped script payload")
	}
}

func TestBuildStacksOnlyReportAndRender(t *testing.T) {
	dir := t.TempDir()
	profile := &stacks.Profile{Stacks: []stacks.ResolvedStack{{
		Count: 4,
		Frames: []stacks.Frame{
			{Name: "main"},
			{Name: "npu_launch"},
		},
	}}, Total: 4}
	meta := stacks.Meta{
		Schema: stacks.SchemaV1, Host: "host-a", Kernel: "6.6.0",
		StartedAt: "2026-11-27T00:00:00Z", DurationS: 10, FrequencyHz: 99,
		Samples: 4, UniqueStacks: 1, SymbolCoverage: 0.75, UnknownPct: 25,
	}
	if err := stacks.WriteRun(dir, meta, nil, profile, false); err != nil {
		t.Fatal(err)
	}
	data, err := Build(nil, Options{StacksPath: dir, Title: "stack report"})
	if err != nil {
		t.Fatal(err)
	}
	if data.Flamegraph == nil || !data.Flamegraph.Status.Available {
		t.Fatalf("flamegraph = %+v", data.Flamegraph)
	}
	if data.Summary.Spans != 0 || data.Title != "stack report" || data.Flamegraph.Meta.Host != "host-a" {
		t.Fatalf("stacks-only data = %+v", data)
	}
	out, err := Render(data)
	if err != nil {
		t.Fatal(err)
	}
	text := string(out)
	if !strings.Contains(text, `id="flamegraphPanel"`) || !strings.Contains(text, "const FLAMEGRAPH") {
		t.Fatal("stacks-only report did not include flamegraph panel")
	}
}

func TestBuildStacksOnlyReportKeepsMissingFoldedUnavailable(t *testing.T) {
	dir := t.TempDir()
	meta := stacks.Meta{
		Schema: stacks.SchemaV1, Host: "host-a", Kernel: "6.6.0",
		StartedAt: "2026-11-27T00:00:00Z", DurationS: 1, FrequencyHz: 99,
	}
	metaData, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, stacks.MetaFileName), metaData, 0o644); err != nil {
		t.Fatal(err)
	}
	data, err := Build(nil, Options{StacksPath: dir})
	if err != nil {
		t.Fatal(err)
	}
	if data.Flamegraph == nil || data.Flamegraph.Status.Available {
		t.Fatalf("flamegraph = %+v", data.Flamegraph)
	}
	if !strings.Contains(data.Flamegraph.Status.Reason, "stacks.folded") {
		t.Fatalf("reason = %q", data.Flamegraph.Status.Reason)
	}
	if _, err := Render(data); err != nil {
		t.Fatal(err)
	}
}

func TestLoadFlamegraphRequiresMeta(t *testing.T) {
	dir := t.TempDir()
	folded := filepath.Join(dir, stacks.FoldedFileName)
	if err := os.WriteFile(folded, []byte("main;sys_read 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFlamegraph(dir); err == nil {
		t.Fatal("folded without meta must fail instead of inventing sampling provenance")
	}
	if _, err := LoadFlamegraph(folded); err == nil {
		t.Fatal("folded path without meta must fail instead of inventing sampling provenance")
	}
}
