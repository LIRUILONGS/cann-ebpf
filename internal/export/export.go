// Package export renders a completed cann-ebpf run as Prometheus text metrics.
// It is read-only and derives everything from the same evidence diagnose uses;
// it never attaches probes or rewrites spans.
package export

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"cann-ebpf/internal/diagnose"
)

const SchemaV1 = "cann.ebpf.metrics.v1"

// LatestRun resolves --input to one run directory. If input itself contains
// spans.jsonl it is used as-is; otherwise the newest child run with a complete
// manifest is selected, so Prometheus keeps scraping the latest evidence.
func LatestRun(root string) (string, error) {
	if root == "" {
		return "", fmt.Errorf("metrics input path is empty")
	}
	if _, err := os.Stat(filepath.Join(root, "spans.jsonl")); err == nil {
		return root, nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", root, err)
	}
	type candidate struct {
		path string
		mod  int64
	}
	var candidates []candidate
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(root, entry.Name())
		if _, err := os.Stat(filepath.Join(dir, "spans.jsonl")); err != nil {
			continue
		}
		// Prefer completed runs; a run still being written is skipped.
		manifest, err := diagnose.Load(dir)
		if err != nil {
			continue
		}
		if manifest.Manifest != nil && manifest.Manifest.Status != "complete" && manifest.Manifest.Status != "" {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		mod := info.ModTime().UnixNano()
		if manifest.Manifest != nil && manifest.Manifest.EndedAt != nil {
			mod = manifest.Manifest.EndedAt.UnixNano()
		}
		candidates = append(candidates, candidate{path: dir, mod: mod})
	}
	if len(candidates) == 0 {
		return "", fmt.Errorf("no completed run with spans.jsonl under %s", root)
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].mod > candidates[j].mod })
	return candidates[0].path, nil
}

// Build loads one run and returns the rendered metrics plus the run path used.
func Build(input string) (string, string, error) {
	run, err := LatestRun(input)
	if err != nil {
		return "", "", err
	}
	loaded, err := diagnose.Load(run)
	if err != nil {
		return "", "", err
	}
	report, err := diagnose.Build(loaded, diagnose.Options{})
	if err != nil {
		return "", "", err
	}
	return Render(report), run, nil
}

// Render converts a diagnosis report into Prometheus text exposition format.
// Label values are limited to fixed enums (function/category/device/verdict),
// keeping cardinality bounded.
func Render(report diagnose.Report) string {
	var b strings.Builder
	line := func(name, help, typ string) {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, typ)
	}
	gauge := func(name string, value float64, labels ...string) {
		b.WriteString(name)
		if len(labels) > 0 {
			b.WriteString("{")
			for i := 0; i+1 < len(labels); i += 2 {
				if i > 0 {
					b.WriteString(",")
				}
				fmt.Fprintf(&b, "%s=%q", labels[i], escapeLabel(labels[i+1]))
			}
			b.WriteString("}")
		}
		b.WriteString(" ")
		b.WriteString(formatFloat(value))
		b.WriteString("\n")
	}

	line("cann_ebpf_run_info", "Run identity and completeness.", "gauge")
	gauge("cann_ebpf_run_info", 1,
		"run_id", report.Run.RunID, "profile", report.Run.Profile,
		"host", report.Run.Host, "status", report.Run.Status)

	line("cann_ebpf_spans_total", "Observed host spans in the run.", "gauge")
	gauge("cann_ebpf_spans_total", float64(report.SpanCount), "run_id", report.Run.RunID)
	line("cann_ebpf_dropped_events_total", "Ring buffer events lost during the run.", "gauge")
	gauge("cann_ebpf_dropped_events_total", float64(report.Run.DroppedEvents), "run_id", report.Run.RunID)

	line("cann_ebpf_function_duration_seconds", "Per-function duration quantiles (nearest-rank).", "gauge")
	for _, fn := range report.Functions {
		for _, q := range []struct {
			label string
			value int64
		}{{"0.5", fn.P50NS}, {"0.95", fn.P95NS}, {"1.0", fn.MaxNS}} {
			gauge("cann_ebpf_function_duration_seconds", float64(q.value)/1e9,
				"function", fn.Function, "category", fn.Category, "quantile", q.label)
		}
	}

	line("cann_ebpf_category_spans_total", "Spans by category.", "gauge")
	line("cann_ebpf_category_duration_seconds_total", "Total duration by category.", "gauge")
	for _, cat := range report.Categories {
		gauge("cann_ebpf_category_spans_total", float64(cat.Count), "category", cat.Category)
		gauge("cann_ebpf_category_duration_seconds_total", float64(cat.TotalNS)/1e9, "category", cat.Category)
	}

	line("cann_ebpf_blocking_measured", "1 if off-CPU/futex samples exist for this run.", "gauge")
	gauge("cann_ebpf_blocking_measured", boolFloat(report.Blocking.Measured))
	line("cann_ebpf_blocking_offcpu_seconds_total", "Accumulated host off-CPU time.", "gauge")
	gauge("cann_ebpf_blocking_offcpu_seconds_total", float64(report.Blocking.OffCPUNS)/1e9)
	line("cann_ebpf_blocking_futex_seconds_total", "Accumulated futex wait time.", "gauge")
	gauge("cann_ebpf_blocking_futex_seconds_total", float64(report.Blocking.FutexNS)/1e9)
	line("cann_ebpf_blocking_offcpu_ratio", "off-CPU / sampled wall time.", "gauge")
	gauge("cann_ebpf_blocking_offcpu_ratio", report.Blocking.OffCPURatio)
	line("cann_ebpf_blocking_futex_ratio", "futex / sampled wall time.", "gauge")
	gauge("cann_ebpf_blocking_futex_ratio", report.Blocking.FutexRatio)
	line("cann_ebpf_blocking_invalid_samples_total", "Samples discarded as physically impossible.", "gauge")
	gauge("cann_ebpf_blocking_invalid_samples_total", float64(report.Blocking.InvalidSamples))

	line("cann_ebpf_hardware_available", "1 if npu-smi hardware samples were found.", "gauge")
	gauge("cann_ebpf_hardware_available", boolFloat(report.Hardware.Available))
	if report.Hardware.HBMFreePctMin != nil {
		line("cann_ebpf_hardware_hbm_free_percent_min", "Minimum HBM free percent across devices.", "gauge")
		gauge("cann_ebpf_hardware_hbm_free_percent_min", *report.Hardware.HBMFreePctMin)
	}
	line("cann_ebpf_hardware_device_hbm_free_percent", "Per-device minimum HBM free percent.", "gauge")
	line("cann_ebpf_hardware_device_aicore_percent_max", "Per-device maximum AICore utilisation.", "gauge")
	line("cann_ebpf_hardware_device_power_watts", "Per-device average power.", "gauge")
	line("cann_ebpf_hardware_device_temp_celsius", "Per-device maximum temperature.", "gauge")
	for _, device := range report.Hardware.Devices {
		labels := []string{"device", strconv.Itoa(device.DeviceID), "name", device.Name}
		gauge("cann_ebpf_hardware_device_hbm_free_percent", device.HBMFreePctMin, labels...)
		gauge("cann_ebpf_hardware_device_aicore_percent_max", device.AICoreMax, labels...)
		gauge("cann_ebpf_hardware_device_power_watts", device.PowerAvgW, labels...)
		gauge("cann_ebpf_hardware_device_temp_celsius", device.TempMaxC, labels...)
	}

	line("cann_ebpf_device_match_rate", "Host span to device timeline match rate.", "gauge")
	line("cann_ebpf_device_slow_sync_match_rate", "Slow sync span match rate.", "gauge")
	line("cann_ebpf_device_span_busy_seconds_total", "Device busy union per device.", "gauge")
	if report.Device != nil {
		gauge("cann_ebpf_device_match_rate", report.Device.MatchRate)
		gauge("cann_ebpf_device_slow_sync_match_rate", report.Device.SlowSyncMatchRate)
		for _, device := range report.Device.Devices {
			labels := []string{"device", strconv.Itoa(device.DeviceID)}
			gauge("cann_ebpf_device_span_busy_seconds_total", float64(device.BusyNS)/1e9, labels...)
		}
	} else {
		gauge("cann_ebpf_device_match_rate", 0)
		gauge("cann_ebpf_device_slow_sync_match_rate", 0)
	}

	line("cann_ebpf_verdict", "Active verdicts for this run (1 = present).", "gauge")
	verdicts := append([]diagnose.Verdict(nil), report.Verdicts...)
	sort.Slice(verdicts, func(i, j int) bool { return verdicts[i].Verdict < verdicts[j].Verdict })
	for _, verdict := range verdicts {
		gauge("cann_ebpf_verdict", 1, "verdict", verdict.Verdict)
	}
	line("cann_ebpf_unknowns_total", "Number of explicit unknowns in the report.", "gauge")
	gauge("cann_ebpf_unknowns_total", float64(len(report.Unknowns)))
	line("cann_ebpf_warnings_total", "Number of evidence/collection warnings.", "gauge")
	gauge("cann_ebpf_warnings_total", float64(len(report.Warnings)))
	return b.String()
}

func escapeLabel(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "\n", "\\n")
	return value
}

func formatFloat(value float64) string {
	return strconv.FormatFloat(value, 'g', -1, 64)
}

func boolFloat(value bool) float64 {
	if value {
		return 1
	}
	return 0
}
