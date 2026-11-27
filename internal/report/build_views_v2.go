package report

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"cann-ebpf/internal/device"
	"cann-ebpf/internal/diagnose"
	"cann-ebpf/internal/runlog"
)

// viewsV2BuildInput is the read-only integration boundary between report data
// loading and the ten view builders. Optional evidence files are resolved by
// the caller; builders never scan the filesystem on their own.
type viewsV2BuildInput struct {
	spans         []runlog.Span
	devices       []device.Span
	metrics       *MetricsData
	windowStartNS int64
	runDir        string
	opts          Options
}

type viewsV2Paths struct {
	sweep  string
	ops    string
	events string
	offCPU string
	otel   string
}

func buildViewsV2ForData(data *Data, in *diagnose.Input, metrics *MetricsData, opts Options) ReportViewsV2 {
	input := viewsV2BuildInput{metrics: metrics, opts: opts}
	if data != nil {
		input.windowStartNS, _ = strconv.ParseInt(data.Window.StartNS, 10, 64)
	}
	if in != nil {
		input.spans = in.Spans
		input.devices = in.DeviceSpans
		input.runDir = evidenceRunDir(in.Path)
	}
	if input.runDir == "" && metrics != nil {
		input.runDir = evidenceRunDir(metrics.Path)
	}
	return buildReportViewsV2(input)
}

func evidenceRunDir(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	info, err := os.Stat(path)
	if err == nil {
		if info.IsDir() {
			return filepath.Clean(path)
		}
		return filepath.Clean(filepath.Dir(path))
	}
	return filepath.Clean(filepath.Dir(path))
}

// buildReportViewsV2 integrates every frozen view builder. A builder receives
// only evidence that was explicitly supplied or discovered beside the run;
// missing evidence remains an unavailable view rather than a measured zero.
func buildReportViewsV2(input viewsV2BuildInput) ReportViewsV2 {
	paths := resolveViewsV2Paths(input.opts, input.runDir)
	views := ReportViewsV2{}

	views.LoadLatency = buildLoadLatencyView(paths.sweep)

	offCPUOptions := OffCPUOptions{
		WindowStartNS: input.windowStartNS,
		MaxRows:       input.opts.MaxSpanPoints,
		IntervalPath:  paths.offCPU,
	}
	views.OffCPU = BuildOffCPU(input.spans, offCPUOptions)
	views.OperatorTreemap = BuildOperatorTreemap(input.spans, input.devices)
	views.ServiceTopology = buildTopologyView(paths.otel, input.spans, input.opts.TopologyRootService)
	return views
}

func resolveViewsV2Paths(opts Options, runDir string) viewsV2Paths {
	runDir = strings.TrimSpace(runDir)
	return viewsV2Paths{
		sweep:  resolveOptionalEvidencePath(opts.SweepPath, runDir, "sweep.json"),
		ops:    resolveOptionalEvidencePath(opts.OpsPath, runDir, "ops.jsonl"),
		events: resolveOptionalEvidencePath(opts.EventsPath, runDir, "events.jsonl"),
		offCPU: resolveOptionalEvidencePath(opts.OffCPUPath, runDir, "offcpu.jsonl"),
		otel:   resolveOptionalEvidencePath(opts.OTelPath, runDir, "otel.jsonl"),
	}
}

func resolveOptionalEvidencePath(explicit, runDir, name string) string {
	if path := strings.TrimSpace(explicit); path != "" {
		return filepath.Clean(path)
	}
	if strings.TrimSpace(runDir) == "" {
		return ""
	}
	candidate := filepath.Join(runDir, name)
	if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
		return candidate
	}
	return ""
}

func buildLoadLatencyView(path string) LoadLatencyData {
	if strings.TrimSpace(path) == "" {
		return LoadLatencyData{
			Status: viewsV2Unavailable("sweep.json not provided; at least four concurrency tiers are required", "none"),
			Points: []LoadLatencyPoint{},
		}
	}
	data, err := BuildLoadLatencyWithOptions(path, LoadSweepRunSeries, SweepOptions{})
	if err != nil {
		return LoadLatencyData{
			Status: viewsV2Unavailable(fmt.Sprintf("build load-latency view: %v", err), "sweep.json+metrics"),
			Points: []LoadLatencyPoint{},
		}
	}
	return data
}

func loadChangeEvents(path string, windowStartNS int64) ([]ChangeEvent, error) {
	if strings.TrimSpace(path) == "" {
		return []ChangeEvent{}, nil
	}
	events := make([]ChangeEvent, 0)
	err := scanJSONLFile(path, func(line int, raw []byte) error {
		var record struct {
			TimeMS float64 `json:"time_ms"`
			TSNS   int64   `json:"ts_ns"`
			Label  string  `json:"label"`
			Source string  `json:"source"`
		}
		if err := json.Unmarshal(raw, &record); err != nil {
			return fmt.Errorf("line %d: %w", line, err)
		}
		label := strings.TrimSpace(record.Label)
		if label == "" {
			return fmt.Errorf("line %d: label is required", line)
		}
		timeMS := record.TimeMS
		if timeMS == 0 && record.TSNS > 0 {
			anchor := windowStartNS
			if anchor <= 0 {
				anchor = 0
			}
			timeMS = float64(record.TSNS-anchor) / 1e6
		}
		events = append(events, ChangeEvent{
			TimeMS: timeMS,
			Label:  label,
			Source: strings.TrimSpace(record.Source),
		})
		return nil
	})
	if err != nil {
		return []ChangeEvent{}, err
	}
	return events, nil
}

func buildTopologyView(otelPath string, spans []runlog.Span, rootService string) ServiceTopologyData {
	if strings.TrimSpace(otelPath) != "" {
		topologySpans, err := loadTopologySpans(otelPath)
		if err != nil {
			return ServiceTopologyData{
				Status: viewsV2Unavailable(fmt.Sprintf("read otel.jsonl: %v", err), "otel.jsonl"),
				Nodes:  []TopologyNode{},
				Edges:  []TopologyEdge{},
			}
		}
		return BuildServiceTopology(topologySpans, TopologyOptions{
			RootService: rootService,
			Source:      "otel.jsonl",
		})
	}
	local := topologyFromHostSpans(spans)
	if len(local) == 0 {
		return ServiceTopologyData{
			Status: viewsV2Unavailable("no otel.jsonl or host spans", "none"),
			Nodes:  []TopologyNode{},
			Edges:  []TopologyEdge{},
		}
	}
	return BuildServiceTopology(local, TopologyOptions{
		RootService: rootService,
		Source:      "spans.jsonl",
	})
}

func loadTopologySpans(path string) ([]TopologySpan, error) {
	spans := make([]TopologySpan, 0)
	err := scanJSONLFile(path, func(line int, raw []byte) error {
		var span TopologySpan
		if err := json.Unmarshal(raw, &span); err != nil {
			return fmt.Errorf("line %d: %w", line, err)
		}
		spans = append(spans, span)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return spans, nil
}

func topologyFromHostSpans(spans []runlog.Span) []TopologySpan {
	if len(spans) == 0 {
		return []TopologySpan{}
	}
	out := make([]TopologySpan, 0, len(spans))
	for _, span := range spans {
		service := fmt.Sprintf("pid-%d", span.PID)
		item := TopologySpan{
			Service: service,
			Name:    span.Function,
			Kind:    span.Category,
			StartNS: spanStart(span),
			EndNS:   spanEnd(span),
		}
		if span.Trace != nil {
			item.TraceID = span.Trace.TraceID
			item.SpanID = span.Trace.SpanID
			item.ParentSpanID = span.Trace.ParentSpanID
		}
		out = append(out, item)
	}
	return out
}

func joinViewReason(reason, suffix string) string {
	reason = strings.TrimSpace(reason)
	suffix = strings.TrimSpace(suffix)
	switch {
	case reason == "":
		return suffix
	case suffix == "":
		return reason
	default:
		return reason + "; " + suffix
	}
}

func viewsV2Unavailable(reason, source string) ViewStatus {
	if strings.TrimSpace(source) == "" {
		source = "none"
	}
	return ViewStatus{Available: false, Reason: reason, Source: source}
}

func scanJSONLFile(path string, visit func(line int, raw []byte) error) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	line := 0
	for scanner.Scan() {
		line++
		raw := bytes.TrimSpace(scanner.Bytes())
		if len(raw) == 0 {
			continue
		}
		if err := visit(line, raw); err != nil {
			return err
		}
	}
	return scanner.Err()
}
