package main

import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	"cann-ebpf/internal/metrics"
)

type attachMetricsConfig struct {
	Mode       string
	NPUBackend string
	Interval   time.Duration
	PIDs       []int
	ProcRoot   string
	Proc       bool
	Cgroups    []string
	System     bool
	Prometheus []prometheusSpec
	Include    []string
}

// buildAttachMetricSources reuses the standalone sample sources for the
// integrated attach path. Explicitly requested sources are strict; auto mode
// skips a missing accelerator source with a warning instead of failing the eBPF
// capture.
func buildAttachMetricSources(config attachMetricsConfig) ([]metrics.Source, []string, error) {
	mode := strings.ToLower(strings.TrimSpace(config.Mode))
	switch mode {
	case "", "auto", "on":
		if mode == "" {
			mode = "auto"
		}
	case "off", "none", "false":
		return nil, nil, nil
	default:
		return nil, nil, fmt.Errorf("unknown metrics mode %q (want auto|on|off)", config.Mode)
	}
	if config.Interval <= 0 {
		return nil, nil, fmt.Errorf("metrics interval must be > 0")
	}
	backend := strings.ToLower(strings.TrimSpace(config.NPUBackend))
	if backend == "" {
		backend = "auto"
	}
	switch backend {
	case "auto", "dcmi", "npu-smi", "both", "off":
	default:
		return nil, nil, fmt.Errorf("unknown metrics NPU backend %q (want auto|dcmi|npu-smi|both|off)", config.NPUBackend)
	}

	var sources []metrics.Source
	var warnings []string
	useDCMI := backend == "dcmi" || backend == "both"
	useNPUSmi := backend == "npu-smi" || backend == "both"
	if backend == "auto" {
		if _, ok := metrics.ResolveDCMIProbe(); ok {
			useDCMI = true
		} else if _, err := exec.LookPath("npu-smi"); err == nil {
			useNPUSmi = true
		} else {
			warnings = append(warnings, "NPU metrics unavailable: dcmi_probe and npu-smi are both missing")
		}
	}
	if useDCMI {
		probe, ok := metrics.ResolveDCMIProbe()
		if !ok {
			return nil, warnings, fmt.Errorf("dcmi_probe not found in PATH (set CANN_DCMI_PROBE)")
		}
		sources = append(sources, metrics.NewDCMISource(probe, config.Interval, 2*time.Second))
	}
	if useNPUSmi {
		if _, err := exec.LookPath("npu-smi"); err != nil {
			return nil, warnings, fmt.Errorf("npu-smi not found in PATH: %w", err)
		}
		sources = append(sources, metrics.NewNPUSmiSource("npu-smi", config.Interval, 5*time.Second))
	}
	if config.Proc {
		if len(config.PIDs) == 0 {
			warnings = append(warnings, "process metrics skipped: no target PIDs")
		} else {
			procRoot := strings.TrimSpace(config.ProcRoot)
			if procRoot == "" {
				procRoot = "/proc"
			}
			sources = append(sources, metrics.NewProcSource(config.PIDs, procRoot, config.Interval, 2*time.Second))
		}
	}
	if len(config.Cgroups) > 0 {
		sources = append(sources, metrics.NewCgroupSource(config.Cgroups, config.Interval, 2*time.Second))
	}
	if config.System {
		sources = append(sources, metrics.NewSystemSource("/proc", "/sys", config.Interval, 2*time.Second))
	}
	for _, spec := range config.Prometheus {
		source, err := metrics.NewPrometheusSource(metrics.PrometheusConfig{
			Name: spec.Name, URL: spec.URL, Include: config.Include,
		}, config.Interval, 2*time.Second)
		if err != nil {
			return nil, warnings, fmt.Errorf("prometheus %s: %w", spec.Name, err)
		}
		sources = append(sources, source)
	}
	if mode == "on" && len(sources) == 0 {
		return nil, warnings, fmt.Errorf("metrics mode is on but no usable source is configured")
	}
	return sources, warnings, nil
}

func parsePrometheusSpecs(values []string) ([]prometheusSpec, error) {
	specs := make([]prometheusSpec, 0, len(values))
	for _, value := range values {
		spec, err := parsePrometheusSpec(value)
		if err != nil {
			return nil, err
		}
		specs = append(specs, spec)
	}
	return specs, nil
}
