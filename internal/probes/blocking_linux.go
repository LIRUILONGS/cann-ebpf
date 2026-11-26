//go:build linux

package probes

import (
	"fmt"
	"sync"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/link"
)

// attachBlocking attaches the sched_switch / sched_process_exit and
// sys_enter_futex / sys_exit_futex tracepoints. Attach failures are warnings,
// not fatal errors: host span collection must keep working when tracefs or
// syscall tracepoints are unavailable.
func attachBlocking(collection *ebpf.Collection, offcpu, futex bool, links *[]link.Link, linkMu *sync.Mutex) ([]Attachment, []string, BlockingResult) {
	result := BlockingResult{OffCPU: offcpu, Futex: futex}
	var attachments []Attachment
	var warnings []string
	attached := map[string]bool{}
	addLink := func(l link.Link) {
		linkMu.Lock()
		*links = append(*links, l)
		linkMu.Unlock()
	}
	for _, bp := range blockingPrograms {
		wanted := false
		switch bp.need {
		case "offcpu":
			wanted = offcpu
		case "futex":
			wanted = futex
		case "any":
			wanted = offcpu || futex
		}
		if !wanted {
			continue
		}
		prog := collection.Programs[bp.name]
		attachment := Attachment{Function: bp.symbol, Category: "blocking"}
		if prog == nil {
			attachment.Error = "compiled blocking program missing"
			attachments = append(attachments, attachment)
			result.Failed++
			warnings = append(warnings, fmt.Sprintf("blocking probe %s: %s", bp.name, attachment.Error))
			continue
		}
		l, err := link.Tracepoint(bp.group, bp.symbol, prog, nil)
		if err != nil {
			attachment.Error = err.Error()
			attachments = append(attachments, attachment)
			result.Failed++
			warnings = append(warnings, fmt.Sprintf("blocking probe %s (%s/%s): %v", bp.name, bp.group, bp.symbol, err))
			continue
		}
		addLink(l)
		attachments = append(attachments, attachment)
		attached[bp.name] = true
		result.Attached++
	}
	// Only claim a metric when every tracepoint it needs attached.
	result.OffCPU = offcpu && attached["on_sched_switch"]
	result.Futex = futex && attached["on_futex_enter"] && attached["on_futex_exit"]
	if offcpu && !result.OffCPU {
		warnings = append(warnings, "sched_switch off-CPU probe not active; offcpu_ns will be absent")
	}
	if futex && !result.Futex {
		warnings = append(warnings, "futex syscall tracepoints not active; futex_ns will be absent")
	}
	result.Warnings = warnings
	return attachments, warnings, result
}

// configureBlockingMap publishes which blocking values are actually being
// measured, so span events can mark their samples as present even when zero.
func configureBlockingMap(collection *ebpf.Collection, offcpu, futex bool) error {
	cfgMap, ok := collection.Maps["blocking_cfg"]
	if !ok {
		return fmt.Errorf("BPF map blocking_cfg not found")
	}
	value := struct {
		OffCPU uint32
		Futex  uint32
	}{}
	if offcpu {
		value.OffCPU = 1
	}
	if futex {
		value.Futex = 1
	}
	return cfgMap.Update(uint32(0), value, ebpf.UpdateAny)
}
