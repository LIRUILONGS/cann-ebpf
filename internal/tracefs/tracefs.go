// Package tracefs implements a small tracefs uprobe backend.
//
// It is intentionally a fallback for M1: it can collect host-side CANN
// entry/return spans without loading an eBPF object. The eBPF backend remains
// the primary implementation.
package tracefs

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

type SymbolSpec struct {
	Library  string `json:"library"`
	Function string `json:"function"`
	Category string `json:"category"`
	Offset   uint64 `json:"offset"`
}

type Span struct {
	TimestampNS uint64 `json:"ts_ns"`
	PID         int    `json:"pid"`
	TID         int    `json:"tid"`
	Function    string `json:"function"`
	Category    string `json:"category,omitempty"`
	Library     string `json:"library"`
	DurationNS  uint64 `json:"duration_ns"`
}

type Options struct {
	PID       int
	Duration  time.Duration
	Root      string
	OutputDir string
}

type Result struct {
	Spans    []Span         `json:"spans"`
	Started  time.Time      `json:"started"`
	Ended    time.Time      `json:"ended"`
	Root     string         `json:"root"`
	Group    string         `json:"group"`
	Events   []string       `json:"events"`
	Warnings []string       `json:"warnings,omitempty"`
	Manifest map[string]any `json:"manifest,omitempty"`
}

var traceLineRE = regexp.MustCompile(`^\s*(.*?)-([0-9]+)\s+\[([0-9]+)\]\s+\S+\s+([0-9]+)\.([0-9]+):\s+([^:]+):`)

func Run(ctx context.Context, specs []SymbolSpec, opts Options) (Result, error) {
	if opts.PID <= 0 {
		return Result{}, errors.New("tracefs: pid is required")
	}
	if opts.Duration <= 0 {
		opts.Duration = 10 * time.Second
	}
	if len(specs) == 0 {
		return Result{}, errors.New("tracefs: no symbols")
	}
	root, err := findRoot(opts.Root)
	if err != nil {
		return Result{}, err
	}
	eventFile := filepath.Join(root, "uprobe_events")
	if err := checkWritable(eventFile); err != nil {
		return Result{}, err
	}

	group := fmt.Sprintf("cann_%d_%d", opts.PID, time.Now().UnixNano())
	entry := make(map[string]SymbolSpec, len(specs))
	ret := make(map[string]SymbolSpec, len(specs))
	var added []string
	cleanup := func() {
		for i := len(added) - 1; i >= 0; i-- {
			_ = appendLine(eventFile, "-:"+added[i])
		}
	}
	defer cleanup()

	for i, spec := range specs {
		if spec.Library == "" || spec.Function == "" {
			return Result{}, fmt.Errorf("tracefs: invalid symbol at index %d", i)
		}
		entryName := fmt.Sprintf("e%03d", i)
		retName := fmt.Sprintf("r%03d", i)
		entry[entryName] = spec
		ret[retName] = spec
		if err := appendLine(eventFile, fmt.Sprintf("p:%s/%s %s:0x%x", group, entryName, spec.Library, spec.Offset)); err != nil {
			return Result{}, fmt.Errorf("tracefs: add entry %s: %w", spec.Function, err)
		}
		added = append(added, group+"/"+entryName)
		if err := appendLine(eventFile, fmt.Sprintf("r:%s/%s %s:0x%x", group, retName, spec.Library, spec.Offset)); err != nil {
			return Result{}, fmt.Errorf("tracefs: add return %s: %w", spec.Function, err)
		}
		added = append(added, group+"/"+retName)
	}

	for _, name := range added {
		path := filepath.Join(root, "events", filepath.FromSlash(name), "enable")
		if err := os.WriteFile(path, []byte("1\n"), 0o600); err != nil {
			return Result{}, fmt.Errorf("tracefs: enable %s: %w", name, err)
		}
	}

	tracePath := filepath.Join(root, "trace")
	_ = os.WriteFile(tracePath, nil, 0o600)
	tracingOnPath := filepath.Join(root, "tracing_on")
	previousTracingOn, _ := os.ReadFile(tracingOnPath)
	if err := os.WriteFile(tracingOnPath, []byte("1\n"), 0o600); err != nil {
		return Result{}, fmt.Errorf("tracefs: enable tracing: %w", err)
	}
	defer func() {
		if len(previousTracingOn) > 0 {
			_ = os.WriteFile(tracingOnPath, previousTracingOn, 0o600)
		}
	}()
	for _, name := range added {
		path := filepath.Join(root, "events", filepath.FromSlash(name), "enable")
		defer os.WriteFile(path, []byte("0\n"), 0o600)
	}

	f, err := os.Open(filepath.Join(root, "trace_pipe"))
	if err != nil {
		return Result{}, fmt.Errorf("tracefs: open trace_pipe: %w", err)
	}
	defer f.Close()

	lineCh := make(chan traceLine, 1024)
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		defer close(lineCh)
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			if line, ok := parseTraceLine(sc.Text()); ok {
				select {
				case lineCh <- line:
				case <-ctx.Done():
					return
				}
			}
		}
	}()

	started := time.Now()
	deadline := time.NewTimer(opts.Duration)
	defer deadline.Stop()
	pending := make(map[pairKey]Span)
	var spans []Span
	var warnings []string
	var tgidCache sync.Map
loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case <-deadline.C:
			break loop
		case line, ok := <-lineCh:
			if !ok {
				break loop
			}
			spec, isEntry := entry[line.Event]
			if !isEntry {
				var isReturn bool
				spec, isReturn = ret[line.Event]
				if !isReturn {
					continue
				}
			}
			tgid := processTGID(line.TID, &tgidCache)
			if tgid != opts.PID {
				continue
			}
			key := pairKey{TID: line.TID, Function: spec.Function, Library: spec.Library}
			if isEntry {
				pending[key] = Span{
					TimestampNS: line.TimestampNS,
					PID:         tgid,
					TID:         line.TID,
					Function:    spec.Function,
					Category:    spec.Category,
					Library:     spec.Library,
				}
				continue
			}
			begin, ok := pending[key]
			if !ok {
				continue
			}
			delete(pending, key)
			if line.TimestampNS < begin.TimestampNS {
				warnings = append(warnings, fmt.Sprintf("tracefs: return timestamp before entry for tid=%d function=%s", line.TID, spec.Function))
				continue
			}
			begin.DurationNS = line.TimestampNS - begin.TimestampNS
			spans = append(spans, begin)
		}
	}
	ended := time.Now()
	_ = f.Close()
	<-readDone

	result := Result{
		Spans:    spans,
		Started:  started,
		Ended:    ended,
		Root:     root,
		Group:    group,
		Events:   append([]string(nil), added...),
		Warnings: warnings,
	}
	if opts.OutputDir != "" {
		if err := writeOutput(opts.OutputDir, result); err != nil {
			return result, err
		}
	}
	return result, nil
}

type pairKey struct {
	TID      int
	Function string
	Library  string
}

type traceLine struct {
	TID         int
	TimestampNS uint64
	Event       string
}

func parseTraceLine(s string) (traceLine, bool) {
	m := traceLineRE.FindStringSubmatch(s)
	if len(m) != 7 {
		return traceLine{}, false
	}
	tid, err := strconv.Atoi(m[2])
	if err != nil {
		return traceLine{}, false
	}
	sec, err := strconv.ParseUint(m[4], 10, 64)
	if err != nil {
		return traceLine{}, false
	}
	frac := m[5]
	if len(frac) > 9 {
		frac = frac[:9]
	}
	for len(frac) < 9 {
		frac += "0"
	}
	ns, err := strconv.ParseUint(frac, 10, 64)
	if err != nil {
		return traceLine{}, false
	}
	event := strings.TrimSpace(m[6])
	if i := strings.LastIndexByte(event, '/'); i >= 0 {
		event = event[i+1:]
	}
	return traceLine{TID: tid, TimestampNS: sec*1_000_000_000 + ns, Event: event}, true
}

func processTGID(tid int, cache *sync.Map) int {
	if tid <= 0 {
		return tid
	}
	if v, ok := cache.Load(tid); ok {
		return v.(int)
	}
	tgid := tid
	b, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(tid), "status"))
	if err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(line, "Tgid:") {
				if n, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "Tgid:"))); err == nil {
					tgid = n
				}
				break
			}
		}
	}
	cache.Store(tid, tgid)
	return tgid
}

func findRoot(root string) (string, error) {
	candidates := []string{root, "/sys/kernel/tracing", "/sys/kernel/debug/tracing"}
	seen := map[string]struct{}{}
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if _, ok := seen[candidate]; ok {
			continue
		}
		seen[candidate] = struct{}{}
		if _, err := os.Stat(filepath.Join(candidate, "uprobe_events")); err == nil {
			return candidate, nil
		}
	}
	return "", errors.New("tracefs: uprobe_events not found (is tracefs mounted?)")
}

func checkWritable(path string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("tracefs: open %s: %w", path, err)
	}
	return f.Close()
}

func appendLine(path, line string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.WriteString(line + "\n")
	cerr := f.Close()
	if werr != nil {
		return werr
	}
	return cerr
}

func writeOutput(dir string, result Result) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, "spans.jsonl"), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	for _, span := range result.Spans {
		if err := enc.Encode(span); err != nil {
			_ = f.Close()
			return err
		}
	}
	if err := f.Close(); err != nil {
		return err
	}
	manifest := map[string]any{
		"backend":      "tracefs",
		"started":      result.Started.UTC().Format(time.RFC3339Nano),
		"ended":        result.Ended.UTC().Format(time.RFC3339Nano),
		"spans":        len(result.Spans),
		"tracefs_root": result.Root,
		"event_group":  result.Group,
		"events":       result.Events,
		"warnings":     result.Warnings,
	}
	b, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "manifest.json"), append(b, '\n'), 0o600)
}
