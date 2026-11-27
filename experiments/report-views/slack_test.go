package report

import (
	"fmt"
	"strings"
	"testing"
)

// All fixtures in this file are synthetic. They do not read real run evidence.
func syntheticSlackRootNodes(count int) []CriticalPathNode {
	nodes := make([]CriticalPathNode, 0, count)
	for i := 0; i < count; i++ {
		nodes = append(nodes, CriticalPathNode{
			ID:         fmt.Sprintf("synthetic-slack-%02d", i),
			Label:      "synthetic slack node",
			Category:   "synthetic",
			Kind:       "host",
			StartMS:    0,
			DurationMS: float64(i + 1),
		})
	}
	return nodes
}

func TestBuildSlackCDFUnavailableSynthetic(t *testing.T) {
	cp := CriticalPathData{
		Status: ViewStatus{Available: false, Reason: "not implemented", Source: "none"},
		Nodes:  []CriticalPathNode{{ID: "ignored", DurationMS: 1}},
	}
	result := BuildSlackCDF(cp)
	if result.Status.Available {
		t.Fatal("unavailable critical path must make slack CDF unavailable")
	}
	if !strings.Contains(result.Status.Reason, "critical path unavailable") {
		t.Fatalf("reason = %q", result.Status.Reason)
	}
	if result.Status.Confidence != "" {
		t.Fatalf("unavailable confidence = %q, want empty", result.Status.Confidence)
	}
	if result.Points == nil || len(result.Points) != 0 {
		t.Fatalf("points = %#v, want empty non-nil slice", result.Points)
	}
}

func TestBuildSlackCDFTooFewSamplesSynthetic(t *testing.T) {
	cp := CriticalPathData{
		Status: ViewStatus{
			Available:  true,
			Reason:     "synthetic inferred DAG",
			Source:     "synthetic",
			Confidence: criticalPathConfidenceInferred,
		},
		Nodes:        syntheticSlackRootNodes(29),
		CriticalPath: []string{"synthetic-slack-28"},
		TotalMS:      29,
	}
	result := BuildSlackCDF(cp)
	if result.Status.Available {
		t.Fatal("29 samples must be below the 30-sample minimum")
	}
	if !strings.Contains(result.Status.Reason, "29") || !strings.Contains(result.Status.Reason, "30") {
		t.Fatalf("reason = %q", result.Status.Reason)
	}
	if result.Status.Confidence != "" {
		t.Fatalf("unavailable confidence = %q, want empty", result.Status.Confidence)
	}
}

func TestBuildSlackCDFIncompleteDAGSynthetic(t *testing.T) {
	t.Run("no_nodes", func(t *testing.T) {
		result := BuildSlackCDF(CriticalPathData{
			Status: ViewStatus{Available: true, Source: "synthetic", Confidence: criticalPathConfidenceInferred},
		})
		if result.Status.Available || !strings.Contains(result.Status.Reason, "incomplete") {
			t.Fatalf("result = %+v", result.Status)
		}
	})

	t.Run("missing_parent", func(t *testing.T) {
		result := BuildSlackCDF(CriticalPathData{
			Status: ViewStatus{Available: true, Source: "synthetic", Confidence: criticalPathConfidenceInferred},
			Nodes: []CriticalPathNode{
				{ID: "synthetic-root", StartMS: 0, DurationMS: 1},
				{ID: "synthetic-orphan", Parent: "synthetic-missing", StartMS: 1, DurationMS: 1},
			},
			TotalMS: 2,
		})
		if result.Status.Available || !strings.Contains(result.Status.Reason, "missing parent") {
			t.Fatalf("result = %+v", result.Status)
		}
	})

	t.Run("parent_cycle", func(t *testing.T) {
		result := BuildSlackCDF(CriticalPathData{
			Status: ViewStatus{Available: true, Source: "synthetic", Confidence: criticalPathConfidenceInferred},
			Nodes: []CriticalPathNode{
				{ID: "synthetic-a", Parent: "synthetic-b", StartMS: 0, DurationMS: 1},
				{ID: "synthetic-b", Parent: "synthetic-a", StartMS: 0, DurationMS: 1},
			},
			TotalMS: 1,
		})
		if result.Status.Available || !strings.Contains(result.Status.Reason, "cycle") {
			t.Fatalf("result = %+v", result.Status)
		}
	})

	t.Run("duplicate_id", func(t *testing.T) {
		result := BuildSlackCDF(CriticalPathData{
			Status: ViewStatus{Available: true, Source: "synthetic", Confidence: criticalPathConfidenceInferred},
			Nodes: []CriticalPathNode{
				{ID: "synthetic-duplicate", StartMS: 0, DurationMS: 1},
				{ID: "synthetic-duplicate", StartMS: 0, DurationMS: 1},
			},
			TotalMS: 1,
		})
		if result.Status.Available || !strings.Contains(result.Status.Reason, "duplicate") {
			t.Fatalf("result = %+v", result.Status)
		}
	})
}

func TestBuildSlackCDFThirtySamplesSynthetic(t *testing.T) {
	cp := CriticalPathData{
		Status: ViewStatus{
			Available:  true,
			Reason:     "synthetic inferred slack DAG",
			Source:     "synthetic",
			Confidence: criticalPathConfidenceInferred,
		},
		Nodes:        syntheticSlackRootNodes(30),
		CriticalPath: []string{"synthetic-slack-29"},
		TotalMS:      30,
	}
	result := BuildSlackCDF(cp)
	if !result.Status.Available {
		t.Fatalf("status = %+v", result.Status)
	}
	if result.Status.Confidence != criticalPathConfidenceInferred {
		t.Fatalf("confidence = %q", result.Status.Confidence)
	}
	if result.Status.Source != "synthetic" {
		t.Fatalf("source = %q", result.Status.Source)
	}
	if len(result.Points) != 30 {
		t.Fatalf("points = %d, want 30", len(result.Points))
	}
	if result.Points[0].SlackMS != 0 || result.Points[0].Count != 1 || result.Points[0].Cumulative != 1 {
		t.Fatalf("first point = %+v", result.Points[0])
	}
	if result.Points[29].SlackMS != 29 || result.Points[29].Count != 1 || result.Points[29].Cumulative != 30 {
		t.Fatalf("last point = %+v", result.Points[29])
	}
	assertSyntheticClose(t, result.MinMS, 0)
	assertSyntheticClose(t, result.P50MS, 14)
	assertSyntheticClose(t, result.P95MS, 28)
	if result.NegativeCount != 0 {
		t.Fatalf("negative count = %d", result.NegativeCount)
	}
}

func TestBuildSlackCDFBucketsSynthetic(t *testing.T) {
	nodes := make([]CriticalPathNode, 0, 30)
	for i := 0; i < 30; i++ {
		nodes = append(nodes, CriticalPathNode{
			ID:         fmt.Sprintf("synthetic-bucket-%02d", i),
			Label:      "synthetic bucket node",
			Category:   "synthetic",
			Kind:       "host",
			StartMS:    0,
			DurationMS: float64(1 + i%2),
		})
	}
	cp := CriticalPathData{
		Status: ViewStatus{
			Available:  true,
			Reason:     "synthetic duplicate-slack DAG",
			Source:     "synthetic",
			Confidence: criticalPathConfidenceInferred,
		},
		Nodes:   nodes,
		TotalMS: 2,
	}
	result := BuildSlackCDF(cp)
	if !result.Status.Available {
		t.Fatalf("status = %+v", result.Status)
	}
	if len(result.Points) != 2 {
		t.Fatalf("points = %+v, want 2 buckets", result.Points)
	}
	if result.Points[0].SlackMS != 0 || result.Points[0].Count != 15 || result.Points[0].Cumulative != 15 {
		t.Fatalf("first bucket = %+v", result.Points[0])
	}
	if result.Points[1].SlackMS != 1 || result.Points[1].Count != 15 || result.Points[1].Cumulative != 30 {
		t.Fatalf("second bucket = %+v", result.Points[1])
	}
	assertSyntheticClose(t, result.MinMS, 0)
	assertSyntheticClose(t, result.P50MS, 0)
	assertSyntheticClose(t, result.P95MS, 1)
}

func TestBuildSlackCDFNegativeSlackSynthetic(t *testing.T) {
	nodes := make([]CriticalPathNode, 0, 30)
	nodes = append(nodes,
		CriticalPathNode{ID: "synthetic-parent", Label: "synthetic parent", Category: "synthetic", Kind: "host", StartMS: 0, DurationMS: 5},
		CriticalPathNode{ID: "synthetic-child", Parent: "synthetic-parent", Label: "synthetic overrun child", Category: "synthetic", Kind: "host", StartMS: 4, DurationMS: 4},
	)
	for i := 0; i < 28; i++ {
		nodes = append(nodes, CriticalPathNode{
			ID:         fmt.Sprintf("synthetic-fill-%02d", i),
			Label:      "synthetic fill node",
			Category:   "synthetic",
			Kind:       "host",
			StartMS:    0,
			DurationMS: float64(i + 1),
		})
	}
	cp := CriticalPathData{
		Status: ViewStatus{
			Available:  true,
			Reason:     "synthetic negative-slack DAG",
			Source:     "synthetic",
			Confidence: criticalPathConfidenceInferred,
		},
		Nodes:   nodes,
		TotalMS: 30,
	}
	result := BuildSlackCDF(cp)
	if !result.Status.Available {
		t.Fatalf("status = %+v", result.Status)
	}
	if result.NegativeCount != 1 {
		t.Fatalf("negative count = %d, want 1", result.NegativeCount)
	}
	assertSyntheticClose(t, result.MinMS, -3)
	if result.Points[0].SlackMS != -3 || result.Points[0].Count != 1 {
		t.Fatalf("first point = %+v", result.Points[0])
	}
}

func TestBuildSlackCDFDefaultsConfidenceSynthetic(t *testing.T) {
	cp := CriticalPathData{
		Status:  ViewStatus{Available: true, Source: "synthetic"},
		Nodes:   syntheticSlackRootNodes(30),
		TotalMS: 30,
	}
	result := BuildSlackCDF(cp)
	if !result.Status.Available {
		t.Fatalf("status = %+v", result.Status)
	}
	if result.Status.Confidence == "" {
		t.Fatal("available slack CDF must not return empty confidence")
	}
	if result.Status.Confidence != criticalPathConfidenceInferred {
		t.Fatalf("confidence = %q", result.Status.Confidence)
	}
	if !strings.Contains(result.Status.Reason, "inferred") {
		t.Fatalf("reason = %q", result.Status.Reason)
	}
}
