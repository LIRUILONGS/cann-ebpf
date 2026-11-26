package device

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"cann-ebpf/internal/runlog"
)

const JoinReportSchemaV1 = "cann.device.join.v1"

const (
	ReasonOK              = "ok"
	ReasonNoAPIRecord     = "no_api_record"
	ReasonAPINameMismatch = "api_name_mismatch"
	ReasonDuplicate       = "duplicate"
	ReasonNoDeviceRecord  = "no_device_record"
	ReasonTimeWindow      = "time_window"
)

type Options struct {
	TimeToleranceNS int64
	SlowThresholdNS int64
	BusyThreshold   float64
	IdleThreshold   float64
	CopyThreshold   float64
	CommThreshold   float64
	// PIDMap/TIDMap translate namespace-local collector ids to the host ids
	// recorded by eBPF. They are only applied to API records.
	PIDMap map[int]int
	TIDMap map[int]int
}

func (o *Options) normalize() {
	if o.TimeToleranceNS <= 0 {
		o.TimeToleranceNS = 100_000 // 100µs
	}
	if o.SlowThresholdNS <= 0 {
		o.SlowThresholdNS = int64(time.Millisecond)
	}
	if o.BusyThreshold <= 0 {
		o.BusyThreshold = 0.8
	}
	if o.IdleThreshold <= 0 {
		o.IdleThreshold = 0.2
	}
	if o.CopyThreshold <= 0 {
		o.CopyThreshold = 0.5
	}
	if o.CommThreshold <= 0 {
		o.CommThreshold = 0.5
	}
}

type Match struct {
	Kind          string `json:"kind"`
	Name          string `json:"name,omitempty"`
	DeviceID      int    `json:"device_id"`
	StreamID      int64  `json:"stream_id,omitempty"`
	StartNS       int64  `json:"start_ns"`
	EndNS         int64  `json:"end_ns"`
	DurationNS    int64  `json:"duration_ns"`
	OverlapNS     int64  `json:"overlap_ns"`
	GapNS         int64  `json:"gap_ns,omitempty"`
	Adjacent      bool   `json:"adjacent,omitempty"`
	CorrelationID int64  `json:"correlation_id,omitempty"`
}

type HostResult struct {
	SpanID        string   `json:"span_id"`
	Function      string   `json:"function"`
	PID           int      `json:"pid"`
	TID           int      `json:"tid"`
	StartNS       int64    `json:"start_ns"`
	EndNS         int64    `json:"end_ns"`
	DurationNS    int64    `json:"duration_ns"`
	Matched       bool     `json:"matched"`
	Reason        string   `json:"reason"`
	CorrelationID int64    `json:"correlation_id,omitempty"`
	Matches       []Match  `json:"matches,omitempty"`
	DeviceBusyNS  int64    `json:"device_busy_ns"`
	BusyPct       float64  `json:"busy_pct"`
	KernelNS      int64    `json:"kernel_ns"`
	MemcpyNS      int64    `json:"memcpy_ns"`
	HCCLNS        int64    `json:"hccl_ns"`
	Verdicts      []string `json:"verdicts,omitempty"`
}

type ReasonCount struct {
	Reason string `json:"reason"`
	Count  int64  `json:"count"`
}

type DeviceSummary struct {
	DeviceID int   `json:"device_id"`
	Spans    int64 `json:"spans"`
	BusyNS   int64 `json:"busy_ns"`
	KernelNS int64 `json:"kernel_ns"`
	MemcpyNS int64 `json:"memcpy_ns"`
	HCCLNS   int64 `json:"hccl_ns"`
}

type VerdictCount struct {
	Verdict string `json:"verdict"`
	Count   int64  `json:"count"`
}

type Report struct {
	Schema             string          `json:"schema"`
	HostSpans          int             `json:"host_spans"`
	DeviceSpans        int             `json:"device_spans"`
	DeviceKinds        string          `json:"device_kinds"`
	PIDMapApplied      bool            `json:"pid_map_applied,omitempty"`
	PIDMapEntries      int             `json:"pid_map_entries,omitempty"`
	MatchedHostSpans   int             `json:"matched_host_spans"`
	MatchRate          float64         `json:"match_rate"`
	SlowSyncSpans      int             `json:"slow_sync_spans,omitempty"`
	MatchedSlowSync    int             `json:"matched_slow_sync_spans,omitempty"`
	SlowSyncMatchRate  float64         `json:"slow_sync_match_rate,omitempty"`
	AdjacentMatches    int             `json:"adjacent_matches,omitempty"`
	TimeWindowFallback bool            `json:"time_window_fallback"`
	ClockOffsetNS      int64           `json:"clock_offset_ns"`
	Reasons            []ReasonCount   `json:"reasons"`
	Devices            []DeviceSummary `json:"devices"`
	Verdicts           []VerdictCount  `json:"verdicts"`
	Hosts              []HostResult    `json:"hosts,omitempty"`
	Warnings           []string        `json:"warnings,omitempty"`
}

// Join maps host spans to device intervals. It first matches an API record by
// (pid,tid) and start time, then uses correlationId to pull kernel/memcpy/HCCL.
// When no API records exist at all it falls back to a time-window overlap and
// says so explicitly.
func Join(host []runlog.Span, dev []Span, opts Options) Report {
	opts.normalize()
	if len(opts.PIDMap) > 0 || len(opts.TIDMap) > 0 {
		dev = TranslateIDs(dev, opts.PIDMap, opts.TIDMap)
	}
	report := Report{
		Schema:      JoinReportSchemaV1,
		HostSpans:   len(host),
		DeviceSpans: len(dev),
		DeviceKinds: summaryString(Summary(dev)),
		Reasons:     []ReasonCount{},
		Devices:     []DeviceSummary{},
		Verdicts:    []VerdictCount{},
		Hosts:       []HostResult{},
	}
	if len(opts.PIDMap) > 0 || len(opts.TIDMap) > 0 {
		report.PIDMapApplied = true
		report.PIDMapEntries = len(opts.PIDMap) + len(opts.TIDMap)
	}
	if len(dev) == 0 {
		report.Warnings = append(report.Warnings, "没有 device span，无法做设备侧 join")
		return report
	}

	apiByThread := map[[2]int][]Span{}
	deviceByCorr := map[int64][]Span{}
	var allDevice []Span
	for _, span := range dev {
		if span.Kind == KindAPI {
			key := [2]int{span.PID, span.TID}
			apiByThread[key] = append(apiByThread[key], span)
			continue
		}
		allDevice = append(allDevice, span)
		if span.CorrelationID != 0 {
			deviceByCorr[span.CorrelationID] = append(deviceByCorr[span.CorrelationID], span)
		}
	}
	report.TimeWindowFallback = len(apiByThread) == 0
	if report.TimeWindowFallback {
		report.Warnings = append(report.Warnings, "device.jsonl 没有 API record：退化为时间窗重叠匹配，置信度下降")
	}

	offset := estimateClockOffset(host, apiByThread, opts.TimeToleranceNS)
	report.ClockOffsetNS = offset

	reasons := map[string]int64{}
	devices := map[int]*DeviceSummary{}
	deviceIntervals := map[int][][2]int64{}
	verdicts := map[string]int64{}
	matched := 0
	slowSyncSpans := 0
	matchedSlowSync := 0
	adjacentMatches := 0

	for _, h := range host {
		result := joinOne(h, apiByThread, deviceByCorr, allDevice, offset, opts)
		if result.Matched {
			matched++
		}
		if isSyncFunction(h.Function) && h.DurationNS >= opts.SlowThresholdNS {
			slowSyncSpans++
			if result.Matched {
				matchedSlowSync++
			}
		}
		reasons[result.Reason]++
		for _, match := range result.Matches {
			if match.Adjacent {
				adjacentMatches++
			}
			summary := devices[match.DeviceID]
			if summary == nil {
				summary = &DeviceSummary{DeviceID: match.DeviceID}
				devices[match.DeviceID] = summary
			}
			summary.Spans++
			switch match.Kind {
			case KindKernel:
				summary.KernelNS += match.OverlapNS
			case KindMemcpy:
				summary.MemcpyNS += match.OverlapNS
			case KindHCCL:
				summary.HCCLNS += match.OverlapNS
			}
			deviceIntervals[match.DeviceID] = append(deviceIntervals[match.DeviceID], [2]int64{match.StartNS, match.EndNS})
		}
		if result.Matched && h.DurationNS >= opts.SlowThresholdNS {
			if result.BusyPct >= opts.BusyThreshold {
				result.Verdicts = append(result.Verdicts, "device_bound")
			} else if result.BusyPct <= opts.IdleThreshold {
				result.Verdicts = append(result.Verdicts, "launch_starvation")
			}
			if pct(result.MemcpyNS, h.DurationNS) >= opts.CopyThreshold {
				result.Verdicts = append(result.Verdicts, "copy_bound")
			}
			if pct(result.HCCLNS, h.DurationNS) >= opts.CommThreshold {
				result.Verdicts = append(result.Verdicts, "communication_bound")
			}
			for _, verdict := range result.Verdicts {
				verdicts[verdict]++
			}
		}
		report.Hosts = append(report.Hosts, result)
	}

	report.MatchedHostSpans = matched
	report.AdjacentMatches = adjacentMatches
	if len(host) > 0 {
		report.MatchRate = float64(matched) / float64(len(host))
	}
	if slowSyncSpans > 0 {
		report.SlowSyncSpans = slowSyncSpans
		report.MatchedSlowSync = matchedSlowSync
		report.SlowSyncMatchRate = float64(matchedSlowSync) / float64(slowSyncSpans)
		if report.SlowSyncMatchRate < 0.95 {
			report.Warnings = append(report.Warnings, fmt.Sprintf("慢 Sync 匹配率 %.1f%% 低于 95%%", report.SlowSyncMatchRate*100))
		}
	}
	if adjacentMatches > 0 {
		report.Warnings = append(report.Warnings, fmt.Sprintf(
			"%d 个设备区间与 host span 不重叠，但在 ±%dns 容差内按相邻区间计入关联；这些区间不贡献 busy 时间",
			adjacentMatches, opts.TimeToleranceNS))
	}
	for reason, count := range reasons {
		report.Reasons = append(report.Reasons, ReasonCount{Reason: reason, Count: count})
	}
	sort.Slice(report.Reasons, func(i, j int) bool {
		if report.Reasons[i].Count == report.Reasons[j].Count {
			return report.Reasons[i].Reason < report.Reasons[j].Reason
		}
		return report.Reasons[i].Count > report.Reasons[j].Count
	})
	for id, summary := range devices {
		summary.BusyNS = unionDuration(deviceIntervals[id])
		report.Devices = append(report.Devices, *summary)
	}
	sort.Slice(report.Devices, func(i, j int) bool { return report.Devices[i].DeviceID < report.Devices[j].DeviceID })
	for verdict, count := range verdicts {
		report.Verdicts = append(report.Verdicts, VerdictCount{Verdict: verdict, Count: count})
	}
	sort.Slice(report.Verdicts, func(i, j int) bool { return report.Verdicts[i].Verdict < report.Verdicts[j].Verdict })
	if report.MatchRate < 0.5 {
		report.Warnings = append(report.Warnings, fmt.Sprintf("设备侧匹配率 %.1f%% 偏低，结论只能作为线索", report.MatchRate*100))
	}
	return report
}

func isSyncFunction(name string) bool {
	return strings.HasPrefix(name, "aclrtSynchronizeStream") ||
		strings.HasPrefix(name, "aclrtSynchronizeEvent")
}

func joinOne(h runlog.Span, apiByThread map[[2]int][]Span, deviceByCorr map[int64][]Span, allDevice []Span, offset int64, opts Options) HostResult {
	start := hostStart(h)
	end := hostEnd(h)
	result := HostResult{
		SpanID: h.SpanID, Function: h.Function, PID: h.PID, TID: h.TID,
		StartNS: start, EndNS: end, DurationNS: h.DurationNS, Reason: ReasonNoAPIRecord,
	}

	if len(apiByThread) == 0 {
		// Time-window fallback: any device interval overlapping the host span.
		for _, span := range allDevice {
			if span.PID != 0 && h.PID != 0 && span.PID != h.PID {
				continue
			}
			dStart, dEnd := span.StartNS-offset, span.EndNS-offset
			overlap := overlapNS(start, end, dStart, dEnd)
			if overlap <= 0 {
				continue
			}
			match := toMatch(span, overlap)
			match.StartNS, match.EndNS = dStart, dEnd
			result.Matches = append(result.Matches, match)
		}
		if len(result.Matches) > 0 {
			result.Matched = true
			result.Reason = ReasonTimeWindow
			result.CorrelationID = result.Matches[0].CorrelationID
			fillHostMetrics(&result, start, end)
		}
		return result
	}

	candidates := apiByThread[[2]int{h.PID, h.TID}]
	if len(candidates) == 0 {
		return result
	}
	best, bestDiff, secondDiff, nameMismatch := pickAPI(candidates, start, offset, h.Function, opts.TimeToleranceNS)
	if best == nil {
		result.Reason = ReasonNoAPIRecord
		return result
	}
	result.Matched = true
	result.Reason = ReasonOK
	if secondDiff == bestDiff && bestDiff <= opts.TimeToleranceNS {
		result.Reason = ReasonDuplicate
	}
	if nameMismatch {
		result.Reason = ReasonAPINameMismatch
	}
	result.CorrelationID = best.CorrelationID

	correlated := deviceByCorr[best.CorrelationID]
	for _, span := range correlated {
		dStart, dEnd := span.StartNS-offset, span.EndNS-offset
		overlap := overlapNS(start, end, dStart, dEnd)
		gap := intervalGapNS(start, end, dStart, dEnd)
		if overlap <= 0 && gap > opts.TimeToleranceNS {
			continue
		}
		match := toMatch(span, overlap)
		match.StartNS, match.EndNS = dStart, dEnd
		if overlap == 0 {
			match.GapNS = gap
			match.Adjacent = true
		}
		result.Matches = append(result.Matches, match)
	}
	if len(result.Matches) == 0 {
		result.Reason = ReasonNoDeviceRecord
		result.Matched = false
		return result
	}
	fillHostMetrics(&result, start, end)
	return result
}

// pickAPI returns the closest API record to the host start and the two smallest
// absolute differences (for duplicate detection).
func pickAPI(candidates []Span, hostStart int64, offset int64, function string, tolerance int64) (*Span, int64, int64, bool) {
	var best *Span
	bestDiff := int64(1<<62 - 1)
	secondDiff := int64(1<<62 - 1)
	for i := range candidates {
		diff := candidates[i].StartNS - hostStart - offset
		if diff < 0 {
			diff = -diff
		}
		if diff < bestDiff {
			secondDiff = bestDiff
			bestDiff = diff
			best = &candidates[i]
		} else if diff < secondDiff {
			secondDiff = diff
		}
	}
	if best == nil || bestDiff > tolerance {
		return nil, bestDiff, secondDiff, false
	}
	nameMismatch := best.Name != "" && function != "" && best.Name != function
	return best, bestDiff, secondDiff, nameMismatch
}

func estimateClockOffset(host []runlog.Span, apiByThread map[[2]int][]Span, tolerance int64) int64 {
	var diffs []int64
	limit := tolerance * 100
	if limit < 10*int64(time.Millisecond) {
		limit = 10 * int64(time.Millisecond)
	}
	for _, h := range host {
		candidates := apiByThread[[2]int{h.PID, h.TID}]
		if len(candidates) == 0 {
			continue
		}
		start := hostStart(h)
		var best *Span
		bestDiff := int64(1<<62 - 1)
		for i := range candidates {
			diff := candidates[i].StartNS - start
			if diff < 0 {
				diff = -diff
			}
			if diff < bestDiff {
				bestDiff = diff
				best = &candidates[i]
			}
		}
		if best != nil && bestDiff <= limit {
			diffs = append(diffs, best.StartNS-start)
		}
	}
	if len(diffs) == 0 {
		return 0
	}
	sort.Slice(diffs, func(i, j int) bool { return diffs[i] < diffs[j] })
	return diffs[len(diffs)/2]
}

func fillHostMetrics(result *HostResult, start, end int64) {
	sort.Slice(result.Matches, func(i, j int) bool {
		return result.Matches[i].StartNS < result.Matches[j].StartNS
	})
	var intervals [][2]int64
	for _, match := range result.Matches {
		// busy 只统计 host span 窗口内的设备执行时间；相邻匹配没有重叠，
		// 不能因为 kernel 区间本身而抬高 BusyPct。
		clipStart, clipEnd := match.StartNS, match.EndNS
		if clipStart < start {
			clipStart = start
		}
		if clipEnd > end {
			clipEnd = end
		}
		if clipEnd > clipStart {
			intervals = append(intervals, [2]int64{clipStart, clipEnd})
		}
		switch match.Kind {
		case KindKernel:
			result.KernelNS += match.OverlapNS
		case KindMemcpy:
			result.MemcpyNS += match.OverlapNS
		case KindHCCL:
			result.HCCLNS += match.OverlapNS
		}
	}
	result.DeviceBusyNS = unionDuration(intervals)
	if end > start {
		result.BusyPct = float64(result.DeviceBusyNS) / float64(end-start)
	}
}

func toMatch(span Span, overlap int64) Match {
	return Match{
		Kind: span.Kind, Name: span.Name, DeviceID: span.DeviceID, StreamID: span.StreamID,
		StartNS: span.StartNS, EndNS: span.EndNS, DurationNS: span.DurationNS,
		OverlapNS: overlap, CorrelationID: span.CorrelationID,
	}
}

func unionDuration(intervals [][2]int64) int64 {
	if len(intervals) == 0 {
		return 0
	}
	sort.Slice(intervals, func(i, j int) bool {
		if intervals[i][0] == intervals[j][0] {
			return intervals[i][1] < intervals[j][1]
		}
		return intervals[i][0] < intervals[j][0]
	})
	var total int64
	curStart, curEnd := intervals[0][0], intervals[0][1]
	for _, interval := range intervals[1:] {
		if interval[0] > curEnd {
			total += curEnd - curStart
			curStart, curEnd = interval[0], interval[1]
			continue
		}
		if interval[1] > curEnd {
			curEnd = interval[1]
		}
	}
	total += curEnd - curStart
	return total
}

func overlapNS(aStart, aEnd, bStart, bEnd int64) int64 {
	start := aStart
	if bStart > start {
		start = bStart
	}
	end := aEnd
	if bEnd < end {
		end = bEnd
	}
	if end <= start {
		return 0
	}
	return end - start
}

// intervalGapNS returns the distance between two half-open intervals. It is 0
// when they overlap or touch. It is used only for correlationId-bound device
// records, where a small gap is timestamp skew rather than a different call.
func intervalGapNS(aStart, aEnd, bStart, bEnd int64) int64 {
	if aEnd < bStart {
		return bStart - aEnd
	}
	if bEnd < aStart {
		return aStart - bEnd
	}
	return 0
}

func hostStart(s runlog.Span) int64 {
	if s.StartNS > 0 {
		return s.StartNS
	}
	return s.TsNS
}

func hostEnd(s runlog.Span) int64 {
	if s.EndNS > 0 {
		return s.EndNS
	}
	return hostStart(s) + s.DurationNS
}

func pct(part, total int64) float64 {
	if total <= 0 {
		return 0
	}
	return float64(part) / float64(total)
}
