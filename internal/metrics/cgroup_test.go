package metrics

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestCgroupSourceCollectsV2(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "cpu.stat"), "usage_usec 1500000\nuser_usec 1000000\nsystem_usec 500000\nnr_periods 20\nnr_throttled 3\nthrottled_usec 250000\n")
	writeTestFile(t, filepath.Join(root, "memory.current"), "1048576\n")
	writeTestFile(t, filepath.Join(root, "memory.max"), "max\n")
	writeTestFile(t, filepath.Join(root, "memory.events"), "low 0\nhigh 0\nmax 1\noom 2\noom_kill 0\n")
	writeTestFile(t, filepath.Join(root, "pids.current"), "10\n")
	writeTestFile(t, filepath.Join(root, "pids.max"), "max\n")

	source := NewCgroupSource([]string{root}, 0, 0)
	points, warnings, err := source.Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings=%v", warnings)
	}
	assertPointValue(t, points, "cgroup_cpu_usage_seconds", 1.5)
	assertPointValue(t, points, "cgroup_cpu_throttled_seconds", 0.25)
	assertPointValue(t, points, "cgroup_memory_current_bytes", 1048576)
	assertPointValue(t, points, "cgroup_pids_current", 10)
	if findPointByLabel(points, "cgroup_memory_events_total", "event", "oom") == nil {
		t.Fatalf("oom event missing: %v", pointNames(points))
	}
}

func TestCgroupSourceMissingPathIsWarning(t *testing.T) {
	source := NewCgroupSource([]string{filepath.Join(t.TempDir(), "missing")}, 0, 0)
	points, warnings, err := source.Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 0 || len(warnings) == 0 {
		t.Fatalf("points=%v warnings=%v", points, warnings)
	}
}

func TestCgroupSourceCollectsPressure(t *testing.T) {
	root := t.TempDir()
	pressure := "some avg10=1.50 avg60=2.50 avg300=3.50 total=4000000\n" +
		"full avg10=4.50 avg60=5.50 avg300=6.50 total=7000000\n"
	for _, name := range []string{"cpu.pressure", "memory.pressure", "io.pressure"} {
		writeTestFile(t, filepath.Join(root, name), pressure)
	}

	source := NewCgroupSource([]string{root}, 0, 0)
	points, warnings, err := source.Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings=%v", warnings)
	}
	for _, prefix := range []string{"cgroup_cpu_pressure_", "cgroup_memory_pressure_", "cgroup_io_pressure_"} {
		for _, kind := range []string{"some", "full"} {
			expected := map[string]float64{
				"avg10_ratio":   0.015,
				"avg60_ratio":   0.025,
				"avg300_ratio":  0.035,
				"total_seconds": 4,
			}
			if kind == "full" {
				expected = map[string]float64{
					"avg10_ratio":   0.045,
					"avg60_ratio":   0.055,
					"avg300_ratio":  0.065,
					"total_seconds": 7,
				}
			}
			for suffix, want := range expected {
				name := prefix + kind + "_" + suffix
				unit, pointKind := "ratio", KindGauge
				if suffix == "total_seconds" {
					unit, pointKind = "seconds", KindCounter
				}
				assertPointKindUnitValue(t, points, name, pointKind, unit, want)
			}
		}
	}
}

func TestCgroupMissingPressureFilesAreSilent(t *testing.T) {
	root := t.TempDir()
	source := NewCgroupSource([]string{root}, 0, 0)
	points, warnings, err := source.Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 0 || len(warnings) != 0 {
		t.Fatalf("points=%v warnings=%v", points, warnings)
	}
}

func TestCgroupMalformedPressureWarns(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "cpu.pressure"), "some avg10=oops avg60=0.00 avg300=0.00 total=1\n")

	source := NewCgroupSource([]string{root}, 0, 0)
	points, warnings, err := source.Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 0 {
		t.Fatalf("points=%v", points)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "cpu.pressure") {
		t.Fatalf("warnings=%v", warnings)
	}
}

func assertPointKindUnitValue(t *testing.T, points []Point, name string, kind Kind, unit string, want float64) {
	t.Helper()
	for _, point := range points {
		if point.Name != name {
			continue
		}
		if point.Kind != kind || point.Unit != unit {
			t.Fatalf("%s kind/unit=%s/%s want %s/%s", name, point.Kind, point.Unit, kind, unit)
		}
		if point.Value == nil || *point.Value != want {
			t.Fatalf("%s=%v want %v", name, point.Value, want)
		}
		return
	}
	t.Fatalf("point %s not found in %v", name, pointNames(points))
}

func findPointByLabel(points []Point, name, key, value string) *Point {
	for i := range points {
		if points[i].Name == name && points[i].Labels[key] == value {
			return &points[i]
		}
	}
	return nil
}
