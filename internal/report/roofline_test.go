package report

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func TestBuildRooflineEmpty(t *testing.T) {
	data := BuildRoofline(nil, RooflineOptions{PeakTFLOPS: 400, PeakGBPS: 1600, DType: "bf16"})
	if data.Status.Available {
		t.Fatal("empty roofline must be unavailable")
	}
	if data.Status.Reason == "" {
		t.Fatal("unavailable roofline needs a reason")
	}
	if data.Status.Source != "none" {
		t.Fatalf("empty roofline source = %q, want none", data.Status.Source)
	}
	if data.Status.Confidence != "" {
		t.Fatalf("unavailable roofline must not claim confidence: %q", data.Status.Confidence)
	}
	if data.Points == nil || len(data.Points) != 0 {
		t.Fatalf("empty roofline points = %#v, want empty non-nil slice", data.Points)
	}
	if data.DType != "bf16" || data.PeakTFLOPS != 400 || data.PeakGBPS != 1600 {
		t.Fatalf("explicit dtype/peaks not recorded: %+v", data)
	}
}

func TestBuildRooflineMissingPerOpEvidence(t *testing.T) {
	ops := []RooflineOp{
		{Op: "matmul"},
		{Op: "memcpy", DurationNS: 1_000_000, Flops: 10},
		{Op: "add", DurationNS: 1_000_000, Bytes: 10},
		{DurationNS: 1_000_000, Flops: 10, Bytes: 10},
	}
	data := BuildRoofline(ops, RooflineOptions{PeakTFLOPS: 400, PeakGBPS: 1600, DType: "bf16"})
	if data.Status.Available {
		t.Fatal("roofline without complete per-op fields must be unavailable")
	}
	if !strings.Contains(data.Status.Reason, "per-op") {
		t.Fatalf("reason = %q, want per-op explanation", data.Status.Reason)
	}
	if len(data.Points) != 0 {
		t.Fatalf("missing evidence produced %d points", len(data.Points))
	}
}

func TestBuildRooflineValidPoints(t *testing.T) {
	ops := []RooflineOp{
		{
			RunID: "synthetic-run", Op: "matmul", Category: "gemm",
			DurationNS: 2_000_000_000, Flops: 1e13, Bytes: 1e9, DType: "bf16",
		},
		{
			RunID: "synthetic-run", Op: "memcpy", Category: "movement",
			DurationNS: 100_000_000, Flops: 1e9, Bytes: 1e10, DType: "bf16",
		},
	}
	data := BuildRoofline(ops, RooflineOptions{PeakTFLOPS: 400, PeakGBPS: 1600, DType: "bf16"})
	if !data.Status.Available {
		t.Fatalf("valid roofline unavailable: %+v", data.Status)
	}
	if data.Status.Confidence != "exact" || data.Status.Source != "ops.jsonl" || data.Status.Reason != "" {
		t.Fatalf("status = %+v", data.Status)
	}
	if data.DType != "bf16" || data.PeakTFLOPS != 400 || data.PeakGBPS != 1600 {
		t.Fatalf("explicit dtype/peaks = %+v", data)
	}
	if len(data.Points) != 2 {
		t.Fatalf("points = %d, want 2", len(data.Points))
	}

	byOp := make(map[string]RooflinePoint, len(data.Points))
	for _, point := range data.Points {
		byOp[point.Op] = point
	}
	matmul := byOp["matmul"]
	if !rooflineCloseEnough(matmul.ArithmeticIntensity, 1e4) ||
		!rooflineCloseEnough(matmul.AchievedTFLOPS, 5) ||
		!rooflineCloseEnough(matmul.AchievedGBPS, 0.5) ||
		matmul.Bound != "compute" ||
		!rooflineCloseEnough(matmul.DurationMS, 2000) {
		t.Fatalf("matmul point = %+v", matmul)
	}
	memcpy := byOp["memcpy"]
	if !rooflineCloseEnough(memcpy.ArithmeticIntensity, 0.1) ||
		!rooflineCloseEnough(memcpy.AchievedTFLOPS, 0.01) ||
		!rooflineCloseEnough(memcpy.AchievedGBPS, 100) ||
		memcpy.Bound != "memory" ||
		!rooflineCloseEnough(memcpy.DurationMS, 100) {
		t.Fatalf("memcpy point = %+v", memcpy)
	}

	payload, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(payload), `"points":[{`) {
		t.Fatalf("serialized points are not an array: %s", payload)
	}
}

func TestBuildRooflinePartialSkipsIncompleteRecords(t *testing.T) {
	ops := []RooflineOp{
		{Op: "matmul", DurationNS: 1_000_000_000, Flops: 1e12, Bytes: 1e9, DType: "bf16"},
		{Op: "missing-bytes", DurationNS: 1_000_000_000, Flops: 1e12, DType: "bf16"},
		{Op: "wrong-dtype", DurationNS: 1_000_000_000, Flops: 1e12, Bytes: 1e9, DType: "fp16"},
	}
	data := BuildRoofline(ops, RooflineOptions{PeakTFLOPS: 400, PeakGBPS: 1600, DType: "bf16"})
	if !data.Status.Available {
		t.Fatalf("partial roofline unavailable: %+v", data.Status)
	}
	if data.Status.Confidence != "partial" {
		t.Fatalf("confidence = %q, want partial", data.Status.Confidence)
	}
	if !strings.Contains(data.Status.Reason, "skipped") {
		t.Fatalf("reason = %q, want skipped records note", data.Status.Reason)
	}
	if len(data.Points) != 1 || data.Points[0].Op != "matmul" {
		t.Fatalf("points = %+v, want only valid matmul", data.Points)
	}
}

func TestBuildRooflineRequiresPeaks(t *testing.T) {
	ops := []RooflineOp{{Op: "matmul", DurationNS: 1_000_000_000, Flops: 1e12, Bytes: 1e9, DType: "bf16"}}
	data := BuildRoofline(ops, RooflineOptions{DType: "bf16"})
	if data.Status.Available {
		t.Fatal("roofline without explicit peaks must be unavailable")
	}
	if !strings.Contains(data.Status.Reason, "peak") {
		t.Fatalf("reason = %q, want peak explanation", data.Status.Reason)
	}
}

func rooflineCloseEnough(got, want float64) bool {
	scale := math.Max(1, math.Abs(want))
	return math.Abs(got-want) <= 1e-9*scale
}
