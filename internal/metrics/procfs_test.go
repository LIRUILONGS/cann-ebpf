package metrics

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProcSourceCollectsPID(t *testing.T) {
	root := t.TempDir()
	pidDir := filepath.Join(root, "123")
	if err := os.MkdirAll(pidDir, 0o700); err != nil {
		t.Fatal(err)
	}
	fields := make([]string, 52)
	for i := range fields {
		fields[i] = "0"
	}
	fields[0] = "S"
	fields[7] = "11"   // minflt, field 10
	fields[9] = "13"   // majflt, field 12
	fields[11] = "150" // utime ticks = 1.5s at USER_HZ=100
	fields[12] = "50"  // stime ticks = 0.5s
	fields[17] = "16"  // num_threads
	stat := "123 (python3) " + strings.Join(fields, " ") + "\n"
	writeTestFile(t, filepath.Join(pidDir, "stat"), stat)
	writeTestFile(t, filepath.Join(pidDir, "status"), "Name:\tpython3\nVmSize:\t   8192 kB\nVmRSS:\t   2048 kB\nThreads:\t16\nvoluntary_ctxt_switches:\t7\nnonvoluntary_ctxt_switches:\t9\n")
	writeTestFile(t, filepath.Join(pidDir, "schedstat"), "1000000 250000 7\n")
	writeTestFile(t, filepath.Join(pidDir, "io"), "read_bytes: 4096\nwrite_bytes: 8192\n")
	fdDir := filepath.Join(pidDir, "fd")
	if err := os.MkdirAll(fdDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, fd := range []string{"0", "1", "2"} {
		writeTestFile(t, filepath.Join(fdDir, fd), "")
	}

	source := NewProcSource([]int{123}, root, 0, 0)
	points, warnings, err := source.Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings=%v", warnings)
	}
	assertPointValue(t, points, "proc_cpu_user_seconds", 1.5)
	assertPointValue(t, points, "proc_cpu_system_seconds", 0.5)
	assertPointValue(t, points, "proc_page_faults_minor_total", 11)
	assertPointValue(t, points, "proc_page_faults_major_total", 13)
	assertPointValue(t, points, "proc_fds", 3)
	assertPointValue(t, points, "proc_vm_size_bytes", 8192*1024)
	assertPointValue(t, points, "proc_rss_bytes", 2048*1024)
	assertPointValue(t, points, "proc_vm_rss_bytes", 2048*1024)
	assertPointValue(t, points, "proc_threads", 16)
	assertPointValue(t, points, "proc_context_switches_voluntary_total", 7)
	assertPointValue(t, points, "proc_context_switches_nonvoluntary_total", 9)
	assertPointValue(t, points, "proc_sched_run_delay_seconds", 0.00025)
	assertPointValue(t, points, "proc_io_read_bytes", 4096)
	for _, point := range points {
		if point.Source != "proc" || point.Device != "host" || point.Labels["pid"] != "123" {
			t.Fatalf("identity: %+v", point)
		}
	}
}

func TestProcSourceMissingOptionalFilesAreSkipped(t *testing.T) {
	root := t.TempDir()
	pidDir := filepath.Join(root, "123")
	if err := os.MkdirAll(pidDir, 0o700); err != nil {
		t.Fatal(err)
	}
	fields := make([]string, 18)
	for i := range fields {
		fields[i] = "0"
	}
	fields[0] = "S"
	writeTestFile(t, filepath.Join(pidDir, "stat"), "123 (python3) "+strings.Join(fields, " ")+"\n")
	writeTestFile(t, filepath.Join(pidDir, "status"), "Name:\tpython3\n")

	source := NewProcSource([]int{123}, root, 0, 0)
	points, warnings, err := source.Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings=%v", warnings)
	}
	for _, name := range []string{"proc_fds", "proc_vm_size_bytes", "proc_context_switches_voluntary_total", "proc_context_switches_nonvoluntary_total"} {
		for _, point := range points {
			if point.Name == name {
				t.Fatalf("unexpected fabricated point %s: %v", name, point)
			}
		}
	}
}

func TestProcSourceMissingPIDIsWarning(t *testing.T) {
	source := NewProcSource([]int{999999}, t.TempDir(), 0, 0)
	points, warnings, err := source.Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 0 || len(warnings) == 0 {
		t.Fatalf("points=%v warnings=%v", points, warnings)
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertPointValue(t *testing.T, points []Point, name string, want float64) {
	t.Helper()
	for _, point := range points {
		if point.Name == name && point.Value != nil {
			if *point.Value != want {
				t.Fatalf("%s=%v want %v", name, *point.Value, want)
			}
			return
		}
	}
	t.Fatalf("point %s not found in %v", name, pointNames(points))
}

func pointNames(points []Point) []string {
	names := make([]string, len(points))
	for i, point := range points {
		names[i] = point.Name
	}
	return names
}
