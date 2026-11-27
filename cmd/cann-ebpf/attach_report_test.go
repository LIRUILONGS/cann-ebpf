package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cann-ebpf/internal/metrics"
	"cann-ebpf/internal/runlog"
)

func TestResolveAttachReportPath(t *testing.T) {
	dir := t.TempDir()
	for _, value := range []string{"", "off", "none", "false"} {
		got, err := resolveAttachReportPath(value, dir)
		if err != nil || got != "" {
			t.Fatalf("resolve(%q) = %q, %v; want empty", value, got, err)
		}
	}
	got, err := resolveAttachReportPath("auto", dir)
	if err != nil || got != filepath.Join(dir, "report.html") {
		t.Fatalf("auto = %q, %v", got, err)
	}
	if err := os.WriteFile(got, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err = resolveAttachReportPath("auto", dir)
	if err != nil || got == filepath.Join(dir, "report.html") || !strings.HasSuffix(got, ".html") {
		t.Fatalf("auto collision = %q, %v", got, err)
	}
	sub := filepath.Join(dir, "out")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err = resolveAttachReportPath(sub, dir)
	if err != nil || got != filepath.Join(sub, "report.html") {
		t.Fatalf("directory = %q, %v", got, err)
	}
	got, err = resolveAttachReportPath(filepath.Join(dir, "custom.html"), dir)
	if err != nil || got != filepath.Join(dir, "custom.html") {
		t.Fatalf("custom = %q, %v", got, err)
	}
}

func TestGenerateAttachReportUsesDurableRunAndCapsPoints(t *testing.T) {
	dir := writeAttachReportFixture(t, runlog.ManifestStatusComplete)
	out := filepath.Join(dir, "report.html")
	summary, err := generateAttachReport(dir, out, "fixture report", 2)
	if err != nil {
		t.Fatalf("generateAttachReport: %v", err)
	}
	if summary.Spans != 4 || summary.Bytes == 0 || summary.Status != runlog.ManifestStatusComplete {
		t.Fatalf("summary = %+v", summary)
	}
	if !summary.Downsampled {
		t.Fatal("expected point downsampling to be recorded")
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "cann.report.html.v1") || !strings.Contains(string(raw), "fixture report") {
		t.Fatal("report payload missing")
	}
}

func TestGenerateAttachReportRejectsIncompleteRun(t *testing.T) {
	dir := writeAttachReportFixture(t, runlog.ManifestStatusError)
	if _, err := generateAttachReport(dir, filepath.Join(dir, "report.html"), "", 0); err == nil {
		t.Fatal("expected incomplete run error")
	}
}

func TestGenerateAttachReportLoadsSameRunMetrics(t *testing.T) {
	dir := writeAttachReportFixture(t, runlog.ManifestStatusComplete)
	path := filepath.Join(dir, "metrics.jsonl")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	point := metrics.Gauge("npu_aicore_utilization_ratio", "ratio", 0.42, map[string]string{"name": "910B2"})
	point.Source = "dcmi"
	point.Device = "NPU-0"
	point.MonoNS = 2_000_000
	point.WallNS = 1_700_000_000_000_000_000
	if err := json.NewEncoder(file).Encode(point); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	summary, err := generateAttachReport(dir, filepath.Join(dir, "report.html"), "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if summary.MetricsSeries != 1 {
		t.Fatalf("metrics series=%d, want 1", summary.MetricsSeries)
	}
}

func TestGenerateAttachReportRequiresManifest(t *testing.T) {
	dir := writeAttachReportFixture(t, runlog.ManifestStatusComplete)
	if err := os.Remove(filepath.Join(dir, "manifest.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := generateAttachReport(dir, filepath.Join(dir, "report.html"), "", 0); err == nil {
		t.Fatal("expected missing manifest error")
	}
}

func TestWriteReportAtomicReplacesExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "report.html")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeReportAtomic(path, []byte("new")); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "new" {
		t.Fatalf("report = %q", raw)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "report.html" {
		t.Fatalf("temporary files left behind: %v", entries)
	}
}

func TestGenerateAttachReportRealRunIfConfigured(t *testing.T) {
	runDir := strings.TrimSpace(os.Getenv("CANN_EBPF_REAL_RUN"))
	if runDir == "" {
		t.Skip("set CANN_EBPF_REAL_RUN to a completed run directory")
	}
	out := filepath.Join(t.TempDir(), "report.html")
	summary, err := generateAttachReport(runDir, out, "real run check", 1000)
	if err != nil {
		t.Fatalf("generateAttachReport: %v", err)
	}
	if summary.Spans == 0 || summary.Bytes == 0 || summary.Status != runlog.ManifestStatusComplete {
		t.Fatalf("summary = %+v", summary)
	}
	if !summary.Downsampled {
		t.Fatal("expected capped real run to report downsampling")
	}
}

func writeAttachReportFixture(t *testing.T, status string) string {
	t.Helper()
	dir := t.TempDir()
	manifest := runlog.Manifest{
		Schema:      runlog.ManifestSchemaV1,
		RunID:       "run-fixture",
		ToolVersion: "test",
		Status:      status,
		Profile:     "vllm-ascend",
		TargetPIDs:  []int{100},
		StartedAt:   time.Unix(0, 0),
	}
	if status == runlog.ManifestStatusError {
		manifest.Error = "fixture capture failure"
	}
	rawManifest, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), rawManifest, 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(filepath.Join(dir, "spans.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	encoder := json.NewEncoder(file)
	for i := 0; i < 4; i++ {
		start := int64(i+1) * 1_000_000
		span := runlog.Span{
			Schema: runlog.SpanSchemaV1, RunID: "run-fixture", Profile: "vllm-ascend",
			TsNS: start, StartNS: start, EndNS: start + 2_000_000, PID: 100, TID: 100, TGID: 100,
			Function: "aclrtSynchronizeStream", Category: "sync", DurationNS: 2_000_000,
			SpanID: "span-" + string(rune('a'+i)),
		}
		if err := encoder.Encode(span); err != nil {
			t.Fatal(err)
		}
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return dir
}
