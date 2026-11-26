package runlog

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strings"
)

const SummarySchemaV1 = "cann.summary.v1"

const (
	SummaryStatusOK    = "ok"
	SummaryStatusEmpty = "empty"
)

var ErrNoDurationData = errors.New("runlog: no duration data")

type FunctionStats struct {
	Function string `json:"function"`
	Count    int64  `json:"count"`
	TotalNS  int64  `json:"total_ns"`
	P50NS    int64  `json:"p50_ns"`
	P95NS    int64  `json:"p95_ns"`
	P99NS    int64  `json:"p99_ns"`
	MaxNS    int64  `json:"max_ns"`
}

type CategoryStats struct {
	Category string  `json:"category"`
	Count    int64   `json:"count"`
	SpanPct  float64 `json:"span_pct"`
	TotalNS  int64   `json:"total_ns"`
	P50NS    int64   `json:"p50_ns"`
	P95NS    int64   `json:"p95_ns"`
	P99NS    int64   `json:"p99_ns"`
	MaxNS    int64   `json:"max_ns"`
}

type PIDStats struct {
	PID         int   `json:"pid"`
	Count       int64 `json:"count"`
	BatchSpans  int64 `json:"batch_spans"`
	ThreadSpans int64 `json:"thread_spans"`
	TotalNS     int64 `json:"total_ns"`
}

type LevelStats struct {
	Level string  `json:"level"`
	Count int64   `json:"count"`
	Pct   float64 `json:"span_pct"`
}

type ContextCoverage struct {
	BatchSpans     int64   `json:"batch_spans"`
	ThreadSpans    int64   `json:"thread_spans"`
	Unattributed   int64   `json:"unattributed_spans"`
	BatchPct       float64 `json:"batch_span_pct"`
	UniqueBatches  int     `json:"unique_batches"`
	UniqueRequests int     `json:"unique_request_ids"`
}

type BatchStats struct {
	BatchID        string   `json:"batch_id"`
	Count          int64    `json:"count"`
	TotalNS        int64    `json:"total_ns"`
	P50NS          int64    `json:"p50_ns"`
	P95NS          int64    `json:"p95_ns"`
	P99NS          int64    `json:"p99_ns"`
	MaxNS          int64    `json:"max_ns"`
	RequestIDs     []string `json:"request_ids,omitempty"`
	PIDs           []int    `json:"pids,omitempty"`
	RunIDs         []string `json:"run_ids,omitempty"`
	ExampleSpanIDs []string `json:"example_span_ids,omitempty"`
}

// Summary is derived output. It never changes or replaces spans.jsonl; raw
// nanosecond values remain authoritative in the JSONL and manifest.
type Summary struct {
	Schema         string          `json:"schema"`
	Status         string          `json:"status"`
	SpanCount      int64           `json:"span_count"`
	Functions      []FunctionStats `json:"functions"`
	Categories     []CategoryStats `json:"categories"`
	PIDs           []PIDStats      `json:"pids"`
	Attribution    []LevelStats    `json:"attribution_levels"`
	Coverage       ContextCoverage `json:"context_coverage"`
	TopSlowBatches []BatchStats    `json:"top_slow_batches"`
}

type batchAggregate struct {
	durations  durationAggregate
	requestIDs map[string]struct{}
	pids       map[int]struct{}
	runIDs     map[string]struct{}
	spanIDs    []string
}
type durationAggregate struct {
	values []int64
}

func (a *durationAggregate) add(duration int64) {
	a.values = append(a.values, duration)
}

func (a *durationAggregate) finish() (count int64, totalNS, p50NS, p95NS, p99NS, maxNS int64, err error) {
	if len(a.values) == 0 {
		return 0, 0, 0, 0, 0, 0, ErrNoDurationData
	}
	sort.Slice(a.values, func(i, j int) bool { return a.values[i] < a.values[j] })
	for _, value := range a.values {
		totalNS += value
	}
	count = int64(len(a.values))
	p50NS, _ = percentileNearestRank(a.values, 0.50)
	p95NS, _ = percentileNearestRank(a.values, 0.95)
	p99NS, _ = percentileNearestRank(a.values, 0.99)
	maxNS = a.values[len(a.values)-1]
	return count, totalNS, p50NS, p95NS, p99NS, maxNS, nil
}

// BuildSummary uses nearest-rank percentiles over observed durations. Empty
// input yields status=empty and empty arrays, never synthetic zero-valued rows.
func BuildSummary(spans []Span) (Summary, error) {
	summary := Summary{
		Schema:         SummarySchemaV1,
		Status:         SummaryStatusEmpty,
		SpanCount:      int64(len(spans)),
		Functions:      []FunctionStats{},
		Categories:     []CategoryStats{},
		PIDs:           []PIDStats{},
		Attribution:    []LevelStats{},
		TopSlowBatches: []BatchStats{},
	}
	if len(spans) == 0 {
		return summary, nil
	}

	functionGroups := make(map[string]*durationAggregate)
	categoryGroups := make(map[string]*durationAggregate)
	pidGroups := make(map[int]*PIDStats)
	levelCounts := make(map[string]int64)
	batchGroups := make(map[string]*batchAggregate)
	requestSet := make(map[string]struct{})
	for i, span := range spans {
		if span.Function == "" {
			return Summary{}, fmt.Errorf("span %d: function is empty", i)
		}
		if span.Category == "" {
			return Summary{}, fmt.Errorf("span %d: category is empty", i)
		}
		if span.DurationNS < 0 {
			return Summary{}, fmt.Errorf("span %d: duration_ns is negative", i)
		}
		if functionGroups[span.Function] == nil {
			functionGroups[span.Function] = &durationAggregate{}
		}
		functionGroups[span.Function].add(span.DurationNS)
		if categoryGroups[span.Category] == nil {
			categoryGroups[span.Category] = &durationAggregate{}
		}
		categoryGroups[span.Category].add(span.DurationNS)

		pidStat := pidGroups[span.PID]
		if pidStat == nil {
			pidStat = &PIDStats{PID: span.PID}
			pidGroups[span.PID] = pidStat
		}
		pidStat.Count++
		pidStat.TotalNS += span.DurationNS

		level := ""
		if span.Attribution != nil {
			level = span.Attribution.Level
		}
		if level == "" {
			level = "unattributed"
		}
		levelCounts[level]++
		switch level {
		case "batch":
			summary.Coverage.BatchSpans++
			pidStat.BatchSpans++
		case "thread":
			summary.Coverage.ThreadSpans++
			pidStat.ThreadSpans++
		default:
			summary.Coverage.Unattributed++
		}
		for _, requestID := range span.RequestIDs {
			if requestID != "" {
				requestSet[requestID] = struct{}{}
			}
		}
		if span.BatchID != "" {
			batch := batchGroups[span.BatchID]
			if batch == nil {
				batch = &batchAggregate{
					requestIDs: map[string]struct{}{},
					pids:       map[int]struct{}{},
					runIDs:     map[string]struct{}{},
				}
				batchGroups[span.BatchID] = batch
			}
			batch.durations.add(span.DurationNS)
			for _, requestID := range span.RequestIDs {
				if requestID != "" {
					batch.requestIDs[requestID] = struct{}{}
				}
			}
			batch.pids[span.PID] = struct{}{}
			if span.RunID != "" {
				batch.runIDs[span.RunID] = struct{}{}
			}
			if span.SpanID != "" && len(batch.spanIDs) < 5 {
				batch.spanIDs = append(batch.spanIDs, span.SpanID)
			}
		}
	}

	for function, aggregate := range functionGroups {
		count, totalNS, p50NS, p95NS, p99NS, maxNS, err := aggregate.finish()
		if err != nil {
			return Summary{}, err
		}
		summary.Functions = append(summary.Functions, FunctionStats{
			Function: function,
			Count:    count,
			TotalNS:  totalNS,
			P50NS:    p50NS,
			P95NS:    p95NS,
			P99NS:    p99NS,
			MaxNS:    maxNS,
		})
	}
	for category, aggregate := range categoryGroups {
		count, totalNS, p50NS, p95NS, p99NS, maxNS, err := aggregate.finish()
		if err != nil {
			return Summary{}, err
		}
		summary.Categories = append(summary.Categories, CategoryStats{
			Category: category,
			Count:    count,
			SpanPct:  float64(count) * 100 / float64(summary.SpanCount),
			TotalNS:  totalNS,
			P50NS:    p50NS,
			P95NS:    p95NS,
			P99NS:    p99NS,
			MaxNS:    maxNS,
		})
	}
	for pid, stats := range pidGroups {
		_ = pid
		summary.PIDs = append(summary.PIDs, *stats)
	}
	sort.Slice(summary.PIDs, func(i, j int) bool { return summary.PIDs[i].PID < summary.PIDs[j].PID })

	for level, count := range levelCounts {
		summary.Attribution = append(summary.Attribution, LevelStats{
			Level: level, Count: count, Pct: float64(count) * 100 / float64(summary.SpanCount),
		})
	}
	sort.Slice(summary.Attribution, func(i, j int) bool {
		if summary.Attribution[i].Count == summary.Attribution[j].Count {
			return summary.Attribution[i].Level < summary.Attribution[j].Level
		}
		return summary.Attribution[i].Count > summary.Attribution[j].Count
	})
	summary.Coverage.UniqueBatches = len(batchGroups)
	summary.Coverage.UniqueRequests = len(requestSet)
	if summary.SpanCount > 0 {
		summary.Coverage.BatchPct = float64(summary.Coverage.BatchSpans) * 100 / float64(summary.SpanCount)
	}

	for batchID, aggregate := range batchGroups {
		count, totalNS, p50NS, p95NS, p99NS, maxNS, err := aggregate.durations.finish()
		if err != nil {
			return Summary{}, err
		}
		stat := BatchStats{
			BatchID: batchID, Count: count, TotalNS: totalNS, P50NS: p50NS,
			P95NS: p95NS, P99NS: p99NS, MaxNS: maxNS,
			RequestIDs:     sortedStrings(aggregate.requestIDs),
			PIDs:           sortedInts(aggregate.pids),
			RunIDs:         sortedStrings(aggregate.runIDs),
			ExampleSpanIDs: append([]string(nil), aggregate.spanIDs...),
		}
		summary.TopSlowBatches = append(summary.TopSlowBatches, stat)
	}
	sort.Slice(summary.TopSlowBatches, func(i, j int) bool {
		if summary.TopSlowBatches[i].TotalNS == summary.TopSlowBatches[j].TotalNS {
			return summary.TopSlowBatches[i].BatchID < summary.TopSlowBatches[j].BatchID
		}
		return summary.TopSlowBatches[i].TotalNS > summary.TopSlowBatches[j].TotalNS
	})
	if len(summary.TopSlowBatches) > 10 {
		summary.TopSlowBatches = summary.TopSlowBatches[:10]
	}

	sort.Slice(summary.Functions, func(i, j int) bool {
		if summary.Functions[i].TotalNS == summary.Functions[j].TotalNS {
			return summary.Functions[i].Function < summary.Functions[j].Function
		}
		return summary.Functions[i].TotalNS > summary.Functions[j].TotalNS
	})
	sort.Slice(summary.Categories, func(i, j int) bool {
		if summary.Categories[i].TotalNS == summary.Categories[j].TotalNS {
			return summary.Categories[i].Category < summary.Categories[j].Category
		}
		return summary.Categories[i].TotalNS > summary.Categories[j].TotalNS
	})
	summary.Status = SummaryStatusOK
	return summary, nil
}

func BuildSummaryFromReader(reader io.Reader) (Summary, error) {
	spans, err := ReadSpans(reader)
	if err != nil {
		return Summary{}, err
	}
	return BuildSummary(spans)
}

func BuildSummaryFromFile(path string) (Summary, error) {
	file, err := os.Open(path)
	if err != nil {
		return Summary{}, err
	}
	defer file.Close()
	return BuildSummaryFromReader(file)
}

func sortedStrings(values map[string]struct{}) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func sortedInts(values map[int]struct{}) []int {
	out := make([]int, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Ints(out)
	return out
}

func percentileNearestRank(sortedValues []int64, percentile float64) (int64, error) {
	if len(sortedValues) == 0 {
		return 0, ErrNoDurationData
	}
	if percentile <= 0 || percentile > 1 {
		return 0, fmt.Errorf("percentile must be in (0,1]: %v", percentile)
	}
	rank := int(math.Ceil(percentile * float64(len(sortedValues))))
	if rank < 1 {
		rank = 1
	}
	if rank > len(sortedValues) {
		rank = len(sortedValues)
	}
	return sortedValues[rank-1], nil
}

// Markdown renders a deterministic derived report. The empty case explicitly
// states that no statistics were generated.
func (s Summary) Markdown() string {
	s = normalizeSummary(s)
	var output strings.Builder
	output.WriteString("# CANN eBPF 运行摘要\n\n")
	fmt.Fprintf(&output, "- Schema：`%s`\n", SummarySchemaV1)
	fmt.Fprintf(&output, "- 状态：`%s`\n", s.Status)
	fmt.Fprintf(&output, "- Span 总数：`%d`\n", s.SpanCount)
	output.WriteString("- 分位数口径：nearest-rank；耗时字段单位均为 ns。\n")
	output.WriteString("- 原始值以 `spans.jsonl` 和 `manifest.json` 为准，本文件仅为派生汇总。\n")

	if s.Status == SummaryStatusEmpty || s.SpanCount == 0 {
		output.WriteString("\n## 数据状态\n\n")
		output.WriteString("未发现 span 数据；未生成任何 count、p50、p95、p99、max 或 total 统计。\n")
		return output.String()
	}

	output.WriteString("\n## 按 function\n\n")
	output.WriteString("| function | count | p50 ns | p95 ns | p99 ns | max ns | total ns |\n")
	output.WriteString("|---|---:|---:|---:|---:|---:|---:|\n")
	for _, stat := range s.Functions {
		fmt.Fprintf(&output, "| `%s` | %d | %d | %d | %d | %d | %d |\n",
			escapeMarkdownCell(stat.Function), stat.Count, stat.P50NS, stat.P95NS,
			stat.P99NS, stat.MaxNS, stat.TotalNS)
	}

	output.WriteString("\n## 上下文与归因覆盖\n\n")
	fmt.Fprintf(&output, "- batch span：`%d`（%.2f%%）\n", s.Coverage.BatchSpans, s.Coverage.BatchPct)
	fmt.Fprintf(&output, "- thread 回退 span：`%d`\n", s.Coverage.ThreadSpans)
	fmt.Fprintf(&output, "- 未归因 span：`%d`\n", s.Coverage.Unattributed)
	fmt.Fprintf(&output, "- 唯一 batch：`%d`，唯一 request_id：`%d`\n", s.Coverage.UniqueBatches, s.Coverage.UniqueRequests)
	if len(s.Attribution) == 0 {
		output.WriteString("- 归因层级：无数据\n")
	} else {
		output.WriteString("\n| level | count | span % |\n|---|---:|---:|\n")
		for _, stat := range s.Attribution {
			fmt.Fprintf(&output, "| `%s` | %d | %.2f%% |\n", escapeMarkdownCell(stat.Level), stat.Count, stat.Pct)
		}
	}

	output.WriteString("\n## 按 PID\n\n")
	output.WriteString("| pid | count | batch spans | thread spans | total ns |\n|---:|---:|---:|---:|---:|\n")
	for _, stat := range s.PIDs {
		fmt.Fprintf(&output, "| %d | %d | %d | %d | %d |\n", stat.PID, stat.Count, stat.BatchSpans, stat.ThreadSpans, stat.TotalNS)
	}

	if len(s.TopSlowBatches) == 0 {
		output.WriteString("\n## Top slow batch\n\n未发现带 batch_id 的 span；未生成 batch 排名。\n")
	} else {
		output.WriteString("\n## Top slow batch\n\n")
		output.WriteString("| batch_id | count | p50 ns | p95 ns | p99 ns | max ns | total ns | requests | pids |\n|---|---:|---:|---:|---:|---:|---:|---:|---|\n")
		for _, stat := range s.TopSlowBatches {
			fmt.Fprintf(&output, "| `%s` | %d | %d | %d | %d | %d | %d | %d | %s |\n",
				escapeMarkdownCell(stat.BatchID), stat.Count, stat.P50NS, stat.P95NS, stat.P99NS, stat.MaxNS, stat.TotalNS, len(stat.RequestIDs), joinInts(stat.PIDs))
		}
	}

	output.WriteString("\n## 按 category\n\n")
	output.WriteString("| category | count | span % | p50 ns | p95 ns | p99 ns | max ns | total ns |\n")
	output.WriteString("|---|---:|---:|---:|---:|---:|---:|---:|\n")
	for _, stat := range s.Categories {
		fmt.Fprintf(&output, "| `%s` | %d | %.2f%% | %d | %d | %d | %d | %d |\n",
			escapeMarkdownCell(stat.Category), stat.Count, stat.SpanPct, stat.P50NS,
			stat.P95NS, stat.P99NS, stat.MaxNS, stat.TotalNS)
	}
	return output.String()
}

func (s Summary) WriteJSON(path string) error {
	s = normalizeSummary(s)
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return writeFileAtomic(path, data, 0o600)
}

func (s Summary) WriteMarkdown(path string) error {
	return writeFileAtomic(path, []byte(s.Markdown()), 0o600)
}

func normalizeSummary(summary Summary) Summary {
	if summary.Schema == "" {
		summary.Schema = SummarySchemaV1
	}
	if summary.Functions == nil {
		summary.Functions = []FunctionStats{}
	}
	if summary.Categories == nil {
		summary.Categories = []CategoryStats{}
	}
	if summary.PIDs == nil {
		summary.PIDs = []PIDStats{}
	}
	if summary.Attribution == nil {
		summary.Attribution = []LevelStats{}
	}
	if summary.TopSlowBatches == nil {
		summary.TopSlowBatches = []BatchStats{}
	}
	if summary.Status == "" {
		if summary.SpanCount == 0 {
			summary.Status = SummaryStatusEmpty
		} else {
			summary.Status = SummaryStatusOK
		}
	}
	return summary
}

func joinInts(values []int) string {
	if len(values) == 0 {
		return ""
	}
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, fmt.Sprintf("%d", value))
	}
	return strings.Join(parts, ",")
}

func escapeMarkdownCell(value string) string {
	return strings.ReplaceAll(value, "|", "\\|")
}
