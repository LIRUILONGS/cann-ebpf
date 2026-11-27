package stacks

import "strings"

// FilterProfile applies the report-side kernel filter to a symbolized profile.
//
// "npu" keeps whole stacks that contain at least one NPU-classified frame. It
// deliberately keeps the surrounding kernel path (el0_sync, ioctl, mutex, ...)
// instead of trimming to the NPU frame, so the filtered flame graph still shows
// why the NPU call was entered. The returned dropped counts are whole-run
// denominators for the meta warning; they are not inferred from stack_fail.
func FilterProfile(profile *Profile, mode string) (*Profile, uint64, int) {
	if profile == nil || strings.ToLower(strings.TrimSpace(mode)) != "npu" {
		return profile, 0, 0
	}

	filtered := &Profile{Stacks: make([]ResolvedStack, 0, len(profile.Stacks))}
	var dropped uint64
	for _, stack := range profile.Stacks {
		if stackHasNPUFrame(stack) {
			filtered.Stacks = append(filtered.Stacks, stack)
			filtered.Total += stack.Count
		} else {
			dropped += stack.Count
		}
	}
	return filtered, dropped, len(profile.Stacks) - len(filtered.Stacks)
}

func stackHasNPUFrame(stack ResolvedStack) bool {
	for _, frame := range stack.Frames {
		if isNPUFrame(frame) {
			return true
		}
	}
	return false
}

func isNPUFrame(frame Frame) bool {
	category := strings.ToLower(strings.TrimSpace(frame.Category))
	if category == "npu" || category == "aicore" || category == "aicpu" {
		return true
	}
	module := strings.ToLower(frame.Module)
	for _, marker := range []string{"devdrv", "davinci", "hisi"} {
		if strings.Contains(module, marker) {
			return true
		}
	}
	name := strings.ToLower(frame.Name)
	for _, prefix := range []string{
		"devdrv_", "drv_", "davinci", "hisi_", "svm_", "ascend",
		"aicore", "aicpu", "hdc_",
	} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}
