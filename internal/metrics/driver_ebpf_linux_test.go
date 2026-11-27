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

func TestParseDriverKernelSymbolsTracksDuplicatesAndModules(t *testing.T) {
	data := []byte("ffffffff81000000 T drv_ascend_intf_ioctl\nffffffffc0100000 t devmm_svm_mmap\t[drv_devmm_host]\nffffffffc0200000 t devmm_svm_mmap\t[drv_devmm_host_agent]\nffffffffc0300000 t xsmem_alloc\t[drv_xsmem]\nffffffffc0400000 t unrelated_symbol\t[other]\n")
	wanted := map[string]struct{}{
		"drv_ascend_intf_ioctl": {},
		"devmm_svm_mmap":        {},
		"xsmem_alloc":           {},
	}
	symbols := parseDriverKernelSymbols(data, wanted)
	if got := symbols["drv_ascend_intf_ioctl"].Count; got != 1 {
		t.Fatalf("unique symbol count=%d want 1", got)
	}
	duplicate := symbols["devmm_svm_mmap"]
	if duplicate.Count != 2 {
		t.Fatalf("duplicate count=%d want 2", duplicate.Count)
	}
	if got := strings.Join(duplicate.Modules, ","); got != "drv_devmm_host,drv_devmm_host_agent" {
		t.Fatalf("duplicate modules=%q", got)
	}
	if got := strings.Join(symbols["xsmem_alloc"].Modules, ","); got != "drv_xsmem" {
		t.Fatalf("xsmem modules=%q", got)
	}
	if _, ok := symbols["unrelated_symbol"]; ok {
		t.Fatal("unrelated symbol should be ignored")
	}
}

func TestPrepareDriverCollectionSpecSkipsAmbiguousAndMissingProbes(t *testing.T) {
	spec, err := loadDriverCollectionSpec()
	if err != nil {
		t.Fatal(err)
	}
	symbols := map[string]driverSymbolResolution{
		"drv_ascend_intf_ioctl": {Count: 1, Modules: []string{"drv_ascend"}},
		"devmm_svm_mmap":        {Count: 2, Modules: []string{"drv_devmm_host", "drv_devmm_host_agent"}},
		"xsmem_alloc":           {Count: 0},
	}
	failures := prepareDriverCollectionSpec(spec, symbols)
	if !strings.Contains(failures[24], "ambiguous_kernel_symbol") {
		t.Fatalf("devmm_svm_mmap failure=%q", failures[24])
	}
	if !strings.Contains(failures[26], "symbol_not_found") {
		t.Fatalf("xsmem_alloc failure=%q", failures[26])
	}
	if spec.Programs["driver_enter_24"] != nil || spec.Programs["driver_exit_24"] != nil {
		t.Fatal("ambiguous devmm_svm_mmap programs were not pruned")
	}
	if spec.Programs["driver_enter_26"] != nil || spec.Programs["driver_exit_26"] != nil {
		t.Fatal("missing xsmem_alloc programs were not pruned")
	}
	if spec.Programs["driver_enter_1"] == nil || spec.Programs["driver_exit_1"] == nil {
		t.Fatal("unique drv_ascend_intf_ioctl programs were pruned")
	}
	if spec.Programs["on_sys_enter_ioctl"] == nil || spec.Programs["on_sys_enter_mmap"] == nil {
		t.Fatal("tracepoint programs were pruned")
	}
	if got := spec.Programs["on_sys_enter_ioctl"].AttachTo; got != "syscalls/sys_enter_ioctl" {
		t.Fatalf("tracepoint AttachTo=%q, want syscalls/sys_enter_ioctl", got)
	}
	for name, program := range spec.Programs {
		if isDriverKprobeSection(program.SectionName) && program.AttachTo != "" {
			t.Fatalf("%s still has kprobe AttachTo=%q", name, program.AttachTo)
		}
	}
}

func TestPrepareDriverCollectionSpecWithoutInventoryKeepsKprobes(t *testing.T) {
	spec, err := loadDriverCollectionSpec()
	if err != nil {
		t.Fatal(err)
	}
	failures := prepareDriverCollectionSpec(spec, nil)
	if len(failures) != 0 {
		t.Fatalf("failures=%v want none without inventory", failures)
	}
	if spec.Programs["driver_enter_24"] == nil || spec.Programs["driver_exit_24"] == nil {
		t.Fatal("kprobes should remain when /proc/kallsyms is unreadable")
	}
	if spec.Programs["on_sys_enter_ioctl"] == nil {
		t.Fatal("ioctl tracepoint should remain")
	}
	for name, program := range spec.Programs {
		if isDriverKprobeSection(program.SectionName) && program.AttachTo != "" {
			t.Fatalf("%s still has kprobe AttachTo=%q", name, program.AttachTo)
		}
	}
}

func TestDriverStatusReportsAmbiguousProbeAsDegraded(t *testing.T) {
	source := NewDriverEBPFSource([]int{123}, time.Second, time.Second)
	source.attached = map[uint32]bool{1: true, 33: true}
	source.failures = map[uint32]string{24: "ambiguous_kernel_symbol: devmm_svm_mmap has 2 function entries"}
	points := source.statusPoints()
	var sourceStatus, metricStatus, reason string
	for _, point := range points {
		switch point.Name {
		case "npu_driver_source_info":
			sourceStatus = point.Labels["status"]
		case "npu_driver_metric_status":
			if point.Labels["handler"] == "devmm_svm_mmap" {
				metricStatus = point.Labels["status"]
				reason = point.Labels["reason"]
			}
		}
	}
	if sourceStatus != "degraded" {
		t.Fatalf("source status=%q want degraded", sourceStatus)
	}
	if metricStatus != "unavailable" || !strings.Contains(reason, "ambiguous_kernel_symbol") {
		t.Fatalf("metric status=%q reason=%q", metricStatus, reason)
	}
}

func TestDriverStatusWithOnlyIoctlTracepointIsDegraded(t *testing.T) {
	source := NewDriverEBPFSource([]int{123}, time.Second, time.Second)
	source.attached = map[uint32]bool{}
	source.failures = map[uint32]string{24: "ambiguous_kernel_symbol: devmm_svm_mmap has 2 function entries"}
	source.ioctlTracepoints = true
	points := source.statusPoints()
	for _, point := range points {
		if point.Name != "npu_driver_source_info" {
			continue
		}
		if got := point.Labels["status"]; got != "degraded" {
			t.Fatalf("source status=%q want degraded", got)
		}
		if got := point.Labels["probes_attached"]; got != "1" {
			t.Fatalf("probes_attached=%q want 1 (ioctl tracepoint)", got)
		}
		return
	}
	t.Fatal("npu_driver_source_info point missing")
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
