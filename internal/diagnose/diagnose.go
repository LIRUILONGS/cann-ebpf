// Package diagnose turns an existing run's raw evidence (spans.jsonl plus
// manifest.json) into a read-only low-level localization report.
//
// It never attaches probes and never rewrites the evidence. Everything it
// prints is derived from the observed spans, and every claim it cannot support
// with evidence is surfaced explicitly as "unknown" rather than guessed.
package diagnose

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"cann-ebpf/internal/runlog"
)

const ReportSchemaV1 = "cann.diagnose.report.v1"

const (
	StatusOK    = "ok"
	StatusEmpty = "empty"
)

// Options are the read-only filters and thresholds for one diagnosis.
type Options struct {
	StartNS              int64
	EndNS                int64
	Last                 time.Duration
	PIDs                 []int
	TIDs                 []int
	BatchID              string
	RequestID            string
	TraceID              string
	TopN                 int
	SlowThresholdNS      int64
	OffCPURatioThreshold float64
	FutexRatioThreshold  float64
}

func (o *Options) normalize() {
	if o.TopN <= 0 {
		o.TopN = 10
	}
	if o.SlowThresholdNS <= 0 {
		o.SlowThresholdNS = int64(time.Millisecond)
	}
	if o.OffCPURatioThreshold <= 0 {
		o.OffCPURatioThreshold = 0.5
	}
	if o.FutexRatioThreshold <= 0 {
		o.FutexRatioThreshold = 0.3
	}
}

// Input is one loaded evidence run.
type Input struct {
	Path         string
	JSONLPath    string
	ManifestPath string
	Manifest     *runlog.Manifest
	Spans        []runlog.Span
	Warnings     []string
}

// Load reads spans.jsonl (and manifest.json when present) from a run directory
// or a direct spans.jsonl path. A missing manifest is a warning, not an error.
func Load(path string) (*Input, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("input path is empty")
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", path, err)
	}
	in := &Input{Path: path}
	if info.IsDir() {
		in.JSONLPath = filepath.Join(path, "spans.jsonl")
		in.ManifestPath = filepath.Join(path, "manifest.json")
	} else {
		in.JSONLPath = path
		in.ManifestPath = filepath.Join(filepath.Dir(path), "manifest.json")
	}
	file, err := os.Open(in.JSONLPath)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", in.JSONLPath, err)
	}
	defer file.Close()
	spans, err := runlog.ReadSpans(file)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", in.JSONLPath, err)
	}
	in.Spans = spans
	if data, mErr := os.ReadFile(in.ManifestPath); mErr == nil {
		var manifest runlog.Manifest
		if jErr := json.Unmarshal(data, &manifest); jErr != nil {
			in.Warnings = append(in.Warnings, fmt.Sprintf("manifest.json 无法解析：%v", jErr))
		} else {
			in.Manifest = &manifest
		}
	} else if !os.IsNotExist(mErr) {
		in.Warnings = append(in.Warnings, fmt.Sprintf("manifest.json 读取失败：%v", mErr))
	} else {
		in.Warnings = append(in.Warnings, "缺少 manifest.json：运行环境与完整性无法核对")
	}
	return in, nil
}

type Filters struct {
	PIDs      []int  `json:"pids,omitempty"`
	TIDs      []int  `json:"tids,omitempty"`
	BatchID   string `json:"batch_id,omitempty"`
	RequestID string `json:"request_id,omitempty"`
	TraceID   string `json:"trace_id,omitempty"`
}

type Window struct {
	StartNS    int64  `json:"start_ns"`
	EndNS      int64  `json:"end_ns"`
	DurationNS int64  `json:"duration_ns"`
	Source     string `json:"source"`
}

type RunInfo struct {
	RunID         string   `json:"run_id,omitempty"`
	Profile       string   `json:"profile,omitempty"`
	ToolVersion   string   `json:"tool_version,omitempty"`
	Host          string   `json:"host,omitempty"`
	Kernel        string   `json:"kernel,omitempty"`
	Status        string   `json:"status,omitempty"`
	TargetPIDs    []int    `json:"target_pids,omitempty"`
	EventCount    uint64   `json:"event_count"`
	DroppedEvents uint64   `json:"dropped_events"`
	StartedAt     string   `json:"started_at,omitempty"`
	EndedAt       string   `json:"ended_at,omitempty"`
	Warnings      []string `json:"warnings,omitempty"`
}

type PIDSummary struct {
	PID         int      `json:"pid"`
	Spans       int64    `json:"spans"`
	BatchSpans  int64    `json:"batch_spans"`
	Threads     int      `json:"threads"`
	TotalNS     int64    `json:"total_ns"`
	MaxNS       int64    `json:"max_ns"`
	ThreadNames []string `json:"thread_names,omitempty"`
}

type TIDSummary struct {
	TID     int   `json:"tid"`
	PID     int   `json:"pid"`
	Spans   int64 `json:"spans"`
	TotalNS int64 `json:"total_ns"`
	MaxNS   int64 `json:"max_ns"`
}

type FunctionSummary struct {
	Function        string `json:"function"`
	Category        string `json:"category"`
	Count           int64  `json:"count"`
	TotalNS         int64  `json:"total_ns"`
	P50NS           int64  `json:"p50_ns"`
	P95NS           int64  `json:"p95_ns"`
	MaxNS           int64  `json:"max_ns"`
	OffCPUNS        int64  `json:"offcpu_ns"`
	FutexNS         int64  `json:"futex_ns"`
	BlockingSamples int64  `json:"blocking_samples"`
}

type CategorySummary struct {
	Category string  `json:"category"`
	Count    int64   `json:"count"`
	TotalNS  int64   `json:"total_ns"`
	SpanPct  float64 `json:"span_pct"`
}

type BatchSummary struct {
	BatchID        string   `json:"batch_id"`
	Spans          int64    `json:"spans"`
	TotalNS        int64    `json:"total_ns"`
	MaxNS          int64    `json:"max_ns"`
	RequestIDs     []string `json:"request_ids,omitempty"`
	PIDs           []int    `json:"pids,omitempty"`
	ExampleSpanIDs []string `json:"example_span_ids,omitempty"`
}

// BlockingSummary aggregates host-side off-CPU and futex samples for the
// population where blocking matters (sync spans when present, else all spans).
type BlockingSummary struct {
	Measured      bool     `json:"measured"`
	Population    string   `json:"population"`
	Spans         int64    `json:"spans"`
	SampledSpans  int64    `json:"sampled_spans"`
	WallNS        int64    `json:"wall_ns"`
	OffCPUNS      int64    `json:"offcpu_ns"`
	FutexNS       int64    `json:"futex_ns"`
	OffCPURatio   float64  `json:"offcpu_ratio"`
	FutexRatio    float64  `json:"futex_ratio"`
	NestedWarning string   `json:"nested_warning,omitempty"`
	Evidence      []string `json:"evidence,omitempty"`
}

type Verdict struct {
	Verdict    string   `json:"verdict"`
	Reason     string   `json:"reason"`
	Confidence float64  `json:"confidence"`
	Evidence   []string `json:"evidence,omitempty"`
}

type EvidenceSpan struct {
	SpanID     string   `json:"span_id"`
	Function   string   `json:"function"`
	Category   string   `json:"category"`
	PID        int      `json:"pid"`
	TID        int      `json:"tid"`
	StartNS    int64    `json:"start_ns"`
	EndNS      int64    `json:"end_ns"`
	DurationNS int64    `json:"duration_ns"`
	OffCPUNS   *int64   `json:"offcpu_ns,omitempty"`
	FutexNS    *int64   `json:"futex_ns,omitempty"`
	BatchID    string   `json:"batch_id,omitempty"`
	RequestIDs []string `json:"request_ids,omitempty"`
	TraceID    string   `json:"trace_id,omitempty"`
}

type Report struct {
	Schema       string            `json:"schema"`
	Status       string            `json:"status"`
	GeneratedAt  string            `json:"generated_at"`
	InputPath    string            `json:"input_path"`
	JSONLPath    string            `json:"jsonl_path"`
	ManifestPath string            `json:"manifest_path"`
	Run          RunInfo           `json:"run"`
	Window       Window            `json:"window"`
	Filters      Filters           `json:"filters"`
	SpanCount    int               `json:"span_count"`
	MatchedSpans int               `json:"matched_spans"`
	PIDs         []PIDSummary      `json:"pids"`
	TIDs         []TIDSummary      `json:"tids"`
	Batches      []BatchSummary    `json:"batches"`
	RequestIDs   []string          `json:"request_ids"`
	Functions    []FunctionSummary `json:"functions"`
	Categories   []CategorySummary `json:"categories"`
	Blocking     BlockingSummary   `json:"blocking"`
	Verdicts     []Verdict         `json:"verdicts"`
	Evidence     []EvidenceSpan    `json:"evidence"`
	Unknowns     []string          `json:"unknowns"`
	Warnings     []string          `json:"warnings"`
}

// Build produces a diagnosis. It is read-only and deterministic for the same
// input and options.
func Build(in *Input, opts Options) (Report, error) {
	opts.normalize()
	report := Report{
		Schema:       ReportSchemaV1,
		GeneratedAt:  time.Now().UTC().Format(time.RFC3339),
		InputPath:    in.Path,
		JSONLPath:    in.JSONLPath,
		ManifestPath: in.ManifestPath,
		SpanCount:    len(in.Spans),
		Filters: Filters{
			PIDs: opts.PIDs, TIDs: opts.TIDs, BatchID: opts.BatchID,
			RequestID: opts.RequestID, TraceID: opts.TraceID,
		},
		PIDs:       []PIDSummary{},
		TIDs:       []TIDSummary{},
		Batches:    []BatchSummary{},
		RequestIDs: []string{},
		Functions:  []FunctionSummary{},
		Categories: []CategorySummary{},
		Verdicts:   []Verdict{},
		Evidence:   []EvidenceSpan{},
		Unknowns:   []string{},
		Warnings:   append([]string{}, in.Warnings...),
	}
	report.Run = runInfo(in.Manifest)

	window, err := computeWindow(in.Spans, opts)
	if err != nil {
		return Report{}, err
	}
	report.Window = window

	matched := make([]runlog.Span, 0, len(in.Spans))
	for _, span := range in.Spans {
		if spanMatches(span, opts, window) {
			matched = append(matched, span)
		}
	}
	report.MatchedSpans = len(matched)
	if len(matched) == 0 {
		report.Status = StatusEmpty
		report.Unknowns = append(report.Unknowns,
			"没有 span 匹配当前时间窗和过滤条件：不生成任何统计，也不补造数据。")
		report.Unknowns = append(report.Unknowns, deviceUnknowns()...)
		return report, nil
	}
	report.Status = StatusOK

	aggregate(&report, matched, opts)
	report.Blocking = buildBlocking(matched, opts)
	report.Verdicts = buildVerdicts(report.Blocking, opts)
	report.Unknowns = buildUnknowns(report.Blocking, in.Manifest)
	report.Functions = topFunctions(report.Functions, opts.TopN)
	report.Evidence = buildEvidence(matched, opts)
	return report, nil
}

func runInfo(manifest *runlog.Manifest) RunInfo {
	if manifest == nil {
		return RunInfo{}
	}
	info := RunInfo{
		RunID: manifest.RunID, Profile: manifest.Profile, ToolVersion: manifest.ToolVersion,
		Host: manifest.Host, Kernel: manifest.Kernel, Status: manifest.Status,
		TargetPIDs: manifest.TargetPIDs, EventCount: manifest.EventCount,
		DroppedEvents: manifest.DroppedEvents, Warnings: manifest.Warnings,
	}
	if !manifest.StartedAt.IsZero() {
		info.StartedAt = manifest.StartedAt.Format(time.RFC3339Nano)
	}
	if manifest.EndedAt != nil {
		info.EndedAt = manifest.EndedAt.Format(time.RFC3339Nano)
	}
	return info
}

func spanStart(s runlog.Span) int64 {
	if s.StartNS > 0 {
		return s.StartNS
	}
	return s.TsNS
}

func spanEnd(s runlog.Span) int64 {
	if s.EndNS > 0 {
		return s.EndNS
	}
	return spanStart(s) + s.DurationNS
}

// computeWindow resolves the analysis window. Timestamps are the monotonic
// ktime values recorded in spans.jsonl, not wall clock, so windows are given in
// the same domain and "last" is measured backwards from the newest span.
func computeWindow(spans []runlog.Span, opts Options) (Window, error) {
	if opts.StartNS > 0 && opts.EndNS > 0 && opts.EndNS < opts.StartNS {
		return Window{}, fmt.Errorf("end_ns %d precedes start_ns %d", opts.EndNS, opts.StartNS)
	}
	var minStart, maxEnd int64
	for _, span := range spans {
		start, end := spanStart(span), spanEnd(span)
		if start > 0 && (minStart == 0 || start < minStart) {
			minStart = start
		}
		if end > maxEnd {
			maxEnd = end
		}
	}
	if len(spans) == 0 {
		return Window{Source: "empty"}, nil
	}
	window := Window{StartNS: minStart, EndNS: maxEnd, Source: "run"}
	if opts.StartNS > 0 || opts.EndNS > 0 {
		window.Source = "explicit"
		if opts.StartNS > 0 {
			window.StartNS = opts.StartNS
		}
		if opts.EndNS > 0 {
			window.EndNS = opts.EndNS
		}
	} else if opts.Last > 0 {
		window.Source = "last"
		window.EndNS = maxEnd
		window.StartNS = maxEnd - int64(opts.Last)
		if window.StartNS < 0 {
			window.StartNS = 0
		}
	}
	window.DurationNS = window.EndNS - window.StartNS
	return window, nil
}

func spanMatches(span runlog.Span, opts Options, window Window) bool {
	if len(opts.PIDs) > 0 && !intIn(span.PID, opts.PIDs) {
		return false
	}
	if len(opts.TIDs) > 0 && !intIn(span.TID, opts.TIDs) {
		return false
	}
	if opts.BatchID != "" && span.BatchID != opts.BatchID {
		return false
	}
	if opts.RequestID != "" && !stringIn(span.RequestIDs, opts.RequestID) {
		return false
	}
	if opts.TraceID != "" {
		if span.Trace == nil || span.Trace.TraceID != opts.TraceID {
			return false
		}
	}
	if window.Source == "empty" {
		return false
	}
	start, end := spanStart(span), spanEnd(span)
	if end <= window.StartNS || start >= window.EndNS {
		return false
	}
	return true
}

type functionAgg struct {
	category string
	durs     []int64
	total    int64
	offcpu   int64
	futex    int64
	samples  int64
}

func aggregate(report *Report, spans []runlog.Span, opts Options) {
	pidGroups := map[int]*PIDSummary{}
	tidGroups := map[int]*TIDSummary{}
	funcGroups := map[string]*functionAgg{}
	catGroups := map[string]*CategorySummary{}
	batchGroups := map[string]*BatchSummary{}
	requestSet := map[string]struct{}{}
	threadNames := map[int]map[string]struct{}{}

	for _, span := range spans {
		pid := pidGroups[span.PID]
		if pid == nil {
			pid = &PIDSummary{PID: span.PID}
			pidGroups[span.PID] = pid
			threadNames[span.PID] = map[string]struct{}{}
		}
		pid.Spans++
		pid.TotalNS += span.DurationNS
		if span.DurationNS > pid.MaxNS {
			pid.MaxNS = span.DurationNS
		}
		if span.Category == "batch" {
			pid.BatchSpans++
		}
		if span.ThreadName != "" {
			threadNames[span.PID][span.ThreadName] = struct{}{}
		}

		tid := tidGroups[span.TID]
		if tid == nil {
			tid = &TIDSummary{TID: span.TID, PID: span.PID}
			tidGroups[span.TID] = tid
		}
		tid.Spans++
		tid.TotalNS += span.DurationNS
		if span.DurationNS > tid.MaxNS {
			tid.MaxNS = span.DurationNS
		}

		fa := funcGroups[span.Function]
		if fa == nil {
			fa = &functionAgg{category: span.Category}
			funcGroups[span.Function] = fa
		}
		fa.durs = append(fa.durs, span.DurationNS)
		fa.total += span.DurationNS
		if span.OffCPUNS != nil || span.FutexNS != nil {
			fa.samples++
		}
		if span.OffCPUNS != nil {
			fa.offcpu += *span.OffCPUNS
		}
		if span.FutexNS != nil {
			fa.futex += *span.FutexNS
		}

		ca := catGroups[span.Category]
		if ca == nil {
			ca = &CategorySummary{Category: span.Category}
			catGroups[span.Category] = ca
		}
		ca.Count++
		ca.TotalNS += span.DurationNS

		if span.BatchID != "" {
			batch := batchGroups[span.BatchID]
			if batch == nil {
				batch = &BatchSummary{BatchID: span.BatchID}
				batchGroups[span.BatchID] = batch
			}
			batch.Spans++
			batch.TotalNS += span.DurationNS
			if span.DurationNS > batch.MaxNS {
				batch.MaxNS = span.DurationNS
			}
			for _, requestID := range span.RequestIDs {
				if requestID != "" {
					batch.RequestIDs = appendUnique(batch.RequestIDs, requestID)
					requestSet[requestID] = struct{}{}
				}
			}
			batch.PIDs = appendUniqueInt(batch.PIDs, span.PID)
			if span.SpanID != "" && len(batch.ExampleSpanIDs) < 5 {
				batch.ExampleSpanIDs = append(batch.ExampleSpanIDs, span.SpanID)
			}
		}
		for _, requestID := range span.RequestIDs {
			if requestID != "" {
				requestSet[requestID] = struct{}{}
			}
		}
	}

	for pid, summary := range pidGroups {
		summary.Threads = len(threadNames[pid])
		for name := range threadNames[pid] {
			summary.ThreadNames = append(summary.ThreadNames, name)
		}
		sort.Strings(summary.ThreadNames)
		report.PIDs = append(report.PIDs, *summary)
	}
	sort.Slice(report.PIDs, func(i, j int) bool {
		if report.PIDs[i].TotalNS == report.PIDs[j].TotalNS {
			return report.PIDs[i].PID < report.PIDs[j].PID
		}
		return report.PIDs[i].TotalNS > report.PIDs[j].TotalNS
	})
	for _, summary := range tidGroups {
		report.TIDs = append(report.TIDs, *summary)
	}
	sort.Slice(report.TIDs, func(i, j int) bool {
		if report.TIDs[i].TotalNS == report.TIDs[j].TotalNS {
			return report.TIDs[i].TID < report.TIDs[j].TID
		}
		return report.TIDs[i].TotalNS > report.TIDs[j].TotalNS
	})

	for function, fa := range funcGroups {
		summary := FunctionSummary{
			Function: function, Category: fa.category, Count: int64(len(fa.durs)),
			TotalNS: fa.total, MaxNS: maxInt64(fa.durs),
			OffCPUNS: fa.offcpu, FutexNS: fa.futex, BlockingSamples: fa.samples,
		}
		summary.P50NS, _ = percentile(fa.durs, 0.50)
		summary.P95NS, _ = percentile(fa.durs, 0.95)
		report.Functions = append(report.Functions, summary)
	}
	for _, ca := range catGroups {
		ca.SpanPct = float64(ca.Count) * 100 / float64(len(spans))
		report.Categories = append(report.Categories, *ca)
	}
	for _, batch := range batchGroups {
		sort.Strings(batch.RequestIDs)
		sort.Ints(batch.PIDs)
		report.Batches = append(report.Batches, *batch)
	}
	for requestID := range requestSet {
		report.RequestIDs = append(report.RequestIDs, requestID)
	}
	sort.Strings(report.RequestIDs)
	sort.Slice(report.Batches, func(i, j int) bool {
		if report.Batches[i].TotalNS == report.Batches[j].TotalNS {
			return report.Batches[i].BatchID < report.Batches[j].BatchID
		}
		return report.Batches[i].TotalNS > report.Batches[j].TotalNS
	})
	if len(report.Batches) > opts.TopN {
		report.Batches = report.Batches[:opts.TopN]
	}
	sort.Slice(report.Categories, func(i, j int) bool {
		if report.Categories[i].TotalNS == report.Categories[j].TotalNS {
			return report.Categories[i].Category < report.Categories[j].Category
		}
		return report.Categories[i].TotalNS > report.Categories[j].TotalNS
	})
}

// topFunctions sorts by total time and truncates to n (n<=0 keeps all).
func topFunctions(functions []FunctionSummary, n int) []FunctionSummary {
	sort.Slice(functions, func(i, j int) bool {
		if functions[i].TotalNS == functions[j].TotalNS {
			return functions[i].Function < functions[j].Function
		}
		return functions[i].TotalNS > functions[j].TotalNS
	})
	if n > 0 && len(functions) > n {
		return functions[:n]
	}
	return functions
}

func buildBlocking(spans []runlog.Span, opts Options) BlockingSummary {
	population := "all"
	hasSync := false
	for _, span := range spans {
		if span.Category == "sync" {
			hasSync = true
			break
		}
	}
	if hasSync {
		population = "sync"
	}
	summary := BlockingSummary{Population: population}
	var evidence []struct {
		spanID string
		total  int64
	}
	for _, span := range spans {
		if hasSync && span.Category != "sync" {
			continue
		}
		summary.Spans++
		if span.OffCPUNS == nil && span.FutexNS == nil {
			continue
		}
		summary.SampledSpans++
		summary.Measured = true
		summary.WallNS += span.DurationNS
		var total int64
		if span.OffCPUNS != nil {
			summary.OffCPUNS += *span.OffCPUNS
			total += *span.OffCPUNS
		}
		if span.FutexNS != nil {
			summary.FutexNS += *span.FutexNS
			total += *span.FutexNS
		}
		if span.SpanID != "" {
			evidence = append(evidence, struct {
				spanID string
				total  int64
			}{span.SpanID, total})
		}
	}
	if summary.WallNS > 0 {
		summary.OffCPURatio = float64(summary.OffCPUNS) / float64(summary.WallNS)
		summary.FutexRatio = float64(summary.FutexNS) / float64(summary.WallNS)
	}
	if summary.Measured {
		summary.NestedWarning = "off-CPU/futex 按调用线程累计；嵌套 CANN span 的时间会重叠，不能跨嵌套函数直接相加。"
	}
	sort.Slice(evidence, func(i, j int) bool {
		if evidence[i].total == evidence[j].total {
			return evidence[i].spanID < evidence[j].spanID
		}
		return evidence[i].total > evidence[j].total
	})
	for i := 0; i < len(evidence) && i < 3; i++ {
		summary.Evidence = append(summary.Evidence, evidence[i].spanID)
	}
	return summary
}

func buildVerdicts(blocking BlockingSummary, opts Options) []Verdict {
	if !blocking.Measured {
		return []Verdict{{
			Verdict: "blocking_unknown",
			Reason:  "匹配到的 span 没有 offcpu_ns/futex_ns 采样：M2 blocking 探针未开启，或本窗口未触发。",
		}}
	}
	ratio := blocking.OffCPURatio
	futexRatio := blocking.FutexRatio
	switch {
	case ratio >= opts.OffCPURatioThreshold && futexRatio >= opts.FutexRatioThreshold:
		return []Verdict{{
			Verdict:    "host_lock_contention",
			Reason:     fmt.Sprintf("off-CPU 占比 %.1f%% ≥ %.0f%%，futex 占比 %.1f%% ≥ %.0f%%，阻塞主要来自 host 侧锁/futex 等待。", ratio*100, opts.OffCPURatioThreshold*100, futexRatio*100, opts.FutexRatioThreshold*100),
			Confidence: clamp01(0.5 + ratio*0.3 + futexRatio*0.2),
			Evidence:   blocking.Evidence,
		}}
	case ratio >= opts.OffCPURatioThreshold:
		return []Verdict{{
			Verdict:    "host_blocked_non_futex",
			Reason:     fmt.Sprintf("off-CPU 占比 %.1f%% ≥ %.0f%%，但 futex 占比仅 %.1f%%：host 被阻塞，但主要不是 futex 锁等待（可能是驱动等待或调度）。", ratio*100, opts.OffCPURatioThreshold*100, futexRatio*100),
			Confidence: clamp01(0.4 + ratio*0.3),
			Evidence:   blocking.Evidence,
		}}
	case ratio < 0.2:
		return []Verdict{{
			Verdict:    "host_cpu_running",
			Reason:     fmt.Sprintf("off-CPU 占比仅 %.1f%%：同步调用大部分时间在 CPU 上执行，host 没有被阻塞。是否 device_bound 仍需设备侧证据。", ratio*100),
			Confidence: 0.6,
			Evidence:   blocking.Evidence,
		}}
	default:
		return []Verdict{{
			Verdict:    "host_blocking_mixed",
			Reason:     fmt.Sprintf("off-CPU 占比 %.1f%%、futex 占比 %.1f%%，未达到明确阈值；阻塞与 CPU 执行混合。", ratio*100, futexRatio*100),
			Confidence: 0.3,
			Evidence:   blocking.Evidence,
		}}
	}
}

func buildUnknowns(blocking BlockingSummary, manifest *runlog.Manifest) []string {
	unknowns := deviceUnknowns()
	if !blocking.Measured {
		unknowns = append(unknowns,
			"host 阻塞原因无法判定：匹配窗口内没有 off-CPU/futex 采样（M2 未开启或未采到）。")
	} else if blocking.FutexNS == 0 && blocking.SampledSpans > 0 {
		unknowns = append(unknowns,
			"futex 等待采样为 0：若目标线程未发生 futex 等待则属正常；若 futex 探针未生效，host_lock_contention 判定不完整。")
	}
	if manifest == nil {
		unknowns = append(unknowns, "缺少 manifest.json：无法核对运行环境、目标 PID、库 inode 和事件完整性。")
	} else if manifest.DroppedEvents > 0 {
		unknowns = append(unknowns, fmt.Sprintf("manifest 记录 dropped_events=%d：统计可能不完整。", manifest.DroppedEvents))
	}
	return unknowns
}

func deviceUnknowns() []string {
	return []string{
		"无法回答设备侧执行时间（NPU kernel / memcpy / HCCL）：msPTI（M3）未接入。",
		"无法区分 device_bound / launch_starvation / copy_bound / communication_bound：缺少设备时间线证据。",
		"无法回答单 request 延迟：batch span 是 SchedulerOutput.num_scheduled_tokens 的观测窗口，不是逐 request 执行时长。",
		"无法回答 execute_model 的 Python 函数级耗时：vLLM 插件未激活，纯 eBPF 的 PyObject_GetAttr 不是函数边界。",
	}
}

func buildEvidence(spans []runlog.Span, opts Options) []EvidenceSpan {
	sorted := append([]runlog.Span(nil), spans...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].DurationNS == sorted[j].DurationNS {
			return sorted[i].SpanID < sorted[j].SpanID
		}
		return sorted[i].DurationNS > sorted[j].DurationNS
	})
	var evidence []EvidenceSpan
	for _, span := range sorted {
		if span.DurationNS < opts.SlowThresholdNS {
			continue
		}
		item := EvidenceSpan{
			SpanID: span.SpanID, Function: span.Function, Category: span.Category,
			PID: span.PID, TID: span.TID, StartNS: spanStart(span), EndNS: spanEnd(span),
			DurationNS: span.DurationNS, OffCPUNS: span.OffCPUNS, FutexNS: span.FutexNS,
			BatchID: span.BatchID, RequestIDs: span.RequestIDs,
		}
		if span.Trace != nil {
			item.TraceID = span.Trace.TraceID
		}
		evidence = append(evidence, item)
		if len(evidence) >= opts.TopN {
			break
		}
	}
	return evidence
}

func percentile(values []int64, p float64) (int64, bool) {
	if len(values) == 0 {
		return 0, false
	}
	sorted := append([]int64(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	rank := int(math.Ceil(p * float64(len(sorted))))
	if rank < 1 {
		rank = 1
	}
	if rank > len(sorted) {
		rank = len(sorted)
	}
	return sorted[rank-1], true
}

func maxInt64(values []int64) int64 {
	var max int64
	for i, v := range values {
		if i == 0 || v > max {
			max = v
		}
	}
	return max
}

func intIn(value int, values []int) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func stringIn(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func appendUniqueInt(values []int, value int) []int {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
