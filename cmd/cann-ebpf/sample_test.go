package main

import (
	"strings"
	"testing"
	"time"

	"cann-ebpf/internal/metrics"
)

func TestParseSampleArgs(t *testing.T) {
	config, err := parseSampleArgs([]string{
		"--out", "runs/metrics-test",
		"--interval", "2s",
		"--duration", "10m",
		"--system",
		"--pid", "123",
		"--pid", "456",
		"--cgroup", "/sys/fs/cgroup/test",
		"--prometheus", "vllm=http://127.0.0.1:8000/metrics",
		"--include", "^vllm:",
		"--json",
	})
	if err != nil {
		t.Fatal(err)
	}
	if config.Out != "runs/metrics-test" || config.Interval != 2*time.Second || config.Duration != 10*time.Minute {
		t.Fatalf("config=%+v", config)
	}
	if len(config.PIDs) != 2 || config.PIDs[1] != 456 {
		t.Fatalf("pids=%v", config.PIDs)
	}
	if len(config.Cgroups) != 1 || len(config.Prometheus) != 1 || config.Prometheus[0].Name != "vllm" {
		t.Fatalf("sources=%+v", config)
	}
	if !config.JSON {
		t.Fatal("json flag not set")
	}
	if !config.System {
		t.Fatal("system flag not set")
	}
}

func TestParseSampleArgsRejectsBadInput(t *testing.T) {
	cases := [][]string{
		{"--interval", "1s"},
		{"--out", "x", "--interval", "0"},
		{"--out", "x", "--duration", "-1s"},
		{"--out", "x", "--pid", "0"},
		{"--out", "x", "--prometheus", "ftp://example/metrics"},
		{"--out", "x", "--include", "["},
	}
	for _, args := range cases {
		if _, err := parseSampleArgs(args); err == nil {
			t.Fatalf("args %v should fail", args)
		}
	}
}

func TestParsePrometheusSpec(t *testing.T) {
	spec, err := parsePrometheusSpec("http://127.0.0.1:8000/metrics")
	if err != nil {
		t.Fatal(err)
	}
	if spec.Name != "127_0_0_1_8000" || !strings.HasPrefix(spec.URL, "http://") {
		t.Fatalf("spec=%+v", spec)
	}
	spec, err = parsePrometheusSpec("litellm=https://litellm.example/metrics")
	if err != nil || spec.Name != "litellm" {
		t.Fatalf("spec=%+v err=%v", spec, err)
	}
}

func TestBuildSampleSourcesProcOnly(t *testing.T) {
	sources, err := buildSampleSources(&sampleConfig{Interval: time.Second, PIDs: []int{123}})
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 1 || sources[0].Name() != "proc" {
		t.Fatalf("sources=%v", sources)
	}
}

func TestBuildSampleSourcesSystemOnly(t *testing.T) {
	sources, err := buildSampleSources(&sampleConfig{Interval: time.Second, System: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 1 || sources[0].Name() != "system" {
		t.Fatalf("sources=%v", sources)
	}
	systemSource, ok := sources[0].(*metrics.SystemSource)
	if !ok {
		t.Fatalf("source type=%T", sources[0])
	}
	if systemSource.ProcRoot != "/proc" || systemSource.SysRoot != "/sys" {
		t.Fatalf("system roots=%q/%q", systemSource.ProcRoot, systemSource.SysRoot)
	}
	if systemSource.Interval() != time.Second || systemSource.Timeout() != 2*time.Second {
		t.Fatalf("system interval/timeout=%s/%s", systemSource.Interval(), systemSource.Timeout())
	}
}

func TestBuildSampleSourcesSystemWithProc(t *testing.T) {
	sources, err := buildSampleSources(&sampleConfig{Interval: time.Second, System: true, PIDs: []int{123}})
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 2 || sources[0].Name() != "proc" || sources[1].Name() != "system" {
		t.Fatalf("sources=%v", sources)
	}
	if _, ok := sources[1].(*metrics.SystemSource); !ok {
		t.Fatalf("system source type=%T", sources[1])
	}
}

func TestParseSampleArgsDCMIExtendedAndHCCS(t *testing.T) {
	config, err := parseSampleArgs([]string{
		"--out", "runs/hw-ext",
		"--dcmi-ext",
		"--dcmi-ext-interval", "5s",
		"--hccs",
		"--hccs-interval", "10s",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !config.DCMIExt || config.DCMIExtInterval != 5*time.Second {
		t.Fatalf("dcmi ext config = %+v", config)
	}
	if !config.HCCS || config.HCCSInterval != 10*time.Second {
		t.Fatalf("hccs config = %+v", config)
	}
}
