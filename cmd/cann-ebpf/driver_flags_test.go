package main

import (
	"testing"
	"time"
)

func TestBuildAttachMetricSourcesDriverOnly(t *testing.T) {
	sources, warnings, err := buildAttachMetricSources(attachMetricsConfig{
		Mode: "on", NPUBackend: "off", Interval: time.Second, Driver: true, PIDs: []int{123},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 1 || sources[0].Name() != "driver-ebpf" {
		t.Fatalf("sources=%v warnings=%v", sources, warnings)
	}
}

func TestParseSampleArgsDriverRequiresPID(t *testing.T) {
	configWithoutPID, err := parseSampleArgs([]string{"--out", "runs/driver-no-pid", "--driver"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := buildSampleSources(configWithoutPID); err == nil {
		t.Fatal("expected buildSampleSources --driver without --pid to fail")
	}
	config, err := parseSampleArgs([]string{"--out", "runs/driver", "--driver", "--pid", "123"})
	if err != nil {
		t.Fatal(err)
	}
	if !config.Driver || len(config.PIDs) != 1 || config.PIDs[0] != 123 {
		t.Fatalf("config=%+v", config)
	}
}
