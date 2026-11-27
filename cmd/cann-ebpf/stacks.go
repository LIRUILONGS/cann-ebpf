package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"cann-ebpf/internal/config"
	"cann-ebpf/internal/discovery"
	"cann-ebpf/internal/stacks"
)

const stacksDefaultSymbolsPath = "profiles/kernel-symbols.yaml"

var stacksClock = time.Now

type stacksCommand struct {
	PIDs         []int
	Auto         bool
	Profile      string
	Duration     time.Duration
	FrequencyHz  int
	Out          string
	Raw          bool
	KernelFilter string
	SymbolsPath  string
}

type pidListFlag struct {
	values []int
	auto   bool
}

func (f *pidListFlag) String() string {
	if f == nil {
		return ""
	}
	parts := make([]string, 0, len(f.values)+1)
	for _, pid := range f.values {
		parts = append(parts, strconv.Itoa(pid))
	}
	if f.auto {
		parts = append(parts, "auto")
	}
	return strings.Join(parts, ",")
}

func (f *pidListFlag) Set(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("--pid must be a positive host PID or auto")
	}
	if strings.EqualFold(value, "auto") {
		f.auto = true
		return nil
	}
	pid, err := strconv.Atoi(value)
	if err != nil || pid <= 0 {
		return fmt.Errorf("--pid %q is invalid; expected a positive host PID or auto", value)
	}
	f.values = append(f.values, pid)
	return nil
}

func parseStacksCommand(args []string, output io.Writer) (stacksCommand, error) {
	fs := flag.NewFlagSet("stacks", flag.ContinueOnError)
	fs.SetOutput(output)
	var pids pidListFlag
	fs.Var(&pids, "pid", "target host PID (repeatable), or auto to select the vLLM process tree")
	profile := fs.String("profile", "vllm-ascend", "discovery profile used by --pid auto")
	duration := fs.Duration("duration", 30*time.Second, "sampling duration (must be > 0)")
	frequency := fs.Int("freq", 99, "sampling frequency in Hz (1..999)")
	out := fs.String("out", "", "output run directory (default runs/stacks-<UTC timestamp>)")
	raw := fs.Bool("raw", false, "also write raw addresses to stacks.jsonl")
	kernelFilter := fs.String("kernel-filter", "all", "kernel stack filter: all or npu")
	symbols := fs.String("symbols", stacksDefaultSymbolsPath, "kernel symbol category rules YAML; a missing file uses built-in defaults")
	if err := fs.Parse(args); err != nil {
		return stacksCommand{}, err
	}
	if fs.NArg() > 0 {
		return stacksCommand{}, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if pids.auto && len(pids.values) > 0 {
		return stacksCommand{}, fmt.Errorf("--pid auto cannot be combined with explicit --pid values")
	}
	if !pids.auto && len(pids.values) == 0 {
		return stacksCommand{}, fmt.Errorf("at least one --pid is required (repeat the flag, or use --pid auto)")
	}
	if *duration <= 0 {
		return stacksCommand{}, fmt.Errorf("--duration must be positive, got %s", *duration)
	}
	if *frequency < 1 || *frequency > 999 {
		return stacksCommand{}, fmt.Errorf("--freq must be in 1..999, got %d", *frequency)
	}
	filter := strings.ToLower(strings.TrimSpace(*kernelFilter))
	if filter != "all" && filter != "npu" {
		return stacksCommand{}, fmt.Errorf("--kernel-filter must be all or npu, got %q", *kernelFilter)
	}
	profileName := strings.TrimSpace(*profile)
	if profileName == "" {
		profileName = "vllm-ascend"
	}
	symbolsPath := strings.TrimSpace(*symbols)
	if symbolsPath == "" {
		symbolsPath = stacksDefaultSymbolsPath
	}
	outPath := strings.TrimSpace(*out)
	if outPath == "" {
		outPath = filepath.Join("runs", "stacks-"+stacksClock().UTC().Format("20060102T150405Z"))
	}
	return stacksCommand{
		PIDs:         append([]int(nil), pids.values...),
		Auto:         pids.auto,
		Profile:      profileName,
		Duration:     *duration,
		FrequencyHz:  *frequency,
		Out:          outPath,
		Raw:          *raw,
		KernelFilter: filter,
		SymbolsPath:  symbolsPath,
	}, nil
}

// cmdStacks samples host kernel stacks for explicit PIDs. It never samples the
// whole host and never writes a folded profile when collection or
// symbolization fails.
func cmdStacks(args []string) int {
	cfg, err := parseStacksCommand(args, os.Stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		fmt.Fprintf(os.Stderr, "stacks: %v\n", err)
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	pids := cfg.PIDs
	if cfg.Auto {
		resolved, warnings, err := autoStacksPIDs(cfg.Profile, "profiles")
		for _, warning := range warnings {
			fmt.Fprintf(os.Stderr, "stacks: warn: %s\n", warning)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "stacks: --pid auto: %v\n", err)
			return 2
		}
		pids = resolved
		fmt.Fprintf(os.Stderr, "stacks: auto selected pids=%v profile=%s\n", pids, cfg.Profile)
	}

	fmt.Fprintf(os.Stderr, "stacks: sampling pids=%v duration=%s freq=%dHz filter=%s\n",
		pids, cfg.Duration, cfg.FrequencyHz, cfg.KernelFilter)
	result, err := stacks.Collect(ctx, stacks.Options{
		PIDs:         pids,
		FrequencyHz:  cfg.FrequencyHz,
		Duration:     cfg.Duration,
		Raw:          cfg.Raw,
		KernelFilter: cfg.KernelFilter,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "stacks: collect: %v\n", err)
		printStacksCollectHints(err)
		return 3
	}
	if result == nil {
		fmt.Fprintln(os.Stderr, "stacks: collect: collector returned no result")
		return 3
	}
	result.Meta.ToolVersion = toolVersion

	profile, meta, err := stacks.Symbolize(result.Samples, result.Meta, stacks.SymbolizeOptions{
		RulesPath: cfg.SymbolsPath,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "stacks: symbolize: %v\n", err)
		printStacksSymbolizeHints(err)
		return 4
	}
	if profile == nil {
		fmt.Fprintln(os.Stderr, "stacks: symbolize: symbolizer returned a nil profile; refusing to write folded output")
		printStacksSymbolizeHints(nil)
		return 4
	}
	if cfg.KernelFilter == "npu" {
		originalStacks := meta.UniqueStacks
		originalSamples := meta.Samples
		filtered, droppedSamples, droppedStacks := stacks.FilterProfile(profile, cfg.KernelFilter)
		if filtered == nil {
			fmt.Fprintln(os.Stderr, "stacks: filter: kernel-filter=npu returned no profile")
			return 4
		}
		profile = filtered
		meta.Samples = filtered.Total
		meta.UniqueStacks = len(filtered.Stacks)
		meta.Warnings = append(meta.Warnings, fmt.Sprintf(
			"kernel_filter=npu kept %d/%d stacks and %d/%d samples; stack_fail/filtered remain whole-run counters",
			len(filtered.Stacks), originalStacks, filtered.Total, originalSamples))
		if droppedStacks > 0 || droppedSamples > 0 {
			fmt.Fprintf(os.Stderr, "stacks: kernel-filter=npu kept %d/%d stacks and %d/%d samples\n",
				len(filtered.Stacks), originalStacks, filtered.Total, originalSamples)
		}
	}
	if err := stacks.WriteRun(cfg.Out, meta, result.Samples, profile, cfg.Raw); err != nil {
		fmt.Fprintf(os.Stderr, "stacks: write run %s: %v\n", cfg.Out, err)
		fmt.Fprintf(os.Stderr, "stacks: hint: choose a new --out; existing stacks.folded/stacks.meta.json are never overwritten\n")
		return 4
	}

	for _, warning := range meta.Warnings {
		fmt.Fprintf(os.Stderr, "stacks: warn: %s\n", warning)
	}
	if meta.Samples == 0 {
		fmt.Fprintln(os.Stderr, "stacks: warn: collected 0 samples; the run is written but the report will show the flame graph as unavailable")
	}
	if meta.SymbolCoverage < 0.5 {
		fmt.Fprintf(os.Stderr, "stacks: warn: symbol coverage %.2f%%; check /proc/sys/kernel/kptr_restrict (current=%s) and CAP_SYSLOG\n",
			meta.SymbolCoverage*100, readKernelParam("/proc/sys/kernel/kptr_restrict"))
	}
	fmt.Printf("stacks out=%s samples=%d unique=%d stack_fail=%d coverage=%.2f%% folded=%s meta=%s\n",
		cfg.Out,
		meta.Samples,
		meta.UniqueStacks,
		meta.StackFail,
		meta.SymbolCoverage*100,
		filepath.Join(cfg.Out, stacks.FoldedFileName),
		filepath.Join(cfg.Out, stacks.MetaFileName),
	)
	return 0
}

func autoStacksPIDs(profileName, profilesDir string) ([]int, []string, error) {
	profileName = strings.TrimSpace(profileName)
	if profileName == "" || strings.EqualFold(profileName, "auto") {
		profileName = "vllm-ascend"
	}
	profile, warnings, err := discovery.LoadProfileByName(profilesDir, profileName)
	if err != nil {
		return nil, warnings, fmt.Errorf("load profile %q: %w (use an explicit --pid instead)", profileName, err)
	}
	procs, err := discovery.FindProcesses("/proc")
	if err != nil {
		return nil, warnings, fmt.Errorf("read /proc: %w (use an explicit --pid instead)", err)
	}
	pids, err := selectVLLMProcessTree(procs, profile.Doc.Spec.Match)
	if err != nil {
		return nil, warnings, err
	}
	return pids, warnings, nil
}

// selectVLLMProcessTree is intentionally stricter than a generic profile
// match: only explicit vLLM/VLLM::Worker_TP/EngineCore identities may become
// roots, and orinfer processes plus their subtrees are always excluded. This
// prevents --pid auto from accidentally sampling an orinfer deployment or the
// whole host.
func selectVLLMProcessTree(procs []*discovery.Process, match *config.Match) ([]int, error) {
	children := make(map[int][]*discovery.Process, len(procs))
	for _, proc := range procs {
		if proc == nil {
			continue
		}
		children[proc.PPID] = append(children[proc.PPID], proc)
	}

	queue := make([]*discovery.Process, 0)
	for _, proc := range procs {
		if proc == nil || !isExplicitVLLMProcess(proc) {
			continue
		}
		if match != nil && discovery.EvaluateMatch(match, proc).Excluded {
			continue
		}
		queue = append(queue, proc)
	}
	if len(queue) == 0 {
		return nil, fmt.Errorf("no explicit vllm/VLLM::Worker_TP/EngineCore process matched; refusing whole-host sampling (use an explicit --pid)")
	}

	selected := make(map[int]struct{}, len(queue))
	for len(queue) > 0 {
		proc := queue[0]
		queue = queue[1:]
		if proc == nil || isOrinferProcess(proc) {
			continue
		}
		if _, ok := selected[proc.PID]; ok {
			continue
		}
		selected[proc.PID] = struct{}{}
		queue = append(queue, children[proc.PID]...)
	}
	if len(selected) == 0 {
		return nil, fmt.Errorf("only orinfer processes matched; refusing to sample orinfer (use an explicit --pid)")
	}
	if len(selected) > 512 {
		return nil, fmt.Errorf("vLLM process tree has %d PIDs, exceeding the safety limit 512; narrow the target with explicit --pid", len(selected))
	}

	pids := make([]int, 0, len(selected))
	for pid := range selected {
		pids = append(pids, pid)
	}
	sort.Ints(pids)
	return pids, nil
}

func isExplicitVLLMProcess(proc *discovery.Process) bool {
	if proc == nil || isOrinferProcess(proc) {
		return false
	}
	comm := strings.ToLower(strings.TrimSpace(proc.Comm))
	cmdline := strings.ToLower(proc.Cmdline)
	switch {
	case comm == "vllm":
		return true
	case strings.Contains(comm, "worker_tp"):
		return true
	case strings.Contains(comm, "enginecor"):
		return true
	case strings.Contains(cmdline, "vllm serve"):
		return true
	case strings.Contains(cmdline, "vllm.entrypoints"):
		return true
	case strings.Contains(cmdline, "vllm.worker"):
		return true
	default:
		return false
	}
}

func isOrinferProcess(proc *discovery.Process) bool {
	if proc == nil {
		return false
	}
	comm := strings.ToLower(strings.TrimSpace(proc.Comm))
	cmdline := strings.ToLower(proc.Cmdline)
	return strings.Contains(comm, "orinfer") || strings.Contains(cmdline, "orinfer")
}

func printStacksCollectHints(err error) {
	paranoid := readKernelParam("/proc/sys/kernel/perf_event_paranoid")
	kptr := readKernelParam("/proc/sys/kernel/kptr_restrict")
	fmt.Fprintln(os.Stderr, "stacks: hint: verify the target PIDs are alive on this host (ps -o pid,comm,cmd -p <pid>)")
	fmt.Fprintf(os.Stderr, "stacks: hint: perf stack sampling usually needs root or CAP_PERFMON/CAP_BPF; current kernel.perf_event_paranoid=%s\n", displayKernelParam(paranoid))
	fmt.Fprintf(os.Stderr, "stacks: hint: symbolization needs readable kernel addresses; current kernel.kptr_restrict=%s (use CAP_SYSLOG or kptr_restrict=0)\n", displayKernelParam(kptr))
	if err != nil {
		text := strings.ToLower(err.Error())
		if strings.Contains(text, "permission") || strings.Contains(text, "operation not permitted") || strings.Contains(text, "eperm") || strings.Contains(text, "eacces") {
			fmt.Fprintln(os.Stderr, "stacks: hint: re-run with sudo, or grant CAP_PERFMON+CAP_BPF and ensure perf_event_paranoid permits per-process sampling")
		}
	}
}

func printStacksSymbolizeHints(err error) {
	kptr := readKernelParam("/proc/sys/kernel/kptr_restrict")
	fmt.Fprintf(os.Stderr, "stacks: hint: if /proc/kallsyms is all zeros, kernel.kptr_restrict=%s is blocking symbols; run with CAP_SYSLOG or set kptr_restrict=0\n", displayKernelParam(kptr))
	if err != nil {
		fmt.Fprintln(os.Stderr, "stacks: hint: symbolization also requires /proc/kallsyms and /proc/modules to be readable")
	}
}

func readKernelParam(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func displayKernelParam(value string) string {
	if strings.TrimSpace(value) == "" {
		return "unknown"
	}
	return value
}
