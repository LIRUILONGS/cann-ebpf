//go:build linux

package preflight

import (
	"os"
	"os/exec"
	"runtime"
	"strings"

	"golang.org/x/sys/unix"
)

var tracepointChecks = []string{
	"sched/sched_switch",
	"sched/sched_process_exit",
	"syscalls/sys_enter_futex",
	"syscalls/sys_exit_futex",
}

func Collect() Environment {
	host, _ := os.Hostname()
	env := Environment{
		Hostname: host, Kernel: readTrim("/proc/sys/kernel/osrelease"),
		Arch: runtime.GOARCH, Root: os.Geteuid() == 0,
		TracepointIDs: map[string]string{},
	}
	if _, err := os.Stat("/sys/kernel/btf/vmlinux"); err == nil {
		env.BTFPath = "/sys/kernel/btf/vmlinux"
	}
	for _, base := range []string{"/sys/kernel/tracing", "/sys/kernel/debug/tracing"} {
		if _, err := os.Stat(base + "/events/sched/sched_switch/id"); err != nil {
			continue
		}
		env.TracefsPath = base
		break
	}
	if env.TracefsPath != "" {
		if _, err := os.Stat(env.TracefsPath + "/uprobe_events"); err == nil {
			env.UprobeEvents = true
		}
		for _, name := range tracepointChecks {
			env.TracepointIDs[name] = readTrim(env.TracefsPath + "/events/" + name + "/id")
		}
	}
	env.PerfEventParanoid = readTrim("/proc/sys/kernel/perf_event_paranoid")
	env.UnprivilegedBPF = readTrim("/proc/sys/kernel/unprivileged_bpf_disabled")
	env.Lockdown = readTrim("/sys/kernel/security/lockdown")
	env.MemlockKB = readTrim("/proc/sys/kernel/perf_event_mlock_kb")

	var mem unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_MEMLOCK, &mem); err == nil {
		env.MemlockBytes = mem.Cur
		env.MemlockInfinite = mem.Cur == unix.RLIM_INFINITY
	}
	var nofile unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_NOFILE, &nofile); err == nil {
		env.Nofile = nofile.Cur
	}

	if path, err := exec.LookPath("npu-smi"); err == nil {
		env.NPUSmiPath = path
	}

	if _, err := os.Stat("/sys/fs/cgroup/cgroup.controllers"); err == nil {
		env.CgroupVersion = "cgroup v2"
	} else if data := readTrim("/proc/self/cgroup"); data != "" {
		env.CgroupVersion = "cgroup v1 (" + firstField(data) + ")"
	}
	return env
}

func readTrim(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func firstField(value string) string {
	line := strings.SplitN(value, "\n", 2)[0]
	fields := strings.SplitN(line, ":", 3)
	if len(fields) >= 3 && fields[2] != "" {
		return fields[2]
	}
	return strings.SplitN(line, " ", 2)[0]
}
