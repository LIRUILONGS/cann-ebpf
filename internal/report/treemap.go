package report

import (
	"fmt"
	"sort"
	"strings"

	"cann-ebpf/internal/device"
	"cann-ebpf/internal/runlog"
)

const (
	treemapSourceMsPTI = "mspti"
	treemapSourceHost  = "host"
	treemapOtherName   = "other"

	// treemapMaxTopNodes bounds the root-level treemap breadth so a run with
	// thousands of distinct operator names cannot bloat the report payload.
	// The tail is collapsed into one "other" node that keeps the summed
	// duration and count.
	treemapMaxTopNodes = 20
)

type treemapLeafAgg struct {
	name       string
	category   string
	kind       string
	deviceID   int
	streamID   int64
	durationNS int64
	count      int
}

type treemapGroupAgg struct {
	name       string
	category   string
	kind       string
	durationNS int64
	count      int
	children   []treemapLeafAgg
}

// BuildOperatorTreemap builds an operator Treemap from measured msPTI
// KERNEL/MEMCPY/HCCL device intervals. Only when there is no usable device
// evidence does it fall back to host function/category spans; that fallback is
// explicitly source=host and inferred because host span durations are not
// device operator execution time.
func BuildOperatorTreemap(spans []runlog.Span, devices []device.Span) TreemapData {
	leaves := collectTreemapDeviceLeaves(devices)
	if len(leaves) > 0 {
		return buildTreemapFromLeaves(leaves, true)
	}
	leaves = collectTreemapHostLeaves(spans)
	if len(leaves) > 0 {
		return buildTreemapFromLeaves(leaves, false)
	}
	return TreemapData{Status: ViewStatus{
		Available: false,
		Reason:    treemapUnavailableReason(len(spans), len(devices)),
		Source:    "none",
	}}
}

func buildTreemapFromLeaves(leaves []treemapLeafAgg, deviceMode bool) TreemapData {
	groups := groupTreemapLeaves(leaves)
	totalNS, totalCount := treemapTotals(leaves)
	groups = capTreemapGroups(groups)

	root := &TreemapNode{
		DurationMS: nanosToMillis(totalNS),
		Count:      totalCount,
		Children:   make([]TreemapNode, 0, len(groups)),
	}
	status := ViewStatus{Available: true}
	if deviceMode {
		root.Name = "Operator treemap"
		root.Category = "device"
		status.Source = treemapSourceMsPTI
		status.Confidence = "exact"
	} else {
		root.Name = "Host function aggregation (inferred)"
		root.Category = "host"
		status.Source = treemapSourceHost
		status.Confidence = "inferred"
		status.Reason = "no msPTI KERNEL/MEMCPY/HCCL device evidence; host span durations are not operator-level exact data"
	}
	for _, group := range groups {
		if deviceMode {
			root.Children = append(root.Children, treemapDeviceGroupNode(group))
		} else {
			root.Children = append(root.Children, treemapHostGroupNode(group))
		}
	}
	return TreemapData{Status: status, Root: root}
}

func collectTreemapDeviceLeaves(devices []device.Span) []treemapLeafAgg {
	type deviceKey struct {
		name     string
		kind     string
		deviceID int
		streamID int64
	}
	groups := make(map[deviceKey]*treemapLeafAgg)
	for _, span := range devices {
		kind := strings.ToUpper(strings.TrimSpace(span.Kind))
		switch kind {
		case device.KindKernel, device.KindMemcpy, device.KindHCCL:
		default:
			continue
		}
		durationNS := treemapDeviceDurationNS(span)
		if durationNS <= 0 {
			continue
		}
		name := strings.TrimSpace(span.Name)
		if name == "" {
			name = "(unknown)"
		}
		key := deviceKey{name: name, kind: kind, deviceID: span.DeviceID, streamID: span.StreamID}
		leaf := groups[key]
		if leaf == nil {
			leaf = &treemapLeafAgg{name: name, kind: kind, deviceID: span.DeviceID, streamID: span.StreamID}
			groups[key] = leaf
		}
		leaf.durationNS += durationNS
		leaf.count++
	}
	leaves := make([]treemapLeafAgg, 0, len(groups))
	for _, leaf := range groups {
		leaves = append(leaves, *leaf)
	}
	sortTreemapLeaves(leaves)
	return leaves
}

func collectTreemapHostLeaves(spans []runlog.Span) []treemapLeafAgg {
	type hostKey struct {
		function string
		category string
	}
	groups := make(map[hostKey]*treemapLeafAgg)
	for _, span := range spans {
		durationNS := treemapHostDurationNS(span)
		if durationNS <= 0 {
			continue
		}
		function := strings.TrimSpace(span.Function)
		if function == "" {
			function = "(unknown)"
		}
		category := strings.TrimSpace(span.Category)
		if category == "" {
			category = "other"
		}
		key := hostKey{function: function, category: category}
		leaf := groups[key]
		if leaf == nil {
			leaf = &treemapLeafAgg{name: function, category: category}
			groups[key] = leaf
		}
		leaf.durationNS += durationNS
		leaf.count++
	}
	leaves := make([]treemapLeafAgg, 0, len(groups))
	for _, leaf := range groups {
		leaves = append(leaves, *leaf)
	}
	sortTreemapLeaves(leaves)
	return leaves
}

func groupTreemapLeaves(leaves []treemapLeafAgg) []treemapGroupAgg {
	index := make(map[string]*treemapGroupAgg)
	for _, leaf := range leaves {
		group := index[leaf.name]
		if group == nil {
			group = &treemapGroupAgg{name: leaf.name}
			index[leaf.name] = group
		}
		group.durationNS += leaf.durationNS
		group.count += leaf.count
		group.children = append(group.children, leaf)
	}
	groups := make([]treemapGroupAgg, 0, len(index))
	for _, group := range index {
		sortTreemapLeaves(group.children)
		group.kind = uniformTreemapKind(group.children)
		group.category = uniformTreemapCategory(group.children)
		groups = append(groups, *group)
	}
	sort.SliceStable(groups, func(i, j int) bool {
		if groups[i].durationNS == groups[j].durationNS {
			return groups[i].name < groups[j].name
		}
		return groups[i].durationNS > groups[j].durationNS
	})
	return groups
}

func capTreemapGroups(groups []treemapGroupAgg) []treemapGroupAgg {
	if len(groups) <= treemapMaxTopNodes {
		return groups
	}
	keep := treemapMaxTopNodes - 1
	if keep < 0 {
		keep = 0
	}
	capped := make([]treemapGroupAgg, 0, treemapMaxTopNodes)
	capped = append(capped, groups[:keep]...)
	other := treemapGroupAgg{name: treemapOtherName, category: treemapOtherName}
	for _, group := range groups[keep:] {
		other.durationNS += group.durationNS
		other.count += group.count
	}
	return append(capped, other)
}

func treemapDeviceGroupNode(group treemapGroupAgg) TreemapNode {
	node := TreemapNode{
		Name:       group.name,
		Category:   group.kind,
		Kind:       group.kind,
		DurationMS: nanosToMillis(group.durationNS),
		Count:      group.count,
	}
	if len(group.children) == 0 {
		node.Category = treemapOtherName
		return node
	}
	children := make([]TreemapNode, 0, len(group.children))
	for _, leaf := range group.children {
		deviceLabel := fmt.Sprintf("NPU %d", leaf.deviceID)
		streamLabel := fmt.Sprintf("stream %d", leaf.streamID)
		children = append(children, TreemapNode{
			Name:       leaf.kind,
			Category:   leaf.kind,
			Kind:       leaf.kind,
			Device:     deviceLabel,
			Stream:     streamLabel,
			DurationMS: nanosToMillis(leaf.durationNS),
			Count:      leaf.count,
		})
	}
	node.Children = children
	return node
}

func treemapHostGroupNode(group treemapGroupAgg) TreemapNode {
	node := TreemapNode{
		Name:       group.name,
		Category:   group.category,
		DurationMS: nanosToMillis(group.durationNS),
		Count:      group.count,
	}
	if len(group.children) == 0 {
		node.Category = treemapOtherName
		return node
	}
	children := make([]TreemapNode, 0, len(group.children))
	for _, leaf := range group.children {
		children = append(children, TreemapNode{
			Name:       leaf.category,
			Category:   leaf.category,
			DurationMS: nanosToMillis(leaf.durationNS),
			Count:      leaf.count,
		})
	}
	node.Children = children
	return node
}

func sortTreemapLeaves(leaves []treemapLeafAgg) {
	sort.SliceStable(leaves, func(i, j int) bool {
		if leaves[i].durationNS != leaves[j].durationNS {
			return leaves[i].durationNS > leaves[j].durationNS
		}
		if leaves[i].kind != leaves[j].kind {
			return leaves[i].kind < leaves[j].kind
		}
		if leaves[i].deviceID != leaves[j].deviceID {
			return leaves[i].deviceID < leaves[j].deviceID
		}
		if leaves[i].streamID != leaves[j].streamID {
			return leaves[i].streamID < leaves[j].streamID
		}
		if leaves[i].category != leaves[j].category {
			return leaves[i].category < leaves[j].category
		}
		return leaves[i].name < leaves[j].name
	})
}

func uniformTreemapKind(leaves []treemapLeafAgg) string {
	if len(leaves) == 0 {
		return ""
	}
	kind := leaves[0].kind
	for _, leaf := range leaves[1:] {
		if leaf.kind != kind {
			return ""
		}
	}
	return kind
}

func uniformTreemapCategory(leaves []treemapLeafAgg) string {
	if len(leaves) == 0 {
		return ""
	}
	category := leaves[0].category
	for _, leaf := range leaves[1:] {
		if leaf.category != category {
			return ""
		}
	}
	return category
}

func treemapTotals(leaves []treemapLeafAgg) (int64, int) {
	var durationNS int64
	count := 0
	for _, leaf := range leaves {
		durationNS += leaf.durationNS
		count += leaf.count
	}
	return durationNS, count
}

func treemapDeviceDurationNS(span device.Span) int64 {
	if span.DurationNS > 0 {
		return span.DurationNS
	}
	if span.EndNS > span.StartNS {
		return span.EndNS - span.StartNS
	}
	return 0
}

func treemapHostDurationNS(span runlog.Span) int64 {
	if span.DurationNS > 0 {
		return span.DurationNS
	}
	startNS := span.StartNS
	if startNS <= 0 {
		startNS = span.TsNS
	}
	if span.EndNS > startNS {
		return span.EndNS - startNS
	}
	return 0
}

func nanosToMillis(ns int64) float64 {
	return float64(ns) / 1e6
}

func treemapUnavailableReason(hostSpans, deviceSpans int) string {
	switch {
	case hostSpans == 0 && deviceSpans == 0:
		return "no device or host span evidence"
	case deviceSpans == 0:
		return "no host spans with positive durations"
	case hostSpans == 0:
		return "no usable msPTI KERNEL/MEMCPY/HCCL device spans"
	default:
		return "no usable msPTI KERNEL/MEMCPY/HCCL device spans and no host spans with positive durations"
	}
}
