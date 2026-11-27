package main

import (
	"io"
	"strings"
	"testing"
	"time"

	"cann-ebpf/internal/config"
	"cann-ebpf/internal/discovery"
)

func TestParseStacksCommandDefaults(t *testing.T) {
	previous := stacksClock
	stacksClock = func() time.Time {
		return time.Date(2026, 11, 27, 12, 34, 56, 0, time.UTC)
	}
	defer func() { stacksClock = previous }()

	cfg, err := parseStacksCommand([]string{"--pid", "123"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.PIDs) != 1 || cfg.PIDs[0] != 123 || cfg.Auto {
		t.Fatalf("pids = %+v", cfg)
	}
	if cfg.Duration != 30*time.Second || cfg.FrequencyHz != 99 {
		t.Fatalf("sampling defaults = %s/%dHz", cfg.Duration, cfg.FrequencyHz)
	}
	if cfg.KernelFilter != "all" || cfg.Raw || cfg.Profile != "vllm-ascend" || cfg.SymbolsPath != stacksDefaultSymbolsPath {
		t.Fatalf("flags = %+v", cfg)
	}
	if cfg.Out != "runs/stacks-20261127T123456Z" {
		t.Fatalf("out = %q", cfg.Out)
	}
}

func TestParseStacksCommandRepeatedPIDsAndAuto(t *testing.T) {
	cfg, err := parseStacksCommand([]string{"--pid", "10", "--pid", "20", "--raw", "--kernel-filter", "npu", "--freq", "199", "--duration", "3s", "--out", "runs/custom"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.PIDs) != 2 || cfg.PIDs[0] != 10 || cfg.PIDs[1] != 20 {
		t.Fatalf("pids = %v", cfg.PIDs)
	}
	if !cfg.Raw || cfg.KernelFilter != "npu" || cfg.FrequencyHz != 199 || cfg.Duration != 3*time.Second || cfg.Out != "runs/custom" {
		t.Fatalf("cfg = %+v", cfg)
	}

	cfg, err = parseStacksCommand([]string{"--pid", "auto", "--profile", "vllm-ascend"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Auto || len(cfg.PIDs) != 0 || cfg.Profile != "vllm-ascend" {
		t.Fatalf("auto cfg = %+v", cfg)
	}
}

func TestParseStacksCommandValidation(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{name: "missing pid", args: nil, want: "at least one --pid"},
		{name: "zero pid", args: []string{"--pid", "0"}, want: "positive host PID"},
		{name: "negative pid", args: []string{"--pid", "-3"}, want: "positive host PID"},
		{name: "non numeric pid", args: []string{"--pid", "abc"}, want: "positive host PID"},
		{name: "auto plus explicit", args: []string{"--pid", "auto", "--pid", "42"}, want: "cannot be combined"},
		{name: "zero duration", args: []string{"--pid", "42", "--duration", "0s"}, want: "duration must be positive"},
		{name: "negative duration", args: []string{"--pid", "42", "--duration", "-1s"}, want: "duration must be positive"},
		{name: "frequency low", args: []string{"--pid", "42", "--freq", "0"}, want: "1..999"},
		{name: "frequency high", args: []string{"--pid", "42", "--freq", "1000"}, want: "1..999"},
		{name: "bad filter", args: []string{"--pid", "42", "--kernel-filter", "cpu"}, want: "all or npu"},
		{name: "unexpected argument", args: []string{"--pid", "42", "extra"}, want: "unexpected argument"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseStacksCommand(test.args, io.Discard)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestSelectVLLMProcessTreeExcludesOrinferAndIncludesChildren(t *testing.T) {
	procs := []*discovery.Process{
		{PID: 100, PPID: 1, Comm: "python3", Cmdline: "python3 -m vllm.entrypoints.openai.api_server --model model"},
		{PID: 101, PPID: 100, Comm: "VLLM::Worker_TP", Cmdline: "VLLM::Worker_TP"},
		{PID: 102, PPID: 100, Comm: "VLLM::EngineCor", Cmdline: "VLLM::EngineCor"},
		{PID: 103, PPID: 101, Comm: "python3", Cmdline: "worker child"},
		{PID: 104, PPID: 100, Comm: "orinfer", Cmdline: "orinfer worker"},
		{PID: 105, PPID: 104, Comm: "python3", Cmdline: "orinfer grandchild"},
		{PID: 200, PPID: 1, Comm: "orinfer", Cmdline: "start-orinfer.sh"},
		{PID: 201, PPID: 200, Comm: "python3", Cmdline: "orinfer server"},
		{PID: 300, PPID: 1, Comm: "nginx", Cmdline: "nginx: worker process"},
	}
	match := &config.Match{Any: []config.MatchExpr{
		{Comm: "VLLM::Worker_TP"},
		{Comm: "VLLM::EngineCor"},
		{Comm: "vllm"},
	}}
	pids, err := selectVLLMProcessTree(procs, match)
	if err != nil {
		t.Fatal(err)
	}
	want := []int{100, 101, 102, 103}
	if len(pids) != len(want) {
		t.Fatalf("pids = %v, want %v", pids, want)
	}
	for index := range want {
		if pids[index] != want[index] {
			t.Fatalf("pids = %v, want %v", pids, want)
		}
	}
}

func TestSelectVLLMProcessTreeRefusesWholeHostFallback(t *testing.T) {
	procs := []*discovery.Process{
		{PID: 200, PPID: 1, Comm: "orinfer", Cmdline: "start-orinfer.sh"},
		{PID: 300, PPID: 1, Comm: "nginx", Cmdline: "nginx: worker process"},
	}
	_, err := selectVLLMProcessTree(procs, nil)
	if err == nil {
		t.Fatal("expected an error when no vLLM process exists")
	}
	if !strings.Contains(err.Error(), "explicit --pid") || !strings.Contains(err.Error(), "refusing") {
		t.Fatalf("error = %v", err)
	}
}

func TestPIDListFlagSet(t *testing.T) {
	var flagValue pidListFlag
	if err := flagValue.Set("auto"); err != nil {
		t.Fatal(err)
	}
	if !flagValue.auto || flagValue.String() != "auto" {
		t.Fatalf("auto flag = %+v", flagValue)
	}
	var explicit pidListFlag
	if err := explicit.Set("7"); err != nil {
		t.Fatal(err)
	}
	if err := explicit.Set("9"); err != nil {
		t.Fatal(err)
	}
	if len(explicit.values) != 2 || explicit.String() != "7,9" {
		t.Fatalf("explicit flag = %+v", explicit)
	}
}
