package report

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"

	"cann-ebpf/internal/runlog"
)

const (
	offCPUModeAggregate = "aggregate"
	offCPUModeExact     = "exact"
	offCPUKindDefault   = "offcpu"
	offCPUKindFutex     = "futex"
)

// OffCPUOptions controls BuildOffCPU.
//
// Exact mode is selected only when the caller supplies real interval input via
// IntervalPath, Intervals, or IntervalsBySpan. Aggregate offcpu_ns/futex_ns
// totals are never converted into exact segments.
type OffCPUOptions struct {
	// WindowStartNS is the report-window origin used to convert span and
	// interval timestamps into *_ms fields. When <= 0, the earliest span start
	// is used.
	WindowStartNS int64
	// MaxRows caps the number of returned rows after chronological sorting.
	// <= 0 means no cap.
	MaxRows int
	// Intervals supplies exact off-CPU intervals in absolute milliseconds on
	// the same clock as span start_ns. Intervals without identity are matched
	// to the narrowest span containing them.
	Intervals []OffCPUSegment
	// IntervalsBySpan supplies exact intervals keyed by runlog.Span.SpanID.
	IntervalsBySpan map[string][]OffCPUSegment
	// IntervalPath points at an optional offcpu.jsonl file. Records carry
	// pid/tid/span_id and absolute start_ns/end_ns.
	IntervalPath string
}

func (o OffCPUOptions) exactRequested() bool {
	return len(o.Intervals) > 0 || len(o.IntervalsBySpan) > 0 || o.IntervalPath != ""
}

// LoadOffCPUIntervals decodes an offcpu.jsonl file into exact, absolute
// millisecond intervals. Blank lines are ignored; malformed records fail with
// a one-based line number.
func LoadOffCPUIntervals(path string) ([]OffCPUSegment, error) {
	records, err := readOffCPUIntervalRecords(path)
	if err != nil {
		return nil, err
	}
	out := make([]OffCPUSegment, 0, len(records))
	for _, record := range records {
		out = append(out, OffCPUSegment{
			StartMS: float64(record.StartNS) / 1e6,
			EndMS:   float64(record.EndNS) / 1e6,
			Kind:    offCPUKind(record.Kind),
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].StartMS != out[j].StartMS {
			return out[i].StartMS < out[j].StartMS
		}
		if out[i].EndMS != out[j].EndMS {
			return out[i].EndMS < out[j].EndMS
		}
		return out[i].Kind < out[j].Kind
	})
	return out, nil
}

type offCPUIntervalRecord struct {
	PID     int    `json:"pid"`
	TID     int    `json:"tid"`
	SpanID  string `json:"span_id"`
	StartNS int64  `json:"start_ns"`
	EndNS   int64  `json:"end_ns"`
	Kind    string `json:"kind"`
}

func readOffCPUIntervalRecords(path string) ([]offCPUIntervalRecord, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	var records []offCPUIntervalRecord
	line := 0
	for scanner.Scan() {
		line++
		raw := bytes.TrimSpace(scanner.Bytes())
		if len(raw) == 0 {
			continue
		}
		var record offCPUIntervalRecord
		if err := json.Unmarshal(raw, &record); err != nil {
			return records, fmt.Errorf("line %d: %w", line, err)
		}
		if record.StartNS < 0 || record.EndNS <= record.StartNS {
			return records, fmt.Errorf("line %d: invalid interval start_ns=%d end_ns=%d", line, record.StartNS, record.EndNS)
		}
		record.Kind = offCPUKind(record.Kind)
		records = append(records, record)
	}
	if err := scanner.Err(); err != nil {
		return records, err
	}
	sort.SliceStable(records, func(i, j int) bool {
		if records[i].StartNS != records[j].StartNS {
			return records[i].StartNS < records[j].StartNS
		}
		if records[i].EndNS != records[j].EndNS {
			return records[i].EndNS < records[j].EndNS
		}
		return records[i].SpanID < records[j].SpanID
	})
	return records, nil
}

// BuildOffCPU builds the off-CPU ribbon data. Without exact interval input it
// returns per-span aggregate rows (mode=aggregate); with exact interval input
// it returns rows whose segments come only from those intervals (mode=exact).
// A span without measured blocking fields never becomes a row.
func BuildOffCPU(spans []runlog.Span, opts OffCPUOptions) OffCPUData {
	if opts.exactRequested() {
		return buildOffCPUExact(spans, opts)
	}
	return buildOffCPUAggregate(spans, opts)
}

func buildOffCPUAggregate(spans []runlog.Span, opts OffCPUOptions) OffCPUData {
	out := OffCPUData{
		Status: ViewStatus{Available: false},
		Mode:   offCPUModeAggregate,
		Rows:   []OffCPURow{},
	}
	if len(spans) == 0 {
		out.Status.Reason = "no spans"
		out.Status.Source = "none"
		return out
	}

	anchor := opts.WindowStartNS
	if anchor <= 0 {
		anchor = earliestSpanStart(spans)
	}

	rows := make([]OffCPURow, 0, len(spans))
	invalid := 0
	for _, span := range spans {
		duration := spanDurationNS(span)
		if duration < 0 {
			duration = 0
		}
		offCPU, futex, bad := validOffCPUSample(span, duration)
		if bad {
			invalid++
		}
		if offCPU == nil && futex == nil {
			continue
		}

		durationMS := float64(duration) / 1e6
		offCPUMS := 0.0
		onCPUMS := 0.0
		if offCPU != nil {
			offCPUMS = float64(*offCPU) / 1e6
			onCPUMS = durationMS - offCPUMS
			if onCPUMS < 0 {
				onCPUMS = 0
			}
		}
		futexMS := 0.0
		if futex != nil {
			futexMS = float64(*futex) / 1e6
		}

		rows = append(rows, OffCPURow{
			Lane:     offCPULane(span),
			StartMS:  float64(spanStart(span)-anchor) / 1e6,
			EndMS:    float64(spanEnd(span)-anchor) / 1e6,
			OnCPUMS:  onCPUMS,
			OffCPUMS: offCPUMS,
			FutexMS:  futexMS,
			Segments: []OffCPUSegment{},
		})
	}
	if len(rows) == 0 {
		out.Status.Source = "spans"
		if invalid > 0 {
			out.Status.Reason = fmt.Sprintf("no valid blocking samples: %d invalid", invalid)
		} else {
			out.Status.Reason = "no blocking samples (offcpu_ns/futex_ns)"
		}
		return out
	}

	sortOffCPURows(rows)
	if opts.MaxRows > 0 && len(rows) > opts.MaxRows {
		rows = rows[:opts.MaxRows]
	}
	out.Rows = rows
	out.Status = ViewStatus{Available: true, Source: "spans", Confidence: "partial"}
	return out
}

type exactOffCPUInterval struct {
	SpanID  string
	PID     int
	TID     int
	StartMS float64
	EndMS   float64
	Kind    string
}

func buildOffCPUExact(spans []runlog.Span, opts OffCPUOptions) OffCPUData {
	out := OffCPUData{
		Status: ViewStatus{Available: false},
		Mode:   offCPUModeExact,
		Rows:   []OffCPURow{},
	}
	if len(spans) == 0 {
		out.Status.Reason = "no spans"
		out.Status.Source = "none"
		return out
	}

	intervals, source, invalid, err := collectExactOffCPUIntervals(opts)
	if err != nil {
		out.Status.Reason = "offcpu intervals: " + err.Error()
		out.Status.Source = source
		return out
	}
	if len(intervals) == 0 {
		out.Status.Source = source
		if invalid > 0 {
			out.Status.Reason = fmt.Sprintf("no valid exact off-CPU intervals: %d invalid", invalid)
		} else {
			out.Status.Reason = "no exact off-CPU intervals"
		}
		return out
	}

	assignments := assignExactIntervals(spans, intervals)
	if len(assignments) == 0 {
		out.Status.Reason = "exact off-CPU intervals do not match any span"
		out.Status.Source = source
		return out
	}

	anchor := opts.WindowStartNS
	if anchor <= 0 {
		anchor = earliestSpanStart(spans)
	}
	anchorMS := float64(anchor) / 1e6

	indices := make([]int, 0, len(assignments))
	for index := range assignments {
		indices = append(indices, index)
	}
	sort.SliceStable(indices, func(i, j int) bool {
		left, right := spans[indices[i]], spans[indices[j]]
		if spanStart(left) != spanStart(right) {
			return spanStart(left) < spanStart(right)
		}
		return left.SpanID < right.SpanID
	})

	rows := make([]OffCPURow, 0, len(indices))
	for _, index := range indices {
		span := spans[index]
		spanStartNS, spanEndNS := spanStart(span), spanEnd(span)
		if spanEndNS <= spanStartNS {
			continue
		}
		rowStartAbsMS := float64(spanStartNS) / 1e6
		rowEndAbsMS := float64(spanEndNS) / 1e6

		segments := make([]OffCPUSegment, 0, len(assignments[index]))
		var allIntervals []msInterval
		var futexIntervals []msInterval
		for _, interval := range assignments[index] {
			start := math.Max(interval.StartMS, rowStartAbsMS)
			end := math.Min(interval.EndMS, rowEndAbsMS)
			if end <= start {
				continue
			}
			kind := offCPUKind(interval.Kind)
			segments = append(segments, OffCPUSegment{
				StartMS: start - anchorMS,
				EndMS:   end - anchorMS,
				Kind:    kind,
			})
			clipped := msInterval{start: start, end: end}
			allIntervals = append(allIntervals, clipped)
			if kind == offCPUKindFutex {
				futexIntervals = append(futexIntervals, clipped)
			}
		}
		if len(segments) == 0 {
			continue
		}
		sort.SliceStable(segments, func(i, j int) bool {
			if segments[i].StartMS != segments[j].StartMS {
				return segments[i].StartMS < segments[j].StartMS
			}
			if segments[i].EndMS != segments[j].EndMS {
				return segments[i].EndMS < segments[j].EndMS
			}
			return segments[i].Kind < segments[j].Kind
		})

		offCPUMS := unionMillis(allIntervals)
		futexMS := unionMillis(futexIntervals)
		durationMS := float64(spanEndNS-spanStartNS) / 1e6
		onCPUMS := durationMS - offCPUMS
		if onCPUMS < 0 {
			onCPUMS = 0
		}
		rows = append(rows, OffCPURow{
			Lane:     offCPULane(span),
			StartMS:  rowStartAbsMS - anchorMS,
			EndMS:    rowEndAbsMS - anchorMS,
			OnCPUMS:  onCPUMS,
			OffCPUMS: offCPUMS,
			FutexMS:  futexMS,
			Segments: segments,
		})
	}
	if len(rows) == 0 {
		out.Status.Reason = "exact off-CPU intervals do not overlap any span"
		out.Status.Source = source
		return out
	}

	sortOffCPURows(rows)
	if opts.MaxRows > 0 && len(rows) > opts.MaxRows {
		rows = rows[:opts.MaxRows]
	}
	out.Rows = rows
	out.Status = ViewStatus{Available: true, Source: source, Confidence: "exact"}
	return out
}

func collectExactOffCPUIntervals(opts OffCPUOptions) ([]exactOffCPUInterval, string, int, error) {
	source := "offcpu intervals"
	var intervals []exactOffCPUInterval
	if opts.IntervalPath != "" {
		source = "offcpu.jsonl"
		records, err := readOffCPUIntervalRecords(opts.IntervalPath)
		if err != nil {
			return nil, source, 0, err
		}
		for _, record := range records {
			intervals = append(intervals, exactOffCPUInterval{
				SpanID:  record.SpanID,
				PID:     record.PID,
				TID:     record.TID,
				StartMS: float64(record.StartNS) / 1e6,
				EndMS:   float64(record.EndNS) / 1e6,
				Kind:    record.Kind,
			})
		}
	}
	for spanID, segments := range opts.IntervalsBySpan {
		for _, segment := range segments {
			intervals = append(intervals, exactOffCPUInterval{
				SpanID:  spanID,
				StartMS: segment.StartMS,
				EndMS:   segment.EndMS,
				Kind:    segment.Kind,
			})
		}
	}
	for _, segment := range opts.Intervals {
		intervals = append(intervals, exactOffCPUInterval{
			StartMS: segment.StartMS,
			EndMS:   segment.EndMS,
			Kind:    segment.Kind,
		})
	}

	valid := make([]exactOffCPUInterval, 0, len(intervals))
	invalid := 0
	for _, interval := range intervals {
		if interval.StartMS < 0 || interval.EndMS <= interval.StartMS {
			invalid++
			continue
		}
		interval.Kind = offCPUKind(interval.Kind)
		valid = append(valid, interval)
	}
	sort.SliceStable(valid, func(i, j int) bool {
		if valid[i].StartMS != valid[j].StartMS {
			return valid[i].StartMS < valid[j].StartMS
		}
		if valid[i].EndMS != valid[j].EndMS {
			return valid[i].EndMS < valid[j].EndMS
		}
		if valid[i].SpanID != valid[j].SpanID {
			return valid[i].SpanID < valid[j].SpanID
		}
		return valid[i].Kind < valid[j].Kind
	})
	return valid, source, invalid, nil
}

func assignExactIntervals(spans []runlog.Span, intervals []exactOffCPUInterval) map[int][]exactOffCPUInterval {
	bySpanID := make(map[string]int, len(spans))
	for index, span := range spans {
		if span.SpanID == "" {
			continue
		}
		if _, exists := bySpanID[span.SpanID]; !exists {
			bySpanID[span.SpanID] = index
		}
	}
	out := map[int][]exactOffCPUInterval{}
	for _, interval := range intervals {
		index := -1
		if interval.SpanID != "" {
			if candidate, ok := bySpanID[interval.SpanID]; ok {
				index = candidate
			}
		}
		if index < 0 {
			index = matchIntervalToSpan(spans, interval)
		}
		if index >= 0 {
			out[index] = append(out[index], interval)
		}
	}
	return out
}

func matchIntervalToSpan(spans []runlog.Span, interval exactOffCPUInterval) int {
	contained := -1
	containedDuration := int64(0)
	for index, span := range spans {
		if interval.PID > 0 && span.PID != interval.PID {
			continue
		}
		if interval.TID > 0 && span.TID != interval.TID {
			continue
		}
		startMS := float64(spanStart(span)) / 1e6
		endMS := float64(spanEnd(span)) / 1e6
		if interval.StartMS < startMS || interval.EndMS > endMS {
			continue
		}
		duration := spanEnd(span) - spanStart(span)
		if contained < 0 || duration < containedDuration ||
			(duration == containedDuration && spanStart(span) < spanStart(spans[contained])) {
			contained = index
			containedDuration = duration
		}
	}
	if contained >= 0 {
		return contained
	}

	best := -1
	bestOverlap := 0.0
	bestDuration := int64(0)
	for index, span := range spans {
		if interval.PID > 0 && span.PID != interval.PID {
			continue
		}
		if interval.TID > 0 && span.TID != interval.TID {
			continue
		}
		startMS := float64(spanStart(span)) / 1e6
		endMS := float64(spanEnd(span)) / 1e6
		overlap := math.Min(interval.EndMS, endMS) - math.Max(interval.StartMS, startMS)
		if overlap <= 0 {
			continue
		}
		duration := spanEnd(span) - spanStart(span)
		if best < 0 || overlap > bestOverlap || (overlap == bestOverlap && duration < bestDuration) {
			best = index
			bestOverlap = overlap
			bestDuration = duration
		}
	}
	return best
}

type msInterval struct {
	start float64
	end   float64
}

func unionMillis(intervals []msInterval) float64 {
	if len(intervals) == 0 {
		return 0
	}
	sorted := append([]msInterval(nil), intervals...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].start != sorted[j].start {
			return sorted[i].start < sorted[j].start
		}
		return sorted[i].end < sorted[j].end
	})
	total := 0.0
	currentStart, currentEnd := sorted[0].start, sorted[0].end
	for _, next := range sorted[1:] {
		if next.start > currentEnd {
			total += currentEnd - currentStart
			currentStart, currentEnd = next.start, next.end
			continue
		}
		if next.end > currentEnd {
			currentEnd = next.end
		}
	}
	total += currentEnd - currentStart
	return total
}

func validOffCPUSample(span runlog.Span, duration int64) (offCPU, futex *int64, invalid bool) {
	if span.OffCPUNS != nil {
		if *span.OffCPUNS >= 0 && *span.OffCPUNS <= duration {
			offCPU = span.OffCPUNS
		} else {
			invalid = true
		}
	}
	if span.FutexNS != nil {
		if *span.FutexNS >= 0 && *span.FutexNS <= duration {
			futex = span.FutexNS
		} else {
			invalid = true
		}
	}
	return offCPU, futex, invalid
}

func earliestSpanStart(spans []runlog.Span) int64 {
	anchor := int64(0)
	for index, span := range spans {
		start := spanStart(span)
		if index == 0 || start < anchor {
			anchor = start
		}
	}
	if anchor < 0 {
		return 0
	}
	return anchor
}

func offCPULane(span runlog.Span) string {
	function := span.Function
	if function == "" {
		function = "(unknown)"
	}
	lane := fmt.Sprintf("%s · tid %d", function, span.TID)
	if span.SpanID != "" {
		lane += " · " + span.SpanID
	}
	return lane
}

func offCPUKind(kind string) string {
	normalized := strings.ToLower(strings.TrimSpace(kind))
	if normalized == "" {
		return offCPUKindDefault
	}
	return normalized
}

func sortOffCPURows(rows []OffCPURow) {
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].StartMS != rows[j].StartMS {
			return rows[i].StartMS < rows[j].StartMS
		}
		if rows[i].EndMS != rows[j].EndMS {
			return rows[i].EndMS < rows[j].EndMS
		}
		return rows[i].Lane < rows[j].Lane
	})
}
