package report

import (
	"crypto/sha256"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cann-ebpf/internal/diagnose"
	"cann-ebpf/internal/runlog"
)

// All fixtures and generated files in this test are synthetic. They exercise
// integration boundaries only and are not production vLLM evidence.

func qaViews2FixturePath(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "views2", name)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("synthetic fixture %s: %v", path, err)
	}
	return path
}

func qaWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func qaCopyFixture(t *testing.T, targetDir, fixtureName, targetName string) string {
	t.Helper()
	raw, err := os.ReadFile(qaViews2FixturePath(t, fixtureName))
	if err != nil {
		t.Fatalf("read synthetic fixture %s: %v", fixtureName, err)
	}
	path := filepath.Join(targetDir, targetName)
	qaWriteFile(t, path, string(raw))
	return path
}

func qaAlmostEqual(a, b, tolerance float64) bool {
	return math.Abs(a-b) <= tolerance
}

func qaFileSHA256(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("%x", sum[:])
}

func qaAssertUnavailable(t *testing.T, name string, status ViewStatus) {
	t.Helper()
	if status.Available {
		t.Fatalf("%s: expected unavailable, got %+v", name, status)
	}
	if strings.TrimSpace(status.Reason) == "" {
		t.Fatalf("%s: unavailable status needs a reason", name)
	}
	if status.Source != "none" {
		t.Fatalf("%s: unavailable source = %q, want none", name, status.Source)
	}
	if status.Confidence != "" {
		t.Fatalf("%s: unavailable status must not claim confidence", name)
	}
}

func qaWriteSyntheticMetrics(t *testing.T, dir string, base float64) {
	t.Helper()
	var body strings.Builder
	for i := 0; i < 3; i++ {
		monoNS := int64(1_000_000 + i*1_000_000_000)
		wallNS := int64(1_700_000_000_000_000_000) + monoNS
		fmt.Fprintf(&body,
			`{"schema":"cann.metrics.v1","mono_ns":%d,"wall_ns":%d,"source":"qa-synthetic","name":"vllm_requests_running","kind":"gauge","value":%g,"unit":"count"}`+"\n",
			monoNS, wallNS, base+float64(i))
	}
	qaWriteFile(t, filepath.Join(dir, "metrics.jsonl"), body.String())
}

func TestViewsV2IntegrationAutoDiscoversOptionalEvidence(t *testing.T) {
	runDir := t.TempDir()
	names := []string{"sweep.json", "ops.jsonl", "events.jsonl", "offcpu.jsonl", "otel.jsonl"}
	for _, name := range names {
		qaWriteFile(t, filepath.Join(runDir, name), "{}\n")
	}

	paths := resolveViewsV2Paths(Options{}, runDir)
	checks := []struct {
		name string
		got  string
	}{
		{"sweep", paths.sweep},
		{"ops", paths.ops},
		{"events", paths.events},
		{"offcpu", paths.offCPU},
		{"otel", paths.otel},
	}
	for _, check := range checks {
		want := filepath.Join(runDir, check.name+map[string]string{
			"sweep": ".json", "ops": ".jsonl", "events": ".jsonl", "offcpu": ".jsonl", "otel": ".jsonl",
		}[check.name])
		if filepath.Clean(check.got) != filepath.Clean(want) {
			t.Fatalf("auto-discovered %s path = %q, want %q", check.name, check.got, want)
		}
	}

	explicitDir := t.TempDir()
	explicit := map[string]string{}
	for _, name := range names {
		explicit[name] = filepath.Join(explicitDir, name)
		qaWriteFile(t, explicit[name], "{}\n")
	}
	explicitPaths := resolveViewsV2Paths(Options{
		SweepPath:  explicit["sweep.json"],
		OpsPath:    explicit["ops.jsonl"],
		EventsPath: explicit["events.jsonl"],
		OffCPUPath: explicit["offcpu.jsonl"],
		OTelPath:   explicit["otel.jsonl"],
	}, runDir)
	for _, item := range []struct {
		name string
		got  string
	}{
		{"sweep", explicitPaths.sweep},
		{"ops", explicitPaths.ops},
		{"events", explicitPaths.events},
		{"offcpu", explicitPaths.offCPU},
		{"otel", explicitPaths.otel},
	} {
		want := explicit[map[string]string{
			"sweep": "sweep.json", "ops": "ops.jsonl", "events": "events.jsonl", "offcpu": "offcpu.jsonl", "otel": "otel.jsonl",
		}[item.name]]
		if filepath.Clean(item.got) != filepath.Clean(want) {
			t.Fatalf("explicit %s path = %q, want %q", item.name, item.got, want)
		}
	}

	missing := resolveViewsV2Paths(Options{}, t.TempDir())
	if missing.sweep != "" || missing.ops != "" || missing.events != "" || missing.offCPU != "" || missing.otel != "" {
		t.Fatalf("missing optional evidence must resolve to empty paths: %+v", missing)
	}
}

func TestViewsV2IntegrationRooflineCompleteAndMissingFields(t *testing.T) {
	t.Run("complete_plus_incomplete_is_partial", func(t *testing.T) {
		runDir := t.TempDir()
		qaCopyFixture(t, runDir, "ops-complete.jsonl", "ops.jsonl")
		views := buildReportViewsV2(viewsV2BuildInput{
			runDir: runDir,
			opts: Options{
				RooflinePeakTFLOPS: 100,
				RooflinePeakGBPS:   1000,
				RooflineDType:      "bf16",
			},
		})
		if !views.Roofline.Status.Available {
			t.Fatalf("complete synthetic op should make roofline available: %+v", views.Roofline.Status)
		}
		if views.Roofline.Status.Source != "ops.jsonl" || views.Roofline.Status.Confidence != "partial" {
			t.Fatalf("roofline status = %+v, want source=ops.jsonl confidence=partial", views.Roofline.Status)
		}
		if !strings.Contains(views.Roofline.Status.Reason, "skipped") {
			t.Fatalf("partial roofline reason must mention skipped rows: %q", views.Roofline.Status.Reason)
		}
		if len(views.Roofline.Points) != 1 {
			t.Fatalf("roofline points = %d, want 1 complete synthetic point", len(views.Roofline.Points))
		}
		point := views.Roofline.Points[0]
		if point.Op != "MatMul" || point.Bound == "" || point.ArithmeticIntensity <= 0 {
			t.Fatalf("unexpected synthetic roofline point: %+v", point)
		}
		if views.Roofline.PeakTFLOPS != 100 || views.Roofline.PeakGBPS != 1000 || views.Roofline.DType != "bf16" {
			t.Fatalf("roofline reference values not preserved: %+v", views.Roofline)
		}
	})

	t.Run("all_incomplete_is_unavailable", func(t *testing.T) {
		runDir := t.TempDir()
		qaCopyFixture(t, runDir, "ops-missing-fields.jsonl", "ops.jsonl")
		views := buildReportViewsV2(viewsV2BuildInput{
			runDir: runDir,
			opts: Options{
				RooflinePeakTFLOPS: 100,
				RooflinePeakGBPS:   1000,
				RooflineDType:      "bf16",
			},
		})
		if views.Roofline.Status.Available {
			t.Fatalf("incomplete synthetic ops must not create a roofline: %+v", views.Roofline)
		}
		if len(views.Roofline.Points) != 0 {
			t.Fatalf("incomplete synthetic ops fabricated %d points", len(views.Roofline.Points))
		}
		if views.Roofline.Status.Source != "ops.jsonl" {
			t.Fatalf("incomplete evidence source = %q, want ops.jsonl", views.Roofline.Status.Source)
		}
		if !strings.Contains(views.Roofline.Status.Reason, "per-op") {
			t.Fatalf("unexpected unavailable reason: %q", views.Roofline.Status.Reason)
		}
	})
}

func TestViewsV2IntegrationEventsTimeConversion(t *testing.T) {
	const windowStartNS int64 = 1_700_000_000_000_000_000
	timestamps := make([]float64, 31)
	values := make([]float64, len(timestamps))
	for i := range timestamps {
		timestamps[i] = float64(i) * 10_000
		values[i] = float64(i % 5)
	}
	metrics := &MetricsData{
		Available: true,
		StartNS:   windowStartNS,
		EndNS:     windowStartNS + 300_000_000_000,
		Series: []MetricSeries{{
			Name: "qa_synthetic_gauge",
			Kind: "gauge",
			Unit: "count",
			T:    timestamps,
			V:    values,
		}},
	}

	t.Run("ts_ns_is_relative_to_window_start", func(t *testing.T) {
		runDir := t.TempDir()
		qaCopyFixture(t, runDir, "events.jsonl", "events.jsonl")
		views := buildReportViewsV2(viewsV2BuildInput{
			metrics:       metrics,
			windowStartNS: windowStartNS,
			runDir:        runDir,
		})
		if !views.ChangePoint.Status.Available {
			t.Fatalf("synthetic 5-minute metrics should enable change-point annotations: %+v", views.ChangePoint.Status)
		}
		if len(views.ChangePoint.Events) != 2 {
			t.Fatalf("events = %d, want 2", len(views.ChangePoint.Events))
		}
		if !qaAlmostEqual(views.ChangePoint.Events[0].TimeMS, 125, 1e-9) {
			t.Fatalf("ts_ns conversion = %.6f ms, want 125 ms", views.ChangePoint.Events[0].TimeMS)
		}
		if views.ChangePoint.Events[0].Label != "synthetic deploy" || views.ChangePoint.Events[0].Source != "qa-synthetic" {
			t.Fatalf("first event metadata not preserved: %+v", views.ChangePoint.Events[0])
		}
		if !qaAlmostEqual(views.ChangePoint.Events[1].TimeMS, 4500, 1e-9) {
			t.Fatalf("time_ms conversion = %.6f ms, want 4500 ms", views.ChangePoint.Events[1].TimeMS)
		}
	})

	t.Run("malformed_events_are_annotated_not_silent", func(t *testing.T) {
		runDir := t.TempDir()
		qaWriteFile(t, filepath.Join(runDir, "events.jsonl"), "not-json\n")
		views := buildReportViewsV2(viewsV2BuildInput{
			metrics:       metrics,
			windowStartNS: windowStartNS,
			runDir:        runDir,
		})
		if len(views.ChangePoint.Events) != 0 {
			t.Fatalf("malformed events must not be partially accepted: %+v", views.ChangePoint.Events)
		}
		if !strings.Contains(views.ChangePoint.Status.Reason, "events.jsonl ignored") {
			t.Fatalf("malformed event reason missing: %q", views.ChangePoint.Status.Reason)
		}
	})
}

func TestViewsV2IntegrationOffCPUExactAndAggregate(t *testing.T) {
	const spanStartNS int64 = 1_000_000_000
	baseSpan := testSpan(123, 456, spanStartNS, 1_000_000_000, "aclrtSynchronizeStream", "sync", "qa-span-exact")

	t.Run("exact_comes_only_from_interval_file", func(t *testing.T) {
		runDir := t.TempDir()
		qaCopyFixture(t, runDir, "offcpu-exact.jsonl", "offcpu.jsonl")
		views := buildReportViewsV2(viewsV2BuildInput{
			spans:         []runlog.Span{baseSpan},
			windowStartNS: spanStartNS,
			runDir:        runDir,
		})
		if !views.OffCPU.Status.Available || views.OffCPU.Mode != "exact" {
			t.Fatalf("exact off-CPU view = %+v", views.OffCPU)
		}
		if views.OffCPU.Status.Source != "offcpu.jsonl" || views.OffCPU.Status.Confidence != "exact" {
			t.Fatalf("exact off-CPU status = %+v", views.OffCPU.Status)
		}
		if len(views.OffCPU.Rows) != 1 || len(views.OffCPU.Rows[0].Segments) != 1 {
			t.Fatalf("exact rows/segments = %+v", views.OffCPU.Rows)
		}
		row := views.OffCPU.Rows[0]
		if !qaAlmostEqual(row.OffCPUMS, 300, 1e-6) || !qaAlmostEqual(row.FutexMS, 300, 1e-6) || !qaAlmostEqual(row.OnCPUMS, 700, 1e-6) {
			t.Fatalf("exact row durations = %+v", row)
		}
		if row.Segments[0].Kind != "futex" {
			t.Fatalf("exact segment kind = %q, want futex", row.Segments[0].Kind)
		}
	})

	t.Run("aggregate_not_promoted_without_intervals", func(t *testing.T) {
		span := baseSpan
		offCPU := int64(250_000_000)
		futex := int64(50_000_000)
		span.OffCPUNS = &offCPU
		span.FutexNS = &futex
		views := buildReportViewsV2(viewsV2BuildInput{
			spans:         []runlog.Span{span},
			windowStartNS: spanStartNS,
			runDir:        t.TempDir(),
		})
		if !views.OffCPU.Status.Available || views.OffCPU.Mode != "aggregate" {
			t.Fatalf("aggregate off-CPU view = %+v", views.OffCPU)
		}
		if len(views.OffCPU.Rows) != 1 || len(views.OffCPU.Rows[0].Segments) != 0 {
			t.Fatalf("aggregate must not fabricate exact segments: %+v", views.OffCPU.Rows)
		}
		row := views.OffCPU.Rows[0]
		if !qaAlmostEqual(row.OffCPUMS, 250, 1e-6) || !qaAlmostEqual(row.FutexMS, 50, 1e-6) {
			t.Fatalf("aggregate row = %+v", row)
		}
	})
}

func TestViewsV2IntegrationABGating(t *testing.T) {
	missingPair := buildReportViewsV2(viewsV2BuildInput{})
	if missingPair.ABDiff.Status.Available || !strings.Contains(missingPair.ABDiff.Status.Reason, "--ab-a") {
		t.Fatalf("missing A/B pair gate = %+v", missingPair.ABDiff.Status)
	}

	aDir, bDir := t.TempDir(), t.TempDir()
	qaWriteSyntheticMetrics(t, aDir, 1)
	qaWriteSyntheticMetrics(t, bDir, 2)
	missingWorkload := buildReportViewsV2(viewsV2BuildInput{opts: Options{
		ABAPath:    aDir,
		ABBRunPath: bDir,
	}})
	if missingWorkload.ABDiff.Status.Available || !strings.Contains(missingWorkload.ABDiff.Status.Reason, "--ab-workload-id") {
		t.Fatalf("missing workload gate = %+v", missingWorkload.ABDiff.Status)
	}

	valid := buildReportViewsV2(viewsV2BuildInput{opts: Options{
		ABAPath:      aDir,
		ABBRunPath:   bDir,
		ABWorkloadID: "synthetic-fixed-v1",
	}})
	if !valid.ABDiff.Status.Available {
		t.Fatalf("valid synthetic A/B should be available: %+v", valid.ABDiff.Status)
	}
	if valid.ABDiff.WorkloadID != "synthetic-fixed-v1" || len(valid.ABDiff.Metrics) == 0 {
		t.Fatalf("unexpected synthetic A/B result: %+v", valid.ABDiff)
	}
	if valid.ABDiff.Status.Source != "metrics" || valid.ABDiff.Status.Confidence != "partial" {
		t.Fatalf("metrics-only A/B status = %+v", valid.ABDiff.Status)
	}

	series := []MetricSeries{{Name: "vllm_requests_running", Kind: "gauge", Unit: "count", T: []float64{0, 1}, V: []float64{1, 2}}}
	mismatch := BuildABDiff(
		ABRunInput{RunID: "qa-a", WorkloadID: "synthetic-a", Metrics: series},
		ABRunInput{RunID: "qa-b", WorkloadID: "synthetic-b", Metrics: series},
	)
	if mismatch.Status.Available || !strings.Contains(mismatch.Status.Reason, "workload_id mismatch") {
		t.Fatalf("workload mismatch gate = %+v", mismatch.Status)
	}
	incomplete := BuildABDiff(
		ABRunInput{RunID: "qa-a", WorkloadID: "synthetic-fixed", Metrics: series, Incomplete: true},
		ABRunInput{RunID: "qa-b", WorkloadID: "synthetic-fixed", Metrics: series},
	)
	if incomplete.Status.Available || !strings.Contains(incomplete.Status.Reason, "incomplete") {
		t.Fatalf("incomplete run gate = %+v", incomplete.Status)
	}
}

func TestViewsV2IntegrationServiceTopologyFromSyntheticOTel(t *testing.T) {
	runDir := t.TempDir()
	qaCopyFixture(t, runDir, "otel.jsonl", "otel.jsonl")
	views := buildReportViewsV2(viewsV2BuildInput{runDir: runDir})
	if !views.ServiceTopology.Status.Available {
		t.Fatalf("synthetic OTel topology should be available: %+v", views.ServiceTopology.Status)
	}
	if views.ServiceTopology.Scope != "end_to_end" || len(views.ServiceTopology.Nodes) != 2 || len(views.ServiceTopology.Edges) != 1 {
		t.Fatalf("unexpected synthetic topology: %+v", views.ServiceTopology)
	}
}

func TestViewsV2IntegrationNoEvidenceNeverFabricates(t *testing.T) {
	views := buildReportViewsV2(viewsV2BuildInput{})
	checks := []struct {
		name   string
		status ViewStatus
	}{
		{"load_latency", views.LoadLatency.Status},
		{"critical_path", views.CriticalPath.Status},
		{"summed_delay", views.SummedDelay.Status},
		{"slack_cdf", views.SlackCDF.Status},
		{"off_cpu", views.OffCPU.Status},
		{"operator_treemap", views.OperatorTreemap.Status},
		{"roofline", views.Roofline.Status},
		{"ab_diff", views.ABDiff.Status},
		{"change_point", views.ChangePoint.Status},
		{"service_topology", views.ServiceTopology.Status},
	}
	for _, check := range checks {
		qaAssertUnavailable(t, check.name, check.status)
	}
	if len(views.LoadLatency.Points) != 0 || len(views.CriticalPath.Nodes) != 0 || len(views.CriticalPath.CriticalPath) != 0 ||
		len(views.SummedDelay.NodesByCategory) != 0 || len(views.SlackCDF.Points) != 0 || len(views.OffCPU.Rows) != 0 ||
		views.OperatorTreemap.Root != nil || len(views.Roofline.Points) != 0 || len(views.ABDiff.Metrics) != 0 ||
		len(views.ABDiff.Functions) != 0 || len(views.ABDiff.Significant) != 0 || len(views.ChangePoint.Series) != 0 ||
		len(views.ChangePoint.Points) != 0 || len(views.ChangePoint.Events) != 0 || len(views.ServiceTopology.Nodes) != 0 ||
		len(views.ServiceTopology.Edges) != 0 {
		t.Fatalf("evidence-free views fabricated data: %+v", views)
	}
}

func TestViewsV2IntegrationRealAttachReadOnly(t *testing.T) {
	if testing.Short() {
		t.Skip("real attach report is skipped in -short mode")
	}
	runDir := filepath.Join("..", "..", ".analysis", "views2", "attach-real")
	spansPath := filepath.Join(runDir, "spans.jsonl")
	if _, err := os.Stat(spansPath); err != nil {
		t.Skipf("real attach synthetic-worktree evidence not present: %v", err)
	}
	manifestPath := filepath.Join(runDir, "manifest.json")
	beforeSpans := qaFileSHA256(t, spansPath)
	beforeManifest := qaFileSHA256(t, manifestPath)

	in, err := diagnose.Load(runDir)
	if err != nil {
		t.Fatalf("load real attach run: %v", err)
	}
	data, err := BuildWithMetrics(in, nil, Options{})
	if err != nil {
		t.Fatalf("build real attach report: %v", err)
	}
	html, err := Render(data)
	if err != nil {
		t.Fatalf("render real attach report: %v", err)
	}
	if len(html) == 0 || !strings.Contains(string(html), `id="viewsV2Grid"`) {
		t.Fatal("real attach report missing views_v2 panel")
	}
	if data.ViewsV2.Roofline.Status.Available || data.ViewsV2.LoadLatency.Status.Available {
		t.Fatal("real attach run without optional evidence fabricated roofline/load-latency views")
	}

	if after := qaFileSHA256(t, spansPath); after != beforeSpans {
		t.Fatalf("spans.jsonl changed during read-only report generation: %s != %s", after, beforeSpans)
	}
	if after := qaFileSHA256(t, manifestPath); after != beforeManifest {
		t.Fatalf("manifest.json changed during read-only report generation: %s != %s", after, beforeManifest)
	}
}
