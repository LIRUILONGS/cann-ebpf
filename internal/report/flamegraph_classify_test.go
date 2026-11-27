package report

import (
	"testing"

	"cann-ebpf/internal/stacks"
)

func TestBuildFlamegraphUsesSharedNPUClassification(t *testing.T) {
	profile := &stacks.Profile{
		Stacks: []stacks.ResolvedStack{
			{Count: 3, Frames: []stacks.Frame{
				{Name: "ksys_ioctl"},
				{Name: "trs_ioctl"},
			}},
			{Count: 1, Frames: []stacks.Frame{
				{Name: "devmm_svm_ioctl"},
			}},
			{Count: 1, Frames: []stacks.Frame{
				{Name: "dma_map_page"},
			}},
		},
	}
	data := BuildFlamegraph(profile, stacks.Meta{Samples: 5, SymbolCoverage: 1}, "test")
	if !data.Status.Available {
		t.Fatalf("expected available flamegraph: %+v", data.Status)
	}
	if len(data.TopNPU) != 2 {
		t.Fatalf("top npu = %+v, want trs_ioctl and devmm_svm_ioctl", data.TopNPU)
	}
	if data.TopNPU[0].Name != "trs_ioctl" || data.TopNPU[0].Samples != 3 {
		t.Fatalf("top npu[0] = %+v", data.TopNPU[0])
	}
	if data.TopNPU[1].Name != "devmm_svm_ioctl" || data.TopNPU[1].Samples != 1 {
		t.Fatalf("top npu[1] = %+v", data.TopNPU[1])
	}
	for _, node := range data.Tree.Children {
		if node.Name == "dma_map_page" && node.Category != "dma" {
			t.Fatalf("generic dma category = %q, want dma", node.Category)
		}
	}
}
