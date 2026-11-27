package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cann-ebpf/internal/diagnose"
	"cann-ebpf/internal/report"
	"cann-ebpf/internal/runlog"
)

// The default report is the browser-friendly projection. Operators who need
// every scatter point can pass --report-max-spans 0 explicitly; raw
// spans.jsonl is always complete and is never changed by report generation.
const attachReportDefaultMaxSpans = 20_000

type attachReportSummary struct {
	Path          string `json:"path"`
	Bytes         int    `json:"bytes"`
	Spans         int    `json:"spans"`
	MetricsSeries int    `json:"metrics_series"`
	Status        string `json:"status"`
	Downsampled   bool   `json:"downsampled"`
}

// resolveAttachReportPath maps the --report flag to an output path. "off" and
// an empty value disable report generation. "auto" writes report.html into the
// run directory; an existing auto target gets a timestamp suffix so a previous
// derived report is never silently overwritten. A trailing path separator or
// an existing directory is treated as a directory.
func resolveAttachReportPath(value, runDir string) (string, error) {
	value = strings.TrimSpace(value)
	switch strings.ToLower(value) {
	case "", "off", "none", "false":
		return "", nil
	case "auto":
		candidate := filepath.Join(runDir, "report.html")
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate, nil
		} else if err != nil {
			return "", fmt.Errorf("stat report target %s: %w", candidate, err)
		}
		return filepath.Join(runDir, fmt.Sprintf("report-%d.html", time.Now().Unix())), nil
	}
	if info, err := os.Stat(value); err == nil {
		if info.IsDir() {
			return filepath.Join(value, "report.html"), nil
		}
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("stat report target %s: %w", value, err)
	}
	if strings.HasSuffix(value, "/") || strings.HasSuffix(value, `\`) {
		return filepath.Join(value, "report.html"), nil
	}
	return value, nil
}

// generateAttachReport reloads the durable run from disk, verifies the manifest
// is complete, optionally loads metrics.jsonl, and writes one standalone HTML
// document. It never invokes attach and never rewrites raw evidence.
func generateAttachReport(runDir, outPath, title string, maxSpanPoints int) (attachReportSummary, error) {
	var summary attachReportSummary
	summary.Path = outPath
	if strings.TrimSpace(runDir) == "" {
		return summary, fmt.Errorf("run directory is empty")
	}
	in, err := diagnose.Load(runDir)
	if err != nil {
		return summary, fmt.Errorf("load run: %w", err)
	}
	if in.Manifest == nil {
		return summary, fmt.Errorf("manifest.json is missing; refusing to generate a report")
	}
	if in.Manifest.Status != runlog.ManifestStatusComplete {
		detail := strings.TrimSpace(in.Manifest.Error)
		if detail == "" {
			detail = "no error detail in manifest"
		}
		return summary, fmt.Errorf("run status is %q, not complete: %s", in.Manifest.Status, detail)
	}
	summary.Status = in.Manifest.Status

	metricsPath := filepath.Join(runDir, "metrics.jsonl")
	var metricsData *report.MetricsData
	metricsWarning := ""
	if _, statErr := os.Stat(metricsPath); statErr == nil {
		metricsData, err = report.LoadMetrics(metricsPath, report.MetricsOptions{MaxSeries: 256})
		if err != nil {
			metricsWarning = fmt.Sprintf("metrics.jsonl 读取失败，报告按 spans-only 生成：%v", err)
		}
	} else if !os.IsNotExist(statErr) {
		metricsWarning = fmt.Sprintf("metrics.jsonl 无法访问：%v", statErr)
	}

	data, err := report.BuildWithMetrics(in, metricsData, report.Options{
		Title:               title,
		MaxSpanPoints:       maxSpanPoints,
		SlowThresholdNS:     int64(time.Millisecond),
		MinDurationNS:       50_000,
		DeviceMode:          "matched",
		TopologyRootService: "",
	})
	if err != nil {
		return summary, fmt.Errorf("build report: %w", err)
	}
	if data.Run.Status != "" && data.Run.Status != runlog.ManifestStatusComplete {
		return summary, fmt.Errorf("derived report status is %q, not complete", data.Run.Status)
	}
	if metricsWarning != "" {
		data.Warnings = append(data.Warnings, metricsWarning)
	}
	summary.Spans = data.Summary.Spans
	summary.Downsampled = len(data.Spans) < data.Summary.Spans
	if data.Metrics != nil {
		summary.MetricsSeries = len(data.Metrics.Series)
	}
	html, err := report.Render(data)
	if err != nil {
		return summary, fmt.Errorf("render report: %w", err)
	}
	if err := writeReportAtomic(outPath, html); err != nil {
		return summary, err
	}
	summary.Bytes = len(html)
	return summary, nil
}

func writeReportAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create report directory %s: %w", dir, err)
	}
	file, err := os.CreateTemp(dir, ".cann-report-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary report: %w", err)
	}
	tempPath := file.Name()
	cleanup := func() {
		_ = file.Close()
		_ = os.Remove(tempPath)
	}
	if _, err := file.Write(data); err != nil {
		cleanup()
		return fmt.Errorf("write report: %w", err)
	}
	if err := file.Chmod(0o644); err != nil {
		cleanup()
		return fmt.Errorf("chmod report: %w", err)
	}
	if err := file.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("sync report: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("close report: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		// Windows cannot atomically replace an existing file. The report is a
		// derived artifact, so a remove+retry is acceptable; raw evidence is
		// never touched.
		if removeErr := os.Remove(path); removeErr != nil && !os.IsNotExist(removeErr) {
			_ = os.Remove(tempPath)
			return fmt.Errorf("replace report %s: %w", path, err)
		}
		if retryErr := os.Rename(tempPath, path); retryErr != nil {
			_ = os.Remove(tempPath)
			return fmt.Errorf("rename report %s: %w", path, retryErr)
		}
	}
	return nil
}
