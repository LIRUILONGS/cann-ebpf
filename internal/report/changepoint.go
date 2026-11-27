package report

import (
	"fmt"
	"math"
	"sort"
)

const (
	// DefaultChangePointMinSamples is the v2 contract's minimum number of
	// valid metric points for a change-point view.
	DefaultChangePointMinSamples = 30
	// DefaultChangePointWindow is the minimum segment length used by the
	// detector and the number of samples used for before/after summaries.
	DefaultChangePointWindow = 30
	// DefaultChangePointThreshold is the penalty strength in robust standard
	// deviation units. Larger values produce fewer change points.
	DefaultChangePointThreshold = 5.0
	// DefaultChangePointMinRunDurationMS is the v2 contract's minimum run
	// duration for a change-point view (five minutes).
	DefaultChangePointMinRunDurationMS = 5 * 60 * 1000
)

// ChangePointOptions controls BuildChangePoint. Zero values select the
// documented defaults. MinSamples is never lowered below 30 because the v2
// contract treats shorter evidence as unavailable.
type ChangePointOptions struct {
	MinSamples       int
	Window           int
	Threshold        float64
	MinRunDurationMS float64
}

func (o ChangePointOptions) withDefaults() ChangePointOptions {
	if o.MinSamples < DefaultChangePointMinSamples {
		o.MinSamples = DefaultChangePointMinSamples
	}
	if o.Window <= 0 {
		o.Window = DefaultChangePointWindow
	} else if o.Window < 2 {
		o.Window = 2
	}
	if o.Threshold <= 0 {
		o.Threshold = DefaultChangePointThreshold
	}
	if o.MinRunDurationMS <= 0 {
		o.MinRunDurationMS = DefaultChangePointMinRunDurationMS
	}
	return o
}

// BuildChangePoint detects mean shifts in long metric series with a
// PELT-like penalized least-squares segmentation.
//
// Method:
//  1. Each series is sorted by timestamp and restricted to finite points.
//  2. Values are standardized by a robust scale estimated from the median
//     absolute deviation (MAD) of first differences.
//  3. A PELT-style dynamic program minimizes within-segment squared error
//     plus a BIC-like penalty of threshold^2 * scale^2 * ln(n) per segment.
//     Window is the minimum segment length, and Threshold is the penalty
//     strength in robust standard deviations.
//  4. Before/after values and score are computed from up to Window samples
//     immediately on each side of every detected boundary.
//
// Events are returned only as annotations. They neither seed nor validate
// change points and must not be interpreted as causal evidence.
func BuildChangePoint(series []MetricSeries, events []ChangeEvent, opts ChangePointOptions) ChangePointData {
	opts = opts.withDefaults()
	method := changePointMethod(opts)
	out := ChangePointData{
		Status:     ViewStatus{Available: false},
		Series:     []ChangeSeries{},
		Points:     []ChangePoint{},
		Events:     []ChangeEvent{},
		Method:     method,
		MinSamples: opts.MinSamples,
	}
	if len(series) == 0 {
		out.Status = changePointUnavailable("no metric series")
		return out
	}

	cleaned := make([]changePointMetricSeries, 0, len(series))
	totalPoints := 0
	firstMS := math.Inf(1)
	lastMS := math.Inf(-1)
	for _, item := range series {
		samples := cleanChangePointSamples(item)
		if len(samples) == 0 {
			continue
		}
		totalPoints += len(samples)
		if samples[0].timeMS < firstMS {
			firstMS = samples[0].timeMS
		}
		if samples[len(samples)-1].timeMS > lastMS {
			lastMS = samples[len(samples)-1].timeMS
		}
		cleaned = append(cleaned, changePointMetricSeries{
			name:    changePointSeriesName(item),
			samples: samples,
		})
	}
	if totalPoints == 0 {
		out.Status = changePointUnavailable("no valid metric points")
		return out
	}

	durationMS := lastMS - firstMS
	tooFew := totalPoints < opts.MinSamples
	tooShort := durationMS < opts.MinRunDurationMS
	switch {
	case tooFew && tooShort:
		out.Status = changePointUnavailable(fmt.Sprintf(
			"run too short and insufficient valid points: %d < %d valid points, %.0f ms < %.0f ms",
			totalPoints, opts.MinSamples, durationMS, opts.MinRunDurationMS,
		))
		return out
	case tooFew:
		out.Status = changePointUnavailable(fmt.Sprintf(
			"insufficient valid points: %d < %d", totalPoints, opts.MinSamples,
		))
		return out
	case tooShort:
		out.Status = changePointUnavailable(fmt.Sprintf(
			"run too short: %.0f ms < %.0f ms", durationMS, opts.MinRunDurationMS,
		))
		return out
	}

	out.Status = ViewStatus{Available: true, Source: "metrics", Confidence: "inferred"}
	out.Events = normalizeChangePointEvents(events)
	out.Series = buildChangePointSeries(cleaned)
	for _, metric := range cleaned {
		scale := changePointScaleFromSamples(metric.samples)
		for _, boundary := range peltChangePointBoundaries(metric.samples, opts) {
			before, after, deltaPct, score := summarizeChangePoint(metric.samples, boundary, opts.Window, scale)
			out.Points = append(out.Points, ChangePoint{
				TimeMS:   metric.samples[boundary].timeMS,
				Metric:   metric.name,
				Before:   before,
				After:    after,
				DeltaPct: deltaPct,
				Score:    score,
				Method:   method,
			})
		}
	}
	sort.SliceStable(out.Points, func(i, j int) bool {
		if out.Points[i].TimeMS == out.Points[j].TimeMS {
			return out.Points[i].Metric < out.Points[j].Metric
		}
		return out.Points[i].TimeMS < out.Points[j].TimeMS
	})
	return out
}

func changePointUnavailable(reason string) ViewStatus {
	return ViewStatus{Available: false, Reason: reason, Source: "none"}
}

func changePointMethod(opts ChangePointOptions) string {
	return fmt.Sprintf(
		"pelt-like(window=%d,threshold=%g,min_samples=%d,min_run_ms=%g)",
		opts.Window, opts.Threshold, opts.MinSamples, opts.MinRunDurationMS,
	)
}

type changePointSample struct {
	timeMS float64
	value  float64
}

type changePointMetricSeries struct {
	name    string
	samples []changePointSample
}

func cleanChangePointSamples(series MetricSeries) []changePointSample {
	count := len(series.T)
	if len(series.V) < count {
		count = len(series.V)
	}
	samples := make([]changePointSample, 0, count)
	for i := 0; i < count; i++ {
		t, v := series.T[i], series.V[i]
		if math.IsNaN(t) || math.IsInf(t, 0) || math.IsNaN(v) || math.IsInf(v, 0) {
			continue
		}
		samples = append(samples, changePointSample{timeMS: t, value: v})
	}
	sort.SliceStable(samples, func(i, j int) bool {
		return samples[i].timeMS < samples[j].timeMS
	})
	return samples
}

func changePointSeriesName(series MetricSeries) string {
	if series.Name != "" {
		return series.Name
	}
	if series.ID != "" {
		return series.ID
	}
	return "metric"
}

func buildChangePointSeries(series []changePointMetricSeries) []ChangeSeries {
	out := make([]ChangeSeries, 0, len(series))
	for _, item := range series {
		values := make([]float64, len(item.samples))
		for i := range item.samples {
			values[i] = item.samples[i].value
		}
		out = append(out, ChangeSeries{Name: item.name, Values: values})
	}
	return out
}

func normalizeChangePointEvents(events []ChangeEvent) []ChangeEvent {
	out := make([]ChangeEvent, 0, len(events))
	for _, event := range events {
		if math.IsNaN(event.TimeMS) || math.IsInf(event.TimeMS, 0) {
			continue
		}
		out = append(out, event)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].TimeMS < out[j].TimeMS
	})
	return out
}

// peltChangePointBoundaries returns sample indexes at which a new segment
// starts. The implementation follows the PELT dynamic program: each candidate
// start keeps the minimum penalized cost, and candidates that cannot improve a
// future segmentation are pruned. Window enforces the minimum segment size.
func peltChangePointBoundaries(samples []changePointSample, opts ChangePointOptions) []int {
	n := len(samples)
	if n < 2*opts.Window {
		return nil
	}

	values := make([]float64, n)
	maxAbs := 0.0
	for i, sample := range samples {
		values[i] = sample.value
		if abs := math.Abs(sample.value); abs > maxAbs {
			maxAbs = abs
		}
	}
	center := medianChangePointValue(values)
	sigma := changePointScale(values, maxAbs)
	prefixSum := make([]float64, n+1)
	prefixSquares := make([]float64, n+1)
	for i, value := range values {
		z := (value - center) / sigma
		prefixSum[i+1] = prefixSum[i] + z
		prefixSquares[i+1] = prefixSquares[i] + z*z
	}

	penalty := opts.Threshold * opts.Threshold * math.Log(float64(n))
	if penalty <= 0 {
		penalty = math.SmallestNonzeroFloat64
	}
	cost := func(start, end int) float64 {
		count := float64(end - start)
		if count <= 0 {
			return 0
		}
		sum := prefixSum[end] - prefixSum[start]
		segmentCost := prefixSquares[end] - prefixSquares[start] - sum*sum/count
		if segmentCost < 0 {
			return 0
		}
		return segmentCost
	}

	bestCost := make([]float64, n+1)
	previous := make([]int, n+1)
	for i := 1; i <= n; i++ {
		bestCost[i] = math.Inf(1)
		previous[i] = -1
	}
	candidates := []int{0}
	for end := opts.Window; end <= n; end++ {
		best := math.Inf(1)
		bestStart := -1
		for _, start := range candidates {
			if end-start < opts.Window {
				continue
			}
			value := bestCost[start] + cost(start, end)
			if value < best {
				best = value
				bestStart = start
			}
		}
		if bestStart < 0 {
			continue
		}
		bestCost[end] = best + penalty
		previous[end] = bestStart

		kept := make([]int, 0, len(candidates)+1)
		for _, start := range candidates {
			if end-start < opts.Window || bestCost[start]+cost(start, end) < bestCost[end] {
				kept = append(kept, start)
			}
		}
		candidates = append(kept, end)
	}

	if previous[n] < 0 {
		return nil
	}
	var boundaries []int
	for end := n; end > 0; {
		start := previous[end]
		if start <= 0 {
			break
		}
		boundaries = append(boundaries, start)
		end = start
	}
	for left, right := 0, len(boundaries)-1; left < right; left, right = left+1, right-1 {
		boundaries[left], boundaries[right] = boundaries[right], boundaries[left]
	}
	return boundaries
}

func robustChangePointSigma(values []float64) float64 {
	if len(values) < 2 {
		return 0
	}
	differences := make([]float64, len(values)-1)
	for i := 1; i < len(values); i++ {
		differences[i-1] = values[i] - values[i-1]
	}
	center := medianChangePointValue(differences)
	absolute := make([]float64, len(differences))
	for i, value := range differences {
		absolute[i] = math.Abs(value - center)
	}
	mad := medianChangePointValue(absolute)
	if mad <= 0 || math.IsNaN(mad) || math.IsInf(mad, 0) {
		return 0
	}
	return 1.4826 * mad / math.Sqrt2
}

// changePointScale returns the robust first-difference scale, with a floor
// relative to the global series variance. The floor keeps exact or nearly
// noiseless steps from producing normalized values large enough to erase the
// PELT penalty through floating-point rounding.
func changePointScale(values []float64, maxAbs float64) float64 {
	sigma := robustChangePointSigma(values)
	floor := math.Max(1, maxAbs) * 1e-12
	if len(values) > 0 {
		mean := 0.0
		for _, value := range values {
			mean += value
		}
		mean /= float64(len(values))
		variance := 0.0
		for _, value := range values {
			delta := value - mean
			variance += delta * delta
		}
		variance /= float64(len(values))
		if variance > 0 {
			floor = math.Sqrt(variance) * 1e-6
		}
	}
	if sigma < floor {
		sigma = floor
	}
	if sigma <= 0 || math.IsNaN(sigma) || math.IsInf(sigma, 0) {
		sigma = floor
	}
	return sigma
}

func medianChangePointValue(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	middle := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[middle]
	}
	return (sorted[middle-1] + sorted[middle]) / 2
}

func summarizeChangePoint(samples []changePointSample, boundary, window int, scale float64) (before, after, deltaPct, score float64) {
	leftStart := boundary - window
	if leftStart < 0 {
		leftStart = 0
	}
	rightEnd := boundary + window
	if rightEnd > len(samples) {
		rightEnd = len(samples)
	}
	leftCount := boundary - leftStart
	rightCount := rightEnd - boundary
	if leftCount <= 0 || rightCount <= 0 {
		return 0, 0, 0, 0
	}
	for i := leftStart; i < boundary; i++ {
		before += samples[i].value
	}
	before /= float64(leftCount)
	for i := boundary; i < rightEnd; i++ {
		after += samples[i].value
	}
	after /= float64(rightCount)

	switch {
	case math.Abs(before) > 1e-12:
		deltaPct = (after - before) / math.Abs(before) * 100
	case math.Abs(after) <= 1e-12:
		deltaPct = 0
	case after > 0:
		deltaPct = 100
	default:
		deltaPct = -100
	}

	standardError := scale * math.Sqrt(1/float64(leftCount)+1/float64(rightCount))
	if standardError <= 0 || math.IsNaN(standardError) || math.IsInf(standardError, 0) {
		score = 1e12
	} else {
		score = math.Abs(after-before) / standardError
		if math.IsNaN(score) || math.IsInf(score, 0) || score > 1e12 {
			score = 1e12
		}
	}
	return before, after, deltaPct, score
}

func changePointScaleFromSamples(samples []changePointSample) float64 {
	values := make([]float64, len(samples))
	maxAbs := 0.0
	for i := range samples {
		values[i] = samples[i].value
		if abs := math.Abs(samples[i].value); abs > maxAbs {
			maxAbs = abs
		}
	}
	return changePointScale(values, maxAbs)
}
