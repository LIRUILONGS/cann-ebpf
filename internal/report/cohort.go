package report

import (
	"fmt"
	"math"
	"sort"

	"cann-ebpf/internal/runlog"
)

const (
	defaultAggregateBuckets   = 60
	defaultAggregateFunctions = 12
	defaultCohortTopRows      = 12
)

// AggregateOptions controls BuildAggregateBands.
type AggregateOptions struct {
	BucketCount  int
	TopFunctions int
	MinSamples   int
}

func (o *AggregateOptions) normalize() {
	if o.BucketCount <= 0 {
		o.BucketCount = defaultAggregateBuckets
	}
	if o.TopFunctions <= 0 {
		o.TopFunctions = defaultAggregateFunctions
	}
	if o.MinSamples <= 0 {
		o.MinSamples = 1
	}
}

// AggregateBand is one function's latency percentiles per time bucket.
// P50/P95/P99 and Count are parallel slices indexed by bucket column.
type AggregateBand struct {
	Function string    `json:"function"`
	Category string    `json:"category"`
	P50      []float64 `json:"p50"`
	P95      []float64 `json:"p95"`
	P99      []float64 `json:"p99"`
	Count    []int     `json:"count"`
}

// AggregateBandsData is the function percentile-band view. It is derived
// from spans only and never represents stack samples.
type AggregateBandsData struct {
	Available bool            `json:"available"`
	Reason    string          `json:"reason,omitempty"`
	BucketMS  float64         `json:"bucket_ms"`
	Columns   int             `json:"columns"`
	MaxP95MS  float64         `json:"max_p95_ms"`
	Bands     []AggregateBand `json:"bands"`
}

// CohortOptions controls BuildTailCohort.
type CohortOptions struct {
	SlowFraction float64
	MinSlow      int
	MinSamples   int
}

func (o *CohortOptions) normalize() {
	if o.SlowFraction <= 0 || o.SlowFraction >= 1 {
		o.SlowFraction = 0.99
	}
	if o.MinSlow <= 0 {
		o.MinSlow = 30
	}
	if o.MinSamples <= 0 {
		o.MinSamples = 100
	}
}

// CohortRow compares a function or category share between the slow and
// typical cohorts. DeltaPct is SlowPct - TypicalPct (percentage points).
type CohortRow struct {
	Label      string  `json:"label"`
	Kind       string  `json:"kind"` // function|category|wait
	SlowPct    float64 `json:"slow_pct"`
	TypicalPct float64 `json:"typical_pct"`
	DeltaPct   float64 `json:"delta_pct"`
}

// TailCohortData contrasts p99 slow spans with p45-p55 typical spans.
// It describes correlation, not causation, and never substitutes for a
// controlled experiment.
type TailCohortData struct {
	Available       bool        `json:"available"`
	Reason          string      `json:"reason,omitempty"`
	ThresholdMS     float64     `json:"threshold_ms"`
	SlowCount       int         `json:"slow_count"`
	TypicalCount    int         `json:"typical_count"`
	Rows            []CohortRow `json:"rows"`
	SlowOffCPUMS    float64     `json:"slow_offcpu_ms"`
	TypicalOffCPUMS float64     `json:"typical_offcpu_ms"`
	SlowFutexMS     float64     `json:"slow_futex_ms"`
	TypicalFutexMS  float64     `json:"typical_futex_ms"`
	Notes           []string    `json:"notes"`
}

type functionBucketAgg struct {
	totalNS        int64
	count          int
	durations      [][]int64
	categoryTotals map[string]int64
}

// BuildAggregateBands aggregates spans into per-function percentile bands.
// Functions are ranked by total duration (descending); only the top
// TopFunctions are returned. Each time bucket computes its own nearest-rank
// p50/p95/p99, and empty buckets keep zero values and zero counts.
func BuildAggregateBands(spans []runlog.Span, windowStart, windowNS int64, opts AggregateOptions) AggregateBandsData {
	opts.normalize()
	out := AggregateBandsData{Bands: []AggregateBand{}}
	if len(spans) == 0 {
		out.Reason = "no spans"
		return out
	}

	if windowNS <= 0 {
		start, end := int64(0), int64(0)
		for index, span := range spans {
			s := spanStart(span)
			e := spanEnd(span)
			if index == 0 || s < start {
				start = s
			}
			if index == 0 || e > end {
				end = e
			}
		}
		windowStart = start
		windowNS = end - start
	}
	if windowNS <= 0 {
		out.Reason = "invalid window"
		return out
	}

	columns := opts.BucketCount
	bucketWidth := float64(windowNS) / float64(columns)
	out.Available = true
	out.BucketMS = bucketWidth / 1e6
	out.Columns = columns

	byFunction := map[string]*functionBucketAgg{}
	for _, span := range spans {
		duration := spanDurationNS(span)
		if duration < 0 {
			duration = 0
		}
		function := span.Function
		if function == "" {
			function = "(unknown)"
		}
		category := span.Category
		if category == "" {
			category = "other"
		}

		entry := byFunction[function]
		if entry == nil {
			entry = &functionBucketAgg{
				durations:      make([][]int64, columns),
				categoryTotals: map[string]int64{},
			}
			byFunction[function] = entry
		}
		entry.totalNS += duration
		entry.count++
		entry.categoryTotals[category] += duration

		offset := spanStart(span) - windowStart
		column := int(float64(offset) / bucketWidth)
		if column < 0 {
			column = 0
		}
		if column >= columns {
			column = columns - 1
		}
		entry.durations[column] = append(entry.durations[column], duration)
	}

	type rankedFunction struct {
		name     string
		category string
		entry    *functionBucketAgg
	}
	ranked := make([]rankedFunction, 0, len(byFunction))
	for name, entry := range byFunction {
		if entry.count < opts.MinSamples {
			continue
		}
		ranked = append(ranked, rankedFunction{
			name:     name,
			category: dominantCategory(entry.categoryTotals),
			entry:    entry,
		})
	}
	if len(ranked) == 0 {
		out.Available = false
		out.Reason = fmt.Sprintf("no functions with at least %d samples", opts.MinSamples)
		return out
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].entry.totalNS != ranked[j].entry.totalNS {
			return ranked[i].entry.totalNS > ranked[j].entry.totalNS
		}
		return ranked[i].name < ranked[j].name
	})
	if len(ranked) > opts.TopFunctions {
		ranked = ranked[:opts.TopFunctions]
	}

	for _, item := range ranked {
		band := AggregateBand{
			Function: item.name,
			Category: item.category,
			P50:      make([]float64, columns),
			P95:      make([]float64, columns),
			P99:      make([]float64, columns),
			Count:    make([]int, columns),
		}
		for column := 0; column < columns; column++ {
			durations := item.entry.durations[column]
			band.Count[column] = len(durations)
			if len(durations) == 0 {
				continue
			}
			sort.Slice(durations, func(a, b int) bool { return durations[a] < durations[b] })
			band.P50[column] = float64(percentile(durations, 0.50)) / 1e6
			band.P95[column] = float64(percentile(durations, 0.95)) / 1e6
			band.P99[column] = float64(percentile(durations, 0.99)) / 1e6
			if band.P95[column] > out.MaxP95MS {
				out.MaxP95MS = band.P95[column]
			}
		}
		out.Bands = append(out.Bands, band)
	}
	return out
}

func dominantCategory(totals map[string]int64) string {
	best := "other"
	var bestNS int64 = -1
	for category, totalNS := range totals {
		if totalNS > bestNS || (totalNS == bestNS && category < best) {
			best = category
			bestNS = totalNS
		}
	}
	return best
}

// BuildTailCohort compares the p99 slow cohort with the p45-p55 typical
// cohort. Rows report function/category share differences and are sorted by
// absolute delta. Blocking averages only use spans whose pointer fields were
// actually measured; an unmeasured cohort stays at zero with a note.
func BuildTailCohort(spans []runlog.Span, opts CohortOptions) TailCohortData {
	opts.normalize()
	out := TailCohortData{Rows: []CohortRow{}, Notes: []string{}}
	out.Notes = append(out.Notes, "cohort 是相关性描述，不是因果证明：慢样本与某个函数/类别同现，不代表该函数或类别导致变慢。")
	if len(spans) == 0 {
		out.Reason = "no spans"
		return out
	}

	durations := make([]int64, 0, len(spans))
	for _, span := range spans {
		durations = append(durations, spanDurationNS(span))
	}
	sorted := append([]int64(nil), durations...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	thresholdNS := percentile(sorted, opts.SlowFraction)
	typicalLowNS := percentile(sorted, 0.45)
	typicalHighNS := percentile(sorted, 0.55)
	out.ThresholdMS = float64(thresholdNS) / 1e6

	slow := make([]runlog.Span, 0, len(spans))
	typical := make([]runlog.Span, 0, len(spans))
	for _, span := range spans {
		duration := spanDurationNS(span)
		if duration >= thresholdNS {
			slow = append(slow, span)
		}
		if duration >= typicalLowNS && duration <= typicalHighNS {
			typical = append(typical, span)
		}
	}
	out.SlowCount = len(slow)
	out.TypicalCount = len(typical)

	if len(spans) < opts.MinSamples {
		out.Reason = fmt.Sprintf("insufficient samples: %d < %d", len(spans), opts.MinSamples)
		return out
	}
	if len(slow) < opts.MinSlow {
		out.Reason = fmt.Sprintf("slow cohort too small: %d < %d at p%.1f threshold %.3f ms", len(slow), opts.MinSlow, opts.SlowFraction*100, out.ThresholdMS)
		return out
	}
	if len(typical) == 0 {
		out.Reason = "typical cohort is empty (p45-p55)"
		return out
	}
	out.Available = true

	slowStats := buildCohortStats(slow)
	typicalStats := buildCohortStats(typical)
	rows := make([]CohortRow, 0, len(slowStats.functions)+len(typicalStats.functions)+len(slowStats.categories)+len(typicalStats.categories))
	rows = append(rows, cohortRows("function", slowStats.functions, typicalStats.functions, len(slow), len(typical))...)
	rows = append(rows, cohortRows("category", slowStats.categories, typicalStats.categories, len(slow), len(typical))...)
	sort.Slice(rows, func(i, j int) bool {
		left := math.Abs(rows[i].DeltaPct)
		right := math.Abs(rows[j].DeltaPct)
		if left != right {
			return left > right
		}
		if rows[i].DeltaPct != rows[j].DeltaPct {
			return rows[i].DeltaPct > rows[j].DeltaPct
		}
		if rows[i].Kind != rows[j].Kind {
			return rows[i].Kind < rows[j].Kind
		}
		return rows[i].Label < rows[j].Label
	})
	if len(rows) > defaultCohortTopRows {
		rows = rows[:defaultCohortTopRows]
	}
	out.Rows = rows

	var slowOffSamples, typicalOffSamples, slowFutexSamples, typicalFutexSamples int
	out.SlowOffCPUMS, slowOffSamples = averageOptionalMS(slow, func(span runlog.Span) *int64 { return span.OffCPUNS })
	out.TypicalOffCPUMS, typicalOffSamples = averageOptionalMS(typical, func(span runlog.Span) *int64 { return span.OffCPUNS })
	out.SlowFutexMS, slowFutexSamples = averageOptionalMS(slow, func(span runlog.Span) *int64 { return span.FutexNS })
	out.TypicalFutexMS, typicalFutexSamples = averageOptionalMS(typical, func(span runlog.Span) *int64 { return span.FutexNS })

	out.Notes = append(out.Notes, fmt.Sprintf("slow 使用 %.0f%% nearest-rank 阈值；耗时并列时 slow 组占比可能高于 %.0f%%。", opts.SlowFraction*100, (1-opts.SlowFraction)*100))
	out.Notes = append(out.Notes, fmt.Sprintf("off-CPU 测量样本：slow %d/%d，typical %d/%d；futex 测量样本：slow %d/%d，typical %d/%d。", slowOffSamples, len(slow), typicalOffSamples, len(typical), slowFutexSamples, len(slow), typicalFutexSamples, len(typical)))
	if slowOffSamples == 0 || typicalOffSamples == 0 {
		out.Notes = append(out.Notes, "off-CPU 未完整测量：SlowOffCPUMS/TypicalOffCPUMS 中为 0 的 cohort 表示没有测量值，不是 0 ms。")
	}
	if slowFutexSamples == 0 || typicalFutexSamples == 0 {
		out.Notes = append(out.Notes, "futex 未完整测量：SlowFutexMS/TypicalFutexMS 中为 0 的 cohort 表示没有测量值，不是 0 ms。")
	}
	return out
}

type cohortStats struct {
	functions  map[string]int
	categories map[string]int
}

func buildCohortStats(spans []runlog.Span) cohortStats {
	stats := cohortStats{
		functions:  map[string]int{},
		categories: map[string]int{},
	}
	for _, span := range spans {
		function := span.Function
		if function == "" {
			function = "(unknown)"
		}
		category := span.Category
		if category == "" {
			category = "other"
		}
		stats.functions[function]++
		stats.categories[category]++
	}
	return stats
}

func cohortRows(kind string, slowCounts, typicalCounts map[string]int, slowTotal, typicalTotal int) []CohortRow {
	labels := map[string]struct{}{}
	for label := range slowCounts {
		labels[label] = struct{}{}
	}
	for label := range typicalCounts {
		labels[label] = struct{}{}
	}
	rows := make([]CohortRow, 0, len(labels))
	for label := range labels {
		slowPct := percentage(slowCounts[label], slowTotal)
		typicalPct := percentage(typicalCounts[label], typicalTotal)
		rows = append(rows, CohortRow{
			Label:      label,
			Kind:       kind,
			SlowPct:    slowPct,
			TypicalPct: typicalPct,
			DeltaPct:   slowPct - typicalPct,
		})
	}
	return rows
}

func percentage(part, total int) float64 {
	if total == 0 {
		return 0
	}
	return float64(part) * 100 / float64(total)
}

func averageOptionalMS(spans []runlog.Span, value func(runlog.Span) *int64) (float64, int) {
	var sum int64
	samples := 0
	for _, span := range spans {
		v := value(span)
		if v == nil {
			continue
		}
		sum += *v
		samples++
	}
	if samples == 0 {
		return 0, 0
	}
	return float64(sum) / float64(samples) / 1e6, samples
}
