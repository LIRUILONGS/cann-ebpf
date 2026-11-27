package stacks

import "testing"

func TestFilterProfileNPUKeepsWholeStack(t *testing.T) {
	profile := &Profile{
		Total: 10,
		Stacks: []ResolvedStack{
			{Count: 3, Frames: []Frame{
				{Name: "el0_sync"},
				{Name: "__arm64_sys_ioctl"},
				{Name: "drv_ascend_intf_ioctl", Category: "npu"},
			}},
			{Count: 5, Frames: []Frame{
				{Name: "el0_sync"},
				{Name: "futex_wait", Category: "sync"},
			}},
			{Count: 2, Frames: []Frame{
				{Name: "el0_sync"},
				{Name: "devdrv_dma_map_page", Module: "drv_vascend"},
			}},
		},
	}

	filtered, droppedSamples, droppedStacks := FilterProfile(profile, "npu")
	if filtered == nil || filtered.Total != 5 {
		t.Fatalf("filtered total = %+v", filtered)
	}
	if len(filtered.Stacks) != 2 {
		t.Fatalf("kept stacks = %d, want 2", len(filtered.Stacks))
	}
	if droppedSamples != 5 || droppedStacks != 1 {
		t.Fatalf("dropped samples=%d stacks=%d", droppedSamples, droppedStacks)
	}
	if len(filtered.Stacks[0].Frames) != 3 {
		t.Fatalf("whole stack was not preserved: %+v", filtered.Stacks[0])
	}
}

func TestFilterProfileAllIsNoop(t *testing.T) {
	profile := &Profile{Total: 1, Stacks: []ResolvedStack{{Count: 1}}}
	got, droppedSamples, droppedStacks := FilterProfile(profile, "all")
	if got != profile || droppedSamples != 0 || droppedStacks != 0 {
		t.Fatalf("all filter changed profile: %+v %d %d", got, droppedSamples, droppedStacks)
	}
}

func TestIsNPUFrameFallbacks(t *testing.T) {
	cases := []Frame{
		{Name: "drv_ascend_intf_ioctl"},
		{Name: "whatever", Module: "drv_davinci_intf_host"},
		{Name: "aicore_submit", Category: "aicore"},
	}
	for _, frame := range cases {
		if !isNPUFrame(frame) {
			t.Fatalf("expected NPU frame: %+v", frame)
		}
	}
	if isNPUFrame(Frame{Name: "futex_wait", Category: "sync"}) {
		t.Fatal("sync frame classified as NPU")
	}
}
