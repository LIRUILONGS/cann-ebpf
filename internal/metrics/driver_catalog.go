package metrics

import "sort"

const driverSourceName = "driver-ebpf"

// driverRequestedMetrics is the public contract requested for the driver
// layer. The catalog is also used to emit explicit unavailable status points
// when a symbol or the whole BPF object cannot be attached.
var driverRequestedMetrics = []string{
	"npu_ioctl_total",
	"npu_ioctl_latency_ns",
	"npu_svm_alloc_bytes",
	"npu_svm_free_bytes",
	"npu_svm_alloc_ops_total",
	"npu_svm_free_ops_total",
	"npu_xsmem_alloc_bytes",
	"npu_xsmem_free_bytes",
	"npu_xsmem_alloc_ops_total",
	"npu_xsmem_free_ops_total",
	"npu_xsmem_pool_alloc_bytes",
	"npu_xsmem_pool_free_bytes",
	"npu_xsmem_pool_ops_total",
	"npu_dma_map_latency_ns",
	"npu_dma_unmap_latency_ns",
	"npu_dma_map_bytes",
	"npu_dma_unmap_bytes",
	"npu_dma_map_ops_total",
	"npu_dma_unmap_ops_total",
	"npu_iova_alloc_free",
	"npu_iova_latency_ns",
	"npu_hdc_send_total",
	"npu_hdc_recv_total",
	"npu_hdc_submit_total",
	"npu_hdc_latency_ns",
	"npu_hdc_alloc_bytes",
	"npu_hdc_alloc_ops_total",
	"npu_task_submit_total",
	"npu_task_doorbell_total",
	"npu_task_wait_ns",
	"npu_task_done_total",
	"npu_mmap_bytes",
	"npu_mmap_ops_total",
}

// DriverRequestedMetrics returns the stable driver-layer metric catalog. It is
// intentionally sorted for deterministic tests and reports.
func DriverRequestedMetrics() []string {
	out := append([]string(nil), driverRequestedMetrics...)
	sort.Strings(out)
	return out
}
