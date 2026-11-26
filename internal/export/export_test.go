package export

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cann-ebpf/internal/device"
	"cann-ebpf/internal/diagnose"
)

func floatPtr(v float64) *float64 { return &v }

func sampleReport() diagnose.Report {
	return diagnose.Report{
		Schema: diagnose.ReportSchemaV1, Status: diagnose.StatusOK,
		Run:       diagnose.RunInfo{RunID: "run-1", Profile: "vllm-ascend", Host: "h1", Status: "complete", DroppedEvents: 2},
		SpanCount: 3,
		Functions: []diagnose.FunctionSummary{
			{Function: `f"x`, Category: "sync", Count: 2, P50NS: 1e6, P95NS: 2e6, MaxNS: 3e6, TotalNS: 5e6},
		},
		Categories: []diagnose.CategorySummary{{Category: "sync", Count: 2, TotalNS: 5e6, SpanPct: 66.7}},
		Blocking:   diagnose.BlockingSummary{Measured: true, OffCPUNS: 1e9, FutexNS: 2e8, OffCPURatio: 0.5, FutexRatio: 0.1},
		Hardware: diagnose.HardwareSummary{
			Available: true, HBMFreePctMin: floatPtr(6.3),
			Devices: []diagnose.DeviceHardware{{DeviceID: 0, Name: "910B2", HBMFreePctMin: 6.3, AICoreMax: 46, PowerAvgW: 170, TempMaxC: 43}},
		},
		Device: &device.Report{
			Schema: device.JoinReportSchemaV1, MatchedHostSpans: 2, MatchRate: 1.0, SlowSyncMatchRate: 1.0,
			Devices: []device.DeviceSummary{{DeviceID: 0, Spans: 2, BusyNS: 5e8}},
		},
		Verdicts: []diagnose.Verdict{{Verdict: "host_cpu_running", Confidence: 0.6}, {Verdict: "device_bound", Confidence: 0.9}},
		Unknowns: []string{"u1", "u2"},
		Warnings: []string{"w1"},
	}
}

func TestRenderMetrics(t *testing.T) {
	text := Render(sampleReport())
	for _, want := range []string{
		"# TYPE cann_ebpf_spans_total gauge",
		`cann_ebpf_spans_total{run_id="run-1"} 3`,
		`cann_ebpf_dropped_events_total{run_id="run-1"} 2`,
		`cann_ebpf_function_duration_seconds{function="f\"x",category="sync",quantile="0.5"} 0.001`,
		"cann_ebpf_blocking_offcpu_seconds_total 1",
		"cann_ebpf_blocking_futex_ratio 0.1",
		`cann_ebpf_hardware_hbm_free_percent_min 6.3`,
		`cann_ebpf_hardware_device_aicore_percent_max{device="0",name="910B2"} 46`,
		"cann_ebpf_device_match_rate 1",
		`cann_ebpf_device_span_busy_seconds_total{device="0"} 0.5`,
		`cann_ebpf_verdict{verdict="device_bound"} 1`,
		`cann_ebpf_verdict{verdict="host_cpu_running"} 1`,
		"cann_ebpf_unknowns_total 2",
		"cann_ebpf_warnings_total 1",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in:\n%s", want, text)
		}
	}
}

func TestLatestRun(t *testing.T) {
	root := t.TempDir()
	makeRun := func(name, status string, ended time.Time) {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "spans.jsonl"), []byte{}, 0o600); err != nil {
			t.Fatal(err)
		}
		manifest := `{"schema":"cann.manifest.v1","run_id":"` + name + `","tool_version":"0.6.0-m2","status":"` + status + `","profile":"p","target_pids":[1],"started_at":"2026-11-26T00:00:00Z"`
		if status == "complete" {
			manifest += `,"ended_at":"` + ended.Format(time.RFC3339) + `"`
		}
		manifest += "}"
		if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(manifest), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	makeRun("run-old", "complete", time.Now().Add(-2*time.Hour))
	makeRun("run-new", "complete", time.Now())
	makeRun("run-writing", "running", time.Time{})
	got, err := LatestRun(root)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(got) != "run-new" {
		t.Fatalf("latest = %s", got)
	}
	if _, err := LatestRun(filepath.Join(root, "missing")); err == nil {
		t.Fatal("expected error for missing root")
	}
}

func TestBuildFromDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "spans.jsonl"), []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := `{"schema":"cann.manifest.v1","run_id":"r","tool_version":"0.6.0-m2","status":"complete","profile":"p","target_pids":[1],"started_at":"2026-11-26T00:00:00Z","ended_at":"2026-11-26T00:00:10Z"}`
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	text, run, err := Build(dir)
	if err != nil {
		t.Fatal(err)
	}
	if run != dir || !strings.Contains(text, "cann_ebpf_spans_total") {
		t.Fatalf("run=%s text=%s", run, text)
	}
}
