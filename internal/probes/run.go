//go:build linux

package probes

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"cann-ebpf/internal/contextfeed"
	"cann-ebpf/internal/discovery"
	"cann-ebpf/internal/elfutil"
	"cann-ebpf/internal/runlog"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/link"
	"github.com/cilium/ebpf/ringbuf"
)

func Run(opts Options) (result *Result, retErr error) {
	if err := opts.normalize(); err != nil {
		return nil, err
	}
	result = &Result{Started: time.Now()}
	functions, warnings := requestedFunctions(opts.Profile)
	result.Warnings = append(result.Warnings, warnings...)
	if len(functions) == 0 {
		return nil, fmt.Errorf("profile selects no supported host probe functions")
	}

	var contextFeed *contextfeed.Feed
	contextClosed := false
	if opts.ContextSocket != "" {
		startOptions, credentials := contextSocketOptions(opts.ProcRoot, opts.PIDs)
		feed, feedWarnings, feedErr := contextfeed.StartWithOptions(opts.ContextSocket, startOptions)
		contextFeed = feed
		result.Warnings = append(result.Warnings, feedWarnings...)
		if feedErr != nil {
			result.Warnings = append(result.Warnings, "context attribution disabled: "+feedErr.Error())
		} else {
			result.Warnings = append(result.Warnings, "context attribution listening on "+opts.ContextSocket)
			if startOptions.Chown {
				result.Warnings = append(result.Warnings, fmt.Sprintf(
					"context socket owner=%d:%d allowed_uids=%v target_credentials=%d",
					startOptions.OwnerUID, startOptions.OwnerGID, startOptions.AllowedUIDs, len(credentials)))
			}
		}
	}
	closeContext := func() {
		if contextFeed == nil || contextClosed {
			return
		}
		contextClosed = true
		_ = contextFeed.Close()
		result.Warnings = append(result.Warnings, contextFeed.Warnings()...)
		result.Context = contextFeed.Snapshot()
	}
	defer closeContext()

	collection, err := loadCollection()
	if err != nil {
		return nil, err
	}
	defer collection.Close()
	if err := configureProbeMaps(collection, opts.Profile, uint64(opts.MinDuration)); err != nil {
		return result, err
	}
	eventsMap, ok := collection.Maps["events"]
	if !ok {
		return result, fmt.Errorf("BPF map events not found")
	}
	reader, err := ringbuf.NewReader(eventsMap)
	if err != nil {
		return result, fmt.Errorf("open ring buffer: %w", err)
	}
	defer reader.Close()

	var writer *runlog.Writer
	if opts.OutputDir != "" {
		writer, err = newRunWriter(opts)
		if err != nil {
			return result, err
		}
		defer func() {
			if writer != nil {
				_ = writer.CloseWithError(retErr)
			}
		}()
		result.JSONLPath = writer.JSONLPath()
		result.ManifestPath = filepath.Join(opts.OutputDir, "manifest.json")
	}

	var links []link.Link
	var linkMu sync.Mutex
	closeLinks := func() {
		linkMu.Lock()
		defer linkMu.Unlock()
		for i := len(links) - 1; i >= 0; i-- {
			_ = links[i].Close()
		}
		links = nil
	}
	defer closeLinks()

	blockingOffCPU, blockingFutex, blockingErr := resolveBlocking(opts.Profile, opts.Blocking)
	if blockingErr != nil {
		return result, blockingErr
	}
	var blockingResult BlockingResult
	var blockingCollection *ebpf.Collection
	var blockingAttachments []Attachment
	if blockingOffCPU || blockingFutex {
		loaded, loadErr := loadBlockingCollection(collection)
		if loadErr != nil {
			result.Warnings = append(result.Warnings, loadErr.Error())
			blockingResult = BlockingResult{
				Mode: string(opts.Blocking), OffCPU: false, Futex: false,
				Warnings: []string{loadErr.Error()},
			}
		} else {
			blockingCollection = loaded
			defer func() {
				if blockingCollection != nil {
					blockingCollection.Close()
				}
			}()
			var blockingWarnings []string
			blockingAttachments, blockingWarnings, blockingResult = attachBlocking(blockingCollection, blockingOffCPU, blockingFutex, &links, &linkMu)
			blockingResult.Mode = string(opts.Blocking)
			result.Warnings = append(result.Warnings, blockingWarnings...)
			if err := configureBlockingMap(collection, blockingResult.OffCPU, blockingResult.Futex); err != nil {
				result.Warnings = append(result.Warnings, "configure blocking map: "+err.Error())
				blockingResult.OffCPU = false
				blockingResult.Futex = false
			}
		}
	} else {
		blockingResult = BlockingResult{Mode: string(opts.Blocking)}
		_ = configureBlockingMap(collection, false, false)
	}
	result.Blocking = blockingResult

	attachments, functionLibraries, attachWarnings := attachTargets(opts, collection, functions, &links, &linkMu)
	result.Attachments = append(attachments, blockingAttachments...)
	result.Warnings = append(result.Warnings, attachWarnings...)
	if len(functionLibraries) == 0 {
		return result, fmt.Errorf("no uprobe could be attached")
	}

	batchLayer, batchWarnings := startPythonBatchLayer(opts.Context, opts)
	result.Warnings = append(result.Warnings, batchWarnings...)

	var spanMu sync.Mutex
	var captured []runlog.Span
	spanLimitReached := false
	sink := func(span runlog.Span) {
		spanMu.Lock()
		defer spanMu.Unlock()
		if len(captured) < opts.MaxSpans {
			captured = append(captured, span)
		} else {
			spanLimitReached = true
		}
	}
	consumeDone := make(chan struct{})
	var consumeErr error
	go func() {
		defer close(consumeDone)
		consumeErr = consumeRing(reader, functionLibraries, writer, sink, contextFeed)
	}()

	runCtx, cancel := context.WithCancel(opts.Context)
	defer cancel()
	timer := time.NewTimer(opts.Duration)
	defer timer.Stop()
	var runErr error
	select {
	case <-timer.C:
	case <-runCtx.Done():
		if !errors.Is(runCtx.Err(), context.Canceled) {
			runErr = runCtx.Err()
		}
	}

	closeLinks()
	if blockingCollection != nil {
		blockingCollection.Close()
		blockingCollection = nil
	}
	_ = reader.Flush()
	batchCaptures, batchWarnings := batchLayer.Collect(10 * time.Second)
	result.Warnings = append(result.Warnings, batchWarnings...)
	for _, capture := range batchCaptures {
		if opts.OutputDir != "" && len(capture.ReportJSON) > 0 {
			path := filepath.Join(opts.OutputDir, fmt.Sprintf("python-batch-%d.json", capture.PID))
			if writeErr := os.WriteFile(path, capture.ReportJSON, 0o600); writeErr != nil {
				result.Warnings = append(result.Warnings,
					fmt.Sprintf("write python batch report %s: %v", path, writeErr))
			} else if writer != nil {
				if artifactErr := writer.AddArtifact(path, "python_batch_report"); artifactErr != nil {
					result.Warnings = append(result.Warnings,
						fmt.Sprintf("seal python batch report %s: %v", path, artifactErr))
				}
			}
		}
		for _, span := range capture.Spans {
			if writer != nil {
				if writeErr := writer.WriteSpan(span); writeErr != nil {
					result.Warnings = append(result.Warnings,
						fmt.Sprintf("write python batch span pid %d: %v", capture.PID, writeErr))
					continue
				}
			}
			sink(span)
		}
		result.PythonBatch = append(result.PythonBatch, capture)
	}
	select {
	case <-consumeDone:
	case <-time.After(3 * time.Second):
		_ = reader.Close()
		<-consumeDone
	}
	spanMu.Lock()
	result.Spans = append([]runlog.Span(nil), captured...)
	limitReached := spanLimitReached
	spanMu.Unlock()
	if limitReached {
		result.Warnings = append(result.Warnings, fmt.Sprintf(
			"in-memory span buffer reached max_spans=%d; JSONL remains complete and summary is derived from JSONL", opts.MaxSpans))
	}
	closeContext()
	if consumeErr != nil && runErr == nil {
		runErr = consumeErr
	}
	if stats, statsErr := readProbeStats(collection); statsErr == nil {
		result.Stats = stats
		if writer != nil {
			_ = writer.AddDropped(stats.RingbufLost)
		}
	} else {
		result.Warnings = append(result.Warnings, "read probe stats: "+statsErr.Error())
	}
	result.Ended = time.Now()
	if runErr != nil {
		result.Warnings = append(result.Warnings, runErr.Error())
	}
	if writer != nil {
		if opts.OutputDir != "" {
			result.SummaryPath = filepath.Join(opts.OutputDir, "summary.json")
		}
		for _, warning := range result.Warnings {
			_ = writer.AddWarning(warning)
		}
		closeErr := writer.CloseWithError(runErr)
		result.EventCount = writer.SnapshotManifest().EventCount
		writer = nil
		if closeErr != nil {
			result.Warnings = append(result.Warnings, "close output: "+closeErr.Error())
		}
	}
	if opts.OutputDir != "" {
		if summary, summaryErr := runlog.BuildSummaryFromFile(result.JSONLPath); summaryErr == nil {
			if summaryErr = summary.WriteJSON(filepath.Join(opts.OutputDir, "summary.json")); summaryErr == nil {
				summaryErr = summary.WriteMarkdown(filepath.Join(opts.OutputDir, "summary.md"))
			}
			if summaryErr != nil {
				result.Warnings = append(result.Warnings, "write summary: "+summaryErr.Error())
			}
		} else {
			result.Warnings = append(result.Warnings, "build summary: "+summaryErr.Error())
		}
	}
	return result, nil
}

func attachTargets(opts Options, collection *ebpf.Collection, functions []Function, links *[]link.Link, linkMu *sync.Mutex) ([]Attachment, map[string]string, []string) {
	byName := make(map[string]Function, len(functions))
	names := make([]string, 0, len(functions))
	for _, fn := range functions {
		byName[fn.Name] = fn
		names = append(names, fn.Name)
	}
	var attachments []Attachment
	var warnings []string
	functionLibraries := map[string]string{}
	attached := map[string]struct{}{}
	pidAttached := map[int]int{}
	type cachedLibrary struct {
		executable *link.Executable
		symbols    []elfutil.Symbol
		warnings   []string
		err        error
	}
	libraryCache := map[string]*cachedLibrary{}
	var globs []string
	if opts.Profile != nil {
		globs = opts.Profile.Spec.Cann.LibraryGlobs
	}

	for _, pid := range opts.PIDs {
		proc, err := discovery.ReadProcess(opts.ProcRoot, pid)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("pid %d: %v", pid, err))
			continue
		}
		libraries := discovery.FindLibraries(proc, globs)
		for _, library := range libraries {
			if library.RealPath == "" || library.Note != "" && strings.Contains(library.Note, "no such file") {
				warnings = append(warnings, fmt.Sprintf("pid %d library %s: %s", pid, library.Path, library.Note))
				continue
			}
			cacheKey := fmt.Sprintf("%d:%d:%s", library.Inode, library.Size, filepath.Base(library.RealPath))
			cached := libraryCache[cacheKey]
			if cached == nil {
				cached = &cachedLibrary{}
				cached.symbols, cached.warnings, cached.err = elfutil.Resolve(library.RealPath, names)
				if cached.err == nil {
					cached.executable, cached.err = link.OpenExecutable(library.RealPath)
				}
				libraryCache[cacheKey] = cached
			}
			if len(cached.warnings) > 0 {
				for _, warning := range cached.warnings {
					if !strings.Contains(warning, "symbol not found") && !strings.Contains(warning, "not found") {
						warnings = append(warnings, fmt.Sprintf("library %s: %s", library.RealPath, warning))
					}
				}
				cached.warnings = nil
			}
			if cached.err != nil {
				warnings = append(warnings, fmt.Sprintf("pid %d library %s: %v", pid, library.RealPath, cached.err))
				continue
			}
			symbols := cached.symbols
			executable := cached.executable
			for _, symbol := range symbols {
				if !symbol.Found || symbol.Error != "" {
					continue
				}
				fn, ok := byName[symbol.Wanted]
				if !ok {
					continue
				}
				key := fmt.Sprintf("%d\x00%s", pid, fn.Name)
				if _, exists := attached[key]; exists {
					continue
				}
				entryName, exitName := programNames(fn)
				entryProgram := collection.Programs[entryName]
				exitProgram := collection.Programs[exitName]
				attachment := Attachment{
					PID: pid, Library: library.RealPath, Function: fn.Name,
					Category: fn.Category, Offset: symbol.Offset,
				}
				if entryProgram == nil || exitProgram == nil {
					attachment.Error = "compiled probe program missing"
					attachments = append(attachments, attachment)
					continue
				}
				symbolName := strings.SplitN(symbol.Name, "@", 2)[0]
				uprobeOpts := &link.UprobeOptions{Address: symbol.Offset, PID: pid}
				entry, upErr := executable.Uprobe(symbolName, entryProgram, uprobeOpts)
				if upErr != nil {
					attachment.Error = upErr.Error()
					attachments = append(attachments, attachment)
					continue
				}
				exit, retErr := executable.Uretprobe(symbolName, exitProgram, uprobeOpts)
				if retErr != nil {
					_ = entry.Close()
					attachment.Error = retErr.Error()
					attachments = append(attachments, attachment)
					continue
				}
				linkMu.Lock()
				*links = append(*links, entry, exit)
				linkMu.Unlock()
				attachment.Entry, attachment.Exit = true, true
				attached[key] = struct{}{}
				pidAttached[pid]++
				functionLibraries[fn.Name] = library.RealPath
				attachments = append(attachments, attachment)
			}
		}
	}
	for _, pid := range opts.PIDs {
		if count := pidAttached[pid]; count == 0 {
			warnings = append(warnings, fmt.Sprintf("pid %d: no requested CANN symbol could be attached", pid))
		} else if count < len(functions) {
			warnings = append(warnings, fmt.Sprintf("pid %d: attached %d/%d requested CANN functions", pid, count, len(functions)))
		}
	}
	sort.Slice(attachments, func(i, j int) bool {
		if attachments[i].PID != attachments[j].PID {
			return attachments[i].PID < attachments[j].PID
		}
		return attachments[i].Function < attachments[j].Function
	})
	return attachments, functionLibraries, warnings
}

func consumeRing(reader *ringbuf.Reader, functionLibraries map[string]string, writer *runlog.Writer, sink func(runlog.Span), contextFeed *contextfeed.Feed) error {
	for {
		reader.SetDeadline(time.Now().Add(250 * time.Millisecond))
		record, err := reader.Read()
		if err != nil {
			switch {
			case errors.Is(err, os.ErrDeadlineExceeded):
				continue
			case errors.Is(err, ringbuf.ErrFlushed), errors.Is(err, ringbuf.ErrClosed):
				return nil
			default:
				return err
			}
		}
		event, err := decodeSpanEvent(record.RawSample)
		if err != nil {
			return err
		}
		fn, ok := functionByID(event.Function)
		if !ok {
			continue
		}
		span := runlog.Span{
			TsNS:       int64(event.StartNS),
			StartNS:    int64(event.StartNS),
			EndNS:      int64(event.StartNS + event.DurNS),
			PID:        int(event.PID),
			TID:        int(event.TID),
			TGID:       int(event.PID),
			Function:   fn.Name,
			APIName:    fn.Name,
			Library:    functionLibraries[fn.Name],
			Category:   fn.Category,
			DurationNS: int64(event.DurNS),
			Phase:      "host",
		}
		applyBlockingSample(&span, event)
		if contextFeed != nil {
			span = contextFeed.Enrich(span)
		} else {
			span.Attribution = &runlog.Attribution{
				Level: "thread", Source: "ebpf_tid", Confidence: 0.3,
				Verdict: "no_plugin_context",
			}
		}
		if writer != nil {
			if err := writer.WriteSpan(span); err != nil {
				return err
			}
		}
		sink(span)
	}
}

func newRunWriter(opts Options) (*runlog.Writer, error) {
	runID := opts.ProfileName + "-" + time.Now().UTC().Format("20060102T150405.000000000Z")
	var libraryRefs []runlog.LibraryRef
	seen := map[string]struct{}{}
	var globs []string
	if opts.Profile != nil {
		globs = opts.Profile.Spec.Cann.LibraryGlobs
	}
	for _, pid := range opts.PIDs {
		proc, err := discovery.ReadProcess(opts.ProcRoot, pid)
		if err != nil {
			continue
		}
		for _, library := range discovery.FindLibraries(proc, globs) {
			if library.RealPath == "" {
				continue
			}
			if _, ok := seen[library.RealPath]; ok {
				continue
			}
			seen[library.RealPath] = struct{}{}
			mtime := library.ModTime
			libraryRefs = append(libraryRefs, runlog.LibraryRef{
				Path: library.RealPath, Inode: library.Inode, Size: library.Size, MTime: &mtime,
			})
		}
	}
	host, _ := os.Hostname()
	kernel, _ := os.ReadFile("/proc/sys/kernel/osrelease")
	return runlog.NewWriter(runlog.WriterOptions{
		Dir: opts.OutputDir, RunID: runID, Profile: opts.ProfileName,
		ToolVersion: opts.ToolVersion, TargetPIDs: opts.PIDs, Libraries: libraryRefs,
		Host: host, Kernel: strings.TrimSpace(string(kernel)), StartedAt: time.Now(),
	})
}
