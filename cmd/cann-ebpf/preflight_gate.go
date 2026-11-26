package main

import (
	"fmt"
	"os"
	"runtime"
	"strings"

	"cann-ebpf/internal/preflight"
)

// runPreflightGate runs the read-only environment checks before attach.
// "enforce" blocks attach on fatal failures (exit 4); "warn" only prints;
// "off" skips. It is a no-op on non-Linux hosts because attach itself is
// Linux-only and preflight cannot observe tracefs there.
func runPreflightGate(mode string) int {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "off":
		return 0
	case "warn", "enforce", "":
	default:
		fmt.Fprintf(os.Stderr, "attach: unknown --preflight mode %q (want enforce|warn|off)\n", mode)
		return 2
	}
	if runtime.GOOS != "linux" {
		return 0
	}
	report := preflight.Evaluate(preflight.Collect())
	fmt.Fprintf(os.Stderr, "preflight host=%s pass=%d warn=%d fail=%d\n",
		report.Hostname, report.Passed, report.Warnings, report.Failed)
	for _, check := range report.Checks {
		if check.Status == preflight.StatusFail {
			fmt.Fprintf(os.Stderr, "preflight FAIL %s: %s\n", check.Name, check.Detail)
		}
	}
	if strings.EqualFold(mode, "enforce") && report.Fatal {
		fmt.Fprintln(os.Stderr, "attach: preflight has fatal failures; fix them or pass --preflight warn")
		return 4
	}
	return 0
}
