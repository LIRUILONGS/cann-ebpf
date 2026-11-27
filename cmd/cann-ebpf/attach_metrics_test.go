package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"cann-ebpf/internal/metrics"
)

func TestBuildAttachMetricSourcesOff(t *testing.T) {
	sources, warnings, err := buildAttachMetricSources(attachMetricsConfig{
		Mode: "off", NPUBackend: "auto", Interval: time.Second,
		Prometheus: []prometheusSpec{{Name: "vllm", URL: "http://127.0.0.1:8000/metrics"}},
	})
	if err != nil || len(sources) != 0 || len(warnings) != 0 {
		t.Fatalf("sources=%v warnings=%v err=%v", sources, warnings, err)
	}
}

func TestBuildAttachMetricSourcesPrometheusOnly(t *testing.T) {
	sources, warnings, err := buildAttachMetricSources(attachMetricsConfig{
		Mode: "auto", NPUBackend: "off", Interval: time.Second,
		Prometheus: []prometheusSpec{{Name: "vllm", URL: "http://127.0.0.1:8000/metrics"}},
		Include:    []string{"^vllm:"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 1 || sources[0].Name() != "prometheus:vllm" {
		t.Fatalf("sources=%v", sources)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings=%v", warnings)
	}
}

func TestBuildAttachMetricSourcesOnRequiresSource(t *testing.T) {
	_, _, err := buildAttachMetricSources(attachMetricsConfig{
		Mode: "on", NPUBackend: "off", Interval: time.Second,
	})
	if err == nil || !strings.Contains(err.Error(), "no usable source") {
		t.Fatalf("err=%v", err)
	}
}

func TestBuildAttachMetricSourcesRejectsBadConfig(t *testing.T) {
	cases := []attachMetricsConfig{
		{Mode: "sometimes", NPUBackend: "auto", Interval: time.Second},
		{Mode: "auto", NPUBackend: "sometimes", Interval: time.Second},
		{Mode: "auto", NPUBackend: "off", Interval: 0},
	}
	for _, config := range cases {
		if _, _, err := buildAttachMetricSources(config); err == nil {
			t.Fatalf("config %+v should fail", config)
		}
	}
}

func TestParsePrometheusSpecs(t *testing.T) {
	specs, err := parsePrometheusSpecs([]string{
		"vllm=http://127.0.0.1:8000/metrics",
		"https://example.invalid/metrics",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(specs) != 2 || specs[0].Name != "vllm" || specs[1].Name == "" {
		t.Fatalf("specs=%+v", specs)
	}
	if _, err := parsePrometheusSpecs([]string{"ftp://example/metrics"}); err == nil {
		t.Fatal("expected invalid scheme error")
	}
}

func TestBuildAttachMetricSourcesDCMIExtAndHCCS(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("executable lookup semantics differ on Windows")
	}
	dir := t.TempDir()
	probe := filepath.Join(dir, "dcmi_probe")
	if err := os.WriteFile(probe, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	npuSmi := filepath.Join(dir, "npu-smi")
	if err := os.WriteFile(npuSmi, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CANN_DCMI_PROBE", probe)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	sources, warnings, err := buildAttachMetricSources(attachMetricsConfig{
		Mode: "auto", NPUBackend: "dcmi", Interval: time.Second,
		DCMIExt: true, DCMIExtInterval: 10 * time.Second,
		HCCS: true, HCCSInterval: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("build: %v (warnings=%v)", err, warnings)
	}
	if len(sources) != 2 {
		t.Fatalf("sources = %v", sources)
	}
	dcmi, ok := sources[0].(*metrics.DCMISource)
	if !ok || !dcmi.Extended {
		t.Fatalf("first source = %T %+v, want extended DCMI", sources[0], sources[0])
	}
	if sources[1].Name() != "npu-smi-hccs" {
		t.Fatalf("second source = %s", sources[1].Name())
	}
}
