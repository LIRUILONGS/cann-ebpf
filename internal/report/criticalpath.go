package report

import (
	"fmt"
	"sort"
	"strings"

	"cann-ebpf/internal/device"
	"cann-ebpf/internal/runlog"
)

const (
	criticalPathSourceNone         = "none"
	criticalPathSourceSpans        = "spans"
	criticalPathSourceSpansDevice  = "spans+device"
	criticalPathConfidenceInferred = "inferred"
	criticalPathBatchFunction      = "vllm.scheduler.batch"
)

// criticalPathGroup is one batch/request/trace partition of the host spans.
// A single report may contain more than one batch, so the builder picks the
// group with the strongest device correlation evidence.
type criticalPathGroup struct {
	key     string
	indices []int
	matched int
}

// criticalPathWorkNode keeps the public node plus the raw nanosecond bounds and
// lane keys needed to infer a timing DAG. The public struct intentionally stays
// frozen by the M0 contract.
type criticalPathWorkNode struct {
	CriticalPathNode
	startNS  int64
	endNS    int64
	hostLane string
	devLane  string
}

// BuildCriticalPath derives a batch-level critical-path DAG from host spans and
// the device intervals correlated to them.
//
// runlog.Span has no stream/event dependency edges. The DAG therefore uses
// parent/child links from trace context (when present) and conservative
// same-lane ordering inferred from non-overlapping intervals. Every available
// result is consequently marked confidence=inferred and explains that reason in
// status.reason. The builder never claims an exact critical path.
func BuildCriticalPath(spans []runlog.Span, devices []device.Span) CriticalPathData {
	data := CriticalPathData{
		Status:       ViewStatus{Available: false, Source: criticalPathSourceNone},
		Nodes:        []CriticalPathNode{},
		CriticalPath: []string{},
	}
	if len(spans) == 0 {
		data.Status.Reason = "no host spans"
		return data
	}

	group := selectCriticalPathGroup(spans, devices)
	if group == nil {
		data.Status.Source = criticalPathSourceSpans
		data.Status.Reason = "no batch/request/trace context"
		return data
	}

	selected := make([]runlog.Span, 0, len(group.indices))
	for _, index := range group.indices {
		selected = append(selected, spans[index])
	}
	data.BatchID, data.TraceID, data.RequestID = criticalPathGroupContext(selected)
	data.Status.Source = criticalPathSourceSpans

	if len(devices) == 0 {
		data.Status.Reason = "no device correlation: device spans are absent"
		return data
	}

	joined := device.Join(selected, devices, device.Options{})
	batchStartNS, batchEndNS := criticalPathWindow(selected)
	work, hostNodeIDs := buildCriticalPathHostNodes(selected, batchStartNS)
	work, matchedHosts := attachCriticalPathDeviceNodes(work, hostNodeIDs, selected, joined)
	if matchedHosts == 0 {
		data.Status.Source = criticalPathSourceSpansDevice
		data.Status.Reason = "no device correlation: no host span matched a device interval"
		return data
	}

	linkCriticalPathParents(work, hostNodeIDs, selected)
	sort.SliceStable(work, func(i, j int) bool {
		if work[i].StartMS == work[j].StartMS {
			return work[i].ID < work[j].ID
		}
		return work[i].StartMS < work[j].StartMS
	})

	path, totalMS := computeCriticalPathChain(work, batchStartNS, batchEndNS)
	if totalMS <= 0 {
		data.Status.Source = criticalPathSourceSpansDevice
		data.Status.Reason = "batch window is empty"
		return data
	}

	data.Nodes = make([]CriticalPathNode, len(work))
	for i := range work {
		data.Nodes[i] = work[i].CriticalPathNode
	}
	data.CriticalPath = path
	data.TotalMS = totalMS
	data.Coverage = criticalPathCoverage(joined, matchedHosts, len(selected))
	data.Status = ViewStatus{
		Available:  true,
		Reason:     criticalPathInferredReason(joined),
		Source:     criticalPathSourceSpansDevice,
		Confidence: criticalPathConfidenceInferred,
	}
	return data
}

// BuildSummedDelay compares the sum of leaf work durations with the critical
// path duration. Container spans are excluded from the sum: their wall time is
// represented by their child device intervals, which avoids double counting the
// same work in both the host span and its kernels.
func BuildSummedDelay(cp CriticalPathData) SummedDelayData {
	data := SummedDelayData{
		Status:          ViewStatus{Available: false, Source: criticalPathSourceNone},
		NodesByCategory: []CategoryDelay{},
	}
	if !cp.Status.Available {
		data.Status.Reason = "critical path unavailable"
		if cp.Status.Reason != "" {
			data.Status.Reason += ": " + cp.Status.Reason
		}
		return data
	}

	leaves := criticalPathLeafNodes(cp)
	if len(leaves) == 0 {
		data.Status.Source = cp.Status.Source
		data.Status.Reason = "critical path has no leaf work nodes"
		return data
	}

	categoryTotals := map[string]*CategoryDelay{}
	for _, node := range leaves {
		category := criticalPathNodeCategory(node)
		entry := categoryTotals[category]
		if entry == nil {
			entry = &CategoryDelay{Category: category}
			categoryTotals[category] = entry
		}
		entry.TotalMS += node.DurationMS
		entry.Count++
		data.SumMS += node.DurationMS
	}
	for _, entry := range categoryTotals {
		data.NodesByCategory = append(data.NodesByCategory, *entry)
	}
	sort.Slice(data.NodesByCategory, func(i, j int) bool {
		if data.NodesByCategory[i].TotalMS == data.NodesByCategory[j].TotalMS {
			return data.NodesByCategory[i].Category < data.NodesByCategory[j].Category
		}
		return data.NodesByCategory[i].TotalMS > data.NodesByCategory[j].TotalMS
	})

	data.CriticalPathMS = cp.TotalMS
	if data.CriticalPathMS <= 0 {
		byID := make(map[string]CriticalPathNode, len(cp.Nodes))
		for _, node := range cp.Nodes {
			byID[node.ID] = node
		}
		for _, id := range cp.CriticalPath {
			if node, ok := byID[id]; ok {
				data.CriticalPathMS += node.DurationMS
			}
		}
	}
	data.OverlapMS = data.SumMS - data.CriticalPathMS
	if data.OverlapMS < 0 {
		data.OverlapMS = 0
	}
	if data.CriticalPathMS > 0 {
		data.Parallelism = data.SumMS / data.CriticalPathMS
	}

	data.Status = ViewStatus{
		Available:  true,
		Reason:     cp.Status.Reason,
		Source:     cp.Status.Source,
		Confidence: cp.Status.Confidence,
	}
	if data.Status.Source == "" {
		data.Status.Source = criticalPathSourceNone
	}
	if data.Status.Confidence == "" {
		data.Status.Confidence = criticalPathConfidenceInferred
	}
	return data
}

func criticalPathLeafNodes(cp CriticalPathData) []CriticalPathNode {
	byID := make(map[string]struct{}, len(cp.Nodes))
	for _, node := range cp.Nodes {
		byID[node.ID] = struct{}{}
	}
	parentIDs := make(map[string]struct{}, len(cp.Nodes))
	for _, node := range cp.Nodes {
		if node.Parent == "" {
			continue
		}
		if _, ok := byID[node.Parent]; ok {
			parentIDs[node.Parent] = struct{}{}
		}
	}
	leaves := make([]CriticalPathNode, 0, len(cp.Nodes))
	for _, node := range cp.Nodes {
		if _, isParent := parentIDs[node.ID]; isParent {
			continue
		}
		leaves = append(leaves, node)
	}
	return leaves
}

func criticalPathNodeCategory(node CriticalPathNode) string {
	if category := strings.TrimSpace(node.Category); category != "" {
		return category
	}
	if kind := strings.TrimSpace(node.Kind); kind != "" {
		return kind
	}
	return "other"
}

func criticalPathWindow(spans []runlog.Span) (int64, int64) {
	start := spanStart(spans[0])
	end := spanEnd(spans[0])
	for _, span := range spans[1:] {
		if spanStart(span) < start {
			start = spanStart(span)
		}
		if spanEnd(span) > end {
			end = spanEnd(span)
		}
	}
	if end < start {
		end = start
	}
	return start, end
}

func buildCriticalPathHostNodes(spans []runlog.Span, batchStartNS int64) ([]criticalPathWorkNode, []string) {
	work := make([]criticalPathWorkNode, 0, len(spans))
	hostNodeIDs := make([]string, len(spans))
	usedIDs := map[string]struct{}{}
	for i, span := range spans {
		startNS := spanStart(span)
		endNS := spanEnd(span)
		if endNS < startNS {
			endNS = startNS
		}
		id := uniqueCriticalPathNodeID("host:"+criticalPathSpanID(span, i), usedIDs)
		hostNodeIDs[i] = id
		work = append(work, criticalPathWorkNode{
			CriticalPathNode: CriticalPathNode{
				ID:         id,
				Label:      criticalPathHostLabel(span),
				Category:   criticalPathCategory(span.Category),
				Kind:       criticalPathHostKind(span),
				StartMS:    criticalPathMilliseconds(startNS, batchStartNS),
				DurationMS: float64(endNS-startNS) / 1e6,
				Device:     criticalPathHostDevice(span),
				Confidence: criticalPathConfidenceInferred,
			},
			startNS:  startNS,
			endNS:    endNS,
			hostLane: rowKey(span.PID, span.TID),
		})
	}
	return work, hostNodeIDs
}

func attachCriticalPathDeviceNodes(work []criticalPathWorkNode, hostNodeIDs []string, spans []runlog.Span, joined device.Report) ([]criticalPathWorkNode, int) {
	matchedHosts := 0
	seen := map[string]struct{}{}
	usedIDs := map[string]struct{}{}
	batchStartNS := spansBatchStart(spans)
	for i := range spans {
		if i >= len(work) {
			break
		}
		var result device.HostResult
		if i < len(joined.Hosts) {
			result = joined.Hosts[i]
		}
		added := 0
		for _, match := range result.Matches {
			if match.Kind == device.KindAPI {
				continue
			}
			startNS, endNS := match.StartNS, match.EndNS
			if endNS <= startNS && match.DurationNS > 0 {
				endNS = startNS + match.DurationNS
			}
			if startNS < work[i].startNS {
				startNS = work[i].startNS
			}
			if endNS > work[i].endNS {
				endNS = work[i].endNS
			}
			if endNS <= startNS {
				continue
			}
			key := fmt.Sprintf("%d|%d|%s|%s|%d|%d|%d",
				match.DeviceID, match.StreamID, match.Kind, match.Name, match.CorrelationID, startNS, endNS)
			if _, duplicate := seen[key]; duplicate {
				continue
			}
			seen[key] = struct{}{}
			kind := strings.TrimSpace(match.Kind)
			if kind == "" {
				kind = "device"
			}
			label := strings.TrimSpace(match.Name)
			if label == "" {
				label = kind
			}
			id := uniqueCriticalPathNodeID(fmt.Sprintf("dev:%d:%d:%s:%d:%d",
				match.DeviceID, match.StreamID, kind, match.CorrelationID, startNS), usedIDs)
			work = append(work, criticalPathWorkNode{
				CriticalPathNode: CriticalPathNode{
					ID:         id,
					Parent:     hostNodeIDs[i],
					Label:      label,
					Category:   strings.ToLower(kind),
					Kind:       kind,
					StartMS:    criticalPathMilliseconds(startNS, batchStartNS),
					DurationMS: float64(endNS-startNS) / 1e6,
					Device:     fmt.Sprintf("NPU-%d", match.DeviceID),
					Confidence: criticalPathConfidenceInferred,
				},
				startNS:  startNS,
				endNS:    endNS,
				hostLane: work[i].hostLane,
				devLane:  deviceRowKey(match.DeviceID, match.StreamID),
			})
			added++
		}
		if result.Matched && added > 0 {
			matchedHosts++
		}
	}
	return work, matchedHosts
}

func spansBatchStart(spans []runlog.Span) int64 {
	start := spanStart(spans[0])
	for _, span := range spans[1:] {
		if spanStart(span) < start {
			start = spanStart(span)
		}
	}
	return start
}

func linkCriticalPathParents(work []criticalPathWorkNode, hostNodeIDs []string, spans []runlog.Span) {
	traceIndex := map[string]int{}
	for i, span := range spans {
		if span.Trace == nil {
			continue
		}
		traceID := strings.ToLower(strings.TrimSpace(span.Trace.TraceID))
		spanID := strings.ToLower(strings.TrimSpace(span.Trace.SpanID))
		if traceID != "" && spanID != "" {
			traceIndex[traceID+"|"+spanID] = i
		}
	}
	batchIndex := -1
	for i, span := range spans {
		if isCriticalPathBatchSpan(span) {
			batchIndex = i
			break
		}
	}
	for i, span := range spans {
		if i >= len(work) {
			break
		}
		parent := ""
		if span.Trace != nil {
			traceID := strings.ToLower(strings.TrimSpace(span.Trace.TraceID))
			parentSpanID := strings.ToLower(strings.TrimSpace(span.Trace.ParentSpanID))
			if traceID != "" && parentSpanID != "" {
				if parentIndex, ok := traceIndex[traceID+"|"+parentSpanID]; ok && parentIndex != i {
					parent = hostNodeIDs[parentIndex]
				}
			}
		}
		if parent == "" && batchIndex >= 0 && batchIndex != i {
			parent = hostNodeIDs[batchIndex]
		}
		work[i].CriticalPathNode.Parent = parent
	}
}

func computeCriticalPathChain(work []criticalPathWorkNode, batchStartNS, batchEndNS int64) ([]string, float64) {
	if len(work) == 0 {
		return []string{}, 0
	}
	nodeIndex := make(map[string]int, len(work))
	for i := range work {
		nodeIndex[work[i].ID] = i
	}

	isParent := make([]bool, len(work))
	for i := range work {
		if work[i].Parent == "" {
			continue
		}
		if parentIndex, ok := nodeIndex[work[i].Parent]; ok {
			isParent[parentIndex] = true
		}
	}
	leaves := make([]int, 0, len(work))
	for i := range work {
		if !isParent[i] {
			leaves = append(leaves, i)
		}
	}
	if len(leaves) == 0 {
		for i := range work {
			leaves = append(leaves, i)
		}
	}

	predecessors := map[int][]int{}
	edges := map[[2]int]struct{}{}
	addLaneEdges := func(lanes map[string][]int) {
		for _, indices := range lanes {
			sort.SliceStable(indices, func(i, j int) bool {
				if work[indices[i]].startNS == work[indices[j]].startNS {
					return work[indices[i]].ID < work[indices[j]].ID
				}
				return work[indices[i]].startNS < work[indices[j]].startNS
			})
			for i := 1; i < len(indices); i++ {
				previous, current := indices[i-1], indices[i]
				if work[previous].endNS > work[current].startNS {
					continue
				}
				edge := [2]int{previous, current}
				if _, exists := edges[edge]; exists {
					continue
				}
				edges[edge] = struct{}{}
				predecessors[current] = append(predecessors[current], previous)
			}
		}
	}
	hostLanes := map[string][]int{}
	devLanes := map[string][]int{}
	for _, index := range leaves {
		if work[index].hostLane != "" {
			hostLanes[work[index].hostLane] = append(hostLanes[work[index].hostLane], index)
		}
		if work[index].devLane != "" {
			devLanes[work[index].devLane] = append(devLanes[work[index].devLane], index)
		}
	}
	addLaneEdges(hostLanes)
	addLaneEdges(devLanes)

	order := append([]int(nil), leaves...)
	sort.SliceStable(order, func(i, j int) bool {
		if work[order[i]].startNS == work[order[j]].startNS {
			return work[order[i]].ID < work[order[j]].ID
		}
		return work[order[i]].startNS < work[order[j]].startNS
	})
	durationNS := func(index int) int64 {
		if work[index].endNS <= work[index].startNS {
			return 0
		}
		return work[index].endNS - work[index].startNS
	}
	pathStart := map[int]int64{}
	pathSum := map[int]int64{}
	bestPredecessor := map[int]int{}
	for _, current := range order {
		pathStart[current] = work[current].startNS
		pathSum[current] = durationNS(current)
		for _, previous := range predecessors[current] {
			previousStart, ok := pathStart[previous]
			if !ok {
				continue
			}
			candidateSum := pathSum[previous] + durationNS(current)
			if previousStart < pathStart[current] ||
				(previousStart == pathStart[current] && candidateSum > pathSum[current]) {
				pathStart[current] = previousStart
				pathSum[current] = candidateSum
				bestPredecessor[current] = previous
			}
		}
	}

	best := leaves[0]
	for _, candidate := range leaves[1:] {
		if work[candidate].endNS > work[best].endNS {
			best = candidate
			continue
		}
		if work[candidate].endNS < work[best].endNS {
			continue
		}
		if pathStart[candidate] < pathStart[best] {
			best = candidate
			continue
		}
		if pathStart[candidate] == pathStart[best] && pathSum[candidate] > pathSum[best] {
			best = candidate
			continue
		}
		if pathStart[candidate] == pathStart[best] && pathSum[candidate] == pathSum[best] && work[candidate].ID < work[best].ID {
			best = candidate
		}
	}

	reversed := make([]string, 0, len(work))
	for current := best; ; {
		reversed = append(reversed, work[current].ID)
		previous, ok := bestPredecessor[current]
		if !ok {
			break
		}
		current = previous
	}
	path := make([]string, 0, len(reversed))
	for i := len(reversed) - 1; i >= 0; i-- {
		path = append(path, reversed[i])
	}

	startNS, endNS := work[0].startNS, work[0].endNS
	for _, node := range work[1:] {
		if node.startNS < startNS {
			startNS = node.startNS
		}
		if node.endNS > endNS {
			endNS = node.endNS
		}
	}
	if startNS < batchStartNS {
		startNS = batchStartNS
	}
	if endNS < batchEndNS {
		endNS = batchEndNS
	}
	if endNS < startNS {
		endNS = startNS
	}
	return path, float64(endNS-startNS) / 1e6
}

func selectCriticalPathGroup(spans []runlog.Span, devices []device.Span) *criticalPathGroup {
	groupsByKey := map[string]*criticalPathGroup{}
	for i, span := range spans {
		key := criticalPathSpanGroupKey(span)
		if key == "" {
			continue
		}
		group := groupsByKey[key]
		if group == nil {
			group = &criticalPathGroup{key: key}
			groupsByKey[key] = group
		}
		group.indices = append(group.indices, i)
	}
	if len(groupsByKey) == 0 {
		return nil
	}
	groups := make([]*criticalPathGroup, 0, len(groupsByKey))
	for _, group := range groupsByKey {
		groups = append(groups, group)
	}
	if len(groups) > 1 && len(devices) > 0 {
		matched := criticalPathHostMatchFlags(spans, devices)
		for _, group := range groups {
			group.matched = 0
			for _, index := range group.indices {
				if matched[index] {
					group.matched++
				}
			}
		}
	}
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].matched != groups[j].matched {
			return groups[i].matched > groups[j].matched
		}
		if len(groups[i].indices) != len(groups[j].indices) {
			return len(groups[i].indices) > len(groups[j].indices)
		}
		left := spanStart(spans[groups[i].indices[0]])
		right := spanStart(spans[groups[j].indices[0]])
		if left != right {
			return left < right
		}
		return groups[i].key < groups[j].key
	})
	return groups[0]
}

func criticalPathHostMatchFlags(spans []runlog.Span, devices []device.Span) []bool {
	flags := make([]bool, len(spans))
	if len(devices) == 0 {
		return flags
	}
	joined := device.Join(spans, devices, device.Options{})
	for i := range joined.Hosts {
		if i >= len(flags) {
			break
		}
		flags[i] = joined.Hosts[i].Matched && len(joined.Hosts[i].Matches) > 0
	}
	return flags
}

func criticalPathSpanGroupKey(span runlog.Span) string {
	if batchID := strings.TrimSpace(span.BatchID); batchID != "" {
		return "batch\x00" + batchID
	}
	if span.Trace != nil {
		if traceID := strings.ToLower(strings.TrimSpace(span.Trace.TraceID)); traceID != "" {
			return "trace\x00" + traceID
		}
	}
	if requestIDs := sortedCriticalPathRequestIDs(span.RequestIDs); len(requestIDs) > 0 {
		return "request\x00" + requestIDs[0]
	}
	return ""
}

func criticalPathGroupContext(spans []runlog.Span) (batchID, traceID, requestID string) {
	batchIDs := map[string]struct{}{}
	traceIDs := map[string]struct{}{}
	requestIDs := map[string]struct{}{}
	for _, span := range spans {
		if value := strings.TrimSpace(span.BatchID); value != "" {
			batchIDs[value] = struct{}{}
		}
		if span.Trace != nil {
			if value := strings.ToLower(strings.TrimSpace(span.Trace.TraceID)); value != "" {
				traceIDs[value] = struct{}{}
			}
		}
		for _, value := range span.RequestIDs {
			if value = strings.TrimSpace(value); value != "" {
				requestIDs[value] = struct{}{}
			}
		}
	}
	if len(batchIDs) == 1 {
		batchID = firstCriticalPathValue(batchIDs)
	}
	if len(traceIDs) == 1 {
		traceID = firstCriticalPathValue(traceIDs)
	}
	if len(requestIDs) == 1 {
		requestID = firstCriticalPathValue(requestIDs)
	}
	return batchID, traceID, requestID
}

func sortedCriticalPathRequestIDs(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func firstCriticalPathValue(values map[string]struct{}) string {
	ordered := make([]string, 0, len(values))
	for value := range values {
		ordered = append(ordered, value)
	}
	sort.Strings(ordered)
	return ordered[0]
}

func criticalPathCoverage(joined device.Report, matchedHosts, selectedHosts int) float64 {
	if joined.MatchableHostSpans > 0 {
		coverage := float64(joined.MatchedMatchableHostSpans) / float64(joined.MatchableHostSpans)
		if coverage > 1 {
			return 1
		}
		if coverage < 0 {
			return 0
		}
		return coverage
	}
	if matchedHosts > 0 && selectedHosts > 0 {
		return float64(matchedHosts) / float64(selectedHosts)
	}
	return 0
}

func criticalPathInferredReason(joined device.Report) string {
	parts := []string{"dependency edges inferred from host span and device interval timing; stream/event dependency edges are unavailable"}
	if joined.TimeWindowFallback {
		parts = append(parts, "host/device correlation used time-window fallback")
	}
	if joined.MatchableHostSpans > 0 && joined.MatchedMatchableHostSpans < joined.MatchableHostSpans {
		parts = append(parts, fmt.Sprintf("device correlation covers %d of %d matchable host spans",
			joined.MatchedMatchableHostSpans, joined.MatchableHostSpans))
	}
	return strings.Join(parts, "; ")
}

func criticalPathSpanID(span runlog.Span, index int) string {
	if value := strings.TrimSpace(span.SpanID); value != "" {
		return value
	}
	return fmt.Sprintf("%d-%d-%d-%d", span.PID, span.TID, spanStart(span), index)
}

func criticalPathHostLabel(span runlog.Span) string {
	if value := strings.TrimSpace(span.Function); value != "" {
		return value
	}
	if value := strings.TrimSpace(span.APIName); value != "" {
		return value
	}
	return "(unknown)"
}

func criticalPathCategory(value string) string {
	if value = strings.TrimSpace(value); value != "" {
		return value
	}
	return "other"
}

func criticalPathHostKind(span runlog.Span) string {
	if isCriticalPathBatchSpan(span) {
		return "batch"
	}
	return "host"
}

func isCriticalPathBatchSpan(span runlog.Span) bool {
	return strings.EqualFold(strings.TrimSpace(span.Category), "batch") ||
		strings.EqualFold(strings.TrimSpace(span.Function), criticalPathBatchFunction)
}

func criticalPathHostDevice(span runlog.Span) string {
	if span.DevID > 0 {
		return fmt.Sprintf("NPU-%d", span.DevID)
	}
	return ""
}

func criticalPathMilliseconds(valueNS, originNS int64) float64 {
	return float64(valueNS-originNS) / 1e6
}

func uniqueCriticalPathNodeID(base string, used map[string]struct{}) string {
	if base == "" {
		base = "node"
	}
	candidate := base
	for suffix := 2; ; suffix++ {
		if _, exists := used[candidate]; !exists {
			used[candidate] = struct{}{}
			return candidate
		}
		candidate = fmt.Sprintf("%s#%d", base, suffix)
	}
}
