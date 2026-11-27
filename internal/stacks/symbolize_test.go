package stacks

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func mustWriteTestFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func findTestStack(t *testing.T, profile *Profile, names ...string) *ResolvedStack {
	t.Helper()
	for i := range profile.Stacks {
		if reflect.DeepEqual(frameNames(profile.Stacks[i].Frames), names) {
			return &profile.Stacks[i]
		}
	}
	t.Fatalf("stack %v not found in profile %+v", names, profile.Stacks)
	return nil
}

func frameNames(frames []Frame) []string {
	names := make([]string, 0, len(frames))
	for _, frame := range frames {
		names = append(names, frame.Name)
	}
	return names
}

func TestSymbolizeResolvesNearestModuleAndMetadata(t *testing.T) {
	dir := t.TempDir()
	kallsyms := strings.Join([]string{
		"0000000000003000 A mod_alias [demo_mod]",
		"0000000000000000 A zero_symbol",
		"0000000000001000 T schedule_root",
		"0000000000002100 T tcp_leaf",
		"0000000000002000 t dma_other_text",
		"0000000000003100 d mod_data [demo_mod]",
		"0000000000003000 t devdrv_mod_entry [demo_mod]",
		"0000000000002200 A dma_absolute",
		"0000000000001100 T futex_mid",
		"",
	}, "\n")
	modules := "demo_mod 4096 0 - Live 0x0000000000003000\nother_mod 8192 0 - Live 0x0000000000005000\n"
	kallsymsPath := mustWriteTestFile(t, dir, "kallsyms", kallsyms)
	modulesPath := mustWriteTestFile(t, dir, "modules", modules)

	samples := []Sample{
		{PID: 10, TID: 11, Count: 2, Addrs: []uint64{0x3050, 0x2110, 0x1000}},
		{PID: 20, TID: 21, Count: 3, Addrs: []uint64{0x3050, 0x2110, 0x1000}},
		{PID: 10, TID: 12, Count: 1, Addrs: []uint64{0x10, 0x1100}},
	}
	meta := Meta{Host: "test-host", Kernel: "6.6-test", FrequencyHz: 99}
	profile, gotMeta, err := Symbolize(samples, meta, SymbolizeOptions{
		KallsymsPath: kallsymsPath,
		ModulesPath:  modulesPath,
	})
	if err != nil {
		t.Fatalf("Symbolize: %v", err)
	}

	stackA := findTestStack(t, profile, "schedule_root", "tcp_leaf", "devdrv_mod_entry")
	if stackA.Count != 5 {
		t.Fatalf("stack A count = %d, want 5", stackA.Count)
	}
	if stackA.Frames[0].Category != "sync" || stackA.Frames[1].Category != "net" || stackA.Frames[2].Category != "npu" {
		t.Fatalf("stack A categories = %q, %q, %q", stackA.Frames[0].Category, stackA.Frames[1].Category, stackA.Frames[2].Category)
	}
	if stackA.Frames[1].Offset != 0x10 {
		t.Fatalf("nearest symbol offset = %#x, want 0x10", stackA.Frames[1].Offset)
	}
	moduleFrame := stackA.Frames[2]
	if moduleFrame.Module != "demo_mod" || moduleFrame.Name != "devdrv_mod_entry" || moduleFrame.Offset != 0x50 {
		t.Fatalf("module frame = %+v", moduleFrame)
	}
	if moduleFrame.Unknown {
		t.Fatalf("module frame unexpectedly unknown")
	}

	stackB := findTestStack(t, profile, "futex_mid", "[unknown]")
	if stackB.Count != 1 {
		t.Fatalf("stack B count = %d, want 1", stackB.Count)
	}
	unknown := stackB.Frames[1]
	if !unknown.Unknown || unknown.Offset != 0x10 || unknown.Category != "other" {
		t.Fatalf("unknown frame = %+v", unknown)
	}
	if stackB.Frames[0].Category != "sync" {
		t.Fatalf("futex category = %q", stackB.Frames[0].Category)
	}

	if profile.Total != 6 {
		t.Fatalf("profile total = %d, want 6", profile.Total)
	}
	if gotMeta.Schema != SchemaV1 || gotMeta.Collector != CollectorPerfEvent {
		t.Fatalf("schema/collector = %q/%q", gotMeta.Schema, gotMeta.Collector)
	}
	if gotMeta.Host != "test-host" || gotMeta.Kernel != "6.6-test" || gotMeta.FrequencyHz != 99 {
		t.Fatalf("metadata not preserved: %+v", gotMeta)
	}
	if gotMeta.Samples != 6 || gotMeta.UniqueStacks != 2 {
		t.Fatalf("samples/unique = %d/%d", gotMeta.Samples, gotMeta.UniqueStacks)
	}
	wantHashBytes := sha256.Sum256([]byte(kallsyms))
	if gotMeta.KallsymsSHA256 != hex.EncodeToString(wantHashBytes[:]) {
		t.Fatalf("kallsyms hash = %q", gotMeta.KallsymsSHA256)
	}
	wantCoverage := 16.0 / 17.0
	if math.Abs(gotMeta.SymbolCoverage-wantCoverage) > 1e-12 {
		t.Fatalf("coverage = %v, want %v", gotMeta.SymbolCoverage, wantCoverage)
	}
	if math.Abs(gotMeta.UnknownPct-100.0/17.0) > 1e-12 {
		t.Fatalf("unknown pct = %v, want %v", gotMeta.UnknownPct, 100.0/17.0)
	}
	if len(gotMeta.Warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", gotMeta.Warnings)
	}
}

func TestParseModulesDataAcceptsOptionalOE(t *testing.T) {
	data := strings.Join([]string{
		"drv_vascend 126976 8 - Live 0xffffc076ffee9000",
		"drv_dvpp_cmdlist 53248 2 - Live 0xffffc076fff00000 (OE)",
		"zero_module 4096 0 - Live 0x0000000000000000",
		"bad_module 4096 0 - Live not-an-address",
		"short_module 4096 - Live",
		"",
	}, "\n")

	parsed, err := parseModulesData([]byte(data))
	if err != nil {
		t.Fatalf("parseModulesData: %v", err)
	}
	if len(parsed.Modules) != 2 {
		t.Fatalf("modules = %+v, want 2 valid entries", parsed.Modules)
	}
	byName := make(map[string]kernelModule, len(parsed.Modules))
	for _, module := range parsed.Modules {
		byName[module.Name] = module
	}
	vascend, ok := byName["drv_vascend"]
	if !ok || vascend.Base != 0xffffc076ffee9000 || vascend.Size != 126976 {
		t.Fatalf("drv_vascend = %+v", vascend)
	}
	dvpp, ok := byName["drv_dvpp_cmdlist"]
	if !ok || dvpp.Base != 0xffffc076fff00000 || dvpp.Size != 53248 {
		t.Fatalf("drv_dvpp_cmdlist = %+v", dvpp)
	}
	if parsed.ZeroBases != 1 {
		t.Fatalf("zero bases = %d, want 1", parsed.ZeroBases)
	}
	if parsed.Malformed != 2 {
		t.Fatalf("malformed = %d, want 2", parsed.Malformed)
	}
}

func TestSymbolizePrefersRealFunctionOverDollarAlias(t *testing.T) {
	dir := t.TempDir()
	kallsyms := strings.Join([]string{
		"ffffc076ffee9000 t $x [drv_vascend]",
		"ffffc076ffee9000 t hw_dvt_cleanup_vdavinci_type_groups [drv_vascend]",
		"",
	}, "\n")
	modules := "drv_vascend 126976 8 - Live 0xffffc076ffee9000 (OE)\n"
	kallsymsPath := mustWriteTestFile(t, dir, "kallsyms", kallsyms)
	modulesPath := mustWriteTestFile(t, dir, "modules", modules)

	samples := []Sample{{Count: 1, Addrs: []uint64{0xffffc076ffee9020}}}
	profile, _, err := Symbolize(samples, Meta{}, SymbolizeOptions{
		KallsymsPath: kallsymsPath,
		ModulesPath:  modulesPath,
	})
	if err != nil {
		t.Fatalf("Symbolize: %v", err)
	}
	if len(profile.Stacks) != 1 || len(profile.Stacks[0].Frames) != 1 {
		t.Fatalf("profile = %+v", profile)
	}
	frame := profile.Stacks[0].Frames[0]
	if frame.Name != "hw_dvt_cleanup_vdavinci_type_groups" {
		t.Fatalf("frame name = %q, want real function", frame.Name)
	}
	if frame.Module != "drv_vascend" || frame.Offset != 0x20 || frame.Unknown {
		t.Fatalf("frame = %+v", frame)
	}
}

func TestSymbolPriorityPrefersRealTextAndKeepsDollarFallback(t *testing.T) {
	tests := []struct {
		name   string
		symbol kernelSymbol
		want   int
	}{
		{name: "real lowercase text", symbol: kernelSymbol{Type: 't', Name: "real_fn"}, want: 0},
		{name: "real uppercase text", symbol: kernelSymbol{Type: 'T', Name: "real_fn"}, want: 0},
		{name: "dollar lowercase text", symbol: kernelSymbol{Type: 't', Name: "$x"}, want: 1},
		{name: "dollar uppercase text", symbol: kernelSymbol{Type: 'T', Name: "$x"}, want: 1},
		{name: "other non-absolute", symbol: kernelSymbol{Type: 'd', Name: "data"}, want: 2},
		{name: "absolute", symbol: kernelSymbol{Type: 'A', Name: "abs"}, want: 3},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := symbolPriority(test.symbol); got != test.want {
				t.Fatalf("symbolPriority(%+v) = %d, want %d", test.symbol, got, test.want)
			}
		})
	}
}

func TestSymbolizeFiltersSamplerAndKeepsRepeatedFrames(t *testing.T) {
	dir := t.TempDir()
	kallsyms := strings.Join([]string{
		"0000000000001000 T schedule_root",
		"0000000000001200 T futex_leaf",
		"0000000000002000 T BPF_Get_StackId",
		"0000000000002100 T PERF_EVENT_OPEN",
		"",
	}, "\n")
	kallsymsPath := mustWriteTestFile(t, dir, "kallsyms", kallsyms)
	modulesPath := mustWriteTestFile(t, dir, "modules", "")

	samples := []Sample{
		{Count: 2, Addrs: []uint64{0x1210, 0x2100, 0x2000}},
		{Count: 1, Addrs: []uint64{0x1200, 0x1200, 0x1200}},
		{Count: 4, Addrs: []uint64{0x2100, 0x2000}},
	}
	profile, meta, err := Symbolize(samples, Meta{}, SymbolizeOptions{
		KallsymsPath: kallsymsPath,
		ModulesPath:  modulesPath,
	})
	if err != nil {
		t.Fatalf("Symbolize: %v", err)
	}

	mixed := findTestStack(t, profile, "futex_leaf")
	if mixed.Count != 2 || mixed.Frames[0].Category != "sync" || mixed.Frames[0].Offset != 0x10 {
		t.Fatalf("mixed stack = %+v", mixed)
	}

	repeated := findTestStack(t, profile, "futex_leaf", "futex_leaf", "futex_leaf")
	if repeated.Count != 1 {
		t.Fatalf("repeated count = %d", repeated.Count)
	}

	samplerOnly := findTestStack(t, profile, "BPF_Get_StackId", "PERF_EVENT_OPEN")
	if samplerOnly.Count != 4 {
		t.Fatalf("sampler-only count = %d", samplerOnly.Count)
	}
	for _, frame := range samplerOnly.Frames {
		if frame.Category != "sample" {
			t.Fatalf("sampler fallback frame = %+v", frame)
		}
	}
	if meta.Samples != 7 {
		t.Fatalf("meta samples = %d, want 7", meta.Samples)
	}
}

func TestSymbolizeAllZeroKallsymsIsExplicitlyUnknown(t *testing.T) {
	dir := t.TempDir()
	kallsyms := strings.Join([]string{
		"0000000000000000 T _stext",
		"0000000000000000 t local_symbol",
		"0000000000000000 A absolute_symbol",
		"",
	}, "\n")
	modules := "demo_mod 4096 0 - Live 0x0000000000000000\n"
	kallsymsPath := mustWriteTestFile(t, dir, "kallsyms", kallsyms)
	modulesPath := mustWriteTestFile(t, dir, "modules", modules)

	samples := []Sample{{Count: 7, Addrs: []uint64{0x1234, 0x5678}}}
	profile, meta, err := Symbolize(samples, Meta{Host: "restricted-host"}, SymbolizeOptions{
		KallsymsPath: kallsymsPath,
		ModulesPath:  modulesPath,
	})
	if err != nil {
		t.Fatalf("Symbolize: %v", err)
	}

	stack := findTestStack(t, profile, "[unknown]", "[unknown]")
	if stack.Count != 7 {
		t.Fatalf("unknown stack count = %d", stack.Count)
	}
	if stack.Frames[0].Offset != 0x5678 || stack.Frames[1].Offset != 0x1234 {
		t.Fatalf("unknown offsets = %+v", stack.Frames)
	}
	for _, frame := range stack.Frames {
		if !frame.Unknown {
			t.Fatalf("expected unknown frame: %+v", frame)
		}
	}
	if meta.SymbolCoverage != 0 || meta.UnknownPct != 100 {
		t.Fatalf("coverage/unknown = %v/%v", meta.SymbolCoverage, meta.UnknownPct)
	}
	if len(meta.Warnings) == 0 || !strings.Contains(strings.Join(meta.Warnings, " "), "zero") {
		t.Fatalf("warnings = %v", meta.Warnings)
	}
	if meta.KallsymsSHA256 == "" {
		t.Fatalf("missing kallsyms hash")
	}

	var folded bytes.Buffer
	if err := WriteFolded(&folded, profile); err != nil {
		t.Fatalf("WriteFolded: %v", err)
	}
	if got, want := folded.String(), "[unknown];[unknown] 7\n"; got != want {
		t.Fatalf("folded = %q, want %q", got, want)
	}
}

func TestSymbolizeCoverageIsSampleWeighted(t *testing.T) {
	dir := t.TempDir()
	kallsyms := strings.Join([]string{
		"0000000000001000 T schedule_known",
		"0000000000001100 T futex_known",
		"",
	}, "\n")
	kallsymsPath := mustWriteTestFile(t, dir, "kallsyms", kallsyms)
	modulesPath := mustWriteTestFile(t, dir, "modules", "")

	samples := []Sample{
		{Count: 3, Addrs: []uint64{0x1010, 0x10}},
		{Count: 1, Addrs: []uint64{0x1100, 0x1000}},
	}
	_, meta, err := Symbolize(samples, Meta{}, SymbolizeOptions{
		KallsymsPath: kallsymsPath,
		ModulesPath:  modulesPath,
	})
	if err != nil {
		t.Fatalf("Symbolize: %v", err)
	}
	if math.Abs(meta.SymbolCoverage-0.625) > 1e-12 {
		t.Fatalf("coverage = %v, want 0.625", meta.SymbolCoverage)
	}
	if math.Abs(meta.UnknownPct-37.5) > 1e-12 {
		t.Fatalf("unknown pct = %v, want 37.5", meta.UnknownPct)
	}
}
