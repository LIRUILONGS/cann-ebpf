package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cann-ebpf/internal/diagnose"
	"cann-ebpf/internal/report"
)

// cmdReport renders a standalone, offline performance dashboard from an
// existing run. It is strictly read-only: it never attaches probes and never
// rewrites spans.jsonl/device.jsonl.
func cmdReport(args []string) int {
	fs := flag.NewFlagSet("report", flag.ContinueOnError)
	input := fs.String("input", "", "run directory or spans.jsonl path")
	metricsPath := fs.String("metrics", "", "metrics.jsonl path or metrics run directory (optional)")
	stacksPath := fs.String("stacks", "", "stacks run directory or stacks.folded path (optional)")
	outPath := fs.String("out", "report.html", "write the standalone HTML report to this file")
	title := fs.String("title", "", "report title")
	minDuration := fs.Duration("min-duration", 50*time.Microsecond, "minimum span duration shown on the timeline")
	slowThreshold := fs.Duration("slow-threshold", time.Millisecond, "slow span threshold used by the CDF, heatmap and evidence counters")
	topN := fs.Int("top", 15, "maximum number of functions in the TopN chart")
	maxLanes := fs.Int("max-lanes", 64, "maximum number of TID lanes embedded in the timeline")
	maxSpans := fs.Int("max-spans", attachReportDefaultMaxSpans, "maximum scatter points embedded in the HTML; 0 keeps all spans")
	bucketCount := fs.Int("buckets", 160, "number of time buckets used by the heatmap and device busy chart")
	deviceMode := fs.String("device-mode", "matched", "device embedding: matched or none")
	deviceContainerID := fs.String("device-container-id", "", "container id used to translate device pid/tid when pidmap.json is absent")
	maxSeries := fs.Int("max-series", 256, "maximum number of metric series embedded in the report")
	sweepPath := fs.String("sweep", "", "sweep.json manifest for the load-latency view (optional)")
	opsPath := fs.String("ops", "", "ops.jsonl with per-op FLOPs/bytes evidence for Roofline (optional)")
	eventsPath := fs.String("events", "", "events.jsonl with deployment/change annotations (optional)")
	offCPUPath := fs.String("offcpu", "", "offcpu.jsonl with exact off-CPU intervals (optional)")
	otelPath := fs.String("otel", "", "otel.jsonl with cross-service trace spans (optional)")
	abAPath := fs.String("ab-a", "", "run directory for A/B side A (optional)")
	abBPath := fs.String("ab-b", "", "run directory for A/B side B (optional)")
	abWorkloadID := fs.String("ab-workload-id", "", "fixed workload id shared by A/B runs")
	rooflinePeakTFLOPS := fs.Float64("roofline-peak-tflops", 0, "verified reference peak TFLOPS for Roofline")
	rooflinePeakGBPS := fs.Float64("roofline-peak-gbps", 0, "verified reference peak GB/s for Roofline")
	rooflineDType := fs.String("roofline-dtype", "", "dtype associated with the Roofline peak values")
	topologyRootService := fs.String("topology-root-service", "", "optional observed root service for topology")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if strings.TrimSpace(*input) == "" && strings.TrimSpace(*metricsPath) == "" && strings.TrimSpace(*stacksPath) == "" {
		fmt.Fprintln(os.Stderr, "report: --input, --metrics, and/or --stacks is required")
		return 2
	}
	if strings.TrimSpace(*outPath) == "" {
		fmt.Fprintln(os.Stderr, "report: --out must not be empty")
		return 2
	}
	if *minDuration < 0 || *slowThreshold < 0 || *topN <= 0 || *maxLanes <= 0 || *maxSpans < 0 || *bucketCount <= 0 || *maxSeries <= 0 {
		fmt.Fprintln(os.Stderr, "report: durations must be non-negative and counts must be positive")
		return 2
	}
	if *rooflinePeakTFLOPS < 0 || *rooflinePeakGBPS < 0 {
		fmt.Fprintln(os.Stderr, "report: roofline peak values must be non-negative")
		return 2
	}
	if (strings.TrimSpace(*abAPath) == "") != (strings.TrimSpace(*abBPath) == "") {
		fmt.Fprintln(os.Stderr, "report: --ab-a and --ab-b must be provided together")
		return 2
	}
	mode := strings.ToLower(strings.TrimSpace(*deviceMode))
	if mode != "matched" && mode != "none" {
		fmt.Fprintln(os.Stderr, "report: --device-mode must be matched or none")
		return 2
	}
	var in *diagnose.Input
	var err error
	if strings.TrimSpace(*input) != "" {
		in, err = diagnose.Load(*input)
		if err != nil {
			fmt.Fprintf(os.Stderr, "report: %v\n", err)
			return 4
		}
	}
	path := strings.TrimSpace(*metricsPath)
	if path == "" && strings.TrimSpace(*input) != "" {
		candidate := *input
		if info, statErr := os.Stat(candidate); statErr == nil && info.IsDir() {
			candidate = filepath.Join(candidate, "metrics.jsonl")
		} else {
			candidate = filepath.Join(filepath.Dir(candidate), "metrics.jsonl")
		}
		if _, statErr := os.Stat(candidate); statErr == nil {
			path = candidate
		}
	}
	if info, statErr := os.Stat(path); statErr == nil && info.IsDir() {
		path = filepath.Join(path, "metrics.jsonl")
	}
	var metricsData *report.MetricsData
	if path != "" {
		metricsData, err = report.LoadMetrics(path, report.MetricsOptions{MaxSeries: *maxSeries})
		if err != nil {
			fmt.Fprintf(os.Stderr, "report: metrics: %v\n", err)
			return 4
		}
	}
	options := report.Options{
		Title:               *title,
		StacksPath:          *stacksPath,
		MinDurationNS:       int64(*minDuration),
		SlowThresholdNS:     int64(*slowThreshold),
		BucketCount:         *bucketCount,
		TopN:                *topN,
		MaxLanes:            *maxLanes,
		MaxSpanPoints:       *maxSpans,
		DeviceMode:          mode,
		DeviceContainerID:   *deviceContainerID,
		SweepPath:           *sweepPath,
		OpsPath:             *opsPath,
		EventsPath:          *eventsPath,
		OffCPUPath:          *offCPUPath,
		OTelPath:            *otelPath,
		ABAPath:             *abAPath,
		ABBRunPath:          *abBPath,
		ABWorkloadID:        *abWorkloadID,
		RooflinePeakTFLOPS:  *rooflinePeakTFLOPS,
		RooflinePeakGBPS:    *rooflinePeakGBPS,
		RooflineDType:       *rooflineDType,
		TopologyRootService: *topologyRootService,
	}
	var data report.Data
	if in == nil && metricsData == nil && strings.TrimSpace(*stacksPath) != "" {
		data, err = report.Build(nil, options)
	} else {
		data, err = report.BuildWithMetrics(in, metricsData, options)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "report: %v\n", err)
		return 3
	}
	if strings.TrimSpace(*stacksPath) != "" && data.Flamegraph == nil {
		flamegraph, flamegraphErr := report.LoadFlamegraph(*stacksPath)
		if flamegraphErr != nil {
			fmt.Fprintf(os.Stderr, "report: stacks: %v\n", flamegraphErr)
			return 4
		}
		data.Flamegraph = flamegraph
	}
	html, err := report.Render(data)
	if err != nil {
		fmt.Fprintf(os.Stderr, "report: %v\n", err)
		return 3
	}
	if err := os.WriteFile(*outPath, html, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "report: write %s: %v\n", *outPath, err)
		return 4
	}
	deviceIntervals := 0
	if data.Device != nil {
		deviceIntervals = len(data.Device.Intervals)
	}
	metricSeries := 0
	if data.Metrics != nil {
		metricSeries = len(data.Metrics.Series)
	}
	flamegraphState := "none"
	if data.Flamegraph != nil {
		flamegraphState = "unavailable"
		if data.Flamegraph.Status.Available {
			flamegraphState = "available"
		}
	}
	fmt.Printf("report out=%s spans=%d device_intervals=%d metrics_series=%d flamegraph=%s bytes=%d\n",
		*outPath, data.Summary.Spans, deviceIntervals, metricSeries, flamegraphState, len(html))
	return 0
}
