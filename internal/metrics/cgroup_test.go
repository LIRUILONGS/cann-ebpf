package metrics

import (
	"path/filepath"
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

func findPointByLabel(points []Point, name, key, value string) *Point {
	for i := range points {
		if points[i].Name == name && points[i].Labels[key] == value {
			return &points[i]
		}
	}
	return nil
}
