// Package preflight performs read-only environment checks before attach.
// It never attaches probes and never changes host state; failures are reported
// with an explicit exit code instead of being guessed away.
package preflight

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

const SchemaV1 = "cann.preflight.v1"

const (
	StatusPass = "pass"
	StatusWarn = "warn"
	StatusFail = "fail"
)

type Check struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

type Report struct {
	Schema      string  `json:"schema"`
	GeneratedAt string  `json:"generated_at"`
	Hostname    string  `json:"hostname,omitempty"`
	Kernel      string  `json:"kernel,omitempty"`
	Arch        string  `json:"arch,omitempty"`
	Checks      []Check `json:"checks"`
	Passed      int     `json:"passed"`
	Warnings    int     `json:"warnings"`
	Failed      int     `json:"failed"`
	Fatal       bool    `json:"fatal"`
}

// Environment is the observed host state. Zero values mean "unknown" and are
// reported as warnings, not as pass.
type Environment struct {
	Hostname          string
	Kernel            string
	Arch              string
	Root              bool
	BTFPath           string
	TracefsPath       string
	UprobeEvents      bool
	TracepointIDs     map[string]string
	PerfEventParanoid string
	UnprivilegedBPF   string
	Lockdown          string
	MemlockKB         string
	MemlockBytes      uint64
	MemlockInfinite   bool
	Nofile            uint64
	NPUSmiPath        string
	CgroupVersion     string
}

// Evaluate turns an Environment into pass/warn/fail checks. It is pure so the
// decision table is unit-testable without a Linux host.
func Evaluate(env Environment) Report {
	report := Report{
		Schema:      SchemaV1,
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Hostname:    env.Hostname,
		Kernel:      env.Kernel,
		Arch:        env.Arch,
		Checks:      []Check{},
	}
	add := func(name, status, detail string) {
		report.Checks = append(report.Checks, Check{Name: name, Status: status, Detail: detail})
		switch status {
		case StatusPass:
			report.Passed++
		case StatusWarn:
			report.Warnings++
		case StatusFail:
			report.Failed++
		}
	}

	if env.Root {
		add("privilege", StatusPass, "running as root")
	} else {
		add("privilege", StatusWarn, "not root; attach needs CAP_BPF/CAP_PERFMON/CAP_SYS_ADMIN or root")
	}

	if env.BTFPath != "" {
		add("btf", StatusPass, env.BTFPath)
	} else {
		add("btf", StatusWarn, "/sys/kernel/btf/vmlinux missing; the current BPF object is not CO-RE but BTF is required by the preflight contract")
	}

	if env.TracefsPath == "" {
		add("tracefs", StatusFail, "neither /sys/kernel/tracing nor /sys/kernel/debug/tracing is usable")
	} else {
		add("tracefs", StatusPass, env.TracefsPath)
	}
	if env.UprobeEvents {
		add("uprobe_events", StatusPass, "uprobe_events is writable path present")
	} else {
		add("uprobe_events", StatusFail, "uprobe_events missing; uprobe attach cannot work")
	}

	required := []string{"sched/sched_switch", "sched/sched_process_exit", "syscalls/sys_enter_futex", "syscalls/sys_exit_futex"}
	for _, name := range required {
		if id := env.TracepointIDs[name]; id != "" {
			add("tracepoint:"+name, StatusPass, "id="+id)
		} else {
			add("tracepoint:"+name, StatusWarn, "missing; blocking attribution degrades (host spans still work)")
		}
	}

	if env.PerfEventParanoid == "" {
		add("perf_event_paranoid", StatusWarn, "unknown")
	} else if v, err := strconv.Atoi(strings.TrimSpace(env.PerfEventParanoid)); err == nil {
		if v <= 2 {
			add("perf_event_paranoid", StatusPass, env.PerfEventParanoid)
		} else {
			add("perf_event_paranoid", StatusWarn, env.PerfEventParanoid+" (root may still attach; non-root cannot)")
		}
	} else {
		add("perf_event_paranoid", StatusWarn, "unparsable: "+env.PerfEventParanoid)
	}

	switch {
	case strings.Contains(env.Lockdown, "[integrity]"):
		add("lockdown", StatusFail, "lockdown=integrity blocks BPF")
	case strings.Contains(env.Lockdown, "[confidentiality]"):
		add("lockdown", StatusFail, "lockdown=confidentiality blocks BPF")
	case env.Lockdown == "":
		add("lockdown", StatusWarn, "unknown (no /sys/kernel/security/lockdown)")
	default:
		add("lockdown", StatusPass, env.Lockdown)
	}

	if env.MemlockInfinite {
		add("memlock", StatusPass, "unlimited")
	} else if env.MemlockBytes >= 1<<30 {
		add("memlock", StatusPass, fmt.Sprintf("%d bytes", env.MemlockBytes))
	} else if env.MemlockBytes > 0 {
		add("memlock", StatusWarn, fmt.Sprintf("RLIMIT_MEMLOCK=%d bytes; raise it if BPF maps fail to load", env.MemlockBytes))
	} else {
		add("memlock", StatusWarn, "unknown RLIMIT_MEMLOCK")
	}

	if env.Nofile >= 4096 {
		add("nofile", StatusPass, fmt.Sprintf("RLIMIT_NOFILE=%d", env.Nofile))
	} else if env.Nofile > 0 {
		add("nofile", StatusWarn, fmt.Sprintf("RLIMIT_NOFILE=%d; per-thread perf events may exhaust fds", env.Nofile))
	} else {
		add("nofile", StatusWarn, "unknown RLIMIT_NOFILE")
	}

	if env.NPUSmiPath != "" {
		add("npu-smi", StatusPass, env.NPUSmiPath)
	} else {
		add("npu-smi", StatusWarn, "not found; hardware sampling will be disabled")
	}

	if env.UnprivilegedBPF != "" {
		add("unprivileged_bpf_disabled", StatusPass, env.UnprivilegedBPF)
	}
	if env.CgroupVersion != "" {
		add("cgroup", StatusPass, env.CgroupVersion)
	}

	report.Fatal = report.Failed > 0
	return report
}

func (r Report) ExitCode() int {
	if r.Fatal {
		return 4
	}
	return 0
}
