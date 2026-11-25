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

// Summary is derived output. It never changes or replaces spans.jsonl; raw
// nanosecond values remain authoritative in the JSONL and manifest.
type Summary struct {
	Schema     string          `json:"schema"`
	Status     string          `json:"status"`
	SpanCount  int64           `json:"span_count"`
	Functions  []FunctionStats `json:"functions"`
	Categories []CategoryStats `json:"categories"`
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
		Schema:     SummarySchemaV1,
		Status:     SummaryStatusEmpty,
		SpanCount:  int64(len(spans)),
		Functions:  []FunctionStats{},
		Categories: []CategoryStats{},
	}
	if len(spans) == 0 {
		return summary, nil
	}

	functionGroups := make(map[string]*durationAggregate)
	categoryGroups := make(map[string]*durationAggregate)
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
	if summary.Status == "" {
		if summary.SpanCount == 0 {
			summary.Status = SummaryStatusEmpty
		} else {
			summary.Status = SummaryStatusOK
		}
	}
	return summary
}

func escapeMarkdownCell(value string) string {
	return strings.ReplaceAll(value, "|", "\\|")
}
