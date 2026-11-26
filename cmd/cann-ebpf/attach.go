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
	noContext := fs.Bool("no-context", false, "disable contextfeed/plugin socket; use pure-eBPF spans only")
	pythonBatch := fs.Bool("python-batch", false, "enable read-only pure-eBPF vLLM SchedulerOutput batch probe")
	pythonBatchLib := fs.String("python-batch-lib", "", "explicit libpython3.12.so path for --python-batch")
	pythonBatchOffsets := fs.String("python-batch-offsets", "", "CPython/vLLM offset bundle JSON (default: embedded pinned profile)")
	pythonBatchMaxEvents := fs.Int("python-batch-max-events", 20000, "maximum batch ring-buffer events retained per worker")
	pythonBatchRequireType := fs.String("python-batch-require-type", "SchedulerOutput", "required batch owner type substring")
	blockingMode := fs.String("blocking", "auto", "host blocking probes: auto|on|off (sched_switch off-CPU and futex_wait)")
	hardwareMode := fs.String("hardware", "auto", "coarse hardware sampling: auto|on|off")
	hardwareInterval := fs.Duration("hardware-interval", 0, "hardware sampling interval override (default: profile hardware.interval_s)")
	hardwareBackend := fs.String("hardware-backend", "", "hardware backend: npu-smi (default) or dcmi (requires dcmi_probe)")
	deviceJSONL := fs.String("device-jsonl", "", "external msPTI collector device.jsonl to import into the run directory")
	preflightMode := fs.String("preflight", "enforce", "environment preflight before attach: enforce|warn|off")
	jsonOut := fs.Bool("json", false, "json summary")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if code := runPreflightGate(*preflightMode); code != 0 {
		return code
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
	if *noContext {
		contextSocketPath = ""
	} else if contextSocketPath == "" && profile.Doc.Spec.Context.Plugin != nil {
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
		Blocking:         probes.BlockingMode(*blockingMode),
		HardwareMode:     *hardwareMode,
		HardwareInterval: *hardwareInterval,
		HardwareBackend:  *hardwareBackend,
		DeviceJSONL:      *deviceJSONL,
		OnCaptureStart: func() {
			fmt.Fprintf(os.Stderr, "capture_start=%s\n", time.Now().UTC().Format(time.RFC3339Nano))
		},
		OnCaptureStop: func() {
			fmt.Fprintf(os.Stderr, "capture_stop=%s\n", time.Now().UTC().Format(time.RFC3339Nano))
		},
		PythonBatch: probes.PythonBatchOptions{
			Enabled:     *pythonBatch,
			LibPath:     *pythonBatchLib,
			OffsetsPath: *pythonBatchOffsets,
			MaxEvents:   *pythonBatchMaxEvents,
			RequireType: *pythonBatchRequireType,
		},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "attach: %v\n", err)
		return 5
	}
	fmt.Fprintf(os.Stderr, "capture_end=%s\n", time.Now().UTC().Format(time.RFC3339Nano))
	if *jsonOut {
		writeJSON(map[string]any{
			"backend": "ebpf", "profile": profile.Name, "pids": pids,
			"spans": len(result.Spans), "events": result.EventCount, "attachments": result.Attachments,
			"warnings": result.Warnings, "context": result.Context, "python_batch": result.PythonBatch,
			"blocking": result.Blocking, "hardware": result.Hardware, "output": *outputDir,
			"device": result.DeviceEvidence,
		})
	} else {
		fmt.Printf("ebpf profile=%s pids=%v spans_buffered=%d events=%d output=%s\n", profile.Name, pids, len(result.Spans), result.EventCount, *outputDir)
		fmt.Printf("context batches=%d active=%d requests=%d pids=%v\n", result.Context.TotalBatches, result.Context.ActiveBatches, result.Context.RequestIDs, result.Context.PIDs)
		fmt.Printf("blocking mode=%s offcpu=%v futex=%v attached=%d failed=%d\n",
			result.Blocking.Mode, result.Blocking.OffCPU, result.Blocking.Futex, result.Blocking.Attached, result.Blocking.Failed)
		fmt.Printf("hardware source=%s available=%v samples=%d devices=%d output=%s\n",
			result.Hardware.Source, result.Hardware.Available, result.Hardware.Samples, result.Hardware.Devices, result.Hardware.Path)
		fmt.Printf("timings setup=%s capture=%s teardown=%s close_links=%s batch=%s consume=%s writer=%s summary=%s\n",
			result.SetupDuration.Round(time.Millisecond), result.CaptureDuration.Round(time.Millisecond),
			result.TeardownDuration.Round(time.Millisecond), result.CloseLinksDuration.Round(time.Millisecond),
			result.BatchCollectDuration.Round(time.Millisecond), result.ConsumeWaitDuration.Round(time.Millisecond),
			result.WriterCloseDuration.Round(time.Millisecond), result.SummaryDuration.Round(time.Millisecond))
		fmt.Printf("device_evidence imported=%v device=%s meta=%s pidmap=%s\n",
			result.DeviceEvidence.Imported, result.DeviceEvidence.DevicePath,
			result.DeviceEvidence.MetaPath, result.DeviceEvidence.PIDMapPath)
		for _, batch := range result.PythonBatch {
			fmt.Printf("python_batch pid=%d status=%s batches=%d requests=%d lib=%s\n",
				batch.PID, batch.Status, batch.Batches, batch.Requests, batch.Lib)
		}
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
