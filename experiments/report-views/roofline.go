package report

import (
	"fmt"
	"math"
	"strings"
)

// RooflineOp is one per-operation record from ops.jsonl.
//
// DurationNS, Flops and Bytes are all mandatory for a point. Zero or negative
// values mean the field was absent or unusable, so the record is skipped. No
// value may be estimated from model parameter counts, aggregate vLLM metrics,
// or the theoretical peak used to draw the roof.
type RooflineOp struct {
	RunID      string  `json:"run_id,omitempty"`
	Op         string  `json:"op"`
	Category   string  `json:"category,omitempty"`
	DurationNS int64   `json:"duration_ns"`
	Flops      float64 `json:"flops"`
	Bytes      float64 `json:"bytes"`
	DType      string  `json:"dtype,omitempty"`
}

// RooflineOptions supplies the explicitly recorded reference ceiling. The
// peak values define the roof and are never emitted as measured points. DType
// identifies the precision those peaks apply to.
type RooflineOptions struct {
	PeakTFLOPS float64
	PeakGBPS   float64
	DType      string
	Source     string
}

const defaultRooflineSource = "ops.jsonl"

// BuildRoofline converts per-op evidence into roofline points. It returns an
// unavailable view when no op has complete duration/flops/bytes evidence; it
// never fills gaps with theoretical values or aggregate estimates.
func BuildRoofline(ops []RooflineOp, opts RooflineOptions) RooflineData {
	peakTFLOPS := opts.PeakTFLOPS
	if !rooflineFinite(peakTFLOPS) {
		peakTFLOPS = 0
	}
	peakGBPS := opts.PeakGBPS
	if !rooflineFinite(peakGBPS) {
		peakGBPS = 0
	}
	data := RooflineData{
		PeakTFLOPS: peakTFLOPS,
		PeakGBPS:   peakGBPS,
		DType:      strings.TrimSpace(opts.DType),
		Points:     []RooflinePoint{},
	}
	if len(ops) == 0 {
		data.Status = ViewStatus{Available: false, Reason: "no ops evidence", Source: "none"}
		return data
	}

	source := strings.TrimSpace(opts.Source)
	if source == "" {
		source = defaultRooflineSource
	}

	complete := make([]RooflineOp, 0, len(ops))
	skipped := 0
	for _, op := range ops {
		if !rooflineOpComplete(op) {
			skipped++
			continue
		}
		complete = append(complete, op)
	}
	if len(complete) == 0 {
		data.Status = ViewStatus{
			Available: false,
			Reason:    "no per-op duration/flops/bytes evidence",
			Source:    source,
		}
		return data
	}

	dtype := data.DType
	if dtype == "" {
		dtype = rooflineSingleDType(complete)
		if dtype == "" {
			data.Status = ViewStatus{
				Available: false,
				Reason:    "dtype is required to record roofline peak values",
				Source:    source,
			}
			return data
		}
	}
	data.DType = dtype
	if !rooflineFinitePositive(peakTFLOPS) || !rooflineFinitePositive(peakGBPS) {
		data.Status = ViewStatus{
			Available: false,
			Reason:    "peak_tflops and peak_gbps must be explicit positive reference values",
			Source:    source,
		}
		return data
	}

	ridge := rooflineRidgeIntensity(peakTFLOPS, peakGBPS)
	points := make([]RooflinePoint, 0, len(complete))
	for _, op := range complete {
		if opDType := strings.TrimSpace(op.DType); opDType != "" && !strings.EqualFold(opDType, dtype) {
			skipped++
			continue
		}
		seconds := float64(op.DurationNS) / 1e9
		achievedTFLOPS := op.Flops / seconds / 1e12
		achievedGBPS := op.Bytes / seconds / 1e9
		intensity := op.Flops / op.Bytes
		if !rooflineFinite(achievedTFLOPS) || !rooflineFinite(achievedGBPS) || !rooflineFinite(intensity) {
			skipped++
			continue
		}
		bound := "memory"
		if intensity >= ridge {
			bound = "compute"
		}
		points = append(points, RooflinePoint{
			Op:                  strings.TrimSpace(op.Op),
			Category:            strings.TrimSpace(op.Category),
			DurationMS:          float64(op.DurationNS) / 1e6,
			Flops:               op.Flops,
			Bytes:               op.Bytes,
			ArithmeticIntensity: intensity,
			AchievedTFLOPS:      achievedTFLOPS,
			AchievedGBPS:        achievedGBPS,
			Bound:               bound,
		})
	}
	if len(points) == 0 {
		data.Status = ViewStatus{
			Available: false,
			Reason:    fmt.Sprintf("no roofline points match dtype %q with complete per-op evidence", dtype),
			Source:    source,
		}
		return data
	}

	status := ViewStatus{Available: true, Source: source, Confidence: "exact"}
	if skipped > 0 {
		status.Confidence = "partial"
		status.Reason = fmt.Sprintf("%d op record(s) skipped: incomplete per-op evidence or dtype mismatch", skipped)
	}
	data.Status = status
	data.Points = points
	return data
}

// rooflineRidgeIntensity returns the ridge point in FLOPs/byte. Peak throughput
// is expressed in TFLOPS (1e12 FLOPs/s) while bandwidth is in GB/s (1e9 B/s), so
// the ratio of the two carries a 1e3 factor: 400 TFLOPS / 1600 GB/s must yield
// 250 FLOPs/byte, not 0.25.
func rooflineRidgeIntensity(peakTFLOPS, peakGBPS float64) float64 {
	return peakTFLOPS * 1000 / peakGBPS
}

func rooflineOpComplete(op RooflineOp) bool {
	if strings.TrimSpace(op.Op) == "" || op.DurationNS <= 0 {
		return false
	}
	return rooflineFinitePositive(op.Flops) && rooflineFinitePositive(op.Bytes)
}

// rooflineSingleDType returns the one dtype shared by all non-empty op dtypes.
// Mixed dtypes cannot share one peak pair, so the caller treats an empty
// result as unresolved evidence rather than guessing.
func rooflineSingleDType(ops []RooflineOp) string {
	dtype := ""
	for _, op := range ops {
		current := strings.TrimSpace(op.DType)
		if current == "" {
			continue
		}
		if dtype == "" {
			dtype = current
			continue
		}
		if !strings.EqualFold(dtype, current) {
			return ""
		}
	}
	return dtype
}

func rooflineFinitePositive(value float64) bool {
	return value > 0 && rooflineFinite(value)
}

func rooflineFinite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}
