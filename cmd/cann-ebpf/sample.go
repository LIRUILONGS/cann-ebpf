package main

import (
	"context"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"cann-ebpf/internal/metrics"
)

type repeatStrings []string

func (r *repeatStrings) String() string { return strings.Join(*r, ",") }

func (r *repeatStrings) Set(value string) error {
	*r = append(*r, value)
	return nil
}

type repeatInts []int

func (r *repeatInts) String() string {
	parts := make([]string, len(*r))
	for i, value := range *r {
		parts[i] = strconv.Itoa(value)
	}
	return strings.Join(parts, ",")
}

func (r *repeatInts) Set(value string) error {
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fmt.Errorf("invalid integer %q", value)
	}
	*r = append(*r, parsed)
	return nil
}

type prometheusSpec struct {
	Name string
	URL  string
}

type sampleConfig struct {
	Out        string
	Interval   time.Duration
	Duration   time.Duration
	DCMI       bool
	NPUSMI     bool
	PIDs       []int
	Cgroups    []string
	Prometheus []prometheusSpec
	Include    []string
	JSON       bool
}

// cmdSample implements `cann-ebpf sample`, the independent metrics collector.
// It is intentionally separate from cmdMetrics, which exports a completed run.
func cmdSample(args []string) int {
	config, err := parseSampleArgs(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sample: %v\n", err)
		return 2
	}
	sources, err := buildSampleSources(config)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sample: %v\n", err)
		return 4
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	result, runErr := metrics.Run(ctx, metrics.Options{
		OutDir:      config.Out,
		Interval:    config.Interval,
		Duration:    config.Duration,
		Sources:     sources,
		ToolVersion: toolVersion,
	})
	if config.JSON {
		writeJSON(sampleSummary{
			Out: result.OutDir, Metrics: result.MetricsPath, Meta: result.MetaPath,
			Points: result.Points, Status: result.Status, Bytes: result.Bytes,
			SHA256: result.SHA256, Warnings: result.Warnings,
		})
	} else {
		fmt.Printf("sample out=%s points=%d status=%s bytes=%d sha256=%s\n",
			result.OutDir, result.Points, result.Status, result.Bytes, result.SHA256)
		for _, warning := range result.Warnings {
			fmt.Printf("  WARN %s\n", warning)
		}
	}
	if runErr != nil {
		fmt.Fprintf(os.Stderr, "sample: %v\n", runErr)
		return 4
	}
	return 0
}

type sampleSummary struct {
	Out      string   `json:"out"`
	Metrics  string   `json:"metrics"`
	Meta     string   `json:"meta"`
	Points   int      `json:"points"`
	Status   string   `json:"status"`
	Bytes    int64    `json:"bytes"`
	SHA256   string   `json:"sha256"`
	Warnings []string `json:"warnings,omitempty"`
}

func parseSampleArgs(args []string) (*sampleConfig, error) {
	fs := flag.NewFlagSet("sample", flag.ContinueOnError)
	out := fs.String("out", "", "output directory for metrics.jsonl and metrics.meta.json (required)")
	interval := fs.Duration("interval", time.Second, "base sampling interval")
	duration := fs.Duration("duration", 0, "collection duration; 0 means until SIGINT/SIGTERM")
	dcmi := fs.Bool("dcmi", false, "sample the dcmi_probe backend")
	npuSMI := fs.Bool("npu-smi", false, "sample npu-smi info (minimum interval 5s)")
	var pids repeatInts
	fs.Var(&pids, "pid", "process id to sample from /proc (repeatable)")
	var cgroups repeatStrings
	fs.Var(&cgroups, "cgroup", "cgroup v2 path to sample (repeatable)")
	var prometheus repeatStrings
	fs.Var(&prometheus, "prometheus", "Prometheus endpoint as [name=]http(s)://host/metrics (repeatable)")
	var include repeatStrings
	fs.Var(&include, "include", "Prometheus metric-name allowlist regex (repeatable)")
	jsonOut := fs.Bool("json", false, "print a JSON result summary")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if strings.TrimSpace(*out) == "" {
		return nil, fmt.Errorf("--out is required")
	}
	if *interval <= 0 {
		return nil, fmt.Errorf("--interval must be > 0")
	}
	if *duration < 0 {
		return nil, fmt.Errorf("--duration must be >= 0")
	}
	for _, pattern := range include {
		if _, err := regexp.Compile(pattern); err != nil {
			return nil, fmt.Errorf("--include %q: %w", pattern, err)
		}
	}
	var specs []prometheusSpec
	for _, value := range prometheus {
		spec, err := parsePrometheusSpec(value)
		if err != nil {
			return nil, err
		}
		specs = append(specs, spec)
	}
	for _, pid := range pids {
		if pid <= 0 {
			return nil, fmt.Errorf("--pid must be positive, got %d", pid)
		}
	}
	return &sampleConfig{
		Out: *out, Interval: *interval, Duration: *duration,
		DCMI: *dcmi, NPUSMI: *npuSMI, PIDs: append([]int(nil), pids...),
		Cgroups: append([]string(nil), cgroups...), Prometheus: specs,
		Include: append([]string(nil), include...), JSON: *jsonOut,
	}, nil
}

func parsePrometheusSpec(value string) (prometheusSpec, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return prometheusSpec{}, fmt.Errorf("--prometheus value must not be empty")
	}
	name := ""
	rawURL := value
	if equal := strings.Index(value, "="); equal > 0 && !strings.Contains(value[:equal], "://") {
		name = strings.TrimSpace(value[:equal])
		rawURL = strings.TrimSpace(value[equal+1:])
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return prometheusSpec{}, fmt.Errorf("--prometheus %q: %w", value, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return prometheusSpec{}, fmt.Errorf("--prometheus %q: scheme must be http or https", value)
	}
	if parsed.Host == "" {
		return prometheusSpec{}, fmt.Errorf("--prometheus %q: host is required", value)
	}
	if name == "" {
		name = strings.NewReplacer(":", "_", ".", "_").Replace(parsed.Host)
	}
	return prometheusSpec{Name: name, URL: rawURL}, nil
}

func buildSampleSources(config *sampleConfig) ([]metrics.Source, error) {
	explicit := config.DCMI || config.NPUSMI || len(config.PIDs) > 0 || len(config.Cgroups) > 0 || len(config.Prometheus) > 0
	useDCMI := config.DCMI
	useNPUSMI := config.NPUSMI
	probe := ""
	if !explicit {
		var ok bool
		probe, ok = metrics.ResolveDCMIProbe()
		if ok {
			useDCMI = true
		} else if _, err := exec.LookPath("npu-smi"); err == nil {
			useNPUSMI = true
		} else {
			return nil, fmt.Errorf("no metrics source available: dcmi_probe and npu-smi are both missing")
		}
	}
	if useDCMI && probe == "" {
		var ok bool
		probe, ok = metrics.ResolveDCMIProbe()
		if !ok {
			return nil, fmt.Errorf("dcmi_probe not found in PATH (set CANN_DCMI_PROBE)")
		}
	}
	if useNPUSMI {
		if _, err := exec.LookPath("npu-smi"); err != nil {
			return nil, fmt.Errorf("npu-smi not found in PATH: %w", err)
		}
	}
	var sources []metrics.Source
	if useDCMI {
		sources = append(sources, metrics.NewDCMISource(probe, config.Interval, 2*time.Second))
	}
	if useNPUSMI {
		sources = append(sources, metrics.NewNPUSmiSource("npu-smi", config.Interval, 5*time.Second))
	}
	if len(config.PIDs) > 0 {
		sources = append(sources, metrics.NewProcSource(config.PIDs, "/proc", config.Interval, 2*time.Second))
	}
	if len(config.Cgroups) > 0 {
		sources = append(sources, metrics.NewCgroupSource(config.Cgroups, config.Interval, 2*time.Second))
	}
	for _, spec := range config.Prometheus {
		source, err := metrics.NewPrometheusSource(metrics.PrometheusConfig{
			Name: spec.Name, URL: spec.URL, Include: config.Include,
		}, config.Interval, 2*time.Second)
		if err != nil {
			return nil, err
		}
		sources = append(sources, source)
	}
	if len(sources) == 0 {
		return nil, fmt.Errorf("no metrics source configured")
	}
	return sources, nil
}
