//go:build linux

package metrics

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestDriverBPFObjectMatchesProbeCatalog(t *testing.T) {
	spec, err := loadDriverCollectionSpec()
	if err != nil {
		t.Fatal(err)
	}
	for _, probe := range driverProbes {
		if probe.ID == 33 {
			continue // generic syscall tracepoint, not a kprobe pair
		}
		enter := "driver_enter_" + itoa(probe.ID)
		exit := "driver_exit_" + itoa(probe.ID)
		if spec.Programs[enter] == nil {
			t.Fatalf("BPF object missing program %s (%s)", enter, probe.Symbol)
		}
		if spec.Programs[exit] == nil {
			t.Fatalf("BPF object missing program %s (%s)", exit, probe.Symbol)
		}
	}
	for _, name := range []string{"on_sys_enter_ioctl", "on_sys_exit_ioctl", "on_sys_enter_mmap"} {
		if spec.Programs[name] == nil {
			t.Fatalf("BPF object missing tracepoint program %s", name)
		}
	}
}

func TestDriverMetricCatalogCoversRequestedMetrics(t *testing.T) {
	catalog := make(map[string]struct{}, len(driverRequestedMetrics))
	for _, name := range driverRequestedMetrics {
		catalog[name] = struct{}{}
	}
	required := []string{
		"npu_ioctl_total", "npu_ioctl_latency_ns",
		"npu_svm_alloc_bytes", "npu_svm_free_bytes",
		"npu_xsmem_alloc_bytes", "npu_xsmem_free_bytes", "npu_xsmem_pool_ops_total",
		"npu_dma_map_latency_ns", "npu_dma_unmap_latency_ns", "npu_iova_alloc_free",
		"npu_hdc_send_total", "npu_hdc_recv_total", "npu_hdc_latency_ns",
		"npu_task_submit_total", "npu_task_wait_ns", "npu_mmap_bytes",
	}
	for _, name := range required {
		if _, ok := catalog[name]; !ok {
			t.Fatalf("driver metric catalog missing %s", name)
		}
	}
	if got := len(DriverRequestedMetrics()); got != len(driverRequestedMetrics) {
		t.Fatalf("DriverRequestedMetrics len=%d want %d", got, len(driverRequestedMetrics))
	}
}

func TestHistogramFromDriverBinsIsCumulative(t *testing.T) {
	value := driverHistValue{
		Buckets: [16]uint64{1, 2, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0},
		SumNS:   9000,
		Count:   5,
	}
	hist := histogramFromDriverBins(value)
	if hist.Count != 5 || hist.Sum != 9000 {
		t.Fatalf("hist=%+v", hist)
	}
	if hist.Counts[0] != 1 || hist.Counts[1] != 3 {
		t.Fatalf("counts=%v want cumulative [1 3 ...]", hist.Counts)
	}
	if hist.Counts[15] != 3 {
		t.Fatalf("last cumulative count=%d want 3", hist.Counts[15])
	}
}

func TestDriverCollectEmitsUnavailableWithoutFabricatingSamples(t *testing.T) {
	source := NewDriverEBPFSource([]int{123}, 0, 0)
	source.initOnce.Do(func() {
		source.initErr = errors.New("symbol_not_found: synthetic test")
	})
	points, warnings, err := source.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) == 0 {
		t.Fatal("expected unavailable warning")
	}
	statuses := 0
	for _, point := range points {
		if point.Name != "npu_driver_metric_status" {
			continue
		}
		statuses++
		if point.Labels["status"] != "unavailable" {
			t.Fatalf("status=%q", point.Labels["status"])
		}
		if !strings.Contains(point.Labels["reason"], "symbol_not_found") {
			t.Fatalf("reason=%q", point.Labels["reason"])
		}
	}
	if statuses != len(driverRequestedMetrics) {
		t.Fatalf("status points=%d want %d", statuses, len(driverRequestedMetrics))
	}
}

func TestDriverStatusSeparatesUnverifiedBytesFromOperationCount(t *testing.T) {
	source := NewDriverEBPFSource([]int{123}, time.Second, time.Second)
	source.attached = map[uint32]bool{30: true}
	source.failures = map[uint32]string{}
	points := source.statusPoints()
	var bytesStatus, opsStatus string
	for _, point := range points {
		if point.Name != "npu_driver_metric_status" || point.Labels["handler"] != "hdccom_alloc_mem" {
			continue
		}
		switch point.Labels["metric"] {
		case "npu_hdc_alloc_bytes":
			bytesStatus = point.Labels["status"]
		case "npu_hdc_alloc_ops_total":
			opsStatus = point.Labels["status"]
		}
	}
	if bytesStatus != "unavailable" || opsStatus != "available" {
		t.Fatalf("bytes=%q ops=%q", bytesStatus, opsStatus)
	}
}

func itoa(value uint32) string {
	const digits = "0123456789"
	if value == 0 {
		return "0"
	}
	var buf [10]byte
	i := len(buf)
	for value > 0 {
		i--
		buf[i] = digits[value%10]
		value /= 10
	}
	return string(buf[i:])
}
