//go:build linux

package stacks

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/cilium/ebpf"
	"golang.org/x/sys/unix"
)

func TestNormalizeOptionsDeduplicatesPIDs(t *testing.T) {
	opts := Options{
		PIDs:         []int{42, 7, 42, 9001, 7},
		FrequencyHz:  99,
		Duration:     1500 * time.Millisecond,
		Raw:          true,
		KernelFilter: "npu",
	}
	got, err := normalizeOptions(opts)
	if err != nil {
		t.Fatalf("normalizeOptions: %v", err)
	}
	if want := []int{42, 7, 9001}; !reflect.DeepEqual(got.PIDs, want) {
		t.Fatalf("PIDs = %v, want %v", got.PIDs, want)
	}
	if got.FrequencyHz != 99 || got.Duration != 1500*time.Millisecond {
		t.Fatalf("sampling fields changed: %+v", got)
	}
	if !got.Raw {
		t.Fatalf("Raw was cleared")
	}
	if got.KernelFilter != "npu" {
		t.Fatalf("KernelFilter = %q, want npu", got.KernelFilter)
	}
}

func TestNormalizeOptionsRejectsInvalidInput(t *testing.T) {
	valid := Options{PIDs: []int{123}, FrequencyHz: 99, Duration: time.Second}
	tests := []struct {
		name string
		opts Options
		want string
	}{
		{
			name: "empty pid list",
			opts: Options{FrequencyHz: 99, Duration: time.Second},
			want: "at least one target PID",
		},
		{
			name: "zero pid",
			opts: Options{PIDs: []int{0}, FrequencyHz: 99, Duration: time.Second},
			want: "invalid target PID 0",
		},
		{
			name: "negative pid",
			opts: Options{PIDs: []int{-1}, FrequencyHz: 99, Duration: time.Second},
			want: "invalid target PID -1",
		},
		{
			name: "zero frequency",
			opts: Options{PIDs: []int{123}, FrequencyHz: 0, Duration: time.Second},
			want: "frequency_hz 0",
		},
		{
			name: "frequency above maximum",
			opts: Options{PIDs: []int{123}, FrequencyHz: 1000, Duration: time.Second},
			want: "frequency_hz 1000",
		},
		{
			name: "negative frequency",
			opts: Options{PIDs: []int{123}, FrequencyHz: -5, Duration: time.Second},
			want: "frequency_hz -5",
		},
		{
			name: "zero duration",
			opts: Options{PIDs: []int{123}, FrequencyHz: 99, Duration: 0},
			want: "must be positive",
		},
		{
			name: "negative duration",
			opts: Options{PIDs: []int{123}, FrequencyHz: 99, Duration: -time.Second},
			want: "must be positive",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := normalizeOptions(test.opts)
			if err == nil {
				t.Fatalf("normalizeOptions accepted invalid input")
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %q, want substring %q", err, test.want)
			}
		})
	}

	for _, frequency := range []int{1, 999} {
		opts := valid
		opts.FrequencyHz = frequency
		if _, err := normalizeOptions(opts); err != nil {
			t.Fatalf("frequency %d rejected: %v", frequency, err)
		}
	}
}

func TestStackPerfEventAttr(t *testing.T) {
	attr := stackPerfEventAttr(99)
	if attr.Type != unix.PERF_TYPE_SOFTWARE {
		t.Fatalf("Type = %d, want PERF_TYPE_SOFTWARE", attr.Type)
	}
	if attr.Config != unix.PERF_COUNT_SW_CPU_CLOCK {
		t.Fatalf("Config = %d, want PERF_COUNT_SW_CPU_CLOCK", attr.Config)
	}
	if attr.Sample != 99 {
		t.Fatalf("Sample = %d, want 99", attr.Sample)
	}
	for name, bit := range map[string]uint64{
		"freq":         unix.PerfBitFreq,
		"inherit":      unix.PerfBitInherit,
		"disabled":     unix.PerfBitDisabled,
		"exclude_user": unix.PerfBitExcludeUser,
		"exclude_hv":   unix.PerfBitExcludeHv,
	} {
		if attr.Bits&bit == 0 {
			t.Fatalf("Bits is missing %s (bits=%#x)", name, attr.Bits)
		}
	}
	if attr.Bits&unix.PerfBitExcludeKernel != 0 {
		t.Fatalf("Bits excludes kernel mode (bits=%#x); kernel stacks cannot be collected", attr.Bits)
	}
	if attr.Size != uint32(unsafe.Sizeof(attr)) {
		t.Fatalf("Size = %d, want %d", attr.Size, unsafe.Sizeof(attr))
	}
}

func TestKernelOnlySamplingWarning(t *testing.T) {
	const want = "perf event excludes user-mode time; samples are kernel on-CPU only"
	if kernelOnlySamplingWarning != want {
		t.Fatalf("kernelOnlySamplingWarning = %q, want %q", kernelOnlySamplingWarning, want)
	}
}

func TestTrimStackRemovesOnlyTrailingZeros(t *testing.T) {
	tests := []struct {
		name string
		in   []uint64
		want []uint64
	}{
		{name: "nil", in: nil, want: []uint64{}},
		{name: "all zero", in: []uint64{0, 0, 0}, want: []uint64{}},
		{name: "trailing zeros", in: []uint64{0x10, 0x20, 0, 0}, want: []uint64{0x10, 0x20}},
		{name: "interior zero", in: []uint64{0x10, 0, 0x30, 0}, want: []uint64{0x10, 0, 0x30}},
		{name: "no zeros", in: []uint64{0x10, 0x20}, want: []uint64{0x10, 0x20}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := trimStack(test.in)
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("trimStack(%v) = %v, want %v", test.in, got, test.want)
			}
		})
	}
}

func TestBuildSamplesAggregatesByPIDAndStack(t *testing.T) {
	counts := map[stackCountKey]uint64{
		{PID: 100, StackID: 1}: 3,
		{PID: 100, StackID: 2}: 1,
		{PID: 200, StackID: 1}: 2,
	}
	traces := map[uint32][]uint64{
		1: {0x11, 0x22},
		2: {0x33},
	}
	samples, warnings := buildSamples(counts, traces)
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v, want none", warnings)
	}
	want := []Sample{
		{PID: 100, TID: 0, Count: 3, Addrs: []uint64{0x11, 0x22}},
		{PID: 100, TID: 0, Count: 1, Addrs: []uint64{0x33}},
		{PID: 200, TID: 0, Count: 2, Addrs: []uint64{0x11, 0x22}},
	}
	if !reflect.DeepEqual(samples, want) {
		t.Fatalf("samples = %+v, want %+v", samples, want)
	}
	if total := sampleTotal(samples); total != 6 {
		t.Fatalf("sampleTotal = %d, want 6", total)
	}

	traces[1][0] = 0xdead
	if samples[0].Addrs[0] != 0x11 {
		t.Fatalf("sample aliases the stack_traces buffer")
	}
}

func TestBuildSamplesReportsMissingTrace(t *testing.T) {
	counts := map[stackCountKey]uint64{
		{PID: 7, StackID: 42}: 5,
	}
	samples, warnings := buildSamples(counts, map[uint32][]uint64{})
	if len(samples) != 0 {
		t.Fatalf("samples = %v, want none", samples)
	}
	if len(warnings) != 1 {
		t.Fatalf("warnings = %v, want one", warnings)
	}
	for _, want := range []string{"stack_id 42", "dropped 5"} {
		if !strings.Contains(warnings[0], want) {
			t.Fatalf("warning = %q, want substring %q", warnings[0], want)
		}
	}
	if total := sampleTotal(samples); total != 0 {
		t.Fatalf("sampleTotal = %d, want 0", total)
	}
}

func TestEmbeddedBPFObject(t *testing.T) {
	spec, err := loadCollectionSpec()
	if err != nil {
		t.Fatalf("loadCollectionSpec: %v", err)
	}

	program, ok := spec.Programs[stackProgramName]
	if !ok {
		t.Fatalf("program %q missing; have %v", stackProgramName, stacksTestMapKeys(spec.Programs))
	}
	if program.Type != ebpf.PerfEvent {
		t.Fatalf("program type = %v, want perf_event", program.Type)
	}
	if program.SectionName != "perf_event" {
		t.Fatalf("program section = %q, want perf_event", program.SectionName)
	}

	tests := []struct {
		name      string
		wantType  ebpf.MapType
		wantKey   uint32
		wantValue uint32
		wantMax   uint32
	}{
		{name: targetPIDsMapName, wantType: ebpf.Hash, wantKey: 4, wantValue: 1, wantMax: 1024},
		{name: stackTracesMapName, wantType: ebpf.StackTrace, wantKey: 4, wantValue: stackDepth * 8, wantMax: 16384},
		{name: stackCountsMapName, wantType: ebpf.Hash, wantKey: 8, wantValue: 8, wantMax: 131072},
		{name: stackErrorsMapName, wantType: ebpf.Hash, wantKey: 4, wantValue: 8, wantMax: 64},
		{name: stackStatsMapName, wantType: ebpf.PerCPUArray, wantKey: 4, wantValue: 8, wantMax: 4},
	}
	for _, test := range tests {
		m, ok := spec.Maps[test.name]
		if !ok {
			t.Fatalf("map %q missing; have %v", test.name, stacksTestMapKeys(spec.Maps))
		}
		if m.Type != test.wantType {
			t.Errorf("map %s type = %v, want %v", test.name, m.Type, test.wantType)
		}
		if m.KeySize != test.wantKey {
			t.Errorf("map %s key size = %d, want %d", test.name, m.KeySize, test.wantKey)
		}
		if m.ValueSize != test.wantValue {
			t.Errorf("map %s value size = %d, want %d", test.name, m.ValueSize, test.wantValue)
		}
		if m.MaxEntries != test.wantMax {
			t.Errorf("map %s max entries = %d, want %d", test.name, m.MaxEntries, test.wantMax)
		}
	}
}

func TestEnumerateTargetThreadsDeduplicatesAndSorts(t *testing.T) {
	root := t.TempDir()
	writeFakeTask(t, root, 100, 1002, 1001)
	writeFakeTask(t, root, 200, 1001, 2001)

	targets, err := enumerateTargetThreads(root, []int{100, 200}, 4096)
	if err != nil {
		t.Fatalf("enumerateTargetThreads: %v", err)
	}
	want := []threadTarget{
		{ProcessPID: 100, TID: 1001},
		{ProcessPID: 100, TID: 1002},
		{ProcessPID: 200, TID: 2001},
	}
	if !reflect.DeepEqual(targets, want) {
		t.Fatalf("targets = %+v, want %+v", targets, want)
	}
}

func TestEnumerateTargetThreadsEnforcesLimit(t *testing.T) {
	root := t.TempDir()
	writeFakeTask(t, root, 500, 10, 11, 12, 13)

	_, err := enumerateTargetThreads(root, []int{500}, 3)
	if err == nil {
		t.Fatal("enumerateTargetThreads silently accepted more than three threads")
	}
	for _, want := range []string{"more than 3", "perf events"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want substring %q", err, want)
		}
	}

	targets, err := enumerateTargetThreads(root, []int{500}, 4)
	if err != nil {
		t.Fatalf("limit of exactly four threads rejected: %v", err)
	}
	if len(targets) != 4 {
		t.Fatalf("len(targets) = %d, want 4", len(targets))
	}
}

func TestEnumerateTargetThreadsRejectsEmptyTaskDir(t *testing.T) {
	root := t.TempDir()
	taskDir := writeFakeTask(t, root, 600)

	_, err := enumerateTargetThreads(root, []int{600}, 4096)
	if err == nil {
		t.Fatal("enumerateTargetThreads accepted an empty task directory")
	}
	for _, want := range []string{"pid 600", taskDir} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want substring %q", err, want)
		}
	}
}

func TestEnumerateTargetThreadsReportsEnumerationFailure(t *testing.T) {
	root := t.TempDir()
	taskDir := filepath.Join(root, strconv.Itoa(700), "task")

	_, err := enumerateTargetThreads(root, []int{700}, 4096)
	if err == nil {
		t.Fatal("enumerateTargetThreads accepted a missing proc task directory")
	}
	for _, want := range []string{"pid 700", taskDir} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want substring %q", err, want)
		}
	}
}

func TestFormatStackErrors(t *testing.T) {
	if got := formatStackErrors(nil); got != "" {
		t.Fatalf("formatStackErrors(nil) = %q, want empty", got)
	}
	if got := formatStackErrors(map[int32]uint64{-12: 0}); got != "" {
		t.Fatalf("formatStackErrors(zero count) = %q, want empty", got)
	}
	got := formatStackErrors(map[int32]uint64{-12: 3, -17: 489, -7: 2})
	want := "bpf_get_stackid errors: -17=489, -12=3, -7=2"
	if got != want {
		t.Fatalf("formatStackErrors = %q, want %q", got, want)
	}
}

func TestPerfEventOpenErrorExplainsCommonFailures(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "exists", err: unix.EEXIST, want: "EEXIST"},
		{name: "unsupported event", err: unix.ENOENT, want: "ENOENT"},
		{name: "missing process", err: unix.ESRCH, want: "does not exist"},
		{name: "permission", err: unix.EPERM, want: "permission denied"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := perfEventOpenError(threadTarget{ProcessPID: 321, TID: 987}, test.err)
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %q, want substring %q", err, test.want)
			}
			if !strings.Contains(err.Error(), "pid=321 tid=987") {
				t.Fatalf("error = %q, want pid/tid context", err)
			}
			if !strings.Contains(err.Error(), "perf_event_paranoid") {
				t.Fatalf("error = %q, want perf_event_paranoid context", err)
			}
		})
	}
}

func writeFakeTask(t *testing.T, root string, pid int, tids ...int) string {
	t.Helper()
	taskDir := filepath.Join(root, strconv.Itoa(pid), "task")
	if err := os.MkdirAll(taskDir, 0o755); err != nil {
		t.Fatalf("create fake task dir %s: %v", taskDir, err)
	}
	for _, tid := range tids {
		dir := filepath.Join(taskDir, strconv.Itoa(tid))
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatalf("create fake thread dir %s: %v", dir, err)
		}
	}
	return taskDir
}

func stacksTestMapKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	return keys
}
