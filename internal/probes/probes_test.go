package probes

import (
	"bytes"
	"encoding/binary"
	"testing"

	"cann-ebpf/internal/config"
	"cann-ebpf/internal/runlog"

	"github.com/cilium/ebpf"
)

func TestEmbeddedBPFObject(t *testing.T) {
	spec, err := loadCollectionSpec()
	if err != nil {
		t.Fatalf("loadCollectionSpec: %v", err)
	}
	for _, name := range []string{"events", "inflight", "probe_cfg", "probe_stats", "blocking_cfg", "tid_state"} {
		if spec.Maps[name] == nil {
			t.Errorf("map %q missing", name)
		}
	}
	if spec.Maps["probe_stats"] != nil && spec.Maps["probe_stats"].MaxEntries != bpfFunctionSlots {
		t.Fatalf("probe_stats max_entries = %d, want %d", spec.Maps["probe_stats"].MaxEntries, bpfFunctionSlots)
	}
	if got, want := len(spec.Programs), 2*len(supportedFunctions)+len(blockingPrograms); got != want {
		t.Fatalf("programs = %d, want %d", got, want)
	}
	for _, fn := range supportedFunctions {
		entry, exit := programNames(fn)
		if spec.Programs[entry] == nil {
			t.Errorf("entry program %q missing", entry)
		}
		if spec.Programs[exit] == nil {
			t.Errorf("exit program %q missing", exit)
		}
	}
	for _, bp := range blockingPrograms {
		program := spec.Programs[bp.name]
		if program == nil {
			t.Errorf("blocking program %q missing", bp.name)
			continue
		}
		wantType := ebpf.TracePoint
		if bp.kind != "tracepoint" {
			wantType = ebpf.Kprobe
		}
		if program.Type != wantType {
			t.Errorf("blocking program %q type = %v, want %v", bp.name, program.Type, wantType)
		}
	}
}

func TestDecodeSpanEvent(t *testing.T) {
	want := rawSpanEvent{
		StartNS: 100, DurNS: 25, PID: 42, TID: 43, Function: 7, Flags: blockingFlagOffCPU,
		OffCPUNS: 10, FutexNS: 4,
	}
	var buf bytes.Buffer
	if err := binary.Write(&buf, binary.LittleEndian, want); err != nil {
		t.Fatal(err)
	}
	if buf.Len() != 48 {
		t.Fatalf("encoded span event = %d bytes, want 48", buf.Len())
	}
	got, err := decodeSpanEvent(buf.Bytes())
	if err != nil {
		t.Fatalf("decodeSpanEvent: %v", err)
	}
	if got != want {
		t.Fatalf("event = %+v, want %+v", got, want)
	}
	if _, err := decodeSpanEvent(buf.Bytes()[:8]); err == nil {
		t.Fatal("expected short event error")
	}
}

func TestApplyBlockingSample(t *testing.T) {
	var span runlog.Span
	applyBlockingSample(&span, rawSpanEvent{Flags: blockingFlagOffCPU | blockingFlagFutex, OffCPUNS: 7, FutexNS: 3})
	if span.OffCPUNS == nil || *span.OffCPUNS != 7 {
		t.Fatalf("offcpu = %v", span.OffCPUNS)
	}
	if span.FutexNS == nil || *span.FutexNS != 3 {
		t.Fatalf("futex = %v", span.FutexNS)
	}

	var notMeasured runlog.Span
	applyBlockingSample(&notMeasured, rawSpanEvent{Flags: 0, OffCPUNS: 9, FutexNS: 9})
	if notMeasured.OffCPUNS != nil || notMeasured.FutexNS != nil {
		t.Fatal("values without a measurement flag must stay absent")
	}

	var zero runlog.Span
	applyBlockingSample(&zero, rawSpanEvent{Flags: blockingFlagOffCPU})
	if zero.OffCPUNS == nil || *zero.OffCPUNS != 0 {
		t.Fatal("a measured zero must stay present")
	}
}

func TestResolveBlocking(t *testing.T) {
	off := &config.Document{Spec: config.Spec{Probes: config.Probes{
		Sched: config.Probe{Enabled: false},
		Futex: config.Probe{Enabled: false},
	}}}
	on := &config.Document{Spec: config.Spec{Probes: config.Probes{
		Sched: config.Probe{Enabled: true},
		Futex: config.Probe{Enabled: "auto"},
	}}}
	if o, f, err := resolveBlocking(nil, BlockingAuto); err != nil || o || f {
		t.Fatalf("nil profile auto = %v,%v,%v", o, f, err)
	}
	if o, f, err := resolveBlocking(off, BlockingAuto); err != nil || o || f {
		t.Fatalf("off profile auto = %v,%v,%v", o, f, err)
	}
	if o, f, err := resolveBlocking(off, BlockingOn); err != nil || !o || !f {
		t.Fatalf("off profile on = %v,%v,%v", o, f, err)
	}
	if o, f, err := resolveBlocking(on, BlockingOff); err != nil || o || f {
		t.Fatalf("on profile off = %v,%v,%v", o, f, err)
	}
	if o, f, err := resolveBlocking(on, BlockingAuto); err != nil || !o || !f {
		t.Fatalf("on profile auto = %v,%v,%v", o, f, err)
	}
	if _, _, err := resolveBlocking(on, BlockingMode("sometimes")); err == nil {
		t.Fatal("expected unknown mode error")
	}
}

func TestRequestedFunctionsRespectsProfile(t *testing.T) {
	doc := &config.Document{Spec: config.Spec{
		Cann: config.Cann{SymbolOverrides: config.SymbolOverrides{
			Hccl: []string{"HcclAllReduce", "not-compiled"},
		}},
	}}
	functions, warnings := requestedFunctions(doc)
	if len(functions) != 1 || functions[0].Name != "HcclAllReduce" {
		t.Fatalf("functions = %+v", functions)
	}
	if len(warnings) != 1 {
		t.Fatalf("warnings = %+v", warnings)
	}
}

func TestProbeConfigForProfile(t *testing.T) {
	doc := &config.Document{Spec: config.Spec{Probes: config.Probes{
		Submit: config.Probe{Enabled: true, SampleRate: 64, MinDurationUS: 500},
	}}}
	fn, ok := functionByID(26)
	if !ok {
		t.Fatal("function 6 missing")
	}
	enabled, rate, minNS := probeConfigFor(doc, fn)
	if !enabled || rate != 64 || minNS != 500000 {
		t.Fatalf("config = enabled=%v rate=%d min=%d", enabled, rate, minNS)
	}
}

func TestOptionsRejectDuplicatePID(t *testing.T) {
	opts := Options{PIDs: []int{1, 1}}
	if err := opts.normalize(); err == nil {
		t.Fatal("expected duplicate pid error")
	}
}

func TestSupportedFunctionsAreContiguous(t *testing.T) {
	functions := SupportedFunctions()
	if len(functions) != 37 {
		t.Fatalf("supported functions = %d, want 37", len(functions))
	}
	for i, fn := range functions {
		if fn.ID != uint32(i+1) {
			t.Fatalf("function[%d] id = %d, want %d", i, fn.ID, i+1)
		}
		if fn.Name == "" || fn.Category == "" {
			t.Fatalf("function[%d] incomplete: %+v", i, fn)
		}
	}
}

func TestProbeConfigUsesCategoryProbe(t *testing.T) {
	doc := &config.Document{Spec: config.Spec{Probes: config.Probes{
		Sync:   config.Probe{Enabled: true, SampleRate: 2, MinDurationUS: 10},
		Event:  config.Probe{Enabled: false, SampleRate: 3, MinDurationUS: 20},
		Handle: config.Probe{Enabled: true, SampleRate: 4, MinDurationUS: 30},
		Memory: config.Probe{Enabled: true, SampleRate: 5, MinDurationUS: 40},
		Submit: config.Probe{Enabled: true, SampleRate: 6, MinDurationUS: 50},
		Hccl:   config.Probe{Enabled: true, SampleRate: 7, MinDurationUS: 60},
	}}}
	cases := []struct {
		name    string
		enabled bool
		rate    uint32
		minNS   uint64
	}{
		{"aclrtSynchronizeStream", true, 2, 10000},
		{"aclrtRecordEvent", false, 3, 20000},
		{"aclrtSetDevice", true, 4, 30000},
		{"aclrtMalloc", true, 5, 40000},
		{"aclrtMemcpyAsync", true, 6, 50000},
		{"HcclAllReduce", true, 7, 60000},
	}
	for _, testCase := range cases {
		var fn Function
		for _, candidate := range supportedFunctions {
			if candidate.Name == testCase.name {
				fn = candidate
				break
			}
		}
		if fn.Name == "" {
			t.Fatalf("missing function %s", testCase.name)
		}
		enabled, rate, minNS := probeConfigFor(doc, fn)
		if enabled != testCase.enabled || rate != testCase.rate || minNS != testCase.minNS {
			t.Fatalf("%s config = enabled=%v rate=%d min=%d, want enabled=%v rate=%d min=%d",
				testCase.name, enabled, rate, minNS, testCase.enabled, testCase.rate, testCase.minNS)
		}
	}
}
