package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cann-ebpf/internal/runlog"
)

func TestCmdDiagnoseWritesReport(t *testing.T) {
	dir := t.TempDir()
	span := runlog.Span{
		Schema: runlog.SpanSchemaV1, RunID: "run-cli", Profile: "vllm-ascend",
		TsNS: 10, StartNS: 10, EndNS: 1010, PID: 42, TID: 43, TGID: 42,
		Function: "aclrtSynchronizeStream", Category: "sync", DurationNS: 1000,
		SpanID: "span-1", Seq: 1,
	}
	data, err := json.Marshal(span)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "spans.jsonl"), append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "report.md")
	code := cmdDiagnose([]string{"--input", dir, "--out", out})
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	report, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(report), "CANN eBPF 低层定位报告") {
		t.Fatalf("unexpected report:\n%s", report)
	}

	jsonOut := filepath.Join(dir, "report.json")
	if code := cmdDiagnose([]string{"--input", dir, "--json", "--out", jsonOut}); code != 0 {
		t.Fatalf("json exit = %d", code)
	}
	raw, err := os.ReadFile(jsonOut)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("report json: %v", err)
	}
	if decoded["schema"] != "cann.diagnose.report.v1" {
		t.Fatalf("schema = %v", decoded["schema"])
	}
}

func TestCmdDiagnoseRequiresInput(t *testing.T) {
	if code := cmdDiagnose(nil); code != 2 {
		t.Fatalf("missing input exit = %d, want 2", code)
	}
	if code := cmdDiagnose([]string{"--input", filepath.Join(t.TempDir(), "nope")}); code != 4 {
		t.Fatalf("missing run exit = %d, want 4", code)
	}
}
