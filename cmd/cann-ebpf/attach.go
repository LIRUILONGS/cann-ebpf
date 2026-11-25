package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"cann-ebpf/internal/discovery"
	"cann-ebpf/internal/probes"
)

// cmdAttach is the M1 eBPF entry point. main.go intentionally remains owned by
// the command-integration change; this function can be wired as the "attach"
// case without changing the probes package.
func cmdAttach(args []string) int {
	fs := flag.NewFlagSet("attach", flag.ContinueOnError)
	pidList := fs.String("pid", "", "comma-separated target PIDs (required)")
	profileName := fs.String("profile", "vllm-ascend", "profile name or path")
	profilesDir := fs.String("profiles-dir", "profiles", "profiles directory")
	duration := fs.Duration("duration", 10*time.Second, "capture duration")
	outputDir := fs.String("output", "", "output directory")
	minDuration := fs.Duration("min-duration", 0, "minimum span duration")
	jsonOut := fs.Bool("json", false, "json summary")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	pids, err := parsePIDList(*pidList)
	if err != nil || len(pids) == 0 {
		fmt.Fprintln(os.Stderr, "attach: --pid is required")
		return 2
	}
	profile, _, err := discovery.LoadProfileByName(*profilesDir, *profileName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "attach: load profile: %v\n", err)
		return 2
	}
	for _, pid := range pids {
		proc, err := discovery.ReadProcess("/proc", pid)
		if err != nil {
			fmt.Fprintf(os.Stderr, "attach: pid %d: %v\n", pid, err)
			return 4
		}
		match := discovery.EvaluateMatch(profile.Doc.Spec.Match, proc)
		if !match.Matched || match.Excluded {
			fmt.Fprintf(os.Stderr, "attach: pid %d does not match profile %s\n", pid, profile.Name)
			return 3
		}
	}
	if *outputDir == "" {
		*outputDir = filepath.Join("runs", fmt.Sprintf("ebpf-%d", time.Now().Unix()))
	}
	ctx, cancel := context.WithTimeout(context.Background(), *duration+10*time.Second)
	defer cancel()
	result, err := probes.Run(probes.Options{
		Context: ctx, PIDs: pids, Profile: profile.Doc, ProfileName: profile.Name,
		Duration: *duration, OutputDir: *outputDir, MinDuration: *minDuration,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "attach: %v\n", err)
		return 5
	}
	if *jsonOut {
		writeJSON(map[string]any{
			"backend": "ebpf", "profile": profile.Name, "pids": pids,
			"spans": len(result.Spans), "attachments": result.Attachments,
			"warnings": result.Warnings, "output": *outputDir,
		})
	} else {
		fmt.Printf("ebpf profile=%s pids=%v spans=%d output=%s\n", profile.Name, pids, len(result.Spans), *outputDir)
		for _, warning := range result.Warnings {
			fmt.Printf("WARN %s\n", warning)
		}
	}
	return 0
}

func parsePIDList(value string) ([]int, error) {
	var pids []int
	seen := map[int]struct{}{}
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		pid, err := strconv.Atoi(part)
		if err != nil || pid <= 0 {
			return nil, fmt.Errorf("invalid pid %q", part)
		}
		if _, ok := seen[pid]; ok {
			continue
		}
		seen[pid] = struct{}{}
		pids = append(pids, pid)
	}
	return pids, nil
}
