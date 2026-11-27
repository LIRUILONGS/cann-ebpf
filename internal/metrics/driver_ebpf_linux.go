//go:build linux

package metrics

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/link"
	"github.com/cilium/ebpf/rlimit"
)

const (
	driverModeLatency = 1
	driverModeIoctl   = 2
	driverModeSizeArg = 3
	driverModeSizeVMA = 4

	driverKallsymsPath = "/proc/kallsyms"
)

//go:embed bpf/driver_metrics.bpf.o
var driverBPFObject []byte

// driverProbe is one kprobe/kretprobe pair compiled into driver_metrics.bpf.o.
// IDs are part of the C/Go ABI and must never be renumbered.
type driverProbe struct {
	ID           uint32
	Symbol       string
	Mode         uint32
	ArgIndex     int32
	SizeVerified bool
	Group        string
	Kind         string
}

// driverSymbolResolution is a load-time inventory of a kprobe symbol. A
// duplicate count means the symbol exists in more than one kernel module; the
// driver probe is skipped because cilium/ebpf's kprobe API cannot select a
// module unambiguously.
type driverSymbolResolution struct {
	Count   int
	Modules []string
}

var driverProbes = []driverProbe{
	{ID: 1, Symbol: "drv_ascend_intf_ioctl", Mode: driverModeIoctl, Group: "ioctl", Kind: "davinci"},
	{ID: 2, Symbol: "devmm_svm_ioctl", Mode: driverModeIoctl, Group: "ioctl", Kind: "svm"},
	{ID: 4, Symbol: "hdcdrv_ioctl", Mode: driverModeIoctl, Group: "ioctl", Kind: "hdc"},
	{ID: 5, Symbol: "trs_ioctl", Mode: driverModeIoctl, Group: "ioctl", Kind: "task"},
	{ID: 6, Symbol: "hdcdrv_send", Mode: driverModeLatency, Group: "hdc_send", Kind: "hdc"},
	{ID: 7, Symbol: "hdcdrv_recv", Mode: driverModeLatency, Group: "hdc_recv", Kind: "hdc"},
	{ID: 8, Symbol: "hdcdrv_msg_chan_send", Mode: driverModeLatency, Group: "hdc_send", Kind: "hdc"},
	{ID: 9, Symbol: "hdcdrv_get_w_sq_desc", Mode: driverModeLatency, Group: "hdc_submit", Kind: "hdc"},
	{ID: 10, Symbol: "devdrv_msg_ring_doorbell", Mode: driverModeLatency, Group: "task_submit", Kind: "task"},
	{ID: 11, Symbol: "devdrv_set_sq_doorbell", Mode: driverModeLatency, Group: "task_submit", Kind: "task"},
	{ID: 12, Symbol: "devdrv_dma_map_page", Mode: driverModeSizeArg, ArgIndex: 3, Group: "dma_map", Kind: "dma_map"},
	{ID: 13, Symbol: "devdrv_dma_map_single", Mode: driverModeSizeArg, ArgIndex: 2, Group: "dma_map", Kind: "dma_map"},
	{ID: 14, Symbol: "dma_map_page_attrs", Mode: driverModeSizeArg, ArgIndex: 3, SizeVerified: true, Group: "dma_map", Kind: "dma_map"},
	{ID: 15, Symbol: "iommu_dma_map_page", Mode: driverModeSizeArg, ArgIndex: 3, SizeVerified: true, Group: "dma_map", Kind: "dma_map"},
	{ID: 16, Symbol: "devdrv_dma_unmap_page", Mode: driverModeSizeArg, ArgIndex: 2, Group: "dma_unmap", Kind: "dma_unmap"},
	{ID: 17, Symbol: "dma_unmap_page_attrs", Mode: driverModeSizeArg, ArgIndex: 2, SizeVerified: true, Group: "dma_unmap", Kind: "dma_unmap"},
	{ID: 18, Symbol: "iommu_dma_unmap_page", Mode: driverModeSizeArg, ArgIndex: 2, SizeVerified: true, Group: "dma_unmap", Kind: "dma_unmap"},
	{ID: 19, Symbol: "alloc_iova", Mode: driverModeSizeArg, ArgIndex: 1, SizeVerified: true, Group: "iova_alloc", Kind: "iova"},
	{ID: 20, Symbol: "alloc_iova_fast", Mode: driverModeSizeArg, ArgIndex: 1, SizeVerified: true, Group: "iova_alloc", Kind: "iova"},
	{ID: 21, Symbol: "iommu_dma_alloc_iova", Mode: driverModeSizeArg, ArgIndex: 1, SizeVerified: true, Group: "iova_alloc", Kind: "iova"},
	{ID: 22, Symbol: "free_iova", Mode: driverModeLatency, Group: "iova_free", Kind: "iova"},
	{ID: 23, Symbol: "free_iova_fast", Mode: driverModeLatency, Group: "iova_free", Kind: "iova"},
	{ID: 24, Symbol: "devmm_svm_mmap", Mode: driverModeSizeVMA, ArgIndex: 1, SizeVerified: true, Group: "svm_alloc", Kind: "svm"},
	{ID: 25, Symbol: "devmm_svm_munmap", Mode: driverModeSizeVMA, ArgIndex: 1, SizeVerified: true, Group: "svm_free", Kind: "svm"},
	{ID: 26, Symbol: "xsmem_alloc", Mode: driverModeSizeArg, ArgIndex: 1, Group: "xsmem_alloc", Kind: "xsmem"},
	{ID: 27, Symbol: "xsmem_free", Mode: driverModeSizeArg, ArgIndex: 1, Group: "xsmem_free", Kind: "xsmem"},
	{ID: 28, Symbol: "xsmem_pool_alloc", Mode: driverModeSizeArg, ArgIndex: 1, Group: "xsmem_pool_alloc", Kind: "xsmem_pool"},
	{ID: 29, Symbol: "xsmem_pool_free", Mode: driverModeSizeArg, ArgIndex: 1, Group: "xsmem_pool_free", Kind: "xsmem_pool"},
	{ID: 30, Symbol: "hdccom_alloc_mem", Mode: driverModeSizeArg, ArgIndex: 1, Group: "hdc_alloc", Kind: "hdc"},
	{ID: 31, Symbol: "devdrv_dma_done_task", Mode: driverModeLatency, Group: "task_done", Kind: "task"},
	{ID: 32, Symbol: "hdcdrv_copy_sq_desc_to_remote", Mode: driverModeLatency, Group: "hdc_submit", Kind: "hdc"},
	{ID: 33, Symbol: "sys_enter_mmap", Mode: driverModeSizeArg, ArgIndex: 1, SizeVerified: true, Group: "mmap", Kind: "mmap"},
}

// driverHistogramBounds are finite upper bounds in nanoseconds. The schema's
// implicit +Inf bucket is represented by Histogram.Count, not by a stored
// bound, so anything above 100ms remains visible in Count but not in Counts.
var driverHistogramBounds = []float64{
	1e3, 2e3, 5e3, 1e4, 2.5e4, 5e4, 1e5, 2.5e5,
	5e5, 1e6, 2.5e6, 5e6, 1e7, 2.5e7, 5e7, 1e8,
}

type driverCountKey struct {
	ProbeID  uint32
	Cmd      uint32
	TGID     uint32
	TID      uint32
	FD       uint32
	Pad      uint32
	CgroupID uint64
}

type driverCountValue struct {
	Count uint64
	Bytes uint64
}

type driverHistValue struct {
	Buckets [16]uint64
	SumNS   uint64
	Count   uint64
}

type driverStats struct {
	Entries         uint64
	Exits           uint64
	UpdateErrors    uint64
	MissingInflight uint64
	CountDrops      uint64
	HistDrops       uint64
	PendingDrops    uint64
}

type driverCountSample struct {
	Key   driverCountKey
	Value driverCountValue
}

type driverHistSample struct {
	Key   driverCountKey
	Value driverHistValue
}

// DriverEBPFSource collects host-side NPU driver metrics through kprobes on
// CANN kernel symbols plus sys_enter_ioctl/sys_exit_ioctl tracepoints for
// fd/cmd attribution. It never fabricates samples: every metric that cannot
// be attached is reported as unavailable with a reason.
type DriverEBPFSource struct {
	PIDs     []int
	ProcRoot string
	Every    time.Duration
	Deadline time.Duration

	initOnce           sync.Once
	collection         *ebpf.Collection
	links              []link.Link
	attached           map[uint32]bool
	failures           map[uint32]string
	initErr            error
	ioctlTracepoints   bool
	ioctlTracepointErr string
	mmapTracepointErr  string
	symbolResolveErr   string
	closed             bool
	closeMu            sync.Mutex
	deviceCache        map[string]cachedString
	cgroupCache        map[string]cachedString
}

type cachedString struct {
	value string
	until time.Time
}

// NewDriverEBPFSource creates a driver-layer source. Initialization is lazy:
// a missing BPF object, missing capability or unsupported architecture turns
// into explicit unavailable status points instead of killing the metrics run.
func NewDriverEBPFSource(pids []int, interval, timeout time.Duration) *DriverEBPFSource {
	if interval <= 0 {
		interval = time.Second
	}
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	source := &DriverEBPFSource{
		PIDs: append([]int(nil), pids...), ProcRoot: "/proc",
		Every: interval, Deadline: timeout,
		attached: make(map[uint32]bool), failures: make(map[uint32]string),
		deviceCache: make(map[string]cachedString), cgroupCache: make(map[string]cachedString),
	}
	return source
}

func (s *DriverEBPFSource) Name() string            { return driverSourceName }
func (s *DriverEBPFSource) Interval() time.Duration { return s.Every }
func (s *DriverEBPFSource) Timeout() time.Duration  { return s.Deadline }

func (s *DriverEBPFSource) ensureInit() {
	s.initOnce.Do(func() {
		if runtime.GOARCH != "arm64" {
			s.initErr = fmt.Errorf("driver-ebpf requires linux/arm64 (running linux/%s); argument offsets are arm64-specific", runtime.GOARCH)
			return
		}
		spec, err := loadDriverCollectionSpec()
		if err != nil {
			s.initErr = err
			return
		}
		symbols, symbolErr := readDriverKernelSymbols(driverKallsymsPath)
		if symbolErr != nil {
			s.symbolResolveErr = "read " + driverKallsymsPath + ": " + symbolErr.Error()
		}
		for id, reason := range prepareDriverCollectionSpec(spec, symbols) {
			s.failures[id] = reason
		}
		if err := rlimit.RemoveMemlock(); err != nil {
			// Not fatal: some hardened hosts pre-set memlock; the load below
			// will report EPERM if the limit is actually insufficient.
		}
		collection, err := ebpf.NewCollection(spec)
		if err != nil {
			s.initErr = fmt.Errorf("load driver BPF collection: %w", err)
			return
		}
		pidsMap, ok := collection.Maps["target_pids"]
		if !ok {
			collection.Close()
			s.initErr = fmt.Errorf("driver BPF map target_pids not found")
			return
		}
		for _, pid := range s.PIDs {
			if pid <= 0 {
				continue
			}
			if err := pidsMap.Update(uint32(pid), uint8(1), ebpf.UpdateAny); err != nil {
				collection.Close()
				s.initErr = fmt.Errorf("register target pid %d in driver BPF map: %w", pid, err)
				return
			}
		}

		for _, probe := range driverProbes {
			if probe.ID == 33 {
				continue // tracepoint, attached below
			}
			if s.failures[probe.ID] != "" {
				continue // missing or ambiguous symbol was pruned before load
			}
			enterName := fmt.Sprintf("driver_enter_%d", probe.ID)
			exitName := fmt.Sprintf("driver_exit_%d", probe.ID)
			enterProgram := collection.Programs[enterName]
			exitProgram := collection.Programs[exitName]
			if enterProgram == nil || exitProgram == nil {
				s.failures[probe.ID] = "compiled kprobe program missing"
				continue
			}
			enterLink, err := link.Kprobe(probe.Symbol, enterProgram, nil)
			if err != nil {
				s.failures[probe.ID] = "kprobe_attach_failed: " + err.Error()
				continue
			}
			exitLink, err := link.Kretprobe(probe.Symbol, exitProgram, nil)
			if err != nil {
				_ = enterLink.Close()
				s.failures[probe.ID] = "kretprobe_attach_failed: " + err.Error()
				continue
			}
			s.links = append(s.links, enterLink, exitLink)
			s.attached[probe.ID] = true
		}

		enterTracepoint := collection.Programs["on_sys_enter_ioctl"]
		exitTracepoint := collection.Programs["on_sys_exit_ioctl"]
		if enterTracepoint == nil || exitTracepoint == nil {
			s.ioctlTracepointErr = "compiled ioctl tracepoint program missing"
		} else if err := checkDriverTracepointLayout("syscalls", "sys_enter_ioctl"); err != nil {
			s.ioctlTracepointErr = "sys_enter_ioctl layout validation failed: " + err.Error()
		} else {
			enterLink, err := link.Tracepoint("syscalls", "sys_enter_ioctl", enterTracepoint, nil)
			if err != nil {
				s.ioctlTracepointErr = "sys_enter_ioctl attach failed: " + err.Error()
			} else {
				exitLink, exitErr := link.Tracepoint("syscalls", "sys_exit_ioctl", exitTracepoint, nil)
				if exitErr != nil {
					_ = enterLink.Close()
					s.ioctlTracepointErr = "sys_exit_ioctl attach failed: " + exitErr.Error()
				} else {
					s.links = append(s.links, enterLink, exitLink)
					s.ioctlTracepoints = true
				}
			}
		}
		mmapTracepoint := collection.Programs["on_sys_enter_mmap"]
		if mmapTracepoint == nil {
			s.mmapTracepointErr = "compiled sys_enter_mmap tracepoint program missing"
			s.failures[33] = s.mmapTracepointErr
		} else if err := checkDriverTracepointLayout("syscalls", "sys_enter_mmap"); err != nil {
			s.mmapTracepointErr = "sys_enter_mmap layout validation failed: " + err.Error()
			s.failures[33] = s.mmapTracepointErr
		} else {
			mmapLink, err := link.Tracepoint("syscalls", "sys_enter_mmap", mmapTracepoint, nil)
			if err != nil {
				s.mmapTracepointErr = "sys_enter_mmap attach failed: " + err.Error()
				s.failures[33] = s.mmapTracepointErr
			} else {
				s.links = append(s.links, mmapLink)
				s.attached[33] = true
			}
		}
		s.collection = collection
	})
}

// Close detaches all probes. It is idempotent and safe to call after a failed
// initialization.
func (s *DriverEBPFSource) Close() error {
	s.closeMu.Lock()
	defer s.closeMu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	var firstErr error
	for _, l := range s.links {
		if err := l.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	s.links = nil
	if s.collection != nil {
		s.collection.Close()
		s.collection = nil
	}
	return firstErr
}

// Collect drains the BPF maps. Every point is a cumulative value; callers can
// derive rates from consecutive points exactly like other cann.metrics.v1
// counters.
func (s *DriverEBPFSource) Collect(ctx context.Context) ([]Point, []string, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	s.ensureInit()
	if s.initErr != nil {
		return s.unavailablePoints(s.initErr.Error()), []string{"driver-ebpf: " + s.initErr.Error()}, nil
	}
	collection := s.collection
	if collection == nil {
		return s.unavailablePoints("driver BPF collection is nil"), []string{"driver-ebpf: BPF collection is nil"}, nil
	}
	countsMap, ok := collection.Maps["counts"]
	if !ok {
		return s.unavailablePoints("driver BPF map counts missing"), []string{"driver-ebpf: BPF map counts missing"}, nil
	}
	histMap, ok := collection.Maps["histograms"]
	if !ok {
		return s.unavailablePoints("driver BPF map histograms missing"), []string{"driver-ebpf: BPF map histograms missing"}, nil
	}

	counts, err := readDriverCounts(countsMap)
	if err != nil {
		return s.statusPoints(), []string{"driver-ebpf: read counts: " + err.Error()}, nil
	}
	hists, err := readDriverHistograms(histMap)
	if err != nil {
		return s.statusPoints(), []string{"driver-ebpf: read histograms: " + err.Error()}, nil
	}
	stats, statsErr := readDriverStats(collection)

	points := make([]Point, 0, len(counts)*3+len(hists)*2+len(driverProbes)*2+16)
	points = append(points, s.countPoints(counts)...)
	points = append(points, s.histogramPoints(hists)...)
	points = append(points, s.statusPoints()...)
	var warnings []string
	if statsErr != nil {
		warnings = append(warnings, "driver-ebpf: read stats: "+statsErr.Error())
	} else {
		points = append(points, s.statsPoints(stats)...)
	}
	if !s.ioctlTracepoints && s.ioctlTracepointErr != "" {
		warnings = append(warnings, "driver-ebpf: "+s.ioctlTracepointErr+"; ioctl cmd/fd labels are unavailable")
	}
	if s.mmapTracepointErr != "" {
		warnings = append(warnings, "driver-ebpf: "+s.mmapTracepointErr+"; npu_mmap_bytes is unavailable")
	}
	if s.symbolResolveErr != "" {
		warnings = append(warnings, "driver-ebpf: "+s.symbolResolveErr+"; kprobe symbols were not pre-filtered")
	}
	return points, warnings, nil
}

func loadDriverCollectionSpec() (*ebpf.CollectionSpec, error) {
	if len(driverBPFObject) == 0 {
		return nil, fmt.Errorf("driver-ebpf: embedded BPF object is empty")
	}
	spec, err := ebpf.LoadCollectionSpecFromReader(bytes.NewReader(driverBPFObject))
	if err != nil {
		return nil, fmt.Errorf("driver-ebpf: parse embedded BPF object: %w", err)
	}
	return spec, nil
}

func driverProbeSymbolSet() map[string]struct{} {
	wanted := make(map[string]struct{}, len(driverProbes))
	for _, probe := range driverProbes {
		wanted[probe.Symbol] = struct{}{}
	}
	return wanted
}

// parseDriverKernelSymbols inventories only the configured driver symbols.
// /proc/kallsyms includes both built-in and module function entries. A symbol
// that appears more than once is ambiguous: tracefs cannot select the intended
// module through cilium/ebpf's kprobe API.
func parseDriverKernelSymbols(data []byte, wanted map[string]struct{}) map[string]driverSymbolResolution {
	out := make(map[string]driverSymbolResolution)
	for _, rawLine := range bytes.Split(data, []byte{10}) {
		fields := strings.Fields(string(rawLine))
		if len(fields) < 3 || (fields[1] != "t" && fields[1] != "T") {
			continue
		}
		name := fields[2]
		if _, ok := wanted[name]; !ok {
			continue
		}
		module := "vmlinux"
		if len(fields) >= 4 {
			module = strings.Trim(fields[3], "[]")
		}
		resolution := out[name]
		resolution.Count++
		found := false
		for _, existing := range resolution.Modules {
			if existing == module {
				found = true
				break
			}
		}
		if !found {
			resolution.Modules = append(resolution.Modules, module)
		}
		sort.Strings(resolution.Modules)
		out[name] = resolution
	}
	return out
}

func readDriverKernelSymbols(path string) (map[string]driverSymbolResolution, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parseDriverKernelSymbols(data, driverProbeSymbolSet()), nil
}

func isDriverKprobeSection(section string) bool {
	return strings.HasPrefix(section, "kprobe/") || strings.HasPrefix(section, "kretprobe/")
}

func driverSymbolFailureReason(symbol string, resolution driverSymbolResolution) string {
	if resolution.Count == 0 {
		return "symbol_not_found: no t/T entry for " + symbol + " in " + driverKallsymsPath
	}
	modules := append([]string(nil), resolution.Modules...)
	if len(modules) == 0 {
		modules = []string{"unknown"}
	}
	return fmt.Sprintf("ambiguous_kernel_symbol: %s has %d function entries in modules %s; skipped",
		symbol, resolution.Count, strings.Join(modules, ","))
}

// prepareDriverCollectionSpec clears kprobe AttachTo fields before
// ebpf.NewCollection. cilium/ebpf resolves every non-empty kprobe AttachTo in a
// batch and fails the whole collection on an ambiguous symbol. Link attachment
// is explicit later, so AttachTo is not needed at program load time.
//
// When the symbol inventory is available, missing and ambiguous probes are
// removed from the collection and their reasons are returned. A nil inventory
// means /proc/kallsyms could not be read; keep the programs and let the
// explicit link.Kprobe call report the final attach error.
func prepareDriverCollectionSpec(spec *ebpf.CollectionSpec, symbols map[string]driverSymbolResolution) map[uint32]string {
	failures := make(map[uint32]string)
	if spec == nil {
		return failures
	}
	for _, program := range spec.Programs {
		if isDriverKprobeSection(program.SectionName) {
			program.AttachTo = ""
		}
	}
	if symbols == nil {
		return failures
	}
	for _, probe := range driverProbes {
		if probe.ID == 33 {
			continue
		}
		resolution := symbols[probe.Symbol]
		if resolution.Count == 1 {
			continue
		}
		failures[probe.ID] = driverSymbolFailureReason(probe.Symbol, resolution)
		delete(spec.Programs, fmt.Sprintf("driver_enter_%d", probe.ID))
		delete(spec.Programs, fmt.Sprintf("driver_exit_%d", probe.ID))
	}
	return failures
}

func readDriverCounts(m *ebpf.Map) ([]driverCountSample, error) {
	var out []driverCountSample
	var key driverCountKey
	var value driverCountValue
	iterator := m.Iterate()
	for iterator.Next(&key, &value) {
		if value.Count == 0 {
			continue
		}
		out = append(out, driverCountSample{Key: key, Value: value})
	}
	if err := iterator.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func readDriverHistograms(m *ebpf.Map) ([]driverHistSample, error) {
	var out []driverHistSample
	var key driverCountKey
	var value driverHistValue
	iterator := m.Iterate()
	for iterator.Next(&key, &value) {
		if value.Count == 0 {
			continue
		}
		out = append(out, driverHistSample{Key: key, Value: value})
	}
	if err := iterator.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func readDriverStats(collection *ebpf.Collection) (driverStats, error) {
	var stats driverStats
	statsMap, ok := collection.Maps["stats"]
	if !ok {
		return stats, fmt.Errorf("BPF map stats not found")
	}
	if err := statsMap.Lookup(uint32(0), &stats); err != nil {
		return stats, err
	}
	return stats, nil
}

func driverProbeForID(id uint32) (driverProbe, bool) {
	for _, probe := range driverProbes {
		if probe.ID == id {
			return probe, true
		}
	}
	return driverProbe{}, false
}

func driverMetricNamesForProbe(probe driverProbe) []string {
	switch probe.Group {
	case "ioctl":
		names := []string{"npu_ioctl_total", "npu_ioctl_latency_ns"}
		if probe.Kind == "task" {
			names = append(names, "npu_task_submit_total", "npu_task_wait_ns")
		}
		return names
	case "hdc_send":
		return []string{"npu_hdc_send_total", "npu_hdc_latency_ns"}
	case "hdc_recv":
		return []string{"npu_hdc_recv_total", "npu_hdc_latency_ns"}
	case "hdc_submit":
		return []string{"npu_hdc_submit_total", "npu_hdc_latency_ns"}
	case "hdc_alloc":
		return []string{"npu_hdc_alloc_bytes", "npu_hdc_alloc_ops_total", "npu_hdc_latency_ns"}
	case "dma_map":
		return []string{"npu_dma_map_latency_ns", "npu_dma_map_bytes", "npu_dma_map_ops_total"}
	case "dma_unmap":
		return []string{"npu_dma_unmap_latency_ns", "npu_dma_unmap_bytes", "npu_dma_unmap_ops_total"}
	case "iova_alloc", "iova_free":
		return []string{"npu_iova_alloc_free", "npu_iova_latency_ns"}
	case "svm_alloc":
		return []string{"npu_svm_alloc_bytes", "npu_svm_alloc_ops_total"}
	case "svm_free":
		return []string{"npu_svm_free_bytes", "npu_svm_free_ops_total"}
	case "xsmem_alloc":
		return []string{"npu_xsmem_alloc_bytes", "npu_xsmem_alloc_ops_total"}
	case "xsmem_free":
		return []string{"npu_xsmem_free_bytes", "npu_xsmem_free_ops_total"}
	case "xsmem_pool_alloc":
		return []string{"npu_xsmem_pool_alloc_bytes", "npu_xsmem_pool_ops_total"}
	case "xsmem_pool_free":
		return []string{"npu_xsmem_pool_free_bytes", "npu_xsmem_pool_ops_total"}
	case "task_submit":
		return []string{"npu_task_doorbell_total"}
	case "task_done":
		return []string{"npu_task_done_total"}
	case "mmap":
		return []string{"npu_mmap_bytes", "npu_mmap_ops_total"}
	default:
		return nil
	}
}

func (s *DriverEBPFSource) countPoints(samples []driverCountSample) []Point {
	var points []Point
	for _, sample := range samples {
		probe, ok := driverProbeForID(sample.Key.ProbeID)
		if !ok {
			continue
		}
		labels := s.baseLabels(probe, sample.Key)
		if probe.Group == "mmap" && labels["device"] == "unknown" {
			// sys_enter_mmap also sees anonymous and unrelated mappings. Only
			// emit NPU mmap bytes when the fd resolves to an actual NPU node.
			continue
		}
		count := float64(sample.Value.Count)
		bytes := float64(sample.Value.Bytes)
		if !probe.SizeVerified {
			// The function exists, but the target ABI's size argument has not
			// been verified. Keep the operation count and latency, and mark
			// the byte metric unavailable in statusPoints instead of emitting a
			// possibly wrong byte total.
			bytes = 0
		}
		switch probe.Group {
		case "ioctl":
			points = append(points, driverPoint(Counter("npu_ioctl_total", "count", count, labels)))
			if probe.Kind == "task" {
				points = append(points, driverPoint(Counter("npu_task_submit_total", "count", count, labels)))
			}
		case "hdc_send":
			points = append(points, driverPoint(Counter("npu_hdc_send_total", "count", count, labels)))
		case "hdc_recv":
			points = append(points, driverPoint(Counter("npu_hdc_recv_total", "count", count, labels)))
		case "hdc_submit":
			points = append(points, driverPoint(Counter("npu_hdc_submit_total", "count", count, labels)))
		case "hdc_alloc":
			points = append(points, driverPoint(Counter("npu_hdc_alloc_ops_total", "count", count, labels)))
			if bytes > 0 {
				points = append(points, driverPoint(Counter("npu_hdc_alloc_bytes", "bytes", bytes, labels)))
			}
		case "dma_map":
			points = append(points, driverPoint(Counter("npu_dma_map_ops_total", "count", count, labels)))
			if bytes > 0 {
				points = append(points, driverPoint(Counter("npu_dma_map_bytes", "bytes", bytes, labels)))
			}
		case "dma_unmap":
			points = append(points, driverPoint(Counter("npu_dma_unmap_ops_total", "count", count, labels)))
			if bytes > 0 {
				points = append(points, driverPoint(Counter("npu_dma_unmap_bytes", "bytes", bytes, labels)))
			}
		case "iova_alloc", "iova_free":
			opLabels := cloneLabels(labels)
			opLabels["op"] = strings.TrimPrefix(probe.Group, "iova_")
			points = append(points, driverPoint(Counter("npu_iova_alloc_free", "count", count, opLabels)))
		case "svm_alloc":
			points = append(points, driverPoint(Counter("npu_svm_alloc_ops_total", "count", count, labels)))
			if bytes > 0 {
				points = append(points, driverPoint(Counter("npu_svm_alloc_bytes", "bytes", bytes, labels)))
			}
		case "svm_free":
			points = append(points, driverPoint(Counter("npu_svm_free_ops_total", "count", count, labels)))
			if bytes > 0 {
				points = append(points, driverPoint(Counter("npu_svm_free_bytes", "bytes", bytes, labels)))
			}
		case "xsmem_alloc":
			points = append(points, driverPoint(Counter("npu_xsmem_alloc_ops_total", "count", count, labels)))
			if bytes > 0 {
				points = append(points, driverPoint(Counter("npu_xsmem_alloc_bytes", "bytes", bytes, labels)))
			}
		case "xsmem_free":
			points = append(points, driverPoint(Counter("npu_xsmem_free_ops_total", "count", count, labels)))
			if bytes > 0 {
				points = append(points, driverPoint(Counter("npu_xsmem_free_bytes", "bytes", bytes, labels)))
			}
		case "xsmem_pool_alloc", "xsmem_pool_free":
			opLabels := cloneLabels(labels)
			opLabels["op"] = strings.TrimPrefix(probe.Group, "xsmem_pool_")
			points = append(points, driverPoint(Counter("npu_xsmem_pool_ops_total", "count", count, opLabels)))
			bytesName := "npu_xsmem_pool_alloc_bytes"
			if probe.Group == "xsmem_pool_free" {
				bytesName = "npu_xsmem_pool_free_bytes"
			}
			if bytes > 0 {
				points = append(points, driverPoint(Counter(bytesName, "bytes", bytes, labels)))
			}
		case "task_submit":
			points = append(points, driverPoint(Counter("npu_task_doorbell_total", "count", count, labels)))
		case "task_done":
			points = append(points, driverPoint(Counter("npu_task_done_total", "count", count, labels)))
		case "mmap":
			points = append(points, driverPoint(Counter("npu_mmap_ops_total", "count", count, labels)))
			if bytes > 0 {
				points = append(points, driverPoint(Counter("npu_mmap_bytes", "bytes", bytes, labels)))
			}
		}
	}
	return points
}

func (s *DriverEBPFSource) histogramPoints(samples []driverHistSample) []Point {
	var points []Point
	for _, sample := range samples {
		probe, ok := driverProbeForID(sample.Key.ProbeID)
		if !ok {
			continue
		}
		labels := s.baseLabels(probe, sample.Key)
		hist := histogramFromDriverBins(sample.Value)
		switch probe.Group {
		case "ioctl":
			points = append(points, driverPoint(HistogramPoint("npu_ioctl_latency_ns", "nanoseconds", hist, labels)))
			if probe.Kind == "task" {
				points = append(points, driverPoint(HistogramPoint("npu_task_wait_ns", "nanoseconds", hist, labels)))
			}
		case "hdc_send", "hdc_recv", "hdc_submit", "hdc_alloc":
			points = append(points, driverPoint(HistogramPoint("npu_hdc_latency_ns", "nanoseconds", hist, labels)))
		case "dma_map":
			points = append(points, driverPoint(HistogramPoint("npu_dma_map_latency_ns", "nanoseconds", hist, labels)))
		case "dma_unmap":
			points = append(points, driverPoint(HistogramPoint("npu_dma_unmap_latency_ns", "nanoseconds", hist, labels)))
		case "iova_alloc", "iova_free":
			opLabels := cloneLabels(labels)
			opLabels["op"] = strings.TrimPrefix(probe.Group, "iova_")
			points = append(points, driverPoint(HistogramPoint("npu_iova_latency_ns", "nanoseconds", hist, opLabels)))
		}
	}
	return points
}

func histogramFromDriverBins(value driverHistValue) Histogram {
	counts := make([]int64, len(value.Buckets))
	cumulative := int64(0)
	for i, bucket := range value.Buckets {
		cumulative += int64(bucket)
		counts[i] = cumulative
	}
	return Histogram{
		Buckets:    append([]float64(nil), driverHistogramBounds...),
		Counts:     counts,
		Sum:        float64(value.SumNS),
		Count:      int64(value.Count),
		Cumulative: true,
	}
}

func (s *DriverEBPFSource) baseLabels(probe driverProbe, key driverCountKey) map[string]string {
	var device string
	if probe.Group == "mmap" {
		// Anonymous and unrelated mmap calls must never be counted as NPU
		// mappings. The fd is the only sufficient discriminator for this
		// tracepoint, so do not fall back to a process-wide device guess.
		device = s.deviceForFD(int(key.TGID), key.FD)
		if !strings.HasPrefix(device, "davinci") || device == "davinci_manager" {
			device = ""
		}
	} else {
		device = s.deviceFor(int(key.TGID), key.FD)
	}
	if device == "" {
		device = "unknown"
	}
	labels := map[string]string{
		"handler":    probe.Symbol,
		"pid":        strconv.FormatUint(uint64(key.TGID), 10),
		"tid":        strconv.FormatUint(uint64(key.TID), 10),
		"cgroup":     s.cgroupFor(int(key.TGID), key.CgroupID),
		"kind":       probe.Kind,
		"device":     device,
		"confidence": s.confidence(probe, key, device),
	}
	if probe.Group == "ioctl" || probe.Group == "task_wait" {
		labels["cmd"] = formatDriverCmd(key.Cmd)
	}
	if probe.Mode == driverModeSizeArg {
		signature := fmt.Sprintf("arg%d_size", probe.ArgIndex)
		if !probe.SizeVerified {
			signature += "_unverified"
		}
		labels["signature"] = signature
	} else if probe.Mode == driverModeSizeVMA {
		labels["signature"] = "vma_start_end"
	}
	if strings.HasPrefix(probe.Group, "hdc_") {
		labels["op"] = strings.TrimPrefix(probe.Group, "hdc_")
	}
	return labels
}

func (s *DriverEBPFSource) deviceForFD(pid int, fd uint32) string {
	if pid <= 0 || fd == 0 || fd == 0xffffffff {
		return ""
	}
	target, err := os.Readlink(filepath.Join(s.ProcRoot, strconv.Itoa(pid), "fd", strconv.FormatUint(uint64(fd), 10)))
	if err != nil {
		return ""
	}
	return deviceFromPath(target)
}

func (s *DriverEBPFSource) confidence(probe driverProbe, key driverCountKey, device string) string {
	if probe.Group == "ioctl" || probe.Group == "task_wait" {
		if key.FD != 0 && device != "unknown" {
			return "high"
		}
		return "medium"
	}
	if device != "unknown" {
		return "medium"
	}
	return "low"
}

func formatDriverCmd(cmd uint32) string {
	if cmd == 0 {
		return "unavailable"
	}
	return fmt.Sprintf("0x%08x", cmd)
}

func (s *DriverEBPFSource) deviceFor(pid int, fd uint32) string {
	if pid <= 0 {
		return ""
	}
	if device := s.deviceForFD(pid, fd); device != "" {
		return device
	}
	cacheKey := strconv.Itoa(pid)
	now := time.Now()
	if cached, ok := s.deviceCache[cacheKey]; ok && now.Before(cached.until) {
		return cached.value
	}
	device := s.deviceForPID(pid)
	s.deviceCache[cacheKey] = cachedString{value: device, until: now.Add(2 * time.Second)}
	return device
}

func (s *DriverEBPFSource) deviceForPID(pid int) string {
	fdDir := filepath.Join(s.ProcRoot, strconv.Itoa(pid), "fd")
	entries, err := os.ReadDir(fdDir)
	if err != nil {
		return ""
	}
	devices := make(map[string]struct{})
	aux := ""
	for _, entry := range entries {
		target, err := os.Readlink(filepath.Join(fdDir, entry.Name()))
		if err != nil {
			continue
		}
		device := deviceFromPath(target)
		if strings.HasPrefix(device, "davinci") && device != "davinci_manager" {
			devices[device] = struct{}{}
		} else if aux == "" && (device == "devmm_svm" || device == "hisi_hdc") {
			aux = device
		}
	}
	if len(devices) == 1 {
		for device := range devices {
			return device
		}
	}
	if len(devices) > 1 {
		return "multi"
	}
	return aux
}

func deviceFromPath(path string) string {
	base := filepath.Base(strings.TrimSpace(path))
	switch {
	case base == "davinci_manager":
		return "davinci_manager"
	case strings.HasPrefix(base, "davinci"):
		return base
	case base == "devmm_svm", base == "hisi_hdc", base == "hisi_hdc_sec":
		return base
	default:
		return ""
	}
}

func (s *DriverEBPFSource) cgroupFor(pid int, cgroupID uint64) string {
	if pid <= 0 {
		if cgroupID != 0 {
			return "cgroup:" + strconv.FormatUint(cgroupID, 10)
		}
		return "unknown"
	}
	cacheKey := strconv.Itoa(pid)
	now := time.Now()
	if cached, ok := s.cgroupCache[cacheKey]; ok && now.Before(cached.until) {
		return cached.value
	}
	result := ""
	if data, err := os.ReadFile(filepath.Join(s.ProcRoot, strconv.Itoa(pid), "cgroup")); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			parts := strings.SplitN(line, ":", 3)
			if len(parts) == 3 && strings.TrimSpace(parts[2]) != "" {
				result = strings.TrimSpace(parts[2])
			}
		}
	}
	if result == "" {
		if cgroupID != 0 {
			result = "cgroup:" + strconv.FormatUint(cgroupID, 10)
		} else {
			result = "unknown"
		}
	}
	s.cgroupCache[cacheKey] = cachedString{value: result, until: now.Add(2 * time.Second)}
	return result
}

func driverPoint(point Point) Point {
	point.Source = driverSourceName
	if point.Device == "" && point.Labels != nil {
		point.Device = point.Labels["device"]
	}
	return point
}

func (s *DriverEBPFSource) statusPoints() []Point {
	points := make([]Point, 0, len(driverProbes)*2+1)
	attachedCount := len(s.attached)
	if s.ioctlTracepoints {
		attachedCount++
	}
	failedCount := len(s.failures)
	if s.ioctlTracepointErr != "" && !s.ioctlTracepoints {
		failedCount++
	}
	status := "available"
	if attachedCount == 0 {
		status = "unavailable"
	} else if failedCount > 0 || !s.ioctlTracepoints {
		status = "degraded"
	}
	infoLabels := map[string]string{
		"status":            status,
		"probes_attached":   strconv.Itoa(attachedCount),
		"probes_failed":     strconv.Itoa(failedCount),
		"ioctl_tracepoints": strconv.FormatBool(s.ioctlTracepoints),
		"mmap_tracepoint":   strconv.FormatBool(s.attached[33]),
		"arch":              runtime.GOARCH,
		"pid":               "all",
		"tid":               "all",
		"cgroup":            "all",
		"device":            "host",
		"confidence":        "probe",
	}
	if s.ioctlTracepointErr != "" {
		infoLabels["reason"] = s.ioctlTracepointErr
	}
	points = append(points, driverPoint(Gauge("npu_driver_source_info", "count", 1, infoLabels)))

	for _, probe := range driverProbes {
		for _, name := range driverMetricNamesForProbe(probe) {
			metricStatus := "available"
			reason := ""
			if !s.attached[probe.ID] {
				metricStatus = "unavailable"
				reason = s.failures[probe.ID]
				if reason == "" {
					reason = "symbol_not_found"
				}
			}
			if s.attached[probe.ID] && !probe.SizeVerified && strings.HasSuffix(name, "_bytes") {
				metricStatus = "unavailable"
				reason = "size_signature_unverified"
			}
			labels := map[string]string{
				"metric": name, "handler": probe.Symbol, "status": metricStatus,
				"reason": reason, "pid": "all", "tid": "all", "cgroup": "all",
				"device": "host", "confidence": "probe",
			}
			points = append(points, driverPoint(Gauge("npu_driver_metric_status", "count", 1, labels)))
		}
	}
	return points
}

func (s *DriverEBPFSource) unavailablePoints(reason string) []Point {
	points := []Point{driverPoint(Gauge("npu_driver_source_info", "count", 1, map[string]string{
		"status": "unavailable", "reason": reason, "arch": runtime.GOARCH,
		"pid": "all", "tid": "all", "cgroup": "all", "device": "host", "confidence": "probe",
	}))}
	for _, name := range driverRequestedMetrics {
		points = append(points, driverPoint(Gauge("npu_driver_metric_status", "count", 1, map[string]string{
			"metric": name, "status": "unavailable", "reason": reason,
			"pid": "all", "tid": "all", "cgroup": "all", "device": "host", "confidence": "probe",
		})))
	}
	return points
}

func (s *DriverEBPFSource) statsPoints(stats driverStats) []Point {
	values := []struct {
		name  string
		value uint64
	}{
		{"npu_driver_bpf_entries_total", stats.Entries},
		{"npu_driver_bpf_exits_total", stats.Exits},
		{"npu_driver_bpf_update_errors_total", stats.UpdateErrors},
		{"npu_driver_bpf_missing_inflight_total", stats.MissingInflight},
		{"npu_driver_bpf_count_drops_total", stats.CountDrops},
		{"npu_driver_bpf_hist_drops_total", stats.HistDrops},
		{"npu_driver_bpf_pending_drops_total", stats.PendingDrops},
	}
	points := make([]Point, 0, len(values))
	for _, item := range values {
		point := Counter(item.name, "count", float64(item.value), map[string]string{
			"pid": "all", "tid": "all", "cgroup": "all", "device": "host", "confidence": "probe",
		})
		point.Device = "host"
		points = append(points, driverPoint(point))
	}
	return points
}

// DriverProbeSymbols returns the kprobe target list. Exposed for docs/tests.
func DriverProbeSymbols() []string {
	out := make([]string, 0, len(driverProbes))
	for _, probe := range driverProbes {
		out = append(out, probe.Symbol)
	}
	return out
}
