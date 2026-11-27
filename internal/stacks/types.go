// Package stacks implements the kernel-stack sampler behind the
// `cann-ebpf stacks` command.
//
// It is deliberately independent from the span tracer in internal/probes:
// a span records how long one CANN call took, while a stack sample records
// which kernel call chain was running at a point in time. Keeping the two
// evidence paths separate is what lets the report state sampling rate,
// stack-loss and symbol coverage without mixing denominators.
//
// The sampler only observes host kernel stacks. AI Core operator time
// (MatMul/FlashAttention and friends) never appears in a host kernel stack;
// that evidence still comes from msPTI/device.jsonl.
package stacks

import "time"

const (
	// SchemaV1 is the on-disk schema for stacks.meta.json.
	SchemaV1 = "cann.stacks.v1"

	// FoldedFileName is the collapsed-stack file consumed by the report.
	FoldedFileName = "stacks.folded"
	// MetaFileName carries sampling parameters, loss counters and symbol
	// coverage next to the folded file.
	MetaFileName = "stacks.meta.json"
	// RawFileName is only written when --raw is set. It preserves the raw
	// addresses so a later run can re-symbolize without re-sampling.
	RawFileName = "stacks.jsonl"

	// CollectorPerfEvent identifies the perf_event_open + BPF stack
	// collector. It is written into meta so a report can never mistake a
	// different collector's output for this one.
	CollectorPerfEvent = "perf_event_open+bpf_get_stackid"
)

// Options controls one sampling run. PIDs are host PIDs (inside a container
// namespace the caller must translate first); an empty PID list is rejected by
// the CLI rather than silently sampling the whole host.
type Options struct {
	PIDs         []int
	FrequencyHz  int
	Duration     time.Duration
	Raw          bool
	KernelFilter string // all|npu
}

// Sample is one aggregated stack observed by the BPF program.
//
// Addrs are exactly what bpf_get_stackid returned: innermost frame first.
// Symbolization reverses them into root-first order before writing folded
// output, because the folded format consumed by flame-graph renderers expects
// the root on the left.
type Sample struct {
	PID   int      `json:"pid"`
	TID   int      `json:"tid"`
	Count uint64   `json:"count"`
	Addrs []uint64 `json:"addrs"`
}

// Stats is the BPF-side accounting for one run. Filtered counts perf events
// for non-target processes (the sampler uses inherit=1); StackFail counts
// bpf_get_stackid failures; Lost is reserved for map/ring drain loss and must
// stay zero for the v1 hash-map collector.
type Stats struct {
	Samples   uint64 `json:"samples"`
	Filtered  uint64 `json:"filtered"`
	StackFail uint64 `json:"stack_fail"`
	Lost      uint64 `json:"lost"`
}

// Meta is persisted as stacks.meta.json. Counters are written even when zero
// so a reader can distinguish "not measured" from "measured zero" for every
// field that has a defined denominator.
type Meta struct {
	Schema         string   `json:"schema"`
	ToolVersion    string   `json:"tool_version,omitempty"`
	Collector      string   `json:"collector"`
	Host           string   `json:"host"`
	Kernel         string   `json:"kernel"`
	Arch           string   `json:"arch,omitempty"`
	StartedAt      string   `json:"started_at"`
	EndedAt        string   `json:"ended_at"`
	DurationS      float64  `json:"duration_s"`
	FrequencyHz    int      `json:"frequency_hz"`
	TargetPIDs     []int    `json:"target_pids"`
	KernelFilter   string   `json:"kernel_filter"`
	Samples        uint64   `json:"samples"`
	UniqueStacks   int      `json:"unique_stacks"`
	StackFail      uint64   `json:"stack_fail"`
	Filtered       uint64   `json:"filtered"`
	Lost           uint64   `json:"lost"`
	SymbolCoverage float64  `json:"symbol_coverage"`
	UnknownPct     float64  `json:"unknown_pct"`
	KallsymsSHA256 string   `json:"kallsyms_sha256,omitempty"`
	Warnings       []string `json:"warnings,omitempty"`
}

// CollectResult is returned by Collect. Meta is partially filled by the
// collector (host, kernel, timestamps, rate, PIDs, counters); symbol coverage
// is filled after symbolization.
type CollectResult struct {
	Samples []Sample
	Stats   Stats
	Meta    Meta
}

// Frame is one resolved stack frame. Frames are stored root-first.
type Frame struct {
	Addr     uint64 `json:"addr,omitempty"`
	Name     string `json:"name"`
	Module   string `json:"module,omitempty"`
	Offset   uint64 `json:"offset,omitempty"`
	Category string `json:"category,omitempty"`
	Unknown  bool   `json:"unknown,omitempty"`
}

// ResolvedStack is one unique collapsed stack and its sample count.
type ResolvedStack struct {
	Frames []Frame `json:"frames"`
	Count  uint64  `json:"count"`
}

// Profile is the symbolized view of a sampling run.
type Profile struct {
	Stacks []ResolvedStack `json:"stacks"`
	Total  uint64          `json:"total"`
}

// Run is the read-only evidence bundle consumed by `report --stacks`.
// Profile is nil when stacks.folded is absent; callers must render the
// flamegraph view as unavailable in that case instead of drawing an empty
// chart.
type Run struct {
	Dir     string
	Meta    Meta
	Profile *Profile
}

// SymbolizeOptions configures address resolution. Empty paths fall back to
// /proc/kallsyms and /proc/modules.
type SymbolizeOptions struct {
	KallsymsPath string
	ModulesPath  string
	RulesPath    string
}

// SymbolRules is the on-disk profile format for kernel symbol categories.
// Category order is significant: the first matching prefix wins.
type SymbolRules struct {
	Categories []CategoryRule `yaml:"categories" json:"categories"`
}

// CategoryRule maps symbol prefixes to one report category.
type CategoryRule struct {
	Name     string   `yaml:"name" json:"name"`
	Prefixes []string `yaml:"prefixes" json:"prefixes"`
}
