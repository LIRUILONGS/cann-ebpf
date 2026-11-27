package metrics

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSystemSourceCollectsProcAndSys(t *testing.T) {
	procRoot := t.TempDir()
	sysRoot := t.TempDir()
	writeSystemTestFile(t, filepath.Join(procRoot, "stat"), "cpu  110 20 30 40 50 6 7 8 9 10\ncpu0 55 10 15 20 25 3 4 5 0 0\nintr 1 2 3\nctxt 99\n")
	writeSystemTestFile(t, filepath.Join(procRoot, "loadavg"), "0.50 1.25 2.00 3/200 12345\n")
	writeSystemTestFile(t, filepath.Join(procRoot, "meminfo"), ""+
		"MemTotal:       100 kB\n"+
		"MemFree:         50 kB\n"+
		"MemAvailable:    80 kB\n"+
		"Buffers:         10 kB\n"+
		"Cached:          20 kB\n"+
		"SwapTotal:       30 kB\n"+
		"SwapFree:        15 kB\n"+
		"Dirty:            5 kB\n"+
		"Writeback:        4 kB\n"+
		"Slab:             6 kB\n")
	pressure := "some avg10=1.50 avg60=2.50 avg300=3.50 total=4000000\n" +
		"full avg10=4.50 avg60=5.50 avg300=6.50 total=7000000\n"
	for _, resource := range []string{"cpu", "memory", "io"} {
		writeSystemTestFile(t, filepath.Join(procRoot, "pressure", resource), pressure)
	}
	writeSystemTestFile(t, filepath.Join(procRoot, "diskstats"), "8 0 sda 100 2 300 4 200 5 400 6 7 8 90 10\n")
	writeSystemTestFile(t, filepath.Join(procRoot, "net", "dev"), ""+
		"Inter-|   Receive                                                |  Transmit\n"+
		" face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed\n"+
		"    lo: 999 9 9 9 0 0 0 0 999 9 9 9 0 0 0 0\n"+
		"  eth0: 1000 10 2 3 0 0 0 0 2000 20 4 5 0 0 0 0\n")
	writeSystemTestFile(t, filepath.Join(sysRoot, "class", "thermal", "thermal_zone0", "temp"), "42500\n")
	writeSystemTestFile(t, filepath.Join(sysRoot, "class", "thermal", "thermal_zone0", "type"), "x86_pkg_temp\n")
	writeSystemTestFile(t, filepath.Join(sysRoot, "devices", "system", "cpu", "cpu0", "cpufreq", "scaling_cur_freq"), "2400000\n")

	source := NewSystemSource(procRoot, sysRoot, time.Second, time.Second)
	points, warnings, err := source.Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings=%v", warnings)
	}

	for _, want := range []struct {
		name  string
		kind  Kind
		unit  string
		value float64
	}{
		{"node_load1", KindGauge, "load", 0.5},
		{"node_load5", KindGauge, "load", 1.25},
		{"node_load15", KindGauge, "load", 2},
		{"node_runnable_tasks", KindGauge, "count", 3},
		{"node_total_tasks", KindGauge, "count", 200},
		{"node_memory_mem_total_bytes", KindGauge, "bytes", 100 * 1024},
		{"node_memory_mem_free_bytes", KindGauge, "bytes", 50 * 1024},
		{"node_memory_mem_available_bytes", KindGauge, "bytes", 80 * 1024},
		{"node_memory_buffers_bytes", KindGauge, "bytes", 10 * 1024},
		{"node_memory_cached_bytes", KindGauge, "bytes", 20 * 1024},
		{"node_memory_swap_total_bytes", KindGauge, "bytes", 30 * 1024},
		{"node_memory_swap_free_bytes", KindGauge, "bytes", 15 * 1024},
		{"node_memory_dirty_bytes", KindGauge, "bytes", 5 * 1024},
		{"node_memory_writeback_bytes", KindGauge, "bytes", 4 * 1024},
		{"node_memory_slab_bytes", KindGauge, "bytes", 6 * 1024},
		{"node_disk_reads_completed_total", KindCounter, "count", 100},
		{"node_disk_writes_completed_total", KindCounter, "count", 200},
		{"node_disk_read_bytes_total", KindCounter, "bytes", 300 * 512},
		{"node_disk_written_bytes_total", KindCounter, "bytes", 400 * 512},
		{"node_disk_io_time_seconds_total", KindCounter, "seconds", 0.008},
		{"node_network_receive_bytes_total", KindCounter, "bytes", 1000},
		{"node_network_transmit_bytes_total", KindCounter, "bytes", 2000},
		{"node_network_receive_errs_total", KindCounter, "count", 2},
		{"node_network_transmit_errs_total", KindCounter, "count", 4},
		{"node_network_receive_drop_total", KindCounter, "count", 3},
		{"node_network_transmit_drop_total", KindCounter, "count", 5},
		{"node_thermal_celsius", KindGauge, "celsius", 42.5},
		{"node_cpu_frequency_hz", KindGauge, "hertz", 2.4e9},
	} {
		assertPointKindUnitValue(t, points, want.name, want.kind, want.unit, want.value)
	}
	for index, mode := range []string{"user", "nice", "system", "idle", "iowait", "irq", "softirq", "steal"} {
		point := findPointByLabel(points, "node_cpu_seconds_total", "mode", mode)
		if point == nil {
			t.Fatalf("CPU mode %s missing", mode)
		}
		if point.Kind != KindCounter || point.Unit != "seconds" || point.Value == nil || *point.Value != float64([]uint64{110, 20, 30, 40, 50, 6, 7, 8}[index])/100 {
			t.Fatalf("CPU mode %s=%+v", mode, point)
		}
	}
	for _, resource := range []string{"cpu", "memory", "io"} {
		for _, kind := range []string{"some", "full"} {
			prefix := "node_pressure_" + resource + "_" + kind + "_"
			avg10, avg60, avg300, total := 1.5, 2.5, 3.5, 4.0
			if kind == "full" {
				avg10, avg60, avg300, total = 4.5, 5.5, 6.5, 7.0
			}
			assertPointKindUnitValue(t, points, prefix+"avg10", KindGauge, "percent", avg10)
			assertPointKindUnitValue(t, points, prefix+"avg60", KindGauge, "percent", avg60)
			assertPointKindUnitValue(t, points, prefix+"avg300", KindGauge, "percent", avg300)
			assertPointKindUnitValue(t, points, prefix+"total_seconds", KindCounter, "seconds", total)
		}
	}

	if point := findPointByLabel(points, "node_disk_reads_completed_total", "device", "sda"); point == nil {
		t.Fatal("sda disk metric missing")
	}
	if point := findPointByLabel(points, "node_network_receive_bytes_total", "interface", "lo"); point != nil {
		t.Fatal("loopback network metric must be skipped")
	}
	thermal := findPointByLabel(points, "node_thermal_celsius", "zone", "0")
	if thermal == nil || thermal.Labels["type"] != "x86_pkg_temp" {
		t.Fatalf("thermal labels=%v", thermal)
	}
	if point := findPointByLabel(points, "node_cpu_frequency_hz", "cpu", "0"); point == nil {
		t.Fatal("cpu0 frequency metric missing")
	}
	for _, point := range points {
		if point.Source != "system" || point.Device != "host" || point.Value == nil {
			t.Fatalf("identity: %+v", point)
		}
	}
}

func writeSystemTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSystemSourceMissingFilesAreWarnings(t *testing.T) {
	source := NewSystemSource(t.TempDir(), t.TempDir(), time.Second, time.Second)
	points, warnings, err := source.Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 0 {
		t.Fatalf("points=%v", pointNames(points))
	}
	joined := strings.Join(warnings, "\n")
	for _, fragment := range []string{"stat", "loadavg", "meminfo", "diskstats", "net"} {
		if !strings.Contains(joined, fragment) {
			t.Fatalf("missing warning for %s: %v", fragment, warnings)
		}
	}
	for _, warning := range warnings {
		if strings.Contains(warning, "pressure") {
			t.Fatalf("missing pressure files must be silent: %v", warnings)
		}
	}
}

func TestSystemSourceMissingPressureFilesAreSilent(t *testing.T) {
	procRoot := t.TempDir()
	writeSystemTestFile(t, filepath.Join(procRoot, "stat"), "cpu 1 2 3 4 5 6 7 8\n")
	writeSystemTestFile(t, filepath.Join(procRoot, "loadavg"), "0.10 0.20 0.30 1/10 42\n")
	writeSystemTestFile(t, filepath.Join(procRoot, "meminfo"), "MemTotal: 1 kB\n")
	writeSystemTestFile(t, filepath.Join(procRoot, "diskstats"), "")
	writeSystemTestFile(t, filepath.Join(procRoot, "net", "dev"), "Inter-| Receive | Transmit\n face |bytes|bytes\n")

	source := NewSystemSource(procRoot, t.TempDir(), time.Second, time.Second)
	points, warnings, err := source.Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(points) == 0 || len(warnings) != 0 {
		t.Fatalf("points=%v warnings=%v", pointNames(points), warnings)
	}
	for _, point := range points {
		if strings.HasPrefix(point.Name, "node_pressure_") {
			t.Fatalf("unexpected pressure point: %+v", point)
		}
	}
}

func TestSystemSourceSkipsMalformedLines(t *testing.T) {
	procRoot := t.TempDir()
	sysRoot := t.TempDir()
	writeSystemTestFile(t, filepath.Join(procRoot, "stat"), "cpu 1 2 nope 4 5 6 7 8\ncpu0 bad\nintr 12\n")
	writeSystemTestFile(t, filepath.Join(procRoot, "loadavg"), "0.10 nope 0.30 1/10 42\n")
	writeSystemTestFile(t, filepath.Join(procRoot, "meminfo"), "MemTotal: 100 kB\nMemFree: oops kB\nMemAvailable: 80 MB\nCached: 20 kB\n")
	writeSystemTestFile(t, filepath.Join(procRoot, "pressure", "cpu"), "some avg10=1.50 avg60=2.50 avg300=3.50 total=4000000\n")
	writeSystemTestFile(t, filepath.Join(procRoot, "pressure", "memory"), "some avg10=oops avg60=2.50 avg300=3.50 total=4000000\n")
	writeSystemTestFile(t, filepath.Join(procRoot, "diskstats"), "8 0 sda 1 0 2 0 3 0 4 0 0 5 0\n8 16 sdb 1 2 3\n")
	writeSystemTestFile(t, filepath.Join(procRoot, "net", "dev"), ""+
		"Inter-| Receive | Transmit\n"+
		" face |bytes|bytes\n"+
		"    lo: 999 9 9 9 0 0 0 0 999 9 9 9 0 0 0 0\n"+
		"  eth0: 1000 10 2 3 0 0 0 0 2000 20 4 5 0 0 0 0\n"+
		"  eth1: 1 2 bad 4 5 6 7 8 9 10 11 12\n"+
		": 1 2 3 4 5 6 7 8 9 10 11 12\n")
	writeSystemTestFile(t, filepath.Join(sysRoot, "class", "thermal", "thermal_zone0", "temp"), "not-a-temperature\n")
	writeSystemTestFile(t, filepath.Join(sysRoot, "class", "thermal", "thermal_zone1", "temp"), "1000\n")
	writeSystemTestFile(t, filepath.Join(sysRoot, "class", "thermal", "thermal_zone1", "type"), "acpitz\n")
	writeSystemTestFile(t, filepath.Join(sysRoot, "devices", "system", "cpu", "cpu0", "cpufreq", "scaling_cur_freq"), "bad\n")
	writeSystemTestFile(t, filepath.Join(sysRoot, "devices", "system", "cpu", "cpu1", "cpufreq", "scaling_cur_freq"), "3000000\n")

	source := NewSystemSource(procRoot, sysRoot, time.Second, time.Second)
	points, warnings, err := source.Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) == 0 {
		t.Fatal("expected malformed input warnings")
	}
	assertPointValue(t, points, "node_memory_mem_total_bytes", 100*1024)
	assertPointValue(t, points, "node_memory_cached_bytes", 20*1024)
	assertPointKindUnitValue(t, points, "node_pressure_cpu_some_avg10", KindGauge, "percent", 1.5)
	assertPointValue(t, points, "node_disk_reads_completed_total", 1)
	assertPointValue(t, points, "node_network_receive_bytes_total", 1000)
	if point := findPointByLabel(points, "node_thermal_celsius", "zone", "1"); point == nil || *point.Value != 1 {
		t.Fatalf("valid thermal point missing: %+v", point)
	}
	if point := findPointByLabel(points, "node_cpu_frequency_hz", "cpu", "1"); point == nil || *point.Value != 3e9 {
		t.Fatalf("valid frequency point missing: %+v", point)
	}
	for _, point := range points {
		if point.Name == "node_load1" || point.Name == "node_cpu_seconds_total" {
			t.Fatalf("malformed input produced point: %+v", point)
		}
		if device, ok := point.Labels["device"]; ok && device == "" {
			t.Fatalf("empty device label: %+v", point)
		}
		if interfaceName, ok := point.Labels["interface"]; ok && interfaceName == "" {
			t.Fatalf("empty interface label: %+v", point)
		}
	}
}

func TestSystemSourceSkipsEmptyDeviceAndInterface(t *testing.T) {
	procRoot := t.TempDir()
	writeSystemTestFile(t, filepath.Join(procRoot, "stat"), "cpu 1 2 3 4 5 6 7 8\n")
	writeSystemTestFile(t, filepath.Join(procRoot, "loadavg"), "0.10 0.20 0.30 1/10 42\n")
	writeSystemTestFile(t, filepath.Join(procRoot, "meminfo"), "MemTotal: 1 kB\n")
	writeSystemTestFile(t, filepath.Join(procRoot, "diskstats"), "8 0  \n")
	writeSystemTestFile(t, filepath.Join(procRoot, "net", "dev"), "Inter-| Receive | Transmit\n face |bytes|bytes\n: 1 2 3 4 5 6 7 8 9 10 11 12\n")

	source := NewSystemSource(procRoot, t.TempDir(), time.Second, time.Second)
	points, warnings, err := source.Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) == 0 {
		t.Fatal("expected warnings for empty device/interface lines")
	}
	for _, point := range points {
		if strings.HasPrefix(point.Name, "node_disk_") || strings.HasPrefix(point.Name, "node_network_") {
			t.Fatalf("empty device/interface line produced point: %+v", point)
		}
		if device, ok := point.Labels["device"]; ok && device == "" {
			t.Fatalf("empty device label: %+v", point)
		}
		if interfaceName, ok := point.Labels["interface"]; ok && interfaceName == "" {
			t.Fatalf("empty interface label: %+v", point)
		}
	}
}
