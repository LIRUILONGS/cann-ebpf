package probes

import (
	"fmt"

	"cann-ebpf/internal/config"
	"cann-ebpf/internal/runlog"
)

// Blocking flag bits carried in rawSpanEvent.Flags. They tell the user-space
// decoder whether an offcpu_ns/futex_ns value was actually measured, as opposed
// to not measured at all (for example when the probe was disabled).
const (
	blockingFlagOffCPU uint32 = 1
	blockingFlagFutex  uint32 = 2
)

// BlockingMode selects host blocking probes: "auto" follows the profile,
// "on" forces both probes, "off" disables both.
type BlockingMode string

const (
	BlockingAuto BlockingMode = "auto"
	BlockingOn   BlockingMode = "on"
	BlockingOff  BlockingMode = "off"
)

// BlockingResult records which blocking probes were actually attached.
type BlockingResult struct {
	Mode     string   `json:"mode,omitempty"`
	OffCPU   bool     `json:"offcpu"`
	Futex    bool     `json:"futex"`
	Attached int      `json:"attached"`
	Failed   int      `json:"failed"`
	Warnings []string `json:"warnings,omitempty"`
}

type blockingProgram struct {
	name   string
	kind   string // "tracepoint"
	group  string // tracepoint group
	symbol string // tracepoint name
	need   string // "offcpu", "futex", or "any"
}

var blockingPrograms = []blockingProgram{
	{name: "on_sched_switch", kind: "tracepoint", group: "sched", symbol: "sched_switch", need: "offcpu"},
	{name: "on_sched_exit", kind: "tracepoint", group: "sched", symbol: "sched_process_exit", need: "any"},
	{name: "on_futex_enter", kind: "tracepoint", group: "syscalls", symbol: "sys_enter_futex", need: "futex"},
	{name: "on_futex_exit", kind: "tracepoint", group: "syscalls", symbol: "sys_exit_futex", need: "futex"},
}

// resolveBlocking merges the CLI mode with the profile's probes.sched/futex
// switches. "auto" only enables probes that the profile turns on.
func resolveBlocking(profile *config.Document, mode BlockingMode) (offcpu, futex bool, err error) {
	switch mode {
	case "", BlockingAuto:
		if profile == nil {
			return false, false, nil
		}
		return profile.Spec.Probes.Sched.IsEnabled(), profile.Spec.Probes.Futex.IsEnabled(), nil
	case BlockingOn:
		return true, true, nil
	case BlockingOff:
		return false, false, nil
	default:
		return false, false, fmt.Errorf("unknown blocking mode %q (want auto|on|off)", mode)
	}
}

// applyBlockingSample copies measured off-CPU/futex values into a span. A value
// is only attached when the matching probe actually ran, so an absent pointer
// means "not measured" rather than "measured zero".
func applyBlockingSample(span *runlog.Span, event rawSpanEvent) {
	if event.Flags&blockingFlagOffCPU != 0 {
		v := int64(event.OffCPUNS)
		span.OffCPUNS = &v
	}
	if event.Flags&blockingFlagFutex != 0 {
		v := int64(event.FutexNS)
		span.FutexNS = &v
	}
}
