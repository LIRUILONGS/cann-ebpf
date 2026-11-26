//go:build linux

package probes

import (
	"fmt"
	"sync"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/link"
)

// attachBlocking attaches the sched_switch tracepoint and futex_wait kprobes.
// Attach failures are warnings, not fatal errors: host span collection must
// keep working even when tracefs/kprobes are unavailable.
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
		wanted := offcpu
		if bp.kind != "tracepoint" {
			wanted = futex
		}
		if !wanted {
			continue
		}
		prog := collection.Programs[bp.name]
		attachment := Attachment{Function: bp.symbol, Category: "blocking"}
		if bp.kind == "kprobe" {
			attachment.Entry = true
		} else if bp.kind == "kretprobe" {
			attachment.Exit = true
		}
		if prog == nil {
			attachment.Error = "compiled blocking program missing"
			attachments = append(attachments, attachment)
			result.Failed++
			warnings = append(warnings, fmt.Sprintf("blocking probe %s: %s", bp.name, attachment.Error))
			continue
		}
		var (
			l   link.Link
			err error
		)
		switch bp.kind {
		case "tracepoint":
			l, err = link.Tracepoint(bp.group, bp.symbol, prog, nil)
		case "kprobe":
			l, err = link.Kprobe(bp.symbol, prog, nil)
		case "kretprobe":
			l, err = link.Kretprobe(bp.symbol, prog, nil)
		default:
			err = fmt.Errorf("unknown blocking program kind %q", bp.kind)
		}
		if err != nil {
			attachment.Error = err.Error()
			attachments = append(attachments, attachment)
			result.Failed++
			warnings = append(warnings, fmt.Sprintf("blocking probe %s (%s/%s): %v", bp.name, bp.kind, bp.symbol, err))
			continue
		}
		addLink(l)
		attachments = append(attachments, attachment)
		attached[bp.name] = true
		result.Attached++
	}
	// Only claim a metric when the complete entry/exit pair attached.
	result.OffCPU = offcpu && attached["on_sched_switch"]
	result.Futex = futex && ((attached["enter_futex_wait"] && attached["exit_futex_wait"]) ||
		(attached["enter_futex_wait_requeue_pi"] && attached["exit_futex_wait_requeue_pi"]))
	if offcpu && !result.OffCPU {
		warnings = append(warnings, "sched_switch off-CPU probe not active; offcpu_ns will be absent")
	}
	if futex && !result.Futex {
		warnings = append(warnings, "futex_wait probe pair not active; futex_ns will be absent")
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
