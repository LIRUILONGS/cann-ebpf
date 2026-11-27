package report

import (
	"encoding/json"
	"strings"
	"testing"

	"cann-ebpf/internal/diagnose"
	"cann-ebpf/internal/runlog"
)

func TestReportViewsV2M0Contract(t *testing.T) {
	views := buildReportViewsV2(viewsV2BuildInput{})
	checks := []struct {
		name   string
		status ViewStatus
	}{
		{"load_latency", views.LoadLatency.Status},
		{"critical_path", views.CriticalPath.Status},
		{"summed_delay", views.SummedDelay.Status},
		{"slack_cdf", views.SlackCDF.Status},
		{"off_cpu", views.OffCPU.Status},
		{"operator_treemap", views.OperatorTreemap.Status},
		{"roofline", views.Roofline.Status},
		{"ab_diff", views.ABDiff.Status},
		{"change_point", views.ChangePoint.Status},
		{"service_topology", views.ServiceTopology.Status},
	}
	for _, check := range checks {
		if check.status.Available {
			t.Fatalf("%s: M0 stub must be unavailable", check.name)
		}
		if check.status.Reason == "" {
			t.Fatalf("%s: unavailable view needs a reason", check.name)
		}
		if check.status.Source != "none" {
			t.Fatalf("%s: unavailable view source = %q, want none", check.name, check.status.Source)
		}
		if check.status.Confidence != "" {
			t.Fatalf("%s: unavailable stub must not claim confidence", check.name)
		}
	}
	if len(views.LoadLatency.Points) != 0 || len(views.CriticalPath.Nodes) != 0 ||
		len(views.SlackCDF.Points) != 0 || len(views.OffCPU.Rows) != 0 ||
		len(views.Roofline.Points) != 0 || len(views.ServiceTopology.Edges) != 0 {
		t.Fatal("unavailable views must not contain fabricated data")
	}
}

func TestBuildRendersViewsV2Contract(t *testing.T) {
	data, err := Build(&diagnose.Input{
		Path: "views-v2-test-run",
		Spans: []runlog.Span{
			testSpan(1, 1, 1, 1, "aclrtSynchronizeStream", "sync", "views-v2-span"),
		},
	}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if data.ViewsV2.Roofline.Status.Available || data.ViewsV2.LoadLatency.Status.Available {
		t.Fatal("span report without optional evidence must keep those views unavailable")
	}
	payload, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	text := string(payload)
	for _, key := range []string{
		`"views_v2"`, `"load_latency"`, `"critical_path"`, `"summed_delay"`,
		`"slack_cdf"`, `"off_cpu"`, `"operator_treemap"`, `"roofline"`,
		`"ab_diff"`, `"change_point"`, `"service_topology"`,
	} {
		if !strings.Contains(text, key) {
			t.Fatalf("serialized report missing %s", key)
		}
	}
	html, err := Render(data)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(html), `id="viewsV2Grid"`) {
		t.Fatal("rendered report missing views_v2 panel")
	}
	for _, label := range []string{
		"负载-延迟拐点", "关键路径瀑布", "Summed Delay vs Critical Path", "Slack CDF",
		"off-CPU ribbon", "算子 Treemap", "Roofline", "A/B 差分", "变化点 / 异常带", "服务拓扑",
	} {
		if !strings.Contains(string(html), label) {
			t.Fatalf("rendered report missing label %q", label)
		}
	}
}

func TestBuildWithMetricsOnlyIncludesViewsV2(t *testing.T) {
	data, err := BuildWithMetrics(nil, &MetricsData{
		Available: true,
		StartNS:   1,
		EndNS:     2,
		Series: []MetricSeries{{
			Name: "vllm:num_requests_running", T: []float64{0}, V: []float64{1},
		}},
	}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if data.ViewsV2.CriticalPath.Status.Available || data.ViewsV2.Roofline.Status.Reason == "" {
		t.Fatalf("metrics-only report views_v2 = %+v", data.ViewsV2)
	}
}
