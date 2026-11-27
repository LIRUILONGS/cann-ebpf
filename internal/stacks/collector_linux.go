//go:build linux

package stacks

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/rlimit"
	"golang.org/x/sys/unix"
)

const (
	maxFrequencyHz = 999
	maxPerfEvents  = 4096

	kernelOnlySamplingWarning = "perf event excludes user-mode time; samples are kernel on-CPU only"

	statSamples   = 0
	statFiltered  = 1
	statStackFail = 2
	statLost      = 3
)

// stackCountKey must keep the exact layout of struct stack_key in
// stack_probe.bpf.c: two little-endian u32 values, no padding.
type stackCountKey struct {
	PID     uint32
	StackID uint32
}

// stackProcRoot is a package variable so tests can point enumeration at a fake
// proc tree. Production always reads /proc.
var stackProcRoot = "/proc"

// threadTarget identifies one host thread. Perf events are opened per TID,
// while the BPF target filter remains keyed by the owning process tgid.
type threadTarget struct {
	ProcessPID int
	TID        int
}

type perfEvent struct {
	processPID int
	tid        int
	fd         int
	enabled    bool
}

// Collect samples kernel stacks of the target host PIDs with a CPU-clock
// perf event and a BPF perf_event program. It never restarts, signals or
// otherwise touches the target process.
func Collect(ctx context.Context, opts Options) (*CollectResult, error) {
	opts, err := normalizeOptions(opts)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("stacks: collection canceled before start: %w", err)
	}

	warnings := make([]string, 0, 4)
	host, err := os.Hostname()
	if err != nil {
		warnings = append(warnings, "read hostname: "+err.Error())
	}
	kernel, err := kernelRelease()
	if err != nil {
		return nil, err
	}

	// Raising RLIMIT_MEMLOCK is only needed on kernels before 5.11. A failure
	// is not fatal: many hosts already account BPF memory through memcg.
	if err := rlimit.RemoveMemlock(); err != nil {
		warnings = append(warnings, "raise RLIMIT_MEMLOCK (continuing): "+err.Error())
	}

	collection, err := loadCollection()
	if err != nil {
		switch {
		case errors.Is(err, unix.EPERM), errors.Is(err, unix.EACCES):
			return nil, fmt.Errorf("%w; loading BPF requires root or CAP_BPF/CAP_PERFMON, and on kernels before 5.11 enough RLIMIT_MEMLOCK (kernel.perf_event_paranoid=%s)", err, perfEventParanoid())
		case errors.Is(err, ebpf.ErrNotSupported):
			return nil, fmt.Errorf("%w: %v", ErrUnsupported, err)
		default:
			return nil, err
		}
	}
	defer collection.Close()

	program, ok := collection.Programs[stackProgramName]
	if !ok {
		return nil, fmt.Errorf("stacks: embedded BPF object is missing program %q", stackProgramName)
	}
	if program.Type() != ebpf.PerfEvent {
		return nil, fmt.Errorf("stacks: embedded program %q has type %s, want perf_event", stackProgramName, program.Type())
	}

	targetPIDs, err := requiredMap(collection, targetPIDsMapName)
	if err != nil {
		return nil, err
	}
	stackTraces, err := requiredMap(collection, stackTracesMapName)
	if err != nil {
		return nil, err
	}
	stackCounts, err := requiredMap(collection, stackCountsMapName)
	if err != nil {
		return nil, err
	}
	stackErrors, err := requiredMap(collection, stackErrorsMapName)
	if err != nil {
		return nil, err
	}
	statsMap, err := requiredMap(collection, stackStatsMapName)
	if err != nil {
		return nil, err
	}

	// The BPF-side filter is the fail-safe against host-wide sampling: an
	// empty map would simply mean every event is counted as filtered.
	for _, pid := range opts.PIDs {
		if err := targetPIDs.Put(uint32(pid), uint8(1)); err != nil {
			return nil, fmt.Errorf("stacks: register target pid %d in BPF map: %w", pid, err)
		}
	}

	// Enumerate immediately before opening the events: threads created during
	// BPF setup are still seen, and children created after their creator's
	// event is enabled are covered by inherit=1.
	targets, err := enumerateTargetThreads(stackProcRoot, opts.PIDs, maxPerfEvents)
	if err != nil {
		return nil, err
	}

	events, err := openPerfEvents(targets, opts.FrequencyHz, program)
	if err != nil {
		return nil, err
	}
	warnings = append(warnings, fmt.Sprintf(
		"sampling %d threads across %d processes", len(events), len(opts.PIDs)))
	warnings = append(warnings, kernelOnlySamplingWarning)
	defer func() { _ = closePerfEvents(events) }()

	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("stacks: collection canceled before sampling started: %w", err)
	}

	started := time.Now().UTC()
	if err := enablePerfEvents(events); err != nil {
		return nil, err
	}
	waitErr := waitForDuration(ctx, opts.Duration)
	warnings = append(warnings, disablePerfEvents(events)...)
	ended := time.Now().UTC()
	if err := closePerfEvents(events); err != nil {
		warnings = append(warnings, err.Error())
	}

	counts, err := readStackCounts(stackCounts)
	if err != nil {
		return nil, err
	}
	traces, err := readStackTraces(stackTraces, counts)
	if err != nil {
		return nil, err
	}
	samples, sampleWarnings := buildSamples(counts, traces)
	warnings = append(warnings, sampleWarnings...)

	stackErrorCounts, err := readStackErrors(stackErrors)
	if err != nil {
		warnings = append(warnings, err.Error())
	} else if warning := formatStackErrors(stackErrorCounts); warning != "" {
		warnings = append(warnings, warning)
	}

	bpfStats, statsOK, statsWarnings := readBPFStats(statsMap)
	warnings = append(warnings, statsWarnings...)
	total := sampleTotal(samples)
	if statsOK && bpfStats.Samples != total {
		warnings = append(warnings, fmt.Sprintf(
			"BPF sample counter %d disagrees with drained sample count %d; using the drained count",
			bpfStats.Samples, total))
	}
	if bpfStats.Lost != 0 {
		warnings = append(warnings, fmt.Sprintf(
			"BPF reported lost=%d samples; the hash-map collector is expected to report zero",
			bpfStats.Lost))
	}
	// The report consumes count totals, so keep Stats and Samples on the same
	// denominator even when a stack trace disappeared between drain steps.
	bpfStats.Samples = total

	statsMapOut := Stats{
		Samples:   bpfStats.Samples,
		Filtered:  bpfStats.Filtered,
		StackFail: bpfStats.StackFail,
		Lost:      bpfStats.Lost,
	}
	meta := Meta{
		Schema:       SchemaV1,
		Collector:    CollectorPerfEvent,
		Host:         host,
		Kernel:       kernel,
		Arch:         runtime.GOARCH,
		StartedAt:    started.Format(time.RFC3339Nano),
		EndedAt:      ended.Format(time.RFC3339Nano),
		DurationS:    ended.Sub(started).Seconds(),
		FrequencyHz:  opts.FrequencyHz,
		TargetPIDs:   append([]int(nil), opts.PIDs...),
		KernelFilter: opts.KernelFilter,
		Samples:      statsMapOut.Samples,
		UniqueStacks: len(samples),
		StackFail:    statsMapOut.StackFail,
		Filtered:     statsMapOut.Filtered,
		Lost:         statsMapOut.Lost,
	}
	if len(warnings) > 0 {
		meta.Warnings = warnings
	}
	result := &CollectResult{Samples: samples, Stats: statsMapOut, Meta: meta}
	if waitErr != nil {
		return result, fmt.Errorf("stacks: collection stopped before the requested duration: %w", waitErr)
	}
	return result, nil
}

func normalizeOptions(opts Options) (Options, error) {
	if opts.FrequencyHz < 1 || opts.FrequencyHz > maxFrequencyHz {
		return Options{}, fmt.Errorf(
			"stacks: frequency_hz %d is out of range 1..999 (the CLI default is 99)", opts.FrequencyHz)
	}
	if opts.Duration <= 0 {
		return Options{}, fmt.Errorf("stacks: duration %s must be positive", opts.Duration)
	}
	if len(opts.PIDs) == 0 {
		return Options{}, errors.New("stacks: at least one target PID is required; refusing to sample the whole host")
	}

	seen := make(map[int]struct{}, len(opts.PIDs))
	deduped := make([]int, 0, len(opts.PIDs))
	for _, pid := range opts.PIDs {
		if pid <= 0 {
			return Options{}, fmt.Errorf("stacks: invalid target PID %d; expected a positive host PID", pid)
		}
		if _, ok := seen[pid]; ok {
			continue
		}
		seen[pid] = struct{}{}
		deduped = append(deduped, pid)
	}
	opts.PIDs = deduped
	return opts, nil
}

// enumerateTargetThreads reads /proc/<pid>/task for every target process and
// returns one perf-event target per unique host TID. An unreadable or empty
// task directory is fatal instead of silently shrinking the profile.
func enumerateTargetThreads(procRoot string, pids []int, limit int) ([]threadTarget, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("stacks: perf event limit must be positive")
	}
	seen := make(map[int]struct{})
	targets := make([]threadTarget, 0, len(pids))
	for _, pid := range pids {
		taskDir := filepath.Join(procRoot, strconv.Itoa(pid), "task")
		entries, err := os.ReadDir(taskDir)
		if err != nil {
			return nil, fmt.Errorf("stacks: enumerate threads for target pid %d: read %s: %w", pid, taskDir, err)
		}
		tids := make([]int, 0, len(entries))
		for _, entry := range entries {
			if !entry.IsDir() {
				return nil, fmt.Errorf("stacks: enumerate threads for target pid %d: %s: unexpected non-directory entry %q", pid, taskDir, entry.Name())
			}
			tid, err := strconv.Atoi(entry.Name())
			if err != nil || tid <= 0 {
				return nil, fmt.Errorf("stacks: enumerate threads for target pid %d: %s: invalid task id %q", pid, taskDir, entry.Name())
			}
			tids = append(tids, tid)
		}
		if len(tids) == 0 {
			return nil, fmt.Errorf("stacks: enumerate threads for target pid %d: %s is empty; the process may have exited", pid, taskDir)
		}
		sort.Ints(tids)
		for _, tid := range tids {
			if _, ok := seen[tid]; ok {
				continue
			}
			if len(targets) >= limit {
				return nil, fmt.Errorf("stacks: target processes expose more than %d threads; refusing to open more than %d perf events (sample fewer PIDs)", limit, limit)
			}
			seen[tid] = struct{}{}
			targets = append(targets, threadTarget{ProcessPID: pid, TID: tid})
		}
	}
	return targets, nil
}

// stackPerfEventAttr builds the software CPU-clock event. x/sys/unix exposes
// the frequency through attr.Sample plus PerfBitFreq, not a named Freq field.
// ExcludeUser keeps scheduling and sampling to kernel-mode on-CPU time, where
// bpf_get_stackid can build a kernel stack from valid registers. ExcludeHv
// additionally ignores time attributed to a hypervisor.
func stackPerfEventAttr(frequencyHz int) unix.PerfEventAttr {
	attr := unix.PerfEventAttr{
		Type:   unix.PERF_TYPE_SOFTWARE,
		Config: unix.PERF_COUNT_SW_CPU_CLOCK,
		Sample: uint64(frequencyHz),
		Bits: unix.PerfBitFreq | unix.PerfBitInherit | unix.PerfBitDisabled |
			unix.PerfBitExcludeUser | unix.PerfBitExcludeHv,
	}
	attr.Size = uint32(unsafe.Sizeof(attr))
	return attr
}

func requiredMap(collection *ebpf.Collection, name string) (*ebpf.Map, error) {
	m, ok := collection.Maps[name]
	if !ok {
		return nil, fmt.Errorf("stacks: embedded BPF object is missing map %q", name)
	}
	return m, nil
}

func openPerfEvents(targets []threadTarget, frequencyHz int, program *ebpf.Program) ([]*perfEvent, error) {
	if len(targets) > maxPerfEvents {
		return nil, fmt.Errorf("stacks: refusing to open %d perf events; the limit is %d", len(targets), maxPerfEvents)
	}
	attr := stackPerfEventAttr(frequencyHz)
	events := make([]*perfEvent, 0, len(targets))
	closeOpened := func() {
		_ = closePerfEvents(events)
	}
	for _, target := range targets {
		fd, err := unix.PerfEventOpen(&attr, target.TID, -1, -1, unix.PERF_FLAG_FD_CLOEXEC)
		if err != nil {
			closeOpened()
			return nil, perfEventOpenError(target, err)
		}
		event := &perfEvent{processPID: target.ProcessPID, tid: target.TID, fd: fd}
		if err := unix.IoctlSetInt(fd, unix.PERF_EVENT_IOC_SET_BPF, program.FD()); err != nil {
			_ = event.close()
			closeOpened()
			return nil, fmt.Errorf("stacks: attach BPF stack program to pid %d tid %d perf event: %w", target.ProcessPID, target.TID, err)
		}
		events = append(events, event)
	}
	return events, nil
}

func perfEventOpenError(target threadTarget, err error) error {
	paranoid := perfEventParanoid()
	prefix := fmt.Sprintf("stacks: perf_event_open(pid=%d tid=%d)", target.ProcessPID, target.TID)
	switch {
	case errors.Is(err, unix.EEXIST):
		return fmt.Errorf("%s: EEXIST: a matching perf event is already attached (is another stack sampler running?); stop the other collector and retry (kernel.perf_event_paranoid=%s): %w", prefix, paranoid, err)
	case errors.Is(err, unix.ENOENT):
		return fmt.Errorf("%s: ENOENT: the kernel rejected the software event type or configuration as unavailable; verify PERF_TYPE_SOFTWARE and PERF_COUNT_SW_CPU_CLOCK support on this kernel (kernel.perf_event_paranoid=%s): %w", prefix, paranoid, err)
	case errors.Is(err, unix.ESRCH):
		return fmt.Errorf("%s: ESRCH: target thread does not exist or has exited; vLLM may have restarted, so verify the current host PID with ps (kernel.perf_event_paranoid=%s): %w", prefix, paranoid, err)
	case errors.Is(err, unix.EPERM), errors.Is(err, unix.EACCES):
		return fmt.Errorf("%s: permission denied (kernel.perf_event_paranoid=%s); run as root or grant CAP_PERFMON (CAP_SYS_ADMIN on older kernels): %w", prefix, paranoid, err)
	case errors.Is(err, unix.EINVAL):
		return fmt.Errorf("%s: invalid perf attributes (kernel.perf_event_paranoid=%s); the kernel may reject CPU-clock frequency sampling: %w", prefix, paranoid, err)
	case errors.Is(err, unix.EMFILE):
		return fmt.Errorf("%s: EMFILE: too many open files; raise ulimit -n or sample fewer threads (kernel.perf_event_paranoid=%s): %w", prefix, paranoid, err)
	default:
		return fmt.Errorf("%s failed (kernel.perf_event_paranoid=%s): %w", prefix, paranoid, err)
	}
}

func perfEventParanoid() string {
	data, err := os.ReadFile("/proc/sys/kernel/perf_event_paranoid")
	if err != nil {
		return "unknown"
	}
	value := strings.TrimSpace(string(data))
	if value == "" {
		return "unknown"
	}
	return value
}

func enablePerfEvents(events []*perfEvent) error {
	for _, event := range events {
		if err := unix.IoctlSetInt(event.fd, unix.PERF_EVENT_IOC_ENABLE, 0); err != nil {
			return fmt.Errorf("stacks: enable perf event for pid %d tid %d: %w", event.processPID, event.tid, err)
		}
		event.enabled = true
	}
	return nil
}

func disablePerfEvents(events []*perfEvent) []string {
	warnings := make([]string, 0, len(events))
	for _, event := range events {
		if !event.enabled {
			continue
		}
		if err := unix.IoctlSetInt(event.fd, unix.PERF_EVENT_IOC_DISABLE, 0); err != nil {
			warnings = append(warnings, fmt.Sprintf("disable perf event for pid %d tid %d: %v", event.processPID, event.tid, err))
		}
		event.enabled = false
	}
	return warnings
}

func (event *perfEvent) close() error {
	if event == nil || event.fd < 0 {
		return nil
	}
	var errs []error
	if event.enabled {
		if err := unix.IoctlSetInt(event.fd, unix.PERF_EVENT_IOC_DISABLE, 0); err != nil {
			errs = append(errs, fmt.Errorf("stacks: disable perf event for pid %d tid %d: %w", event.processPID, event.tid, err))
		}
		event.enabled = false
	}
	if err := unix.Close(event.fd); err != nil {
		errs = append(errs, fmt.Errorf("stacks: close perf event for pid %d tid %d: %w", event.processPID, event.tid, err))
	}
	event.fd = -1
	return errors.Join(errs...)
}

func closePerfEvents(events []*perfEvent) error {
	var errs []error
	for _, event := range events {
		if err := event.close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func waitForDuration(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func kernelRelease() (string, error) {
	var uts unix.Utsname
	unameErr := unix.Uname(&uts)
	if unameErr == nil {
		release := strings.TrimSpace(unix.ByteSliceToString(uts.Release[:]))
		if release != "" {
			return release, nil
		}
		unameErr = errors.New("uname returned an empty release")
	}
	data, readErr := os.ReadFile("/proc/sys/kernel/osrelease")
	if readErr == nil {
		release := strings.TrimSpace(string(data))
		if release != "" {
			return release, nil
		}
		readErr = errors.New("empty /proc/sys/kernel/osrelease")
	}
	return "", fmt.Errorf("stacks: read kernel release: uname: %v; /proc/sys/kernel/osrelease: %w", unameErr, readErr)
}

func readStackCounts(m *ebpf.Map) (map[stackCountKey]uint64, error) {
	counts := make(map[stackCountKey]uint64)
	iter := m.Iterate()
	var key stackCountKey
	var value uint64
	for iter.Next(&key, &value) {
		counts[key] += value
	}
	if err := iter.Err(); err != nil {
		return nil, fmt.Errorf("stacks: iterate stack_counts map: %w", err)
	}
	return counts, nil
}

func readStackErrors(m *ebpf.Map) (map[int32]uint64, error) {
	counts := make(map[int32]uint64)
	iter := m.Iterate()
	var code int32
	var count uint64
	for iter.Next(&code, &count) {
		counts[code] += count
	}
	if err := iter.Err(); err != nil {
		return nil, fmt.Errorf("stacks: iterate stack_errors map: %w", err)
	}
	return counts, nil
}

// formatStackErrors renders one deterministic warning such as
// "bpf_get_stackid errors: -17=489". Negative keys are raw helper errno
// values, so the next real-kernel run can distinguish EEXIST from EFAULT,
// E2BIG and ENOMEM.
func formatStackErrors(counts map[int32]uint64) string {
	codes := make([]int32, 0, len(counts))
	for code, count := range counts {
		if count == 0 {
			continue
		}
		codes = append(codes, code)
	}
	if len(codes) == 0 {
		return ""
	}
	sort.Slice(codes, func(i, j int) bool { return codes[i] < codes[j] })
	parts := make([]string, 0, len(codes))
	for _, code := range codes {
		parts = append(parts, fmt.Sprintf("%d=%d", code, counts[code]))
	}
	return "bpf_get_stackid errors: " + strings.Join(parts, ", ")
}

func readStackTraces(m *ebpf.Map, counts map[stackCountKey]uint64) (map[uint32][]uint64, error) {
	traces := make(map[uint32][]uint64)
	for key := range counts {
		if _, done := traces[key.StackID]; done {
			continue
		}
		var raw [stackDepth]uint64
		if err := m.Lookup(key.StackID, &raw); err != nil {
			if errors.Is(err, ebpf.ErrKeyNotExist) {
				// buildSamples reports the dropped count with its stack id.
				continue
			}
			return nil, fmt.Errorf("stacks: read stack_traces[%d]: %w", key.StackID, err)
		}
		traces[key.StackID] = trimStack(raw[:])
	}
	return traces, nil
}

// trimStack removes the zero tail that bpf_get_stackid leaves when the stack
// is shorter than PERF_MAX_STACK_DEPTH. Interior zero words are preserved.
func trimStack(addrs []uint64) []uint64 {
	end := len(addrs)
	for end > 0 && addrs[end-1] == 0 {
		end--
	}
	out := make([]uint64, end)
	copy(out, addrs[:end])
	return out
}

// buildSamples turns BPF map entries into deterministic raw samples. The key
// carries tgid only, so TID stays zero; include a tid in the key if a later
// schema needs per-thread attribution.
func buildSamples(counts map[stackCountKey]uint64, traces map[uint32][]uint64) ([]Sample, []string) {
	samples := make([]Sample, 0, len(counts))
	warnings := make([]string, 0)
	for key, count := range counts {
		if count == 0 {
			continue
		}
		addrs, ok := traces[key.StackID]
		if !ok {
			warnings = append(warnings, fmt.Sprintf(
				"stack_id %d is missing from stack_traces; dropped %d samples", key.StackID, count))
			continue
		}
		samples = append(samples, Sample{
			PID:   int(key.PID),
			TID:   0,
			Count: count,
			Addrs: append([]uint64(nil), addrs...),
		})
	}
	sort.Slice(samples, func(i, j int) bool {
		if samples[i].PID != samples[j].PID {
			return samples[i].PID < samples[j].PID
		}
		if samples[i].Count != samples[j].Count {
			return samples[i].Count > samples[j].Count
		}
		return slices.Compare(samples[i].Addrs, samples[j].Addrs) < 0
	})
	return samples, warnings
}

func sampleTotal(samples []Sample) uint64 {
	var total uint64
	for _, sample := range samples {
		total += sample.Count
	}
	return total
}

func readBPFStats(m *ebpf.Map) (Stats, bool, []string) {
	var stats Stats
	warnings := make([]string, 0, 1)
	cpus, err := ebpf.PossibleCPU()
	if err != nil {
		return stats, false, append(warnings, "read BPF stats: possible CPU count: "+err.Error())
	}
	values := make([]uint64, cpus)
	read := func(index uint32, name string) (uint64, bool) {
		for i := range values {
			values[i] = 0
		}
		if err := m.Lookup(index, values); err != nil {
			warnings = append(warnings, fmt.Sprintf("read BPF stats[%s]: %v", name, err))
			return 0, false
		}
		var total uint64
		for _, value := range values {
			total += value
		}
		return total, true
	}

	allOK := true
	var ok bool
	if stats.Samples, ok = read(statSamples, "samples"); !ok {
		allOK = false
	}
	if stats.Filtered, ok = read(statFiltered, "filtered"); !ok {
		allOK = false
	}
	if stats.StackFail, ok = read(statStackFail, "stack_fail"); !ok {
		allOK = false
	}
	if stats.Lost, ok = read(statLost, "lost"); !ok {
		allOK = false
	}
	return stats, allOK, warnings
}
