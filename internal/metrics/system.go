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

const (
	systemProcStatUserHZ  = 100.0
	systemDiskSectorBytes = 512.0
)

var systemCPUStatModes = []string{
	"user", "nice", "system", "idle", "iowait", "irq", "softirq", "steal",
}

type systemMeminfoSpec struct {
	key  string
	name string
}

var systemMeminfoSpecs = []systemMeminfoSpec{
	{key: "MemTotal", name: "node_memory_mem_total_bytes"},
	{key: "MemFree", name: "node_memory_mem_free_bytes"},
	{key: "MemAvailable", name: "node_memory_mem_available_bytes"},
	{key: "Buffers", name: "node_memory_buffers_bytes"},
	{key: "Cached", name: "node_memory_cached_bytes"},
	{key: "SwapTotal", name: "node_memory_swap_total_bytes"},
	{key: "SwapFree", name: "node_memory_swap_free_bytes"},
	{key: "Dirty", name: "node_memory_dirty_bytes"},
	{key: "Writeback", name: "node_memory_writeback_bytes"},
	{key: "Slab", name: "node_memory_slab_bytes"},
}

// SystemSource reads host-level gauges and counters from procfs and sysfs. It
// is deliberately independent from process and cgroup sources: missing or
// malformed files are reported as warnings and never fail the whole run.
type SystemSource struct {
	ProcRoot string
	SysRoot  string
	Every    time.Duration
	Deadline time.Duration
}

// NewSystemSource creates a host system metrics source. Empty roots default to
// /proc and /sys.
func NewSystemSource(procRoot, sysRoot string, interval, timeout time.Duration) *SystemSource {
	if procRoot == "" {
		procRoot = "/proc"
	}
	if sysRoot == "" {
		sysRoot = "/sys"
	}
	if interval <= 0 {
		interval = time.Second
	}
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	return &SystemSource{ProcRoot: procRoot, SysRoot: sysRoot, Every: interval, Deadline: timeout}
}

func (s *SystemSource) Name() string            { return "system" }
func (s *SystemSource) Interval() time.Duration { return s.Every }
func (s *SystemSource) Timeout() time.Duration  { return s.Deadline }

func (s *SystemSource) Collect(ctx context.Context) ([]Point, []string, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	collector := &systemCollector{procRoot: s.ProcRoot, sysRoot: s.SysRoot}
	if collector.procRoot == "" {
		collector.procRoot = "/proc"
	}
	if collector.sysRoot == "" {
		collector.sysRoot = "/sys"
	}
	for _, collect := range []func(){
		collector.collectCPUStat,
		collector.collectLoadAvg,
		collector.collectMeminfo,
		collector.collectPressure,
		collector.collectDiskstats,
		collector.collectNetDev,
		collector.collectThermal,
		collector.collectCPUFrequency,
	} {
		if err := ctx.Err(); err != nil {
			return collector.points, collector.warnings, err
		}
		collect()
	}
	return collector.points, collector.warnings, nil
}

type systemCollector struct {
	procRoot string
	sysRoot  string
	points   []Point
	warnings []string
}

func (c *systemCollector) warn(format string, args ...any) {
	c.warnings = append(c.warnings, fmt.Sprintf(format, args...))
}

func (c *systemCollector) addGauge(name, unit string, value float64, labels map[string]string) {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		c.warn("system: refusing non-finite value for %s", name)
		return
	}
	point := Gauge(name, unit, value, labels)
	point.Source = "system"
	point.Device = "host"
	c.points = append(c.points, point)
}

func (c *systemCollector) addCounter(name, unit string, value float64, labels map[string]string) {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		c.warn("system: refusing non-finite value for %s", name)
		return
	}
	point := Counter(name, unit, value, labels)
	point.Source = "system"
	point.Device = "host"
	c.points = append(c.points, point)
}

func (c *systemCollector) collectCPUStat() {
	path := filepath.Join(c.procRoot, "stat")
	data, err := os.ReadFile(path)
	if err != nil {
		c.warn("system: %s: %v", path, err)
		return
	}
	sawAggregate := false
	for lineIndex, rawLine := range strings.Split(string(data), "\n") {
		fields := strings.Fields(rawLine)
		if len(fields) == 0 || fields[0] != "cpu" {
			continue
		}
		sawAggregate = true
		if len(fields) < 1+len(systemCPUStatModes) {
			c.warn("system: %s line %d: expected at least %d cpu fields, got %d", path, lineIndex+1, len(systemCPUStatModes), len(fields)-1)
			continue
		}
		values := make([]uint64, len(systemCPUStatModes))
		valid := true
		for i := range values {
			value, err := strconv.ParseUint(fields[i+1], 10, 64)
			if err != nil {
				c.warn("system: %s line %d: %s: %v", path, lineIndex+1, systemCPUStatModes[i], err)
				valid = false
				break
			}
			values[i] = value
		}
		if !valid {
			continue
		}
		for i, mode := range systemCPUStatModes {
			c.addCounter("node_cpu_seconds_total", "seconds", float64(values[i])/systemProcStatUserHZ, map[string]string{"mode": mode})
		}
	}
	if !sawAggregate {
		c.warn("system: %s: aggregate cpu line not found", path)
	}
}

func (c *systemCollector) collectLoadAvg() {
	path := filepath.Join(c.procRoot, "loadavg")
	data, err := os.ReadFile(path)
	if err != nil {
		c.warn("system: %s: %v", path, err)
		return
	}
	var fields []string
	for _, rawLine := range strings.Split(string(data), "\n") {
		if parsed := strings.Fields(rawLine); len(parsed) > 0 {
			fields = parsed
			break
		}
	}
	if len(fields) < 4 {
		c.warn("system: %s: expected at least 4 fields, got %d", path, len(fields))
		return
	}
	load1, ok := parseSystemNonNegativeFloat(fields[0])
	if !ok {
		c.warn("system: %s: invalid load1 %q", path, fields[0])
		return
	}
	load5, ok := parseSystemNonNegativeFloat(fields[1])
	if !ok {
		c.warn("system: %s: invalid load5 %q", path, fields[1])
		return
	}
	load15, ok := parseSystemNonNegativeFloat(fields[2])
	if !ok {
		c.warn("system: %s: invalid load15 %q", path, fields[2])
		return
	}
	tasks := strings.SplitN(fields[3], "/", 2)
	if len(tasks) != 2 {
		c.warn("system: %s: invalid task counts %q", path, fields[3])
		return
	}
	runnable, err := strconv.ParseUint(tasks[0], 10, 64)
	if err != nil {
		c.warn("system: %s: runnable tasks: %v", path, err)
		return
	}
	total, err := strconv.ParseUint(tasks[1], 10, 64)
	if err != nil {
		c.warn("system: %s: total tasks: %v", path, err)
		return
	}
	c.addGauge("node_load1", "load", load1, nil)
	c.addGauge("node_load5", "load", load5, nil)
	c.addGauge("node_load15", "load", load15, nil)
	c.addGauge("node_runnable_tasks", "count", float64(runnable), nil)
	c.addGauge("node_total_tasks", "count", float64(total), nil)
}

func (c *systemCollector) collectMeminfo() {
	path := filepath.Join(c.procRoot, "meminfo")
	data, err := os.ReadFile(path)
	if err != nil {
		c.warn("system: %s: %v", path, err)
		return
	}
	known := make(map[string]struct{}, len(systemMeminfoSpecs))
	for _, spec := range systemMeminfoSpecs {
		known[spec.key] = struct{}{}
	}
	values := make(map[string]uint64, len(systemMeminfoSpecs))
	for lineIndex, rawLine := range strings.Split(string(data), "\n") {
		fields := strings.Fields(rawLine)
		if len(fields) == 0 {
			continue
		}
		key := strings.TrimSuffix(fields[0], ":")
		if _, ok := known[key]; !ok {
			continue
		}
		if len(fields) < 3 {
			c.warn("system: %s line %d: expected value and unit for %s", path, lineIndex+1, key)
			continue
		}
		if !strings.EqualFold(fields[2], "kB") {
			c.warn("system: %s line %d: unsupported unit %q for %s", path, lineIndex+1, fields[2], key)
			continue
		}
		value, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			c.warn("system: %s line %d: %s: %v", path, lineIndex+1, key, err)
			continue
		}
		values[key] = value
	}
	for _, spec := range systemMeminfoSpecs {
		if value, ok := values[spec.key]; ok {
			c.addGauge(spec.name, "bytes", float64(value)*1024, nil)
		}
	}
}

func (c *systemCollector) collectPressure() {
	for _, resource := range []string{"cpu", "memory", "io"} {
		path := filepath.Join(c.procRoot, "pressure", resource)
		samples, err := readPressureFile(path)
		if err != nil {
			if !os.IsNotExist(err) {
				c.warn("system: %s: %v", path, err)
			}
			continue
		}
		for _, kind := range []string{"some", "full"} {
			sample, ok := samples[kind]
			if !ok {
				continue
			}
			prefix := "node_pressure_" + resource + "_" + kind + "_"
			c.addGauge(prefix+"avg10", "percent", sample.avg10, nil)
			c.addGauge(prefix+"avg60", "percent", sample.avg60, nil)
			c.addGauge(prefix+"avg300", "percent", sample.avg300, nil)
			c.addCounter(prefix+"total_seconds", "seconds", sample.total/1e6, nil)
		}
	}
}

func (c *systemCollector) collectDiskstats() {
	path := filepath.Join(c.procRoot, "diskstats")
	data, err := os.ReadFile(path)
	if err != nil {
		c.warn("system: %s: %v", path, err)
		return
	}
	for lineIndex, rawLine := range strings.Split(string(data), "\n") {
		fields := strings.Fields(rawLine)
		if len(fields) == 0 {
			continue
		}
		if len(fields) < 14 {
			c.warn("system: %s line %d: expected at least 14 fields, got %d", path, lineIndex+1, len(fields))
			continue
		}
		device := fields[2]
		if device == "" {
			c.warn("system: %s line %d: empty device name", path, lineIndex+1)
			continue
		}
		stats := make([]uint64, 5)
		fieldIndexes := []int{3, 7, 5, 9, 12}
		valid := true
		for i, fieldIndex := range fieldIndexes {
			value, err := strconv.ParseUint(fields[fieldIndex], 10, 64)
			if err != nil {
				c.warn("system: %s line %d device %q field %d: %v", path, lineIndex+1, device, fieldIndex+1, err)
				valid = false
				break
			}
			stats[i] = value
		}
		if !valid {
			continue
		}
		labels := map[string]string{"device": device}
		c.addCounter("node_disk_reads_completed_total", "count", float64(stats[0]), labels)
		c.addCounter("node_disk_writes_completed_total", "count", float64(stats[1]), labels)
		c.addCounter("node_disk_read_bytes_total", "bytes", float64(stats[2])*systemDiskSectorBytes, labels)
		c.addCounter("node_disk_written_bytes_total", "bytes", float64(stats[3])*systemDiskSectorBytes, labels)
		c.addCounter("node_disk_io_time_seconds_total", "seconds", float64(stats[4])/1000, labels)
	}
}

func (c *systemCollector) collectNetDev() {
	path := filepath.Join(c.procRoot, "net", "dev")
	data, err := os.ReadFile(path)
	if err != nil {
		c.warn("system: %s: %v", path, err)
		return
	}
	for lineIndex, rawLine := range strings.Split(string(data), "\n") {
		interfaceName, statsText, ok := strings.Cut(rawLine, ":")
		if !ok {
			continue
		}
		interfaceName = strings.TrimSpace(interfaceName)
		if interfaceName == "" {
			c.warn("system: %s line %d: empty interface name", path, lineIndex+1)
			continue
		}
		if interfaceName == "lo" {
			continue
		}
		fields := strings.Fields(statsText)
		if len(fields) < 12 {
			c.warn("system: %s line %d interface %q: expected at least 12 counters, got %d", path, lineIndex+1, interfaceName, len(fields))
			continue
		}
		indexes := []int{0, 2, 3, 8, 10, 11}
		values := make([]uint64, len(indexes))
		valid := true
		for i, fieldIndex := range indexes {
			value, err := strconv.ParseUint(fields[fieldIndex], 10, 64)
			if err != nil {
				c.warn("system: %s line %d interface %q counter %d: %v", path, lineIndex+1, interfaceName, fieldIndex, err)
				valid = false
				break
			}
			values[i] = value
		}
		if !valid {
			continue
		}
		labels := map[string]string{"interface": interfaceName}
		c.addCounter("node_network_receive_bytes_total", "bytes", float64(values[0]), labels)
		c.addCounter("node_network_receive_errs_total", "count", float64(values[1]), labels)
		c.addCounter("node_network_receive_drop_total", "count", float64(values[2]), labels)
		c.addCounter("node_network_transmit_bytes_total", "bytes", float64(values[3]), labels)
		c.addCounter("node_network_transmit_errs_total", "count", float64(values[4]), labels)
		c.addCounter("node_network_transmit_drop_total", "count", float64(values[5]), labels)
	}
}

func (c *systemCollector) collectThermal() {
	paths, err := filepath.Glob(filepath.Join(c.sysRoot, "class", "thermal", "thermal_zone*"))
	if err != nil {
		c.warn("system: thermal zones: %v", err)
		return
	}
	for _, zonePath := range paths {
		tempPath := filepath.Join(zonePath, "temp")
		data, err := os.ReadFile(tempPath)
		if err != nil {
			if !os.IsNotExist(err) {
				c.warn("system: %s: %v", tempPath, err)
			}
			continue
		}
		rawTemperature := strings.TrimSpace(string(data))
		millidegrees, err := strconv.ParseFloat(rawTemperature, 64)
		if err != nil || math.IsNaN(millidegrees) || math.IsInf(millidegrees, 0) {
			c.warn("system: %s: invalid temperature %q", tempPath, rawTemperature)
			continue
		}
		zone := strings.TrimPrefix(filepath.Base(zonePath), "thermal_zone")
		if zone == "" {
			zone = filepath.Base(zonePath)
		}
		labels := map[string]string{"zone": zone}
		if typeData, err := os.ReadFile(filepath.Join(zonePath, "type")); err == nil {
			zoneType := strings.TrimSpace(string(typeData))
			if zoneType != "" && len(zoneType) <= 256 {
				labels["type"] = zoneType
			}
		}
		c.addGauge("node_thermal_celsius", "celsius", millidegrees/1000, labels)
	}
}

func (c *systemCollector) collectCPUFrequency() {
	paths, err := filepath.Glob(filepath.Join(c.sysRoot, "devices", "system", "cpu", "cpu*", "cpufreq", "scaling_cur_freq"))
	if err != nil {
		c.warn("system: cpu frequencies: %v", err)
		return
	}
	for _, path := range paths {
		cpu := strings.TrimPrefix(filepath.Base(filepath.Dir(filepath.Dir(path))), "cpu")
		if cpu == "" || !systemDecimalDigits(cpu) {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			if !os.IsNotExist(err) {
				c.warn("system: %s: %v", path, err)
			}
			continue
		}
		frequencyKHz, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
		if err != nil {
			c.warn("system: %s: invalid frequency: %v", path, err)
			continue
		}
		c.addGauge("node_cpu_frequency_hz", "hertz", float64(frequencyKHz)*1000, map[string]string{"cpu": cpu})
	}
}

func parseSystemNonNegativeFloat(raw string) (float64, bool) {
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return 0, false
	}
	return value, true
}

func systemDecimalDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}
