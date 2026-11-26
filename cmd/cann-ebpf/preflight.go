package main

import (
	"flag"
	"fmt"
	"os"

	"cann-ebpf/internal/preflight"
)

// cmdPreflight is a read-only environment gate. It exits 4 when a fatal
// prerequisite is missing, matching the CLI contract in docs/PLAN.md.
func cmdPreflight(args []string) int {
	fs := flag.NewFlagSet("preflight", flag.ContinueOnError)
	jsonOut := fs.Bool("json", false, "json output")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	report := preflight.Evaluate(preflight.Collect())
	if *jsonOut {
		writeJSON(report)
		return report.ExitCode()
	}
	fmt.Printf("preflight host=%s kernel=%s arch=%s pass=%d warn=%d fail=%d\n",
		report.Hostname, report.Kernel, report.Arch, report.Passed, report.Warnings, report.Failed)
	for _, check := range report.Checks {
		fmt.Printf("%-4s %-32s %s\n", statusLabel(check.Status), check.Name, check.Detail)
	}
	if report.Fatal {
		fmt.Fprintln(os.Stderr, "preflight: fatal checks failed; fix them before attach")
	}
	return report.ExitCode()
}

func statusLabel(status string) string {
	switch status {
	case preflight.StatusPass:
		return "PASS"
	case preflight.StatusWarn:
		return "WARN"
	case preflight.StatusFail:
		return "FAIL"
	default:
		return status
	}
}
