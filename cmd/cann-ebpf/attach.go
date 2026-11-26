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

// cmdAttach is the eBPF entry point. With --pid auto (the default) it uses the
// same read-only discovery rules as discover; an explicit comma-separated
// PID list remains available for narrow debugging runs.
func cmdAttach(args []string) int {
	fs := flag.NewFlagSet("attach", flag.ContinueOnError)
	pidList := fs.String("pid", "", "comma-separated target PIDs, or 'auto' (default: auto)")
	all := fs.Bool("all", false, "attach every process matched by the profile")
	profileName := fs.String("profile", "vllm-ascend", "profile name or path")
	profilesDir := fs.String("profiles-dir", "profiles", "profiles directory")
	procRoot := fs.String("proc-root", "/proc", "procfs root for discovery")
	duration := fs.Duration("duration", 10*time.Second, "capture duration")
	outputDir := fs.String("output", "", "output directory")
	minDuration := fs.Duration("min-duration", 0, "minimum span duration")
	contextSocket := fs.String("context-socket", "", "override Unix socket for vLLM batch/request context")
	jsonOut := fs.Bool("json", false, "json summary")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	profile, _, err := discovery.LoadProfileByName(*profilesDir, *profileName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "attach: load profile: %v\n", err)
		return 2
	}

	selection := strings.TrimSpace(*pidList)
	autoSelect := *all || selection == "" || strings.EqualFold(selection, "auto")
	var pids []int
	if autoSelect {
		procs, scanErr := discovery.FindProcesses(*procRoot)
		if scanErr != nil {
			fmt.Fprintf(os.Stderr, "attach: discover %s: %v\n", *procRoot, scanErr)
			return 4
		}
		targets, selectErr := chooseTargets(procs, []*discovery.LoadedProfile{profile}, 0, 0)
		if selectErr != nil {
			fmt.Fprintf(os.Stderr, "attach: automatic discovery: %v\n", selectErr)
			return 3
		}
		for _, target := range targets {
			pids = append(pids, target.Process.PID)
		}
		if len(pids) == 0 {
			fmt.Fprintln(os.Stderr, "attach: automatic discovery found no target")
			return 3
		}
	} else {
		pids, err = parsePIDList(selection)
		if err != nil || len(pids) == 0 {
			fmt.Fprintf(os.Stderr, "attach: invalid --pid %q\n", selection)
			return 2
		}
		for _, pid := range pids {
			proc, readErr := discovery.ReadProcess(*procRoot, pid)
			if readErr != nil {
				fmt.Fprintf(os.Stderr, "attach: pid %d: %v\n", pid, readErr)
				return 4
			}
			match := discovery.EvaluateMatch(profile.Doc.Spec.Match, proc)
			if !match.Matched || match.Excluded {
				fmt.Fprintf(os.Stderr, "attach: pid %d does not match profile %s\n", pid, profile.Name)
				return 3
			}
		}
	}

	contextSocketPath := strings.TrimSpace(*contextSocket)
	if contextSocketPath == "" && profile.Doc.Spec.Context.Plugin != nil {
		contextSocketPath = profile.Doc.Spec.Context.Plugin.Socket
	}
	if *outputDir == "" {
		*outputDir = filepath.Join("runs", fmt.Sprintf("ebpf-%d", time.Now().Unix()))
	}
	ctx, cancel := context.WithTimeout(context.Background(), *duration+10*time.Second)
	defer cancel()
	result, err := probes.Run(probes.Options{
		Context: ctx, PIDs: pids, ProcRoot: *procRoot, Profile: profile.Doc,
		ProfileName: profile.Name, Duration: *duration, OutputDir: *outputDir,
		MinDuration: *minDuration, ContextSocket: contextSocketPath,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "attach: %v\n", err)
		return 5
	}
	if *jsonOut {
		writeJSON(map[string]any{
			"backend": "ebpf", "profile": profile.Name, "pids": pids,
			"spans": len(result.Spans), "events": result.EventCount, "attachments": result.Attachments,
			"warnings": result.Warnings, "context": result.Context, "output": *outputDir,
		})
	} else {
		fmt.Printf("ebpf profile=%s pids=%v spans_buffered=%d events=%d output=%s\n", profile.Name, pids, len(result.Spans), result.EventCount, *outputDir)
		fmt.Printf("context batches=%d active=%d requests=%d pids=%v\n", result.Context.TotalBatches, result.Context.ActiveBatches, result.Context.RequestIDs, result.Context.PIDs)
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
