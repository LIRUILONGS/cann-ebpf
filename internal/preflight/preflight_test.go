package preflight

import "testing"

func goodEnv() Environment {
	return Environment{
		Hostname: "h", Kernel: "5.10", Arch: "arm64", Root: true,
		BTFPath: "/sys/kernel/btf/vmlinux", TracefsPath: "/sys/kernel/tracing",
		UprobeEvents: true,
		TracepointIDs: map[string]string{
			"sched/sched_switch": "266", "sched/sched_process_exit": "263",
			"syscalls/sys_enter_futex": "331", "syscalls/sys_exit_futex": "330",
		},
		PerfEventParanoid: "2", UnprivilegedBPF: "0", Lockdown: "[none] integrity confidentiality",
		MemlockInfinite: true, Nofile: 1048576, NPUSmiPath: "/usr/local/sbin/npu-smi",
		CgroupVersion: "cgroup v2",
	}
}

func TestEvaluateAllPass(t *testing.T) {
	report := Evaluate(goodEnv())
	if report.Fatal || report.Failed != 0 {
		t.Fatalf("report = %+v", report)
	}
	if report.Warnings != 0 {
		t.Fatalf("warnings = %d: %+v", report.Warnings, report.Checks)
	}
	if report.ExitCode() != 0 {
		t.Fatalf("exit = %d", report.ExitCode())
	}
}

func TestEvaluateFatalChecks(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Environment)
	}{
		{"no tracefs", func(e *Environment) { e.TracefsPath = ""; e.UprobeEvents = false }},
		{"no uprobe_events", func(e *Environment) { e.UprobeEvents = false }},
		{"lockdown integrity", func(e *Environment) { e.Lockdown = "[integrity] confidentiality" }},
		{"lockdown confidentiality", func(e *Environment) { e.Lockdown = "[none] [confidentiality]" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := goodEnv()
			tc.mutate(&env)
			report := Evaluate(env)
			if !report.Fatal || report.ExitCode() != 4 || report.Failed == 0 {
				t.Fatalf("report = %+v", report)
			}
		})
	}
}

func TestEvaluateWarnings(t *testing.T) {
	env := goodEnv()
	delete(env.TracepointIDs, "syscalls/sys_enter_futex")
	env.Nofile = 256
	env.NPUSmiPath = ""
	env.PerfEventParanoid = "3"
	env.MemlockBytes = 64 << 20
	report := Evaluate(env)
	if report.Fatal || report.Failed != 0 {
		t.Fatalf("should not be fatal: %+v", report)
	}
	if report.Warnings < 4 {
		t.Fatalf("warnings = %d: %+v", report.Warnings, report.Checks)
	}
	// missing futex tracepoint must be warn, not fail
	for _, check := range report.Checks {
		if check.Name == "tracepoint:syscalls/sys_enter_futex" && check.Status != StatusWarn {
			t.Fatalf("futex tracepoint status = %s", check.Status)
		}
	}
}

func TestEvaluateUnknowns(t *testing.T) {
	report := Evaluate(Environment{TracepointIDs: map[string]string{}})
	if !report.Fatal {
		t.Fatal("empty environment must fail (no tracefs/uprobe)")
	}
}
