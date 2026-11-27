package report

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"cann-ebpf/internal/stacks"
)

const (
	flamegraphMaxDepth      = 128
	flamegraphMaxNodes      = 4096
	flamegraphTopNPULimit   = 40
	flamegraphRootName      = "all"
	flamegraphTruncatedName = "[truncated]"
	flamegraphUnknownName   = "[unknown]"
	flamegraphTruncatedKey  = "\x00truncated"
)

// FlamegraphStatus keeps the availability decision explicit. A report must
// never draw an empty chart and call it a flame graph.
type FlamegraphStatus struct {
	Available  bool   `json:"available"`
	Reason     string `json:"reason,omitempty"`
	Source     string `json:"source,omitempty"`
	Confidence string `json:"confidence,omitempty"`
}

// FlamegraphMeta is the compact sampling provenance shown above the chart.
type FlamegraphMeta struct {
	Host           string  `json:"host,omitempty"`
	Kernel         string  `json:"kernel,omitempty"`
	StartedAt      string  `json:"started_at,omitempty"`
	EndedAt        string  `json:"ended_at,omitempty"`
	DurationS      float64 `json:"duration_s"`
	FrequencyHz    int     `json:"frequency_hz"`
	TargetPIDs     []int   `json:"target_pids,omitempty"`
	KernelFilter   string  `json:"kernel_filter,omitempty"`
	Samples        uint64  `json:"samples"`
	UniqueStacks   int     `json:"unique_stacks"`
	StackFail      uint64  `json:"stack_fail"`
	Filtered       uint64  `json:"filtered"`
	Lost           uint64  `json:"lost"`
	SymbolCoverage float64 `json:"symbol_coverage"`
	UnknownPct     float64 `json:"unknown_pct"`
	KallsymsSHA256 string  `json:"kallsyms_sha256,omitempty"`
}

// FlamegraphNode is one node in the folded-stack tree. Count is the number of
// samples in this subtree; Self is the number of samples whose stack ends here.
type FlamegraphNode struct {
	Name     string            `json:"name"`
	Module   string            `json:"module,omitempty"`
	Category string            `json:"category,omitempty"`
	Unknown  bool              `json:"unknown,omitempty"`
	Self     uint64            `json:"self"`
	Count    uint64            `json:"count"`
	Depth    int               `json:"depth"`
	Children []*FlamegraphNode `json:"children,omitempty"`

	childrenByKey map[string]*FlamegraphNode
}

type FlamegraphCategory struct {
	Name    string  `json:"name"`
	Samples uint64  `json:"samples"`
	Pct     float64 `json:"pct"`
}

type FlamegraphTopNPU struct {
	Name     string  `json:"name"`
	Module   string  `json:"module,omitempty"`
	Samples  uint64  `json:"samples"`
	Self     uint64  `json:"self"`
	SelfPct  float64 `json:"self_pct"`
	Category string  `json:"category,omitempty"`
}

// FlamegraphData is an independent top-level report view. It is deliberately
// not part of ReportViewsV2, whose ten-view contract is frozen.
type FlamegraphData struct {
	Status       FlamegraphStatus     `json:"status"`
	Meta         FlamegraphMeta       `json:"meta"`
	Tree         *FlamegraphNode      `json:"tree,omitempty"`
	Categories   []FlamegraphCategory `json:"categories"`
	TopNPU       []FlamegraphTopNPU   `json:"top_npu"`
	TotalSamples uint64               `json:"total_samples"`
	TotalFrames  uint64               `json:"total_frames"` // sample-weighted frame occurrences
	NPUFramePct  float64              `json:"npu_frame_pct"`
	Truncated    bool                 `json:"truncated,omitempty"`
	Warnings     []string             `json:"warnings"`
}

type flamegraphBuildLimits struct {
	maxDepth int
	maxNodes int
}

type flamegraphBuilder struct {
	root          *FlamegraphNode
	rootTruncated *FlamegraphNode
	limits        flamegraphBuildLimits
	nodes         int
	truncated     bool
}

// LoadFlamegraph reads a run directory or a stacks.folded file through the
// stacks package so the report and the CLI share one on-disk contract.
func LoadFlamegraph(path string) (*FlamegraphData, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("stacks path is empty")
	}
	run, err := stacks.LoadRun(path)
	if err != nil {
		return nil, err
	}
	return BuildFlamegraph(run.Profile, run.Meta, path), nil
}

func buildStacksOnlyReport(flamegraph *FlamegraphData, opts Options) Data {
	durationMS := 1.0
	if flamegraph != nil && flamegraph.Meta.DurationS > 0 {
		durationMS = flamegraph.Meta.DurationS * 1000
	}
	title := strings.TrimSpace(opts.Title)
	if title == "" {
		title = "CANN eBPF 栈采样报告"
	}
	data := Data{
		Schema:      SchemaV1,
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Input:       opts.StacksPath,
		Title:       title,
		Run: RunInfo{
			Profile:   "stacks",
			Host:      flamegraphMetaField(flamegraph, func(meta FlamegraphMeta) string { return meta.Host }),
			Kernel:    flamegraphMetaField(flamegraph, func(meta FlamegraphMeta) string { return meta.Kernel }),
			Status:    "stacks-only",
			StartedAt: flamegraphMetaField(flamegraph, func(meta FlamegraphMeta) string { return meta.StartedAt }),
		},
		Window: Window{
			StartNS:    "0",
			EndNS:      strconv.FormatInt(int64(durationMS*1e6), 10),
			DurationMS: durationMS,
		},
		Summary: Summary{
			SlowThresholdMS: float64(opts.SlowThresholdNS) / 1e6,
		},
		Categories: []CategoryRow{},
		Functions:  []FunctionRow{},
		Spans:      []SpanPoint{},
		Timeline: TimelineData{
			HostRows:      []LaneRow{},
			DeviceRows:    []LaneRow{},
			MinDurationMS: float64(opts.MinDurationNS) / 1e6,
		},
		Heatmap:    HeatmapData{Rows: []HeatRow{}},
		CDF:        []CDFPoint{},
		Flamegraph: flamegraph,
		Warnings:   []string{},
		Notes: []string{
			"本报告只有 host kernel 栈采样证据：没有 spans/device/metrics，因此不绘制调用时间轴或设备视图。",
			"火焰图横轴是样本数，纵轴是栈深度；NPU 算子执行时间不会出现在 host kernel 栈里。",
			"全部计数来自 stacks.meta.json 与 stacks.folded；未知符号保留为 [unknown]，不补零、不伪造折叠栈。",
		},
	}
	if flamegraph != nil {
		data.Warnings = append(data.Warnings, flamegraph.Warnings...)
	}
	return data
}

func flamegraphMetaField(flamegraph *FlamegraphData, pick func(FlamegraphMeta) string) string {
	if flamegraph == nil {
		return ""
	}
	return pick(flamegraph.Meta)
}

// BuildFlamegraph converts a symbolized profile into a bounded tree. All
// symbol names, including [unknown], are retained; truncation only merges
// subtrees, it never drops their sample counts.
func BuildFlamegraph(profile *stacks.Profile, meta stacks.Meta, source string) *FlamegraphData {
	return buildFlamegraphWithLimits(profile, meta, source, flamegraphBuildLimits{
		maxDepth: flamegraphMaxDepth,
		maxNodes: flamegraphMaxNodes,
	})
}

func buildFlamegraphWithLimits(profile *stacks.Profile, meta stacks.Meta, source string, limits flamegraphBuildLimits) *FlamegraphData {
	data := &FlamegraphData{
		Status: FlamegraphStatus{
			Source:     strings.TrimSpace(source),
			Confidence: "unknown",
		},
		Meta:       flamegraphMetaFrom(meta),
		Categories: []FlamegraphCategory{},
		TopNPU:     []FlamegraphTopNPU{},
		Warnings:   append([]string{}, meta.Warnings...),
	}
	if limits.maxDepth < 1 {
		limits.maxDepth = 1
	}
	if limits.maxNodes < 2 {
		limits.maxNodes = 2
	}
	if profile == nil {
		data.Status.Reason = "stacks.folded 缺失：run 目录里没有折叠栈文件，不能绘制火焰图。"
		return data
	}

	total := uint64(0)
	totalFrames := uint64(0)
	for _, stack := range profile.Stacks {
		total += stack.Count
		totalFrames += uint64(len(stack.Frames)) * stack.Count
	}
	if total == 0 || len(profile.Stacks) == 0 {
		data.Status.Reason = "stacks.folded 中没有样本（samples=0），不绘制空火焰图。"
		return data
	}

	root := &FlamegraphNode{
		Name:          flamegraphRootName,
		Category:      "all",
		Depth:         0,
		childrenByKey: make(map[string]*FlamegraphNode),
	}
	builder := &flamegraphBuilder{
		root:   root,
		limits: limits,
		nodes:  1,
	}
	unknownFrames := uint64(0)
	npuFrames := uint64(0)
	for _, stack := range profile.Stacks {
		if stack.Count == 0 {
			continue
		}
		for _, frame := range stack.Frames {
			if isUnknownFrame(frame) {
				unknownFrames += stack.Count
			}
			if isNPUFrame(frame) {
				npuFrames += stack.Count
			}
		}
		builder.add(stack.Frames, stack.Count)
	}
	if root.Count == 0 {
		data.Status.Reason = "stacks.folded 中的样本计数全部为 0，不绘制空火焰图。"
		return data
	}

	builder.sortTree()
	data.Tree = root
	data.TotalSamples = root.Count
	data.TotalFrames = totalFrames
	data.Truncated = builder.truncated
	data.Categories = buildFlamegraphCategories(root, root.Count)
	data.TopNPU = buildFlamegraphTopNPU(profile, root.Count)
	if totalFrames > 0 {
		data.NPUFramePct = float64(npuFrames) * 100 / float64(totalFrames)
	}
	if data.Meta.Samples == 0 {
		data.Meta.Samples = root.Count
	}
	if data.Meta.UniqueStacks == 0 {
		data.Meta.UniqueStacks = len(profile.Stacks)
	}
	data.Status.Available = true
	data.Status.Reason = ""
	data.Status.Confidence = flamegraphConfidence(data.Meta.SymbolCoverage)
	if builder.truncated {
		data.Warnings = append(data.Warnings, fmt.Sprintf(
			"火焰图超过深度上限 %d 或节点上限 %d，超出部分已合并为 %s；样本计数保持完整。",
			limits.maxDepth, limits.maxNodes, flamegraphTruncatedName))
	}
	if unknownFrames > 0 {
		data.Warnings = append(data.Warnings, fmt.Sprintf(
			"%d/%d 帧样本未命中符号；[unknown] 已保留并归入 unknown 类别，未丢弃任何样本。",
			unknownFrames, totalFrames))
	}
	if data.Meta.Samples != 0 && data.Meta.Samples != root.Count {
		data.Warnings = append(data.Warnings, fmt.Sprintf(
			"meta samples=%d 与 folded 样本合计=%d 不一致；火焰图宽度按 folded 计数绘制，meta 原值保留。",
			data.Meta.Samples, root.Count))
	}
	if data.Meta.Lost > 0 {
		data.Warnings = append(data.Warnings, fmt.Sprintf(
			"采集侧报告 lost=%d；该计数按原始 meta 展示，不由折叠栈补零。", data.Meta.Lost))
	}
	return data
}

func flamegraphMetaFrom(meta stacks.Meta) FlamegraphMeta {
	return FlamegraphMeta{
		Host:           meta.Host,
		Kernel:         meta.Kernel,
		StartedAt:      meta.StartedAt,
		EndedAt:        meta.EndedAt,
		DurationS:      meta.DurationS,
		FrequencyHz:    meta.FrequencyHz,
		TargetPIDs:     append([]int{}, meta.TargetPIDs...),
		KernelFilter:   meta.KernelFilter,
		Samples:        meta.Samples,
		UniqueStacks:   meta.UniqueStacks,
		StackFail:      meta.StackFail,
		Filtered:       meta.Filtered,
		Lost:           meta.Lost,
		SymbolCoverage: meta.SymbolCoverage,
		UnknownPct:     meta.UnknownPct,
		KallsymsSHA256: meta.KallsymsSHA256,
	}
}

func (b *flamegraphBuilder) add(frames []stacks.Frame, count uint64) {
	b.root.Count += count
	if len(frames) == 0 {
		b.root.Self += count
		return
	}
	if !b.pathFits(frames) {
		b.mergeRootTruncated(count)
		return
	}
	node := b.root
	limit := len(frames)
	if limit > b.limits.maxDepth {
		limit = b.limits.maxDepth
	}
	for index := 0; index < limit; index++ {
		frame := frames[index]
		key := flamegraphFrameKey(frame)
		child := node.childrenByKey[key]
		if child == nil {
			child = &FlamegraphNode{
				Name:          flamegraphFrameName(frame),
				Module:        strings.TrimSpace(frame.Module),
				Category:      flamegraphFrameCategory(frame),
				Unknown:       isUnknownFrame(frame),
				Depth:         node.Depth + 1,
				childrenByKey: make(map[string]*FlamegraphNode),
			}
			node.childrenByKey[key] = child
			node.Children = append(node.Children, child)
			b.nodes++
		}
		child.Count += count
		node = child
	}
	if len(frames) > limit {
		b.mergeDepthTruncated(node, count)
		return
	}
	node.Self += count
}

// pathFits reserves one node slot for the synthetic root-level [truncated]
// node, so the final tree can never exceed the configured node budget.
func (b *flamegraphBuilder) pathFits(frames []stacks.Frame) bool {
	missing := 0
	node := b.root
	limit := len(frames)
	if limit > b.limits.maxDepth {
		limit = b.limits.maxDepth
	}
	for index := 0; index < limit; index++ {
		if node == nil {
			missing++
			continue
		}
		child, ok := node.childrenByKey[flamegraphFrameKey(frames[index])]
		if !ok {
			missing++
			node = nil
			continue
		}
		node = child
	}
	if len(frames) > limit {
		if node == nil {
			missing++
		} else if _, ok := node.childrenByKey[flamegraphTruncatedKey]; !ok {
			missing++
		}
	}
	return b.nodes+missing <= b.limits.maxNodes-1
}

func (b *flamegraphBuilder) mergeRootTruncated(count uint64) {
	if b.rootTruncated == nil {
		b.rootTruncated = &FlamegraphNode{
			Name:          flamegraphTruncatedName,
			Category:      "other",
			Depth:         1,
			childrenByKey: make(map[string]*FlamegraphNode),
		}
		b.root.childrenByKey[flamegraphTruncatedKey] = b.rootTruncated
		b.root.Children = append(b.root.Children, b.rootTruncated)
		b.nodes++
	}
	b.rootTruncated.Count += count
	b.rootTruncated.Self += count
	b.truncated = true
}

func (b *flamegraphBuilder) mergeDepthTruncated(parent *FlamegraphNode, count uint64) {
	child := parent.childrenByKey[flamegraphTruncatedKey]
	if child == nil {
		child = &FlamegraphNode{
			Name:          flamegraphTruncatedName,
			Category:      "other",
			Depth:         parent.Depth + 1,
			childrenByKey: make(map[string]*FlamegraphNode),
		}
		parent.childrenByKey[flamegraphTruncatedKey] = child
		parent.Children = append(parent.Children, child)
		b.nodes++
	}
	child.Count += count
	child.Self += count
	b.truncated = true
}

func (b *flamegraphBuilder) sortTree() {
	var walk func(node *FlamegraphNode)
	walk = func(node *FlamegraphNode) {
		sort.SliceStable(node.Children, func(i, j int) bool {
			if node.Children[i].Count == node.Children[j].Count {
				if node.Children[i].Name == node.Children[j].Name {
					return node.Children[i].Module < node.Children[j].Module
				}
				return node.Children[i].Name < node.Children[j].Name
			}
			return node.Children[i].Count > node.Children[j].Count
		})
		for _, child := range node.Children {
			walk(child)
		}
	}
	walk(b.root)
}

func buildFlamegraphCategories(root *FlamegraphNode, total uint64) []FlamegraphCategory {
	selfByCategory := map[string]uint64{}
	seen := map[string]struct{}{}
	var walk func(node *FlamegraphNode)
	walk = func(node *FlamegraphNode) {
		if node.Depth > 0 {
			category := strings.TrimSpace(node.Category)
			if category == "" {
				category = "other"
			}
			seen[category] = struct{}{}
			selfByCategory[category] += node.Self
		}
		for _, child := range node.Children {
			walk(child)
		}
	}
	walk(root)
	if root.Self > 0 {
		seen["other"] = struct{}{}
		selfByCategory["other"] += root.Self
	}
	out := make([]FlamegraphCategory, 0, len(seen))
	for category := range seen {
		samples := selfByCategory[category]
		pct := 0.0
		if total > 0 {
			pct = float64(samples) * 100 / float64(total)
		}
		out = append(out, FlamegraphCategory{Name: category, Samples: samples, Pct: pct})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Samples == out[j].Samples {
			return out[i].Name < out[j].Name
		}
		return out[i].Samples > out[j].Samples
	})
	return out
}

func buildFlamegraphTopNPU(profile *stacks.Profile, total uint64) []FlamegraphTopNPU {
	if profile == nil || total == 0 {
		return []FlamegraphTopNPU{}
	}
	type aggregate struct {
		name     string
		module   string
		category string
		samples  uint64
		self     uint64
	}
	items := map[string]*aggregate{}
	for _, stack := range profile.Stacks {
		if stack.Count == 0 {
			continue
		}
		last := len(stack.Frames) - 1
		for index, frame := range stack.Frames {
			if !isNPUFrame(frame) {
				continue
			}
			name := flamegraphFrameName(frame)
			module := strings.TrimSpace(frame.Module)
			category := flamegraphFrameCategory(frame)
			key := name + "\x00" + module + "\x00" + category
			item := items[key]
			if item == nil {
				item = &aggregate{name: name, module: module, category: category}
				items[key] = item
			}
			item.samples += stack.Count
			if index == last {
				item.self += stack.Count
			}
		}
	}
	out := make([]FlamegraphTopNPU, 0, len(items))
	for _, item := range items {
		selfPct := 0.0
		if total > 0 {
			selfPct = float64(item.self) * 100 / float64(total)
		}
		out = append(out, FlamegraphTopNPU{
			Name: item.name, Module: item.module, Samples: item.samples,
			Self: item.self, SelfPct: selfPct, Category: item.category,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Samples == out[j].Samples {
			return out[i].Name < out[j].Name
		}
		return out[i].Samples > out[j].Samples
	})
	if len(out) > flamegraphTopNPULimit {
		out = out[:flamegraphTopNPULimit]
	}
	return out
}

func flamegraphFrameKey(frame stacks.Frame) string {
	return "f:" + flamegraphFrameName(frame) + "\x00" + strings.TrimSpace(frame.Module) + "\x00" + flamegraphFrameCategory(frame)
}

func flamegraphFrameName(frame stacks.Frame) string {
	name := strings.TrimSpace(frame.Name)
	if name == "" {
		return flamegraphUnknownName
	}
	return name
}

func isUnknownFrame(frame stacks.Frame) bool {
	if frame.Unknown {
		return true
	}
	name := strings.ToLower(strings.TrimSpace(frame.Name))
	return name == "" || name == "[unknown]" || name == "unknown"
}

func isNPUFrame(frame stacks.Frame) bool {
	category := strings.ToLower(strings.TrimSpace(frame.Category))
	if category != "" {
		return category == "npu" || category == "aicore" || category == "aicpu"
	}
	return flamegraphFrameCategory(frame) == "npu"
}

func flamegraphFrameCategory(frame stacks.Frame) string {
	if category := strings.ToLower(strings.TrimSpace(frame.Category)); category != "" {
		return category
	}
	if isUnknownFrame(frame) {
		return "unknown"
	}
	if category := stacks.ClassifyName(frame.Name, frame.Module, nil); category != "other" {
		return category
	}
	name := strings.ToLower(strings.TrimSpace(frame.Name))
	module := strings.ToLower(strings.TrimSpace(frame.Module))
	for _, marker := range []string{"devdrv", "davinci", "hisi"} {
		if strings.Contains(module, marker) {
			return "npu"
		}
	}
	rules := []struct {
		category string
		prefixes []string
	}{
		{"npu", []string{"devdrv_", "drv_", "davinci", "hisi_", "svm_", "ascend", "aicore", "aicpu", "hdc_", "npu"}},
		{"dma", []string{"dma_", "smmu_", "iommu_", "iomap_", "arm_smmu"}},
		{"net", []string{"tcp_", "udp_", "unix_", "hccn_", "dev_queue_xmit", "napi_", "sk_", "sock_", "net_"}},
		{"sync", []string{"futex_", "epoll_", "mutex", "rwsem", "schedule", "__schedule", "wait_", "complete", "wake_up", "osq_"}},
		{"vfs", []string{"vfs_", "do_sys_open", "filp_", "path_", "ext4_", "xfs_", "overlayfs_", "fuse_"}},
		{"block", []string{"blk_", "bio_", "submit_bio", "io_schedule", "nvme_"}},
		{"sched", []string{"sched_", "cpu_", "idle_", "irq_", "softirq_", "timer_"}},
		{"sample", []string{"perf_swevent_", "__perf_event_overflow", "bpf_get_stackid", "perf_event_", "bpf_prog_", "__bpf_prog_", "perf_trace_"}},
		{"hccl", []string{"hccl"}},
		{"memory", []string{"memcpy", "copy_", "copy_from", "copy_to"}},
	}
	for _, rule := range rules {
		for _, prefix := range rule.prefixes {
			if prefix != "" && strings.HasPrefix(name, prefix) {
				return rule.category
			}
		}
	}
	text := name + " " + module
	switch {
	case strings.Contains(text, "npu") || strings.Contains(text, "ascend") ||
		strings.Contains(text, "davinci") || strings.Contains(text, "aicore"):
		return "npu"
	case strings.Contains(text, "hccl"):
		return "hccl"
	case strings.Contains(text, "memcpy") || strings.Contains(text, "copy") || strings.Contains(text, "dma"):
		return "memory"
	case strings.Contains(text, "sched") || strings.Contains(text, "schedule") || strings.Contains(text, "irq"):
		return "sched"
	case strings.Contains(text, "futex") || strings.Contains(text, "mutex") ||
		strings.Contains(text, "lock") || strings.Contains(text, "wait") || strings.Contains(text, "sync"):
		return "sync"
	case strings.Contains(text, "tcp") || strings.Contains(text, "socket") || strings.Contains(text, "net"):
		return "net"
	case strings.Contains(text, "block") || strings.Contains(text, "ext4") ||
		strings.Contains(text, "nvme") || strings.Contains(text, "io_"):
		return "block"
	default:
		return "other"
	}
}

func flamegraphConfidence(coverage float64) string {
	percent := normalizeFlamegraphPercent(coverage)
	switch {
	case percent >= 95:
		return "high"
	case percent >= 80:
		return "medium"
	case percent > 0:
		return "low"
	default:
		return "unknown"
	}
}

func normalizeFlamegraphPercent(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 0 && value <= 1 {
		return value * 100
	}
	if value > 100 {
		return 100
	}
	return value
}
