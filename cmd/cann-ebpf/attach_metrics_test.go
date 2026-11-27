package main

import (
	"strings"
	"testing"
	"time"
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
