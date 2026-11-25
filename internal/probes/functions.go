package probes

import (
	"fmt"
	"sort"
	"strings"

	"cann-ebpf/internal/config"
)

type Function struct {
	ID       uint32
	Name     string
	Category string
}

var supportedFunctions = []Function{
	{ID: 1, Name: "aclrtSynchronizeStream", Category: "sync"},
	{ID: 2, Name: "aclrtSynchronizeStreamWithTimeout", Category: "sync"},
	{ID: 3, Name: "aclrtSynchronizeEvent", Category: "sync"},
	{ID: 4, Name: "aclrtRecordEvent", Category: "event"},
	{ID: 5, Name: "aclrtStreamWaitEvent", Category: "event"},
	{ID: 6, Name: "aclrtMemcpyAsync", Category: "submit"},
	{ID: 7, Name: "aclrtLaunchKernel", Category: "submit"},
	{ID: 8, Name: "HcclAllReduce", Category: "hccl"},
}

func SupportedFunctions() []Function {
	out := make([]Function, len(supportedFunctions))
	copy(out, supportedFunctions)
	return out
}

func functionByID(id uint32) (Function, bool) {
	for _, fn := range supportedFunctions {
		if fn.ID == id {
			return fn, true
		}
	}
	return Function{}, false
}

func programNames(fn Function) (entry, exit string) {
	return "enter_" + fn.Name, "exit_" + fn.Name
}

func requestedFunctions(profile *config.Document) ([]Function, []string) {
	if profile == nil {
		return SupportedFunctions(), nil
	}
	overrides := profile.Spec.Cann.SymbolOverrides.All()
	if len(overrides) == 0 {
		return SupportedFunctions(), nil
	}
	requested := make(map[string]struct{}, len(overrides))
	for _, name := range overrides {
		requested[name] = struct{}{}
	}
	var out []Function
	for _, fn := range supportedFunctions {
		if _, ok := requested[fn.Name]; ok {
			out = append(out, fn)
		}
	}
	var unsupported []string
	for name := range requested {
		found := false
		for _, fn := range supportedFunctions {
			if fn.Name == name {
				found = true
				break
			}
		}
		if !found {
			unsupported = append(unsupported, name)
		}
	}
	sort.Strings(unsupported)
	var warnings []string
	if len(unsupported) > 0 {
		warnings = append(warnings, fmt.Sprintf("M1 has no probe program for: %s", strings.Join(unsupported, ", ")))
	}
	return out, warnings
}

func probeConfigFor(profile *config.Document, fn Function) (enabled bool, sampleRate uint32, minDurationNS uint64) {
	enabled = true
	sampleRate = 1
	if profile == nil {
		return
	}
	probe := profile.Spec.Probes.Sync
	switch fn.Category {
	case "event":
		probe = profile.Spec.Probes.Event
	case "submit":
		probe = profile.Spec.Probes.Submit
	case "hccl":
		probe = profile.Spec.Probes.Hccl
	}
	enabled = probe.IsEnabled()
	if probe.SampleRate > 0 {
		sampleRate = uint32(probe.SampleRate)
	}
	if probe.MinDurationUS > 0 {
		minDurationNS = uint64(probe.MinDurationUS) * 1000
	}
	return
}
