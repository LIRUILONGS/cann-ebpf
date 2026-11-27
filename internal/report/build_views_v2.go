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

	views.CriticalPath = BuildCriticalPath(input.spans, input.devices)
	views.SummedDelay = BuildSummedDelay(views.CriticalPath)
	views.SlackCDF = BuildSlackCDF(views.CriticalPath)

	offCPUOptions := OffCPUOptions{
		WindowStartNS: input.windowStartNS,
		IntervalPath:  paths.offCPU,
	}
	views.OffCPU = BuildOffCPU(input.spans, offCPUOptions)
	views.OperatorTreemap = BuildOperatorTreemap(input.spans, input.devices)

	rooflineOptions := RooflineOptions{
		PeakTFLOPS: input.opts.RooflinePeakTFLOPS,
		PeakGBPS:   input.opts.RooflinePeakGBPS,
		DType:      input.opts.RooflineDType,
		Source:     "ops.jsonl",
	}
	if paths.ops == "" {
		views.Roofline = BuildRoofline(nil, rooflineOptions)
		views.Roofline.Status.Reason = "ops.jsonl not provided; per-op FLOPs/bytes evidence is required"
	} else {
		ops, err := loadRooflineOps(paths.ops)
		if err != nil {
			views.Roofline = RooflineData{
				Status:     viewsV2Unavailable(fmt.Sprintf("read ops.jsonl: %v", err), "ops.jsonl"),
				PeakTFLOPS: rooflineOptions.PeakTFLOPS,
				PeakGBPS:   rooflineOptions.PeakGBPS,
				DType:      rooflineOptions.DType,
				Points:     []RooflinePoint{},
			}
		} else {
			views.Roofline = BuildRoofline(ops, rooflineOptions)
		}
	}

	views.ABDiff = buildABDiffView(input.opts)

	events, eventErr := loadChangeEvents(paths.events, input.windowStartNS)
	if input.metrics == nil {
		views.ChangePoint = BuildChangePoint(nil, events, ChangePointOptions{})
	} else {
		views.ChangePoint = BuildChangePoint(input.metrics.Series, events, ChangePointOptions{})
	}
	if eventErr != nil {
		views.ChangePoint.Status.Reason = joinViewReason(
			views.ChangePoint.Status.Reason,
			fmt.Sprintf("events.jsonl ignored: %v", eventErr),
		)
	}

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

func buildABDiffView(opts Options) ABDiffData {
	aPath := strings.TrimSpace(opts.ABAPath)
	bPath := strings.TrimSpace(opts.ABBRunPath)
	workloadID := strings.TrimSpace(opts.ABWorkloadID)
	if aPath == "" || bPath == "" {
		return ABDiffData{
			Status:      viewsV2Unavailable("both --ab-a and --ab-b runs are required", "none"),
			Metrics:     []ABMetricDelta{},
			Functions:   []ABFunctionDelta{},
			Significant: []string{},
		}
	}
	if workloadID == "" {
		return ABDiffData{
			Status:      viewsV2Unavailable("--ab-workload-id is required for a valid A/B comparison", "none"),
			Metrics:     []ABMetricDelta{},
			Functions:   []ABFunctionDelta{},
			Significant: []string{},
		}
	}
	a, err := loadABRunInput(aPath, workloadID)
	if err != nil {
		return ABDiffData{
			Status:      viewsV2Unavailable(fmt.Sprintf("load A/B run A: %v", err), "spans+metrics"),
			ARun:        filepath.Base(filepath.Clean(aPath)),
			BRun:        filepath.Base(filepath.Clean(bPath)),
			Metrics:     []ABMetricDelta{},
			Functions:   []ABFunctionDelta{},
			Significant: []string{},
		}
	}
	b, err := loadABRunInput(bPath, workloadID)
	if err != nil {
		return ABDiffData{
			Status:      viewsV2Unavailable(fmt.Sprintf("load A/B run B: %v", err), "spans+metrics"),
			ARun:        a.RunID,
			BRun:        filepath.Base(filepath.Clean(bPath)),
			Metrics:     []ABMetricDelta{},
			Functions:   []ABFunctionDelta{},
			Significant: []string{},
		}
	}
	return BuildABDiff(a, b)
}

func loadABRunInput(path, workloadID string) (ABRunInput, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return ABRunInput{}, fmt.Errorf("run path is empty")
	}
	info, err := os.Stat(path)
	if err != nil {
		return ABRunInput{}, err
	}
	runDir := path
	if !info.IsDir() {
		runDir = filepath.Dir(path)
	}

	input := ABRunInput{WorkloadID: workloadID}
	metricsPath := filepath.Join(runDir, "metrics.jsonl")
	if info, err := os.Stat(metricsPath); err == nil && !info.IsDir() {
		metrics, loadErr := LoadMetrics(metricsPath, MetricsOptions{MaxSeries: 2048})
		if loadErr != nil {
			return ABRunInput{}, fmt.Errorf("metrics: %w", loadErr)
		}
		if metrics.Available {
			input.Metrics = metrics.Series
		}
	}

	spansPath := filepath.Join(runDir, "spans.jsonl")
	if info, err := os.Stat(spansPath); err == nil && !info.IsDir() {
		loaded, loadErr := diagnoseInputForViews(runDir)
		if loadErr != nil {
			return ABRunInput{}, fmt.Errorf("spans: %w", loadErr)
		}
		opts := Options{}
		opts.normalize()
		input.Functions, _ = buildFunctions(loaded.Spans, opts)
		if loaded.Manifest != nil {
			input.RunID = loaded.Manifest.RunID
		}
		if input.RunID == "" && len(loaded.Spans) > 0 {
			input.RunID = loaded.Spans[0].RunID
		}
	}
	if input.RunID == "" {
		input.RunID = filepath.Base(filepath.Clean(path))
	}
	if len(input.Metrics) == 0 && len(input.Functions) == 0 {
		return ABRunInput{}, fmt.Errorf("no metrics.jsonl or spans.jsonl evidence in %s", runDir)
	}
	return input, nil
}

// diagnoseInputForViews is a narrow indirection used to keep A/B loading in
// this file; the actual reader remains diagnose.Load and is never modified.
var diagnoseInputForViews = loadDiagnoseInput

func loadDiagnoseInput(path string) (*diagnose.Input, error) {
	return diagnose.Load(path)
}

func loadRooflineOps(path string) ([]RooflineOp, error) {
	ops := make([]RooflineOp, 0)
	err := scanJSONLFile(path, func(line int, raw []byte) error {
		var op RooflineOp
		if err := json.Unmarshal(raw, &op); err != nil {
			return fmt.Errorf("line %d: %w", line, err)
		}
		ops = append(ops, op)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return ops, nil
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
