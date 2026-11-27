package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type pidValues []string

func (p *pidValues) String() string { return strings.Join(*p, ",") }

func (p *pidValues) Set(value string) error {
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		pid, err := strconv.Atoi(part)
		if err != nil || pid <= 0 {
			return fmt.Errorf("invalid pid %q (auto-detection is not supported)", part)
		}
		*p = append(*p, strconv.Itoa(pid))
	}
	return nil
}

// cmdReportOnce runs attach and stacks against explicit target PIDs, then
// renders one report. It is a Go-native orchestration command: no shell script.
func cmdReportOnce(args []string) int {
	fs := flag.NewFlagSet("report-once", flag.ContinueOnError)
	var pids pidValues
	fs.Var(&pids, "pid", "target host PID (repeatable; pass all vLLM frontend/EngineCore/Worker PIDs)")
	outPath := fs.String("out", "", "new output directory (required)")
	profile := fs.String("profile", "vllm-ascend", "vllm-ascend or orinfer")
	profilesDir := fs.String("profiles-dir", "profiles", "profile directory")
	duration := fs.Duration("duration", 10*time.Minute, "collection duration")
	freq := fs.Int("freq", 99, "stack sampling frequency in Hz (1..999)")
	metrics := fs.String("metrics", "auto", "metrics mode: auto|on|off")
	noStacks := fs.Bool("no-stacks", false, "skip kernel stack sampling / flamegraph")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if len(pids) == 0 {
		fmt.Fprintln(os.Stderr, "report-once: at least one --pid is required")
		return 2
	}
	if strings.TrimSpace(*outPath) == "" {
		fmt.Fprintln(os.Stderr, "report-once: --out is required")
		return 2
	}
	if *duration <= 0 {
		fmt.Fprintln(os.Stderr, "report-once: --duration must be > 0")
		return 2
	}
	if *freq < 1 || *freq > 999 {
		fmt.Fprintln(os.Stderr, "report-once: --freq must be in 1..999")
		return 2
	}
	switch *profile {
	case "vllm-ascend", "orinfer":
	default:
		fmt.Fprintf(os.Stderr, "report-once: --profile must be vllm-ascend or orinfer (got %q)\n", *profile)
		return 2
	}
	switch *metrics {
	case "auto", "on", "off":
	default:
		fmt.Fprintf(os.Stderr, "report-once: --metrics must be auto|on|off (got %q)\n", *metrics)
		return 2
	}
	if _, err := os.Stat(*outPath); err == nil {
		fmt.Fprintf(os.Stderr, "report-once: refusing to overwrite %s\n", *outPath)
		return 2
	}

	out, err := filepath.Abs(*outPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "report-once: out path: %v\n", err)
		return 4
	}
	attachDir := filepath.Join(out, "attach")
	stackDir := filepath.Join(out, "stacks")
	if err := os.MkdirAll(attachDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "report-once: create out: %v\n", err)
		return 4
	}
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "report-once: locate executable: %v\n", err)
		return 4
	}

	pidArgs := make([]string, 0, len(pids)*2)
	for _, p := range pids {
		pidArgs = append(pidArgs, "--pid", p)
	}
	fmt.Printf("report-once: pids=%s profile=%s duration=%s out=%s\n", strings.Join(pids, ","), *profile, duration.String(), out)

	attachArgs := append([]string{"attach"}, pidArgs...)
	attachArgs = append(attachArgs,
		"--profile", *profile,
		"--profiles-dir", *profilesDir,
		"--duration", duration.String(),
		"--blocking", "on",
		"--hardware", "on",
		"--metrics", *metrics,
		"--output", attachDir,
		"--json",
	)
	attachLog, err := os.Create(filepath.Join(out, "attach.log"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "report-once: create attach.log: %v\n", err)
		return 4
	}
	defer attachLog.Close()
	attachCmd := exec.Command(exe, attachArgs...)
	attachCmd.Stdout = attachLog
	attachCmd.Stderr = attachLog
	if err := attachCmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "report-once: start attach: %v\n", err)
		return 4
	}

	var stackCmd *exec.Cmd
	var stackLog *os.File
	if !*noStacks {
		stackArgs := append([]string{"stacks"}, pidArgs...)
		stackArgs = append(stackArgs,
			"--duration", duration.String(),
			"--freq", strconv.Itoa(*freq),
			"--out", stackDir,
		)
		stackLog, err = os.Create(filepath.Join(out, "stacks.log"))
		if err != nil {
			_ = attachCmd.Process.Kill()
			fmt.Fprintf(os.Stderr, "report-once: create stacks.log: %v\n", err)
			return 4
		}
		defer stackLog.Close()
		stackCmd = exec.Command(exe, stackArgs...)
		stackCmd.Stdout = stackLog
		stackCmd.Stderr = stackLog
		if err := stackCmd.Start(); err != nil {
			_ = attachCmd.Process.Kill()
			fmt.Fprintf(os.Stderr, "report-once: start stacks: %v\n", err)
			return 4
		}
	}

	attachErr := attachCmd.Wait()
	if stackCmd != nil {
		_ = stackCmd.Wait()
	}
	if attachErr != nil {
		fmt.Fprintf(os.Stderr, "report-once: attach failed: %v (see %s)\n", attachErr, filepath.Join(out, "attach.log"))
		return 3
	}
	if info, err := os.Stat(filepath.Join(attachDir, "spans.jsonl")); err != nil || info.Size() == 0 {
		fmt.Fprintf(os.Stderr, "report-once: attach produced no spans; report not generated\n")
		return 3
	}

	reportArgs := []string{"report", "--input", attachDir, "--out", filepath.Join(out, "report.html")}
	if info, err := os.Stat(filepath.Join(attachDir, "metrics.jsonl")); err == nil && info.Size() > 0 {
		reportArgs = append(reportArgs, "--metrics", filepath.Join(attachDir, "metrics.jsonl"))
	}
	if info, err := os.Stat(filepath.Join(stackDir, "stacks.folded")); err == nil && info.Size() > 0 {
		reportArgs = append(reportArgs, "--stacks", stackDir)
	}
	reportLog, err := os.Create(filepath.Join(out, "report.log"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "report-once: create report.log: %v\n", err)
		return 4
	}
	defer reportLog.Close()
	reportCmd := exec.Command(exe, reportArgs...)
	reportCmd.Stdout = reportLog
	reportCmd.Stderr = reportLog
	if err := reportCmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "report-once: report failed: %v (see %s)\n", err, filepath.Join(out, "report.log"))
		return 5
	}
	fmt.Printf("OK report=%s\n", filepath.Join(out, "report.html"))
	return 0
}
