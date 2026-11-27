package report

import (
	"strings"
	"testing"
)

func TestRenderMetricsMatrixUsesGroupedView(t *testing.T) {
	times := []float64{0, 1000, 2000}
	m := &MetricsData{
		Available:  true,
		Samples:    9,
		IntervalMS: 1000,
		Coverage:   1,
		Sources:    []string{"npu-smi", "proc"},
		Series: []MetricSeries{
			{
				ID: "npu-0", Name: "npu_aicore_utilization_ratio", Kind: "gauge",
				Unit: "ratio", Source: "npu-smi", Device: "NPU-0",
				T: times, V: []float64{0.10, 0.40, 0.20},
			},
			{
				ID: "npu-1", Name: "npu_aicore_utilization_ratio", Kind: "gauge",
				Unit: "ratio", Source: "npu-smi", Device: "NPU-1",
				T: times, V: []float64{0.20, 0.60, 0.30},
			},
			{
				ID: "proc-1", Name: "proc_rss_bytes", Kind: "gauge",
				Unit: "bytes", Source: "proc", Labels: map[string]string{"pid": "1", "comm": "vllm"},
				T: times, V: []float64{100, 120, 110},
			},
		},
		Stats: []MetricStat{
			{ID: "npu-0", Name: "npu_aicore_utilization_ratio", Kind: "gauge", Unit: "ratio", Source: "npu-smi", Device: "NPU-0", Points: 3, Last: 0.2, Mean: 0.23, P95: 0.4, Max: 0.4},
		},
	}
	data := Data{
		Schema:  SchemaV1,
		Title:   "matrix test",
		Metrics: m,
	}
	out, err := Render(data)
	if err != nil {
		t.Fatal(err)
	}
	text := string(out)
	for _, needle := range []string{
		"指标矩阵",
		"id=\"metricsMatrix\"",
		"id=\"metricsSourceTabs\"",
		"id=\"metricDetailCanvas\"",
		"按“来源 + 指标名 + 单位”归并为组",
		"每行一条组均值曲线",
		"counter 已转 Δ/s",
		"counterRatePoints",
		"样本不足",
	} {
		if !strings.Contains(text, needle) {
			t.Fatalf("rendered report missing %q", needle)
		}
	}
	if strings.Contains(text, "id=\"metricSeries") {
		t.Fatal("old per-series canvas must not be emitted")
	}
}

func TestRenderMetricsMatrixIncludesHCCSMetrics(t *testing.T) {
	times := []float64{0, 1000}
	m := &MetricsData{
		Available: true, Samples: 2, IntervalMS: 1000, Coverage: 1,
		Sources: []string{"npu-smi"},
		Series: []MetricSeries{{
			ID: "hccs-0", Name: "npu_hccs_tx_bytes_total", Kind: "counter",
			Unit: "bytes", Source: "npu-smi", Device: "NPU-0",
			Labels: map[string]string{"lane": "0"}, T: times, V: []float64{100, 200},
		}},
		Stats: []MetricStat{{
			ID: "hccs-0", Name: "npu_hccs_tx_bytes_total", Kind: "counter",
			Unit: "bytes", Source: "npu-smi", Device: "NPU-0",
			Points: 2, Last: 200, Mean: 150, P95: 200, Max: 200,
		}},
	}
	out, err := Render(Data{Schema: SchemaV1, Title: "hccs matrix", Metrics: m})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "npu_hccs_tx_bytes_total") {
		t.Fatal("metrics matrix missing HCCS metric")
	}
}
