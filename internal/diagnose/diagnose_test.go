package diagnose

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cann-ebpf/internal/device"
	"cann-ebpf/internal/hardware"
	"cann-ebpf/internal/runlog"
	"cann-ebpf/internal/tracectx"
)

func int64Ptr(v int64) *int64 { return &v }

func testSpans() []runlog.Span {
	return []runlog.Span{
		{
			Schema: runlog.SpanSchemaV1, RunID: "run-1", Profile: "vllm-ascend",
			TsNS: 100, StartNS: 100, EndNS: 1100, PID: 100, TID: 11, TGID: 100,
			Function: "aclrtSynchronizeStream", Category: "sync", DurationNS: 1000,
			OffCPUNS: int64Ptr(800), FutexNS: int64Ptr(600),
			BatchID: "b1", RequestIDs: []string{"r1"},
			Trace:  &tracectx.Context{TraceID: "t1", SpanID: "s-11", ParentSpanID: "b1-span"},
			SpanID: "s1", Seq: 1,
		},
		{
			Schema: runlog.SpanSchemaV1, RunID: "run-1", Profile: "vllm-ascend",
			TsNS: 2000, StartNS: 2000, EndNS: 2100, PID: 100, TID: 12, TGID: 100,
			Function: "aclrtLaunchKernel", Category: "submit", DurationNS: 100,
			OffCPUNS: int64Ptr(0), FutexNS: int64Ptr(0),
			BatchID: "b1", RequestIDs: []string{"r2"},
			SpanID: "s2", Seq: 2,
		},
		{
			Schema: runlog.SpanSchemaV1, RunID: "run-1", Profile: "vllm-ascend",
			TsNS: 5000, StartNS: 5000, EndNS: 5500, PID: 200, TID: 21, TGID: 200,
			Function: "aclrtSynchronizeEvent", Category: "sync", DurationNS: 500,
			SpanID: "s3", Seq: 3,
		},
		{
			Schema: runlog.SpanSchemaV1, RunID: "run-1", Profile: "vllm-ascend",
			TsNS: 6000, StartNS: 6000, EndNS: 6050, PID: 200, TID: 21, TGID: 200,
			Function: "aclrtRecordEvent", Category: "event", DurationNS: 50,
			BatchID: "b2", RequestIDs: []string{"r3"},
			SpanID: "s4", Seq: 4,
		},
	}
}

func newInput(spans []runlog.Span) *Input {
	return &Input{
		Path: "test-run", JSONLPath: "test-run/spans.jsonl", ManifestPath: "test-run/manifest.json",
		Spans: spans,
	}
}

func TestBuildFilters(t *testing.T) {
	spans := testSpans()
	cases := []struct {
		name  string
		opts  Options
		count int
	}{
		{"all", Options{}, 4},
		{"pid", Options{PIDs: []int{200}}, 2},
		{"tid", Options{TIDs: []int{11, 21}}, 3},
		{"batch", Options{BatchID: "b1"}, 2},
		{"request", Options{RequestID: "r3"}, 1},
		{"trace", Options{TraceID: "t1"}, 1},
		{"window", Options{StartNS: 3000, EndNS: 5500}, 1},
		{"last", Options{Last: 1200 * time.Nanosecond}, 2},
		{"no match", Options{BatchID: "missing"}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			report, err := Build(newInput(spans), tc.opts)
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			if report.MatchedSpans != tc.count {
				t.Fatalf("matched = %d, want %d", report.MatchedSpans, tc.count)
			}
			if tc.count == 0 {
				if report.Status != StatusEmpty {
					t.Fatalf("status = %q, want empty", report.Status)
				}
				if len(report.Unknowns) == 0 {
					t.Fatal("empty result must explain why")
				}
				return
			}
			if report.Status != StatusOK {
				t.Fatalf("status = %q", report.Status)
			}
		})
	}
}

func TestBuildWindowSources(t *testing.T) {
	report, err := Build(newInput(testSpans()), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if report.Window.Source != "run" || report.Window.StartNS != 100 || report.Window.EndNS != 6050 {
		t.Fatalf("window = %+v", report.Window)
	}
	if _, err = Build(newInput(testSpans()), Options{StartNS: 7000, EndNS: 6000}); err == nil {
		t.Fatal("expected invalid window error")
	}
	report, err = Build(newInput(testSpans()), Options{StartNS: 3000})
	if err != nil {
		t.Fatal(err)
	}
	if report.Window.Source != "explicit" || report.Window.StartNS != 3000 || report.Window.EndNS != 6050 {
		t.Fatalf("explicit window = %+v", report.Window)
	}
}

func TestBuildAggregates(t *testing.T) {
	report, err := Build(newInput(testSpans()), Options{SlowThresholdNS: 500})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.PIDs) != 2 {
		t.Fatalf("pids = %+v", report.PIDs)
	}
	if report.PIDs[0].PID != 100 || report.PIDs[0].Spans != 2 {
		t.Fatalf("top pid = %+v", report.PIDs[0])
	}
	if len(report.Batches) != 2 {
		t.Fatalf("batches = %+v", report.Batches)
	}
	if len(report.RequestIDs) != 3 {
		t.Fatalf("request ids = %v", report.RequestIDs)
	}
	if len(report.Evidence) != 2 || report.Evidence[0].SpanID != "s1" {
		t.Fatalf("evidence = %+v", report.Evidence)
	}
	if report.Evidence[0].OffCPUNS == nil || *report.Evidence[0].OffCPUNS != 800 {
		t.Fatalf("evidence offcpu = %+v", report.Evidence[0])
	}
	var sync *CategorySummary
	for i := range report.Categories {
		if report.Categories[i].Category == "sync" {
			sync = &report.Categories[i]
		}
	}
	if sync == nil || sync.Count != 2 {
		t.Fatalf("sync category = %+v", sync)
	}
}

func TestBlockingVerdicts(t *testing.T) {
	cases := []struct {
		name     string
		mutate   func([]runlog.Span) []runlog.Span
		measured bool
		verdict  string
	}{
		{
			name:     "lock contention",
			mutate:   func(s []runlog.Span) []runlog.Span { return s },
			measured: true,
			verdict:  "host_lock_contention",
		},
		{
			name: "blocked non futex",
			mutate: func(s []runlog.Span) []runlog.Span {
				v := int64(0)
				s[0].FutexNS = &v
				return s
			},
			measured: true,
			verdict:  "host_blocked_non_futex",
		},
		{
			name: "cpu running",
			mutate: func(s []runlog.Span) []runlog.Span {
				v := int64(10)
				s[0].OffCPUNS = &v
				f := int64(0)
				s[0].FutexNS = &f
				return s
			},
			measured: true,
			verdict:  "host_cpu_running",
		},
		{
			name: "no samples",
			mutate: func(s []runlog.Span) []runlog.Span {
				for i := range s {
					s[i].OffCPUNS = nil
					s[i].FutexNS = nil
				}
				return s
			},
			measured: false,
			verdict:  "blocking_unknown",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			report, err := Build(newInput(tc.mutate(testSpans())), Options{})
			if err != nil {
				t.Fatal(err)
			}
			if report.Blocking.Measured != tc.measured {
				t.Fatalf("measured = %v", report.Blocking.Measured)
			}
			if len(report.Verdicts) != 1 || report.Verdicts[0].Verdict != tc.verdict {
				t.Fatalf("verdicts = %+v, want %s", report.Verdicts, tc.verdict)
			}
			if !tc.measured {
				if !contains(report.Unknowns, "没有 off-CPU/futex 采样") {
					t.Fatalf("unknowns = %v", report.Unknowns)
				}
			}
		})
	}
}

func TestBlockingNestedWarningAndRatio(t *testing.T) {
	report, err := Build(newInput(testSpans()), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if report.Blocking.Population != "sync" {
		t.Fatalf("population = %q", report.Blocking.Population)
	}
	if report.Blocking.SampledSpans != 1 || report.Blocking.Spans != 2 {
		t.Fatalf("blocking spans = %+v", report.Blocking)
	}
	if report.Blocking.OffCPURatio != 0.8 || report.Blocking.FutexRatio != 0.6 {
		t.Fatalf("ratios = %+v", report.Blocking)
	}
	if report.Blocking.NestedWarning == "" {
		t.Fatal("nested warning missing")
	}
}

func TestMarkdownStatesBoundaries(t *testing.T) {
	report, err := Build(newInput(testSpans()), Options{})
	if err != nil {
		t.Fatal(err)
	}
	md := report.Markdown()
	for _, want := range []string{
		"CANN eBPF 低层定位报告",
		"host_lock_contention",
		"msPTI",
		"无法回答单 request 延迟",
		"execute_model",
		"不修改原始证据",
	} {
		if !strings.Contains(md, want) {
			t.Fatalf("markdown missing %q\n%s", want, md)
		}
	}
}

func TestLoadRunDirectory(t *testing.T) {
	dir := t.TempDir()
	spans := testSpans()
	var buf strings.Builder
	encoder := json.NewEncoder(&buf)
	for _, span := range spans {
		if err := encoder.Encode(span); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "spans.jsonl"), []byte(buf.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := runlog.Manifest{
		Schema: runlog.ManifestSchemaV1, RunID: "run-1", ToolVersion: "0.5.0-m5",
		Status: runlog.ManifestStatusComplete, Profile: "vllm-ascend",
		TargetPIDs: []int{100, 200}, StartedAt: time.Unix(0, 0),
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	in, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(in.Spans) != 4 || in.Manifest == nil {
		t.Fatalf("load = %d spans, manifest=%v", len(in.Spans), in.Manifest)
	}
	report, err := Build(in, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if report.Run.RunID != "run-1" || report.Run.TargetPIDs[0] != 100 {
		t.Fatalf("run info = %+v", report.Run)
	}

	if err := os.Remove(filepath.Join(dir, "manifest.json")); err != nil {
		t.Fatal(err)
	}
	in, err = Load(filepath.Join(dir, "spans.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !contains(in.Warnings, "缺少 manifest.json") {
		t.Fatalf("warnings = %v", in.Warnings)
	}
	report, err = Build(in, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !contains(report.Unknowns, "缺少 manifest.json") {
		t.Fatalf("unknowns = %v", report.Unknowns)
	}
}

func TestLoadMissingInput(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("expected stat error")
	}
	if _, err := Load(""); err == nil {
		t.Fatal("expected empty path error")
	}
}

func TestBlockingDropsImpossibleSamples(t *testing.T) {
	spans := testSpans()
	huge := int64(5000) // s1 duration is 1000
	spans[0].FutexNS = &huge
	report, err := Build(newInput(spans), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if report.Blocking.InvalidSamples != 1 {
		t.Fatalf("invalid samples = %d", report.Blocking.InvalidSamples)
	}
	if report.Blocking.FutexNS != 0 || report.Blocking.FutexRatio != 0 {
		t.Fatalf("invalid futex leaked into ratio: %+v", report.Blocking)
	}
	if report.Blocking.OffCPUNS != 800 || !report.Blocking.Measured {
		t.Fatalf("valid offcpu sample was dropped: %+v", report.Blocking)
	}
	if !contains(report.Unknowns, "大于 span duration") {
		t.Fatalf("unknowns = %v", report.Unknowns)
	}
	if len(report.Verdicts) != 1 || report.Verdicts[0].Verdict != "host_blocked_non_futex" {
		t.Fatalf("verdicts = %+v", report.Verdicts)
	}
}

func TestBlockingAllSamplesInvalid(t *testing.T) {
	spans := testSpans()
	over := int64(5000)
	spans[0].OffCPUNS = &over
	spans[0].FutexNS = &over
	spans[1].OffCPUNS = nil
	spans[1].FutexNS = nil
	report, err := Build(newInput(spans), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if report.Blocking.Measured {
		t.Fatalf("measured should be false when all samples are invalid: %+v", report.Blocking)
	}
	if len(report.Verdicts) != 1 || report.Verdicts[0].Verdict != "blocking_unknown" {
		t.Fatalf("verdicts = %+v", report.Verdicts)
	}
	if !contains(report.Unknowns, "blocking 采样全部不可用") {
		t.Fatalf("unknowns = %v", report.Unknowns)
	}
}

func TestHardwareSummaryAndHbmPressure(t *testing.T) {
	spans := testSpans()
	spans = append(spans, runlog.Span{
		Schema: runlog.SpanSchemaV1, RunID: "run-1", Profile: "vllm-ascend",
		TsNS: 7000, StartNS: 7000, EndNS: 9000, PID: 100, TID: 13, TGID: 100,
		Function: "aclrtMalloc", Category: "memory", DurationNS: 2000,
		SpanID: "s-mem", Seq: 5,
	})
	used := int64(65000)
	total := int64(65536)
	samples := []hardware.Sample{
		{Schema: hardware.SampleSchemaV1, DeviceID: 0, Name: "910B2", HBMUsedMB: &used, HBMTotalMB: &total, Source: "npu-smi",
			AICorePct: hardwareFloat(25)},
		{Schema: hardware.SampleSchemaV1, DeviceID: 1, Name: "910B2", HBMUsedMB: &used, HBMTotalMB: &total, Source: "npu-smi",
			AICorePct: hardwareFloat(80)},
	}
	input := newInput(spans)
	input.Hardware = samples
	report, err := Build(input, Options{SlowThresholdNS: 500})
	if err != nil {
		t.Fatal(err)
	}
	if !report.Hardware.Available || len(report.Hardware.Devices) != 2 {
		t.Fatalf("hardware = %+v", report.Hardware)
	}
	if report.Hardware.HBMFreePctMin == nil {
		t.Fatal("hbm free pct missing")
	}
	if pct := *report.Hardware.HBMFreePctMin; pct < 0.7 || pct > 0.9 {
		t.Fatalf("hbm free pct = %v", pct)
	}
	if report.Hardware.AICorePctMax != 80 {
		t.Fatalf("aicore max = %v", report.Hardware.AICorePctMax)
	}
	found := false
	for _, verdict := range report.Verdicts {
		if verdict.Verdict == "hbm_pressure" {
			found = true
			if len(verdict.Evidence) != 1 || verdict.Evidence[0] != "s-mem" {
				t.Fatalf("hbm evidence = %+v", verdict)
			}
		}
	}
	if !found {
		t.Fatalf("hbm_pressure verdict missing: %+v", report.Verdicts)
	}
	md := report.Markdown()
	if !strings.Contains(md, "硬件层") || !strings.Contains(md, "hbm_pressure") {
		t.Fatalf("markdown missing hardware section:\n%s", md)
	}
}

func TestHardwareMissingIsUnknown(t *testing.T) {
	report, err := Build(newInput(testSpans()), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if report.Hardware.Available {
		t.Fatal("hardware should be unavailable")
	}
	if !contains(report.Unknowns, "没有硬件层数据") {
		t.Fatalf("unknowns = %v", report.Unknowns)
	}
}

func TestLoadHardwareSamples(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "spans.jsonl"), []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}
	line := `{"schema":"cann.hardware.v1","ts_ns":1,"device_id":0,"name":"910B2","hbm_used_mb":10,"hbm_total_mb":100,"source":"npu-smi"}` + "\n" + "not-json\n"
	if err := os.WriteFile(filepath.Join(dir, "hardware.jsonl"), []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	in, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(in.Hardware) != 1 {
		t.Fatalf("hardware samples = %d", len(in.Hardware))
	}
	if !contains(in.Warnings, "无法解析") {
		t.Fatalf("warnings = %v", in.Warnings)
	}
}

func TestDeviceJoinIntegration(t *testing.T) {
	input := newInput(testSpans())
	input.DevicePath = "test-run/device.jsonl"
	input.DeviceSpans = []device.Span{
		{Schema: device.SchemaV1, Kind: device.KindAPI, Name: "aclrtSynchronizeStream", PID: 100, TID: 11, StartNS: 100, EndNS: 1100, CorrelationID: 1},
		{Schema: device.SchemaV1, Kind: device.KindKernel, Name: "ffn", DeviceID: 0, StartNS: 120, EndNS: 1020, CorrelationID: 1},
	}
	report, err := Build(input, Options{SlowThresholdNS: 500})
	if err != nil {
		t.Fatal(err)
	}
	if report.Device == nil {
		t.Fatal("device report missing")
	}
	if report.Device.MatchedHostSpans != 1 {
		t.Fatalf("matched = %d", report.Device.MatchedHostSpans)
	}
	found := false
	for _, verdict := range report.Verdicts {
		if verdict.Verdict == "device_bound" {
			found = true
			if len(verdict.Evidence) != 1 || verdict.Evidence[0] != "s1" {
				t.Fatalf("device evidence = %+v", verdict)
			}
		}
	}
	if !found {
		t.Fatalf("device_bound missing: %+v", report.Verdicts)
	}
	if contains(report.Unknowns, "缺少设备时间线证据") {
		t.Fatalf("device evidence present but unknown still claims no timeline: %v", report.Unknowns)
	}
	md := report.Markdown()
	for _, want := range []string{"设备侧时间线", "device_bound", "correlationId"} {
		if !strings.Contains(md, want) {
			t.Fatalf("markdown missing %q", want)
		}
	}
}

func hardwareFloat(v float64) *float64 { return &v }

func contains(values []string, substr string) bool {
	for _, value := range values {
		if strings.Contains(value, substr) {
			return true
		}
	}
	return false
}
