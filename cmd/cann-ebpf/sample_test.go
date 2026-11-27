package main

import (
	"strings"
	"testing"
	"time"
)

func TestParseSampleArgs(t *testing.T) {
	config, err := parseSampleArgs([]string{
		"--out", "runs/metrics-test",
		"--interval", "2s",
		"--duration", "10m",
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
