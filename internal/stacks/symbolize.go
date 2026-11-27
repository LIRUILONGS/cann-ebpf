package stacks

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
)

const (
	symbolDefaultKallsymsPath = "/proc/kallsyms"
	symbolDefaultModulesPath  = "/proc/modules"
)

var samplerFrameMarkers = []string{
	"perf_swevent_",
	"__perf_event_overflow",
	"bpf_get_stackid",
	"perf_event_",
	"bpf_prog_",
	"__bpf_prog_",
	"perf_trace_",
}

type kernelSymbol struct {
	Addr   uint64
	Type   byte
	Name   string
	Module string
}

type kernelModule struct {
	Name string
	Base uint64
	Size uint64
}

type kallsymsParseResult struct {
	Symbols     []kernelSymbol
	ZeroEntries int
	Malformed   int
	TotalLines  int
}

type modulesParseResult struct {
	Modules    []kernelModule
	ZeroBases  int
	Malformed  int
	TotalLines int
}

// Symbolize resolves innermost-first raw stacks into root-first categorized
// frames and persists enough metadata for a reader to judge coverage. A
// kptr_restrict deployment with an all-zero kallsyms file is represented as an
// explicit unknown-only profile plus warnings, never as successful
// symbolization.
func Symbolize(samples []Sample, meta Meta, opts SymbolizeOptions) (*Profile, Meta, error) {
	kallsymsPath := opts.KallsymsPath
	if strings.TrimSpace(kallsymsPath) == "" {
		kallsymsPath = symbolDefaultKallsymsPath
	}
	modulesPath := opts.ModulesPath
	if strings.TrimSpace(modulesPath) == "" {
		modulesPath = symbolDefaultModulesPath
	}

	rules := DefaultSymbolRules()
	if strings.TrimSpace(opts.RulesPath) != "" {
		loaded, err := LoadSymbolRules(opts.RulesPath)
		if err != nil {
			return nil, meta, err
		}
		rules = loaded
	}

	kallsymsData, err := os.ReadFile(kallsymsPath)
	if err != nil {
		return nil, meta, fmt.Errorf("read kallsyms %s: %w", kallsymsPath, err)
	}
	modulesData, err := os.ReadFile(modulesPath)
	if err != nil {
		return nil, meta, fmt.Errorf("read modules %s: %w", modulesPath, err)
	}

	kallsyms, err := parseKallsymsData(kallsymsData)
	if err != nil {
		return nil, meta, fmt.Errorf("parse kallsyms %s: %w", kallsymsPath, err)
	}
	modules, err := parseModulesData(modulesData)
	if err != nil {
		return nil, meta, fmt.Errorf("parse modules %s: %w", modulesPath, err)
	}

	symbolHash := sha256.Sum256(kallsymsData)
	meta.KallsymsSHA256 = hex.EncodeToString(symbolHash[:])
	if meta.Schema == "" {
		meta.Schema = SchemaV1
	}
	if meta.Collector == "" {
		meta.Collector = CollectorPerfEvent
	}

	warnings := append([]string(nil), meta.Warnings...)
	if len(kallsyms.Symbols) == 0 {
		if kallsyms.ZeroEntries > 0 {
			warnings = appendWarning(warnings, "kallsyms contains only zero addresses (kptr_restrict?); symbolization unavailable")
		} else {
			warnings = appendWarning(warnings, "kallsyms contains no usable symbols; symbolization unavailable")
		}
	}
	if kallsyms.Malformed > 0 {
		warnings = appendWarning(warnings, fmt.Sprintf("kallsyms: skipped %d malformed line(s)", kallsyms.Malformed))
	}
	if modules.Malformed > 0 {
		warnings = appendWarning(warnings, fmt.Sprintf("modules: skipped %d malformed line(s)", modules.Malformed))
	}

	stackIndex := make(map[string]int)
	stacks := make([]ResolvedStack, 0)
	var sampleTotal uint64

	for _, sample := range samples {
		sampleTotal += sample.Count
		frames := resolveSampleFrames(sample.Addrs, kallsyms.Symbols, modules.Modules, rules)
		key := stackIdentity(frames)
		if index, ok := stackIndex[key]; ok {
			stacks[index].Count += sample.Count
			continue
		}
		stackIndex[key] = len(stacks)
		stacks = append(stacks, ResolvedStack{Frames: frames, Count: sample.Count})
	}

	sort.Slice(stacks, func(i, j int) bool {
		left := foldedStackString(stacks[i].Frames)
		right := foldedStackString(stacks[j].Frames)
		if left != right {
			return left < right
		}
		return stackIdentity(stacks[i].Frames) < stackIdentity(stacks[j].Frames)
	})

	var knownFrames uint64
	var totalFrames uint64
	var profileTotal uint64
	for _, stack := range stacks {
		profileTotal += stack.Count
		frameCount := uint64(len(stack.Frames))
		totalFrames += frameCount * stack.Count
		for _, frame := range stack.Frames {
			if !frame.Unknown {
				knownFrames += stack.Count
			}
		}
	}

	coverage := 0.0
	if totalFrames > 0 {
		coverage = float64(knownFrames) / float64(totalFrames)
		if coverage > 1 {
			coverage = 1
		}
		if coverage < 0 {
			coverage = 0
		}
	}

	meta.Samples = sampleTotal
	meta.UniqueStacks = len(stacks)
	meta.SymbolCoverage = coverage
	meta.UnknownPct = 100 * (1 - coverage)
	meta.Warnings = warnings

	return &Profile{Stacks: stacks, Total: profileTotal}, meta, nil
}

func appendWarning(warnings []string, warning string) []string {
	for _, existing := range warnings {
		if existing == warning {
			return warnings
		}
	}
	return append(warnings, warning)
}

func parseKallsymsData(data []byte) (kallsymsParseResult, error) {
	var result kallsymsParseResult
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 4096), 1024*1024)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		result.TotalLines++

		fields := strings.Fields(line)
		if len(fields) < 3 || len(fields[1]) != 1 {
			result.Malformed++
			continue
		}
		addr, err := strconv.ParseUint(fields[0], 16, 64)
		if err != nil {
			result.Malformed++
			continue
		}
		if addr == 0 {
			result.ZeroEntries++
			continue
		}

		module := ""
		if len(fields) >= 4 {
			field := fields[3]
			if strings.HasPrefix(field, "[") && strings.HasSuffix(field, "]") && len(field) > 2 {
				module = field[1 : len(field)-1]
			}
		}
		result.Symbols = append(result.Symbols, kernelSymbol{
			Addr:   addr,
			Type:   fields[1][0],
			Name:   fields[2],
			Module: module,
		})
	}
	if err := scanner.Err(); err != nil {
		return result, fmt.Errorf("scan: %w", err)
	}
	if result.TotalLines > 0 && len(result.Symbols) == 0 && result.ZeroEntries == 0 {
		return result, fmt.Errorf("no valid symbol entries")
	}

	sort.SliceStable(result.Symbols, func(i, j int) bool {
		if result.Symbols[i].Addr != result.Symbols[j].Addr {
			return result.Symbols[i].Addr < result.Symbols[j].Addr
		}
		return symbolPriority(result.Symbols[i]) < symbolPriority(result.Symbols[j])
	})

	deduplicated := result.Symbols[:0]
	for i, symbol := range result.Symbols {
		if i > 0 && symbol.Addr == result.Symbols[i-1].Addr {
			continue
		}
		deduplicated = append(deduplicated, symbol)
	}
	result.Symbols = deduplicated
	return result, nil
}

func symbolPriority(symbol kernelSymbol) int {
	if symbol.Type == 't' || symbol.Type == 'T' {
		if strings.HasPrefix(symbol.Name, "$") {
			return 1
		}
		return 0
	}
	if symbol.Type != 'a' && symbol.Type != 'A' {
		return 2
	}
	return 3
}

func parseModulesData(data []byte) (modulesParseResult, error) {
	var result modulesParseResult
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 4096), 1024*1024)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		result.TotalLines++

		fields := strings.Fields(line)
		if len(fields) < 6 {
			result.Malformed++
			continue
		}
		size, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil || size == 0 {
			result.Malformed++
			continue
		}
		baseField := moduleBaseField(fields)
		if baseField == "" {
			result.Malformed++
			continue
		}
		base, err := strconv.ParseUint(baseField, 0, 64)
		if err != nil {
			result.Malformed++
			continue
		}
		if base == 0 {
			result.ZeroBases++
			continue
		}
		result.Modules = append(result.Modules, kernelModule{
			Name: fields[0],
			Base: base,
			Size: size,
		})
	}
	if err := scanner.Err(); err != nil {
		return result, fmt.Errorf("scan: %w", err)
	}
	if result.TotalLines > 0 && len(result.Modules) == 0 && result.ZeroBases == 0 {
		return result, fmt.Errorf("no valid module entries")
	}

	sort.Slice(result.Modules, func(i, j int) bool {
		if result.Modules[i].Base != result.Modules[j].Base {
			return result.Modules[i].Base < result.Modules[j].Base
		}
		return result.Modules[i].Name < result.Modules[j].Name
	})
	return result, nil
}

func moduleBaseField(fields []string) string {
	if len(fields) > 5 && isHexAddressField(fields[5]) {
		return fields[5]
	}
	for _, field := range fields[5:] {
		if isHexAddressField(field) {
			return field
		}
	}
	return ""
}

func isHexAddressField(field string) bool {
	return len(field) > 2 && (strings.HasPrefix(field, "0x") || strings.HasPrefix(field, "0X"))
}

func resolveSampleFrames(addrs []uint64, symbols []kernelSymbol, modules []kernelModule, rules *SymbolRules) []Frame {
	if len(addrs) == 0 {
		return []Frame{{
			Name:     "[unknown]",
			Category: ClassifyName("[unknown]", "", rules),
			Unknown:  true,
		}}
	}

	resolved := make([]Frame, 0, len(addrs))
	for i := len(addrs) - 1; i >= 0; i-- {
		resolved = append(resolved, resolveAddress(addrs[i], symbols, modules))
	}

	kept := make([]Frame, 0, len(resolved))
	filtered := make([]Frame, 0, len(resolved))
	for _, frame := range resolved {
		if isSamplerFrame(frame) {
			filtered = append(filtered, frame)
			continue
		}
		kept = append(kept, frame)
	}
	if len(kept) == 0 {
		if len(filtered) > 0 {
			for i := range filtered {
				filtered[i].Category = "sample"
			}
			kept = filtered
		}
	}

	for i := range kept {
		if kept[i].Category == "" {
			kept[i].Category = ClassifyName(kept[i].Name, kept[i].Module, rules)
		}
	}
	return kept
}

func resolveAddress(addr uint64, symbols []kernelSymbol, modules []kernelModule) Frame {
	frame := Frame{Addr: addr}
	module, inModule := moduleForAddress(addr, modules)
	if inModule {
		frame.Module = module.Name
	}

	if addr != 0 {
		var (
			symbol kernelSymbol
			found  bool
		)
		if inModule {
			symbol, found = nearestModuleSymbol(symbols, module, addr)
		} else {
			symbol, found = nearestKernelSymbol(symbols, addr)
		}
		if found {
			frame.Name = symbol.Name
			frame.Offset = addr - symbol.Addr
			return frame
		}
	}

	frame.Name = "[unknown]"
	frame.Unknown = true
	frame.Offset = addr
	return frame
}

func moduleForAddress(addr uint64, modules []kernelModule) (kernelModule, bool) {
	for _, module := range modules {
		if addr >= module.Base && addr-module.Base < module.Size {
			return module, true
		}
	}
	return kernelModule{}, false
}

func nearestModuleSymbol(symbols []kernelSymbol, module kernelModule, addr uint64) (kernelSymbol, bool) {
	index := sort.Search(len(symbols), func(i int) bool {
		return symbols[i].Addr > addr
	})
	for index > 0 {
		index--
		symbol := symbols[index]
		if symbol.Addr < module.Base {
			return kernelSymbol{}, false
		}
		if symbol.Module == "" || strings.EqualFold(symbol.Module, module.Name) {
			return symbol, true
		}
	}
	return kernelSymbol{}, false
}

func nearestKernelSymbol(symbols []kernelSymbol, addr uint64) (kernelSymbol, bool) {
	index := sort.Search(len(symbols), func(i int) bool {
		return symbols[i].Addr > addr
	})
	if index == 0 {
		return kernelSymbol{}, false
	}
	symbol := symbols[index-1]
	if symbol.Module != "" {
		return kernelSymbol{}, false
	}
	return symbol, true
}

func isSamplerFrame(frame Frame) bool {
	lowerName := strings.ToLower(frame.Name)
	for _, marker := range samplerFrameMarkers {
		if strings.Contains(lowerName, marker) {
			return true
		}
	}
	return false
}

func stackIdentity(frames []Frame) string {
	var builder strings.Builder
	for _, frame := range frames {
		builder.WriteString(strconv.Itoa(len(frame.Name)))
		builder.WriteByte(':')
		builder.WriteString(frame.Name)
		builder.WriteByte(';')
	}
	return builder.String()
}
