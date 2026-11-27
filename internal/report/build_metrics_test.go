package report

import "testing"

func TestBuildWithMetricsOnlyHardwareAvailability(t *testing.T) {
	m := &MetricsData{
		Available: true,
		StartNS:   1_000_000_000,
		EndNS:     2_000_000_000,
		Series: []MetricSeries{{
			Name: "proc_rss_bytes", Source: "proc", T: []float64{0, 1000}, V: []float64{1, 2},
		}},
	}

	data, err := BuildWithMetrics(nil, m, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if data.Summary.HardwareAvailable {
		t.Fatal("metrics-only report must not claim hardware availability for proc-only data")
	}

	m.Series[0].Name = "npu_aicore_utilization_ratio"
	data, err = BuildWithMetrics(nil, m, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !data.Summary.HardwareAvailable {
		t.Fatal("metrics-only report should advertise NPU metrics when present")
	}
}

func TestRebaseMetricsClipsBothSides(t *testing.T) {
	m := &MetricsData{
		Available: true,
		StartNS:   1_000_000_000,
		EndNS:     2_000_000_000,
		Series: []MetricSeries{{
			Name: "npu_aicore_utilization_ratio",
			T:    []float64{0, 100, 500, 700, 1200},
			V:    []float64{1, 2, 3, 4, 5},
		}},
	}

	out := rebaseMetrics(m, 1_500_000_000, 2_000_000_000)
	if out == nil || !out.Available {
		t.Fatalf("rebase returned %+v", out)
	}
	if out.StartNS != 1_500_000_000 || out.EndNS != 2_000_000_000 {
		t.Fatalf("window = %d..%d", out.StartNS, out.EndNS)
	}
	if len(out.Series) != 1 || len(out.Series[0].T) != 2 {
		t.Fatalf("series = %+v", out.Series)
	}
	if out.Series[0].T[0] != 0 || out.Series[0].T[1] != 200 {
		t.Fatalf("points = %v", out.Series[0].T)
	}
	if len(out.Warnings) != 2 {
		t.Fatalf("warnings = %v", out.Warnings)
	}
}
