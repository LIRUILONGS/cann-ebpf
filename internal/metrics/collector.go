package metrics

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

// Source is one independent metrics source. Each source has its own ticker and
// timeout so a hung subprocess cannot stall other sources.
type Source interface {
	Name() string
	Interval() time.Duration
	Timeout() time.Duration
	Collect(context.Context) ([]Point, []string, error)
}

// Options configures a metrics collection run.
type Options struct {
	OutDir      string
	Interval    time.Duration
	Duration    time.Duration
	Sources     []Source
	Now         NowFunc
	ToolVersion string
	Host        string
	Kernel      string
}

// ClockMeta records the monotonic/wall-clock relationship for this run.
type ClockMeta struct {
	MonoStartNS int64 `json:"mono_start_ns"`
	WallStartNS int64 `json:"wall_start_ns"`
	OffsetNS    int64 `json:"offset_ns"`
	DriftNS     int64 `json:"drift_ns"`
}

// Artifact describes one output file in the metadata sidecar.
type Artifact struct {
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

// Artifacts groups the output artifacts of a metrics run.
type Artifacts struct {
	MetricsJSONL Artifact `json:"metrics_jsonl"`
}

// SeriesSummary describes one distinct series observed in a run.
type SeriesSummary struct {
	Name   string            `json:"name"`
	Kind   Kind              `json:"kind"`
	Unit   string            `json:"unit"`
	Source string            `json:"source"`
	Device string            `json:"device,omitempty"`
	Labels map[string]string `json:"labels,omitempty"`
	Points int               `json:"points"`
}

// Meta is the cann.metrics.meta.v1 sidecar document.
type Meta struct {
	Schema      string          `json:"schema"`
	ToolVersion string          `json:"tool_version,omitempty"`
	Host        string          `json:"host,omitempty"`
	Kernel      string          `json:"kernel,omitempty"`
	StartedAt   string          `json:"started_at"`
	EndedAt     string          `json:"ended_at"`
	IntervalMS  float64         `json:"interval_ms"`
	Sources     []string        `json:"sources"`
	Clock       ClockMeta       `json:"clock"`
	Series      []SeriesSummary `json:"series"`
	Warnings    []string        `json:"warnings"`
	Status      string          `json:"status"`
	Points      int             `json:"points"`
	Artifacts   Artifacts       `json:"artifacts"`
}

// Result is returned by Run. Meta is always populated when the output file was
// created, including partial-failure runs.
type Result struct {
	OutDir      string
	MetricsPath string
	MetaPath    string
	Points      int
	Status      string
	Warnings    []string
	Bytes       int64
	SHA256      string
	Meta        Meta
}

// Collector runs one metrics collection session.
type Collector struct {
	Options Options
}

// New creates a Collector.
func New(opts Options) *Collector { return &Collector{Options: opts} }

// Run is the package-level convenience entrypoint.
func Run(ctx context.Context, opts Options) (Result, error) {
	return New(opts).Run(ctx)
}

type sourceResult struct {
	source   string
	points   []Point
	warnings []string
	err      error
}

type warningList struct {
	seen  map[string]struct{}
	items []string
}

func newWarningList() *warningList {
	return &warningList{seen: make(map[string]struct{})}
}

func (w *warningList) Add(format string, args ...any) {
	msg := strings.TrimSpace(fmt.Sprintf(format, args...))
	if msg == "" {
		return
	}
	if _, ok := w.seen[msg]; ok {
		return
	}
	w.seen[msg] = struct{}{}
	w.items = append(w.items, msg)
}

func (w *warningList) All() []string {
	out := make([]string, len(w.items))
	copy(out, w.items)
	return out
}

// Run starts all sources, writes metrics.jsonl and always attempts to write
// metrics.meta.json. Existing output files are never overwritten.
func (c *Collector) Run(ctx context.Context) (Result, error) {
	opts := c.Options
	if strings.TrimSpace(opts.OutDir) == "" {
		return Result{}, fmt.Errorf("metrics: output directory is required")
	}
	if len(opts.Sources) == 0 {
		return Result{}, fmt.Errorf("metrics: at least one source is required")
	}
	if opts.Interval <= 0 {
		opts.Interval = time.Second
	}
	effectiveInterval := time.Duration(0)
	for _, source := range opts.Sources {
		if source == nil {
			continue
		}
		if interval := source.Interval(); interval > 0 && (effectiveInterval <= 0 || interval < effectiveInterval) {
			effectiveInterval = interval
		}
	}
	if effectiveInterval <= 0 {
		effectiveInterval = opts.Interval
	}
	now := opts.Now
	if now == nil {
		now = SystemNow
	}
	if opts.Host == "" {
		opts.Host, _ = os.Hostname()
	}
	if opts.Kernel == "" {
		opts.Kernel = kernelRelease()
	}
	if err := os.MkdirAll(opts.OutDir, 0o700); err != nil {
		return Result{}, fmt.Errorf("metrics: create output directory: %w", err)
	}
	metricsPath := filepath.Join(opts.OutDir, "metrics.jsonl")
	metaPath := filepath.Join(opts.OutDir, "metrics.meta.json")
	if _, err := os.Stat(metaPath); err == nil {
		return Result{}, fmt.Errorf("metrics: refusing to overwrite %s", metaPath)
	} else if !os.IsNotExist(err) {
		return Result{}, fmt.Errorf("metrics: stat %s: %w", metaPath, err)
	}
	file, err := os.OpenFile(metricsPath, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return Result{}, fmt.Errorf("metrics: create %s (refusing to overwrite): %w", metricsPath, err)
	}
	writer := bufio.NewWriterSize(file, 64*1024)
	warnings := newWarningList()
	series := make(map[string]*SeriesSummary)
	pointsWritten := 0
	var writeErr error

	startMono, startWall := now()
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if opts.Duration > 0 {
		go func() {
			timer := time.NewTimer(opts.Duration)
			defer timer.Stop()
			select {
			case <-timer.C:
				cancel()
			case <-ctx.Done():
				cancel()
			}
		}()
	} else {
		go func() {
			<-ctx.Done()
			cancel()
		}()
	}

	results := make(chan sourceResult, len(opts.Sources)*2+8)
	var wg sync.WaitGroup
	for _, source := range opts.Sources {
		if source == nil {
			continue
		}
		wg.Add(1)
		go func(source Source) {
			defer wg.Done()
			c.runSource(runCtx, source, results)
		}(source)
	}
	go func() {
		wg.Wait()
		close(results)
	}()

	for result := range results {
		for _, warning := range result.warnings {
			warnings.Add("%s", warning)
		}
		if result.err != nil && runCtx.Err() == nil {
			warnings.Add("source %s: %v", result.source, result.err)
		}
		for _, point := range result.points {
			if point.Schema == "" {
				point.Schema = SchemaV1
			}
			if point.Source == "" {
				point.Source = result.source
			}
			if point.MonoNS == 0 || point.WallNS == 0 {
				mono, wall := now()
				point.MonoNS = mono
				point.WallNS = wall
			}
			if err := point.Validate(); err != nil {
				warnings.Add("source %s: invalid point %q: %v", result.source, point.Name, err)
				continue
			}
			data, err := json.Marshal(point)
			if err != nil {
				writeErr = fmt.Errorf("marshal point: %w", err)
				break
			}
			if _, err := writer.Write(data); err != nil {
				writeErr = fmt.Errorf("write metrics.jsonl: %w", err)
				break
			}
			if err := writer.WriteByte('\n'); err != nil {
				writeErr = fmt.Errorf("write metrics.jsonl: %w", err)
				break
			}
			pointsWritten++
			key := SeriesKey(point)
			summary, ok := series[key]
			if !ok {
				summary = &SeriesSummary{
					Name: point.Name, Kind: point.Kind, Unit: point.Unit,
					Source: point.Source, Device: point.Device, Labels: cloneLabels(point.Labels),
				}
				series[key] = summary
			}
			summary.Points++
		}
		if writeErr == nil {
			if err := writer.Flush(); err != nil {
				writeErr = fmt.Errorf("flush metrics.jsonl: %w", err)
			}
		}
		if writeErr != nil {
			cancel()
			break
		}
	}
	if writeErr == nil {
		if err := writer.Flush(); err != nil {
			writeErr = fmt.Errorf("flush metrics.jsonl: %w", err)
		}
	}
	if err := file.Sync(); err != nil && writeErr == nil {
		writeErr = fmt.Errorf("sync metrics.jsonl: %w", err)
	}
	if err := file.Close(); err != nil && writeErr == nil {
		writeErr = fmt.Errorf("close metrics.jsonl: %w", err)
	}
	endMono, endWall := now()

	bytes, sum, digestErr := digestFile(metricsPath)
	if digestErr != nil && writeErr == nil {
		writeErr = fmt.Errorf("hash metrics.jsonl: %w", digestErr)
	}
	status := "completed"
	switch {
	case writeErr != nil:
		status = "failed"
	case ctx.Err() != nil:
		status = "canceled"
	case pointsWritten == 0:
		status = "no_points"
	}
	meta := Meta{
		Schema:      MetaSchemaV1,
		ToolVersion: opts.ToolVersion,
		Host:        opts.Host,
		Kernel:      opts.Kernel,
		StartedAt:   wallTime(startWall).Format(time.RFC3339Nano),
		EndedAt:     wallTime(endWall).Format(time.RFC3339Nano),
		IntervalMS:  float64(effectiveInterval) / float64(time.Millisecond),
		Sources:     sourceNames(opts.Sources),
		Clock: ClockMeta{
			MonoStartNS: startMono,
			WallStartNS: startWall,
			OffsetNS:    startWall - startMono,
			DriftNS:     (endWall - startWall) - (endMono - startMono),
		},
		Series:    seriesList(series),
		Warnings:  warnings.All(),
		Status:    status,
		Points:    pointsWritten,
		Artifacts: Artifacts{MetricsJSONL: Artifact{Bytes: bytes, SHA256: sum}},
	}
	if err := writeMeta(metaPath, meta); err != nil && writeErr == nil {
		writeErr = fmt.Errorf("write metrics.meta.json: %w", err)
		meta.Status = "failed"
	}
	result := Result{
		OutDir: opts.OutDir, MetricsPath: metricsPath, MetaPath: metaPath,
		Points: pointsWritten, Status: meta.Status, Warnings: meta.Warnings,
		Bytes: bytes, SHA256: sum, Meta: meta,
	}
	if writeErr != nil {
		return result, writeErr
	}
	if pointsWritten == 0 && status != "canceled" {
		return result, ErrNoPoints
	}
	return result, nil
}

func (c *Collector) runSource(ctx context.Context, source Source, out chan<- sourceResult) {
	name := source.Name()
	if strings.TrimSpace(name) == "" {
		name = "source"
	}
	interval := source.Interval()
	if interval <= 0 {
		interval = c.Options.Interval
	}
	if interval <= 0 {
		interval = time.Second
	}
	timeout := source.Timeout()
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	collect := func() {
		collectCtx, cancel := context.WithTimeout(ctx, timeout)
		points, warnings, err := source.Collect(collectCtx)
		cancel()
		result := sourceResult{source: name, points: points, warnings: warnings, err: err}
		select {
		case out <- result:
		case <-ctx.Done():
		}
	}
	collect()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			collect()
		}
	}
}

func sourceNames(sources []Source) []string {
	seen := make(map[string]struct{})
	var out []string
	for _, source := range sources {
		if source == nil {
			continue
		}
		name := source.Name()
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func seriesList(series map[string]*SeriesSummary) []SeriesSummary {
	out := make([]SeriesSummary, 0, len(series))
	for _, item := range series {
		out = append(out, *item)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Source != b.Source {
			return a.Source < b.Source
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		if a.Device != b.Device {
			return a.Device < b.Device
		}
		return labelKey(a.Labels) < labelKey(b.Labels)
	})
	return out
}

func digestFile(path string) (int64, string, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, "", err
	}
	defer file.Close()
	hash := sha256.New()
	bytes, err := io.Copy(hash, file)
	if err != nil {
		return bytes, "", err
	}
	return bytes, hex.EncodeToString(hash.Sum(nil)), nil
}

func writeMeta(path string, meta Meta) error {
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o600)
}

func kernelRelease() string {
	if data, err := os.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
		if value := strings.TrimSpace(string(data)); value != "" {
			return value
		}
	}
	return runtime.GOOS
}
