package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"cann-ebpf/internal/config"
	"cann-ebpf/internal/discovery"
	"cann-ebpf/internal/elfutil"
)

const toolVersion = "0.7.0"

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		usage()
		return 2
	}
	switch args[0] {
	case "validate":
		return cmdValidate(args[1:])
	case "discover":
		return cmdDiscover(args[1:])
	case "preflight":
		return cmdPreflight(args[1:])
	case "attach":
		return cmdAttach(args[1:])
	case "attach-tracefs":
		return cmdAttachTracefs(args[1:])
	case "trace":
		return cmdTrace(args[1:])
	case "diagnose":
		return cmdDiagnose(args[1:])
	case "metrics":
		return cmdMetrics(args[1:])
	case "sample":
		return cmdSample(args[1:])
	case "stacks":
		return cmdStacks(args[1:])
	case "report":
		return cmdReport(args[1:])
	case "report-once":
		return cmdReportOnce(args[1:])
	case "pids":
		return cmdPIDs(args[1:])
	case "version":
		fmt.Printf("cann-ebpf %s %s/%s\n", toolVersion, runtime.GOOS, runtime.GOARCH)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", args[0])
		usage()
		return 2
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: cann-ebpf <validate|preflight|discover|attach|attach-tracefs|trace|diagnose|metrics|sample|stacks|report|report-once|pids|version> [flags]")
}

func cmdValidate(args []string) int {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	path := fs.String("config", "", "config or profile path")
	jsonOut := fs.Bool("json", false, "json output")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *path == "" {
		fmt.Fprintln(os.Stderr, "validate: --config is required")
		return 2
	}
	doc, warnings, err := config.LoadEffective(*path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "validate: %v\n", err)
		return 2
	}
	out := map[string]any{
		"schema":   "cann.config.validation.v1",
		"ok":       true,
		"path":     *path,
		"kind":     doc.Kind,
		"profile":  doc.Metadata.Name,
		"warnings": warnings,
	}
	if *jsonOut {
		writeJSON(out)
	} else {
		fmt.Printf("OK kind=%s profile=%s path=%s\n", doc.Kind, doc.Metadata.Name, *path)
		for _, w := range warnings {
			fmt.Printf("WARN %s\n", w)
		}
	}
	return 0
}

type hostInfo struct {
	Hostname string `json:"hostname"`
	Kernel   string `json:"kernel"`
	Arch     string `json:"arch"`
	BootID   string `json:"boot_id"`
}

type processInfo struct {
	PID            int               `json:"pid"`
	PPID           int               `json:"ppid"`
	Comm           string            `json:"comm"`
	Cmdline        string            `json:"cmdline"`
	Exe            string            `json:"exe"`
	Root           string            `json:"root"`
	ContainerID    string            `json:"container_id,omitempty"`
	ContainerName  string            `json:"container_name,omitempty"`
	ContainerImage string            `json:"container_image,omitempty"`
	StartTime      uint64            `json:"start_time"`
	BootID         string            `json:"boot_id"`
	MapsCount      int               `json:"maps_count"`
	Namespaces     map[string]string `json:"namespaces"`
}

type libraryInfo struct {
	discovery.LibraryFile
	Symbols []elfutil.Symbol `json:"symbols,omitempty"`
	Error   string           `json:"error,omitempty"`
}

type discoverResult struct {
	Schema       string                `json:"schema"`
	GeneratedAt  string                `json:"generated_at"`
	Host         hostInfo              `json:"host"`
	Profile      string                `json:"profile"`
	ProfilePath  string                `json:"profile_path"`
	Match        discovery.MatchResult `json:"match"`
	Process      processInfo           `json:"process"`
	Libraries    []libraryInfo         `json:"libraries"`
	SymbolsTotal int                   `json:"symbols_total"`
	SymbolsFound int                   `json:"symbols_found"`
	Warnings     []string              `json:"warnings"`
}

func cmdDiscover(args []string) int {
	fs := flag.NewFlagSet("discover", flag.ContinueOnError)
	pid := fs.Int("pid", 0, "target pid")
	profileName := fs.String("profile", "", "profile name, path, or auto")
	profilesDir := fs.String("profiles-dir", "profiles", "profiles directory")
	configPath := fs.String("config", "", "explicit config/profile path")
	procRoot := fs.String("proc-root", "/proc", "procfs root")
	maxProcesses := fs.Int("max-processes", 0, "override max processes")
	jsonOut := fs.Bool("json", false, "json output")
	dryRun := fs.Bool("dry-run", true, "discovery only; attach is not implemented in M0")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if !*dryRun {
		fmt.Fprintln(os.Stderr, "attach is not implemented in M0; use --dry-run")
		return 5
	}

	profiles, warnings, err := loadProfiles(*configPath, *profileName, *profilesDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "discover: %v\n", err)
		return 2
	}
	procs, err := discovery.FindProcesses(*procRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "discover: read %s: %v\n", *procRoot, err)
		return 4
	}
	targets, err := chooseTargets(procs, profiles, *pid, *maxProcesses)
	if err != nil {
		fmt.Fprintf(os.Stderr, "discover: %v\n", err)
		return 3
	}
	if len(targets) == 0 {
		fmt.Fprintln(os.Stderr, "discover: no target process")
		return 3
	}

	results := make([]discoverResult, 0, len(targets))
	for _, t := range targets {
		results = append(results, inspect(t, warnings))
	}
	if *jsonOut {
		if len(results) == 1 {
			writeJSON(results[0])
		} else {
			writeJSON(results)
		}
	} else {
		for i, r := range results {
			if i > 0 {
				fmt.Println()
			}
			printHuman(r)
		}
	}
	for _, r := range results {
		if r.SymbolsFound == 0 {
			return 3
		}
	}
	return 0
}

type target struct {
	Process *discovery.Process
	Profile *discovery.LoadedProfile
	Match   discovery.MatchResult
}

func loadProfiles(configPath, profileName, profilesDir string) ([]*discovery.LoadedProfile, []string, error) {
	if configPath != "" {
		p, w, err := discovery.LoadProfile(configPath)
		if err != nil {
			return nil, w, err
		}
		return []*discovery.LoadedProfile{p}, w, nil
	}
	if profileName == "" || profileName == "auto" {
		return discovery.LoadAllProfiles(profilesDir)
	}
	p, w, err := discovery.LoadProfileByName(profilesDir, profileName)
	if err != nil {
		return nil, w, err
	}
	return []*discovery.LoadedProfile{p}, w, nil
}

func chooseTargets(procs []*discovery.Process, profiles []*discovery.LoadedProfile, pid, maxProcesses int) ([]target, error) {
	var all []target
	for _, p := range procs {
		if pid > 0 && p.PID != pid {
			continue
		}
		cands := discovery.CandidatesFor(profiles, p)
		best, err := discovery.SelectBest(cands)
		if err != nil {
			continue
		}
		prof := &discovery.LoadedProfile{Name: best.Profile.Metadata.Name, Path: best.Path, Doc: best.Profile}
		all = append(all, target{Process: p, Profile: prof, Match: best.Match})
	}
	if pid > 0 && len(all) == 0 {
		return nil, fmt.Errorf("pid %d not found or no profile matched", pid)
	}
	if len(all) == 0 {
		return nil, fmt.Errorf("no process matched any profile")
	}
	sort.Slice(all, func(i, j int) bool {
		pi, pj := 0, 0
		if all[i].Profile.Doc.Spec.Match != nil {
			pi = all[i].Profile.Doc.Spec.Match.Priority
		}
		if all[j].Profile.Doc.Spec.Match != nil {
			pj = all[j].Profile.Doc.Spec.Match.Priority
		}
		if pi != pj {
			return pi > pj
		}
		return all[i].Process.PID < all[j].Process.PID
	})
	bestProfile := all[0].Profile.Name
	var filtered []target
	for _, t := range all {
		if t.Profile.Name == bestProfile {
			filtered = append(filtered, t)
		}
	}
	all = filtered

	selector := all[0].Profile.Doc.Spec.Target.Selector
	switch selector {
	case "first_match", "":
		all = expandProcessTree(all, procs)
		all = all[:1]
	case "all_matching":
	case "explicit":
		if pid == 0 {
			return nil, fmt.Errorf("profile requires explicit --pid")
		}
	default:
		return nil, fmt.Errorf("unknown selector %q", selector)
	}
	limit := all[0].Profile.Doc.Spec.Target.MaxProcesses
	if maxProcesses > 0 {
		limit = maxProcesses
	}
	if limit > 0 && len(all) > limit {
		return nil, fmt.Errorf("matched %d processes, exceeds max_processes=%d; narrow selector", len(all), limit)
	}
	return all, nil
}

// expandProcessTree adds descendants of already-matched targets when the
// profile requests process-tree coverage. The descendants are still subject to
// symbol resolution and are reported as explicit per-PID warnings if they do
// not map a CANN library.
func expandProcessTree(matched []target, procs []*discovery.Process) []target {
	if len(matched) == 0 {
		return matched
	}
	profile := matched[0].Profile
	targetSpec := profile.Doc.Spec.Target
	if !targetSpec.ProcessTree && !targetSpec.IncludeChildren {
		return matched
	}
	seen := make(map[int]struct{}, len(matched))
	for _, target := range matched {
		seen[target.Process.PID] = struct{}{}
	}
	changed := true
	for changed {
		changed = false
		for _, proc := range procs {
			if _, ok := seen[proc.PID]; ok {
				continue
			}
			if _, ok := seen[proc.PPID]; !ok {
				continue
			}
			seen[proc.PID] = struct{}{}
			matched = append(matched, target{
				Process: proc,
				Profile: profile,
				Match: discovery.MatchResult{
					Matched:      true,
					Confidence:   0.5,
					MatchedRules: []string{fmt.Sprintf("process_tree: parent=%d", proc.PPID)},
				},
			})
			changed = true
		}
	}
	sort.Slice(matched, func(i, j int) bool { return matched[i].Process.PID < matched[j].Process.PID })
	return matched
}

func inspect(t target, baseWarnings []string) discoverResult {
	doc := t.Profile.Doc
	wanted := doc.Spec.Cann.SymbolOverrides.All()
	if len(wanted) == 0 {
		wanted = defaultSymbols()
	}
	warnings := append([]string{}, baseWarnings...)
	libs := discovery.FindLibraries(t.Process, doc.Spec.Cann.LibraryGlobs)
	var infos []libraryInfo
	found, total := 0, 0
	for _, lib := range libs {
		info := libraryInfo{LibraryFile: lib}
		syms, w, err := elfutil.Resolve(lib.RealPath, wanted)
		warnings = append(warnings, w...)
		if err != nil {
			info.Error = err.Error()
			warnings = append(warnings, fmt.Sprintf("%s: %v", lib.RealPath, err))
		}
		for _, s := range syms {
			total++
			if s.Found {
				found++
			}
		}
		info.Symbols = syms
		infos = append(infos, info)
	}
	return discoverResult{
		Schema:      "cann.discover.v1",
		GeneratedAt: time.Now().Format(time.RFC3339),
		Host: hostInfo{
			Hostname: hostname(),
			Kernel:   readFirstLine("/proc/sys/kernel/osrelease"),
			Arch:     runtime.GOARCH,
			BootID:   readFirstLine("/proc/sys/kernel/random/boot_id"),
		},
		Profile:     t.Profile.Name,
		ProfilePath: filepath.Clean(t.Profile.Path),
		Match:       t.Match,
		Process: processInfo{
			PID:            t.Process.PID,
			PPID:           t.Process.PPID,
			Comm:           t.Process.Comm,
			Cmdline:        t.Process.Cmdline,
			Exe:            t.Process.Exe,
			Root:           t.Process.Root,
			ContainerID:    t.Process.ContainerID,
			ContainerName:  t.Process.ContainerName,
			ContainerImage: t.Process.ContainerImage,
			StartTime:      t.Process.StartTime,
			BootID:         t.Process.BootID,
			MapsCount:      len(t.Process.Maps),
			Namespaces:     t.Process.Namespaces,
		},
		Libraries:    infos,
		SymbolsTotal: total,
		SymbolsFound: found,
		Warnings:     warnings,
	}
}

func defaultSymbols() []string {
	return []string{
		"aclrtSynchronizeStream", "aclrtSynchronizeStreamWithTimeout", "aclrtSynchronizeEvent",
		"aclrtRecordEvent", "aclrtStreamWaitEvent", "aclrtQueryEventStatus", "aclrtQueryEventWaitStatus",
		"aclrtSetDevice", "aclrtGetDevice", "aclrtSetCurrentContext",
		"aclrtCreateStream", "aclrtDestroyStream", "aclrtCreateEvent", "aclrtCreateEventWithFlag", "aclrtResetEvent", "aclrtDestroyEvent",
		"aclrtMalloc", "aclrtFree", "aclrtMallocHost", "aclrtFreeHost", "aclrtHostRegister", "aclrtHostUnregister", "aclrtGetMemInfo", "rtMemGetInfoEx",
		"aclrtMemcpyAsync", "aclrtLaunchKernel", "rtKernelLaunch", "rtKernelLaunchWithFlagV2", "rtMemcpyAsync",
		"HcclAllReduce", "HcclAlltoAll", "HcclAllGather", "HcclReduceScatter", "HcclBroadcast", "HcclSend", "HcclRecv",
	}
}

func printHuman(r discoverResult) {
	fmt.Printf("run=%s profile=%s confidence=%.2f\n", r.GeneratedAt, r.Profile, r.Match.Confidence)
	fmt.Printf("process pid=%d ppid=%d comm=%s\n", r.Process.PID, r.Process.PPID, r.Process.Comm)
	fmt.Printf("  root=%s container=%s image=%s\n", r.Process.Root, r.Process.ContainerName, r.Process.ContainerImage)
	fmt.Printf("  cmdline=%s\n", truncate(r.Process.Cmdline, 220))
	fmt.Printf("  matched=%v rules=%s\n", r.Match.Matched, strings.Join(r.Match.MatchedRules, "; "))
	fmt.Printf("libraries=%d symbols=%d/%d\n", len(r.Libraries), r.SymbolsFound, r.SymbolsTotal)
	for _, lib := range r.Libraries {
		fmt.Printf("  lib %s inode=%d %s", lib.RealPath, lib.Inode, lib.Error)
		if lib.Note != "" {
			fmt.Printf(" note=%s", lib.Note)
		}
		fmt.Println()
		for _, s := range lib.Symbols {
			if s.Found {
				fmt.Printf("    OK   %-38s off=0x%-8x vaddr=0x%-8x %s\n", s.Wanted, s.Offset, s.Value, s.Section)
			} else {
				fmt.Printf("    MISS %-38s %s\n", s.Wanted, s.Error)
			}
		}
	}
	if len(r.Warnings) > 0 {
		fmt.Printf("warnings=%d\n", len(r.Warnings))
		for _, w := range r.Warnings {
			fmt.Printf("  WARN %s\n", w)
		}
	}
}

func writeJSON(v any) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "json: %v\n", err)
		return
	}
	fmt.Println(string(b))
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return ""
	}
	return h
}

func readFirstLine(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(strings.SplitN(string(b), "\n", 2)[0])
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
