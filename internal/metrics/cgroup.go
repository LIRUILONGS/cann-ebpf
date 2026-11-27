package metrics

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// CgroupSource reads cgroup v2 resource counters for one or more cgroup paths.
type CgroupSource struct {
	Paths    []string
	Every    time.Duration
	Deadline time.Duration
}

// NewCgroupSource creates a cgroup v2 source.
func NewCgroupSource(paths []string, interval, timeout time.Duration) *CgroupSource {
	if interval <= 0 {
		interval = time.Second
	}
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	return &CgroupSource{Paths: append([]string(nil), paths...), Every: interval, Deadline: timeout}
}

func (s *CgroupSource) Name() string            { return "cgroup" }
func (s *CgroupSource) Interval() time.Duration { return s.Every }
func (s *CgroupSource) Timeout() time.Duration  { return s.Deadline }

func (s *CgroupSource) Collect(ctx context.Context) ([]Point, []string, error) {
	var points []Point
	var warnings []string
	for _, path := range s.Paths {
		if err := ctx.Err(); err != nil {
			return points, warnings, err
		}
		if strings.TrimSpace(path) == "" {
			warnings = append(warnings, "cgroup: empty path")
			continue
		}
		pathPoints, pathWarnings := collectCgroupPath(path)
		points = append(points, pathPoints...)
		warnings = append(warnings, pathWarnings...)
	}
	return points, warnings, nil
}

func collectCgroupPath(path string) ([]Point, []string) {
	var points []Point
	var warnings []string
	if info, err := os.Stat(path); err != nil {
		return nil, []string{fmt.Sprintf("cgroup %s: %v", path, err)}
	} else if !info.IsDir() {
		return nil, []string{fmt.Sprintf("cgroup %s: not a directory", path)}
	}
	labels := map[string]string{"cgroup": path}
	device := "host"
	addGauge := func(name, unit string, value float64) {
		point := Gauge(name, unit, value, labels)
		point.Source = "cgroup"
		point.Device = device
		points = append(points, point)
	}
	addCounter := func(name, unit string, value float64) {
		point := Counter(name, unit, value, labels)
		point.Source = "cgroup"
		point.Device = device
		points = append(points, point)
	}
	if stat, err := readKeyValueFile(filepath.Join(path, "cpu.stat")); err == nil {
		if usec, ok := stat["usage_usec"]; ok {
			addCounter("cgroup_cpu_usage_seconds", "seconds", usec/1e6)
		}
		if usec, ok := stat["user_usec"]; ok {
			addCounter("cgroup_cpu_user_seconds", "seconds", usec/1e6)
		}
		if usec, ok := stat["system_usec"]; ok {
			addCounter("cgroup_cpu_system_seconds", "seconds", usec/1e6)
		}
		if value, ok := stat["nr_periods"]; ok {
			addCounter("cgroup_cpu_nr_periods", "count", value)
		}
		if value, ok := stat["nr_throttled"]; ok {
			addCounter("cgroup_cpu_nr_throttled", "count", value)
		}
		if usec, ok := stat["throttled_usec"]; ok {
			addCounter("cgroup_cpu_throttled_seconds", "seconds", usec/1e6)
		}
	} else if !os.IsNotExist(err) {
		warnings = append(warnings, fmt.Sprintf("cgroup %s: %v", path, err))
	}
	for _, pressure := range []struct {
		file   string
		prefix string
	}{
		{file: "cpu.pressure", prefix: "cgroup_cpu_pressure_"},
		{file: "memory.pressure", prefix: "cgroup_memory_pressure_"},
		{file: "io.pressure", prefix: "cgroup_io_pressure_"},
	} {
		samples, err := readPressureFile(filepath.Join(path, pressure.file))
		if err != nil {
			if !os.IsNotExist(err) {
				warnings = append(warnings, fmt.Sprintf("cgroup %s: %s: %v", path, pressure.file, err))
			}
			continue
		}
		for _, kind := range []string{"some", "full"} {
			sample, ok := samples[kind]
			if !ok {
				continue
			}
			addGauge(pressure.prefix+kind+"_avg10_ratio", "ratio", sample.avg10/100)
			addGauge(pressure.prefix+kind+"_avg60_ratio", "ratio", sample.avg60/100)
			addGauge(pressure.prefix+kind+"_avg300_ratio", "ratio", sample.avg300/100)
			addCounter(pressure.prefix+kind+"_total_seconds", "seconds", sample.total/1e6)
		}
	}
	if value, ok := readScalarFile(filepath.Join(path, "memory.current")); ok {
		addGauge("cgroup_memory_current_bytes", "bytes", value)
	}
	if value, ok := readScalarFile(filepath.Join(path, "memory.max")); ok {
		addGauge("cgroup_memory_max_bytes", "bytes", value)
	}
	if value, ok := readScalarFile(filepath.Join(path, "memory.peak")); ok {
		addGauge("cgroup_memory_peak_bytes", "bytes", value)
	}
	if events, err := readKeyValueFile(filepath.Join(path, "memory.events")); err == nil {
		for _, event := range []string{"low", "high", "max", "oom", "oom_kill"} {
			if value, ok := events[event]; ok {
				point := Counter("cgroup_memory_events_total", "count", value, eventLabels(labels, event))
				point.Source = "cgroup"
				point.Device = device
				points = append(points, point)
			}
		}
	} else if !os.IsNotExist(err) {
		warnings = append(warnings, fmt.Sprintf("cgroup %s: %v", path, err))
	}
	if value, ok := readScalarFile(filepath.Join(path, "pids.current")); ok {
		addGauge("cgroup_pids_current", "count", value)
	}
	if value, ok := readScalarFile(filepath.Join(path, "pids.max")); ok {
		addGauge("cgroup_pids_max", "count", value)
	}
	return points, warnings
}

type pressureSample struct {
	avg10  float64
	avg60  float64
	avg300 float64
	total  float64
}

func readPressureFile(path string) (map[string]pressureSample, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	out := make(map[string]pressureSample)
	for lineIndex, rawLine := range strings.Split(string(data), "\n") {
		fields := strings.Fields(rawLine)
		if len(fields) == 0 {
			continue
		}
		kind := fields[0]
		if kind != "some" && kind != "full" {
			return nil, fmt.Errorf("line %d: unknown pressure type %q", lineIndex+1, kind)
		}
		if len(fields) != 5 {
			return nil, fmt.Errorf("line %d: expected 5 fields, got %d", lineIndex+1, len(fields))
		}
		values := make(map[string]float64, 4)
		for _, field := range fields[1:] {
			key, rawValue, ok := strings.Cut(field, "=")
			if !ok {
				return nil, fmt.Errorf("line %d: malformed field %q", lineIndex+1, field)
			}
			switch key {
			case "avg10", "avg60", "avg300", "total":
			default:
				return nil, fmt.Errorf("line %d: unknown pressure field %q", lineIndex+1, key)
			}
			if _, duplicate := values[key]; duplicate {
				return nil, fmt.Errorf("line %d: duplicate pressure field %q", lineIndex+1, key)
			}
			value, err := strconv.ParseFloat(rawValue, 64)
			if err != nil {
				return nil, fmt.Errorf("line %d: %s: %w", lineIndex+1, key, err)
			}
			if math.IsNaN(value) || math.IsInf(value, 0) {
				return nil, fmt.Errorf("line %d: %s is not finite", lineIndex+1, key)
			}
			values[key] = value
		}
		for _, key := range []string{"avg10", "avg60", "avg300", "total"} {
			if _, ok := values[key]; !ok {
				return nil, fmt.Errorf("line %d: missing pressure field %q", lineIndex+1, key)
			}
		}
		if _, duplicate := out[kind]; duplicate {
			return nil, fmt.Errorf("line %d: duplicate pressure type %q", lineIndex+1, kind)
		}
		out[kind] = pressureSample{
			avg10:  values["avg10"],
			avg60:  values["avg60"],
			avg300: values["avg300"],
			total:  values["total"],
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("empty pressure file")
	}
	return out, nil
}

func eventLabels(base map[string]string, event string) map[string]string {
	labels := cloneLabels(base)
	labels["event"] = event
	return labels
}

func readScalarFile(path string) (float64, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	text := strings.TrimSpace(string(data))
	if text == "" || text == "max" {
		return 0, false
	}
	value, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return 0, false
	}
	return value, true
}

func readKeyValueFile(path string) (map[string]float64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	out := make(map[string]float64)
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		value, err := strconv.ParseFloat(fields[1], 64)
		if err != nil {
			continue
		}
		out[fields[0]] = value
	}
	return out, nil
}
