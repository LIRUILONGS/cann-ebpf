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
	outPath := fs.String("out", "report.html", "write the standalone HTML report to this file")
	title := fs.String("title", "", "report title")
	minDuration := fs.Duration("min-duration", 50*time.Microsecond, "minimum span duration shown on the timeline")
	slowThreshold := fs.Duration("slow-threshold", time.Millisecond, "slow span threshold used by the CDF, heatmap and evidence counters")
	topN := fs.Int("top", 15, "maximum number of functions in the TopN chart")
	maxLanes := fs.Int("max-lanes", 64, "maximum number of TID lanes embedded in the timeline")
	bucketCount := fs.Int("buckets", 160, "number of time buckets used by the heatmap and device busy chart")
	deviceMode := fs.String("device-mode", "matched", "device embedding: matched or none")
	deviceContainerID := fs.String("device-container-id", "", "container id used to translate device pid/tid when pidmap.json is absent")
	maxSeries := fs.Int("max-series", 256, "maximum number of metric series embedded in the report")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if strings.TrimSpace(*input) == "" && strings.TrimSpace(*metricsPath) == "" {
		fmt.Fprintln(os.Stderr, "report: --input and/or --metrics is required")
		return 2
	}
	if strings.TrimSpace(*outPath) == "" {
		fmt.Fprintln(os.Stderr, "report: --out must not be empty")
		return 2
	}
	if *minDuration < 0 || *slowThreshold < 0 || *topN <= 0 || *maxLanes <= 0 || *bucketCount <= 0 || *maxSeries <= 0 {
		fmt.Fprintln(os.Stderr, "report: durations must be non-negative and counts must be positive")
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
	data, err := report.BuildWithMetrics(in, metricsData, report.Options{
		Title:             *title,
		MinDurationNS:     int64(*minDuration),
		SlowThresholdNS:   int64(*slowThreshold),
		BucketCount:       *bucketCount,
		TopN:              *topN,
		MaxLanes:          *maxLanes,
		DeviceMode:        mode,
		DeviceContainerID: *deviceContainerID,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "report: %v\n", err)
		return 3
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
	fmt.Printf("report out=%s spans=%d device_intervals=%d metrics_series=%d bytes=%d\n",
		*outPath, data.Summary.Spans, deviceIntervals, metricSeries, len(html))
	return 0
}
