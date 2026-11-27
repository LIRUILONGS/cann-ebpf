package report

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

const (
	defaultABMinMetricSamples = 10
	defaultABMinFunctionCalls = 20
	defaultABMinChangePct     = 5.0
)

// ABRunInput is one side of an A/B differential. Metrics and Functions must
// describe the same fixed workload window. Set Incomplete when the caller
// knows the run's manifest or evidence is truncated; incomplete runs are never
// compared.
type ABRunInput struct {
	RunID      string
	WorkloadID string
	Metrics    []MetricSeries
	Functions  []FunctionRow
	Incomplete bool
}

// ABDiffOptions controls the sample-support gates used for significance. A
// metric or function is only listed in Significant when both sides meet the
// configured sample count and the relative change exceeds MinChangePct.
type ABDiffOptions struct {
	MinMetricSamples int
	MinFunctionCalls int
	MinChangePct     float64
}

func (o ABDiffOptions) normalized() ABDiffOptions {
	if o.MinMetricSamples <= 0 {
		o.MinMetricSamples = defaultABMinMetricSamples
	}
	if o.MinFunctionCalls <= 0 {
		o.MinFunctionCalls = defaultABMinFunctionCalls
	}
	if o.MinChangePct <= 0 {
		o.MinChangePct = defaultABMinChangePct
	}
	return o
}

// BuildABDiff compares two complete runs with default sample-support gates.
// The output uses the frozen ABDiffData JSON contract.
func BuildABDiff(a, b ABRunInput) ABDiffData {
	return BuildABDiffWithOptions(a, b, ABDiffOptions{})
}

// BuildABDiffWithOptions compares two complete runs and reports explicit
// metric/function deltas. ADeltaPct is A relative to B, BDeltaPct is B
// relative to A, and Difference is B-A in the metric's native unit. Empty
// workload IDs, mismatched workload IDs and explicitly incomplete runs are
// rejected with status.available=false.
func BuildABDiffWithOptions(a, b ABRunInput, opts ABDiffOptions) ABDiffData {
	opts = opts.normalized()
	data := ABDiffData{
		Status:      ViewStatus{Available: false, Source: "spans+metrics"},
		ARun:        strings.TrimSpace(a.RunID),
		BRun:        strings.TrimSpace(b.RunID),
		Metrics:     []ABMetricDelta{},
		Functions:   []ABFunctionDelta{},
		Significant: []string{},
	}

	if a.Incomplete || b.Incomplete {
		switch {
		case a.Incomplete && b.Incomplete:
			data.Status.Reason = "both runs are incomplete"
		case a.Incomplete:
			data.Status.Reason = "run A is incomplete"
		default:
			data.Status.Reason = "run B is incomplete"
		}
		return data
	}
	workloadA := strings.TrimSpace(a.WorkloadID)
	workloadB := strings.TrimSpace(b.WorkloadID)
	if workloadA == "" || workloadB == "" {
		switch {
		case workloadA == "" && workloadB == "":
			data.Status.Reason = "workload_id is missing for both runs"
		case workloadA == "":
			data.Status.Reason = "workload_id is missing for run A"
		default:
			data.Status.Reason = "workload_id is missing for run B"
		}
		return data
	}
	if workloadA != workloadB {
		data.Status.Reason = fmt.Sprintf("workload_id mismatch: A=%q B=%q", workloadA, workloadB)
		return data
	}
	data.WorkloadID = workloadA
	if len(a.Metrics) == 0 && len(a.Functions) == 0 {
		data.Status.Reason = "run A has no metrics or functions"
		return data
	}
	if len(b.Metrics) == 0 && len(b.Functions) == 0 {
		data.Status.Reason = "run B has no metrics or functions"
		return data
	}

	metricDeltas, metricSignificant := abMetricDeltas(a.Metrics, b.Metrics, opts)
	functionDeltas, functionSignificant := abFunctionDeltas(a.Functions, b.Functions, opts)
	data.Metrics = metricDeltas
	data.Functions = functionDeltas
	if len(data.Metrics) == 0 && len(data.Functions) == 0 {
		data.Status.Reason = "no comparable metrics or functions between runs"
		return data
	}
	data.Significant = abDedupeSorted(append(metricSignificant, functionSignificant...))
	data.Status.Available = true
	if len(data.Metrics) > 0 && len(data.Functions) > 0 {
		data.Status.Source = "spans+metrics"
		data.Status.Confidence = "inferred"
	} else if len(data.Metrics) > 0 {
		data.Status.Source = "metrics"
		data.Status.Confidence = "partial"
	} else {
		data.Status.Source = "spans"
		data.Status.Confidence = "partial"
	}
	return data
}

func abMetricDeltas(seriesA, seriesB []MetricSeries, opts ABDiffOptions) ([]ABMetricDelta, []string) {
	groupsA := abCollectMetricGroups(seriesA)
	groupsB := abCollectMetricGroups(seriesB)
	names := make([]string, 0, len(groupsA))
	for name := range groupsA {
		if _, ok := groupsB[name]; ok {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	deltas := make([]ABMetricDelta, 0, len(names))
	var significant []string
	for _, name := range names {
		valuesA, valuesB := groupsA[name], groupsB[name]
		meanA := abMean(valuesA)
		meanB := abMean(valuesB)
		delta := ABMetricDelta{
			Name:       name,
			ADeltaPct:  abPctChange(meanA, meanB),
			BDeltaPct:  abPctChange(meanB, meanA),
			Difference: meanB - meanA,
		}
		deltas = append(deltas, delta)
		if len(valuesA) >= opts.MinMetricSamples && len(valuesB) >= opts.MinMetricSamples &&
			math.Abs(delta.BDeltaPct) >= opts.MinChangePct && abWelchSignificant(valuesA, valuesB) {
			significant = append(significant, name)
		}
	}
	return deltas, significant
}

func abCollectMetricGroups(series []MetricSeries) map[string][]float64 {
	groups := make(map[string][]float64)
	for _, item := range series {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			continue
		}
		var values []float64
		if strings.EqualFold(strings.TrimSpace(item.Kind), "counter") {
			values = abCounterRateSamples(item)
		} else {
			values = abFiniteValues(item.V)
		}
		if len(values) == 0 {
			continue
		}
		groups[name] = append(groups[name], values...)
	}
	return groups
}

func abCounterRateSamples(series MetricSeries) []float64 {
	if len(series.T) < 2 || len(series.V) < 2 {
		return nil
	}
	count := len(series.T)
	if len(series.V) < count {
		count = len(series.V)
	}
	var rates []float64
	for i := 1; i < count; i++ {
		deltaSeconds := (series.T[i] - series.T[i-1]) / 1000
		if deltaSeconds <= 0 {
			continue
		}
		delta := series.V[i] - series.V[i-1]
		if delta < 0 {
			continue
		}
		rates = append(rates, delta/deltaSeconds)
	}
	return rates
}

type abFunctionGroup struct {
	count   int
	totalMS float64
	p50MS   float64
	p95MS   float64
	maxMS   float64
}

func (group *abFunctionGroup) add(row FunctionRow) {
	group.count += row.Count
	group.totalMS += row.TotalMS
	if row.P50MS > group.p50MS {
		group.p50MS = row.P50MS
	}
	if row.P95MS > group.p95MS {
		group.p95MS = row.P95MS
	}
	if row.MaxMS > group.maxMS {
		group.maxMS = row.MaxMS
	}
}

func (group *abFunctionGroup) meanMS() float64 {
	if group.count > 0 {
		return group.totalMS / float64(group.count)
	}
	if group.p95MS > 0 {
		return group.p95MS
	}
	return group.p50MS
}

func abFunctionDeltas(rowsA, rowsB []FunctionRow, opts ABDiffOptions) ([]ABFunctionDelta, []string) {
	groupsA := abCollectFunctionGroups(rowsA)
	groupsB := abCollectFunctionGroups(rowsB)
	names := make([]string, 0, len(groupsA))
	for name := range groupsA {
		if _, ok := groupsB[name]; ok {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	deltas := make([]ABFunctionDelta, 0, len(names))
	var significant []string
	for _, name := range names {
		groupA, groupB := groupsA[name], groupsB[name]
		meanA := groupA.meanMS()
		meanB := groupB.meanMS()
		delta := ABFunctionDelta{
			Function:   name,
			ADeltaPct:  abPctChange(meanA, meanB),
			BDeltaPct:  abPctChange(meanB, meanA),
			Difference: meanB - meanA,
		}
		deltas = append(deltas, delta)
		if groupA.count >= opts.MinFunctionCalls && groupB.count >= opts.MinFunctionCalls &&
			math.Abs(delta.BDeltaPct) >= opts.MinChangePct {
			significant = append(significant, name)
		}
	}
	return deltas, significant
}

func abCollectFunctionGroups(rows []FunctionRow) map[string]*abFunctionGroup {
	groups := make(map[string]*abFunctionGroup)
	for _, row := range rows {
		name := strings.TrimSpace(row.Function)
		if name == "" {
			continue
		}
		group := groups[name]
		if group == nil {
			group = &abFunctionGroup{}
			groups[name] = group
		}
		group.add(row)
	}
	return groups
}

// abPctChange returns percentage change of value relative to reference. A
// zero reference has no defined percentage change, so it returns zero rather
// than inf/NaN that would break JSON serialization.
func abPctChange(value, reference float64) float64 {
	if reference == 0 {
		return 0
	}
	return (value - reference) / math.Abs(reference) * 100
}

func abMean(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	var sum float64
	for _, value := range values {
		sum += value
	}
	return sum / float64(len(values))
}

func abMeanVariance(values []float64) (float64, float64) {
	if len(values) == 0 {
		return 0, 0
	}
	mean := abMean(values)
	if len(values) < 2 {
		return mean, 0
	}
	var sumSquares float64
	for _, value := range values {
		delta := value - mean
		sumSquares += delta * delta
	}
	return mean, sumSquares / float64(len(values)-1)
}

// abWelchSignificant applies a Welch t-statistic with a normal-approximation
// cutoff. It is intentionally conservative: both sides must already have the
// configured sample support, and when the variance is zero only a real mean
// difference can pass. The caller records the cutoff in the status confidence
// as inferred rather than exact.
func abWelchSignificant(valuesA, valuesB []float64) bool {
	if len(valuesA) < 2 || len(valuesB) < 2 {
		return false
	}
	meanA, varianceA := abMeanVariance(valuesA)
	meanB, varianceB := abMeanVariance(valuesB)
	denominator := math.Sqrt(varianceA/float64(len(valuesA)) + varianceB/float64(len(valuesB)))
	if denominator == 0 {
		return meanA != meanB
	}
	return math.Abs(meanA-meanB)/denominator >= 2.0
}

func abFiniteValues(values []float64) []float64 {
	out := make([]float64, 0, len(values))
	for _, value := range values {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			continue
		}
		out = append(out, value)
	}
	return out
}

func abDedupeSorted(values []string) []string {
	if len(values) == 0 {
		return []string{}
	}
	sort.Strings(values)
	out := values[:0]
	for _, value := range values {
		if len(out) == 0 || out[len(out)-1] != value {
			out = append(out, value)
		}
	}
	return out
}
