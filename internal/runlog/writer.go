package runlog

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

var (
	ErrWriterClosed  = errors.New("runlog: writer closed")
	ErrOutputExists  = errors.New("runlog: JSONL output already exists")
	ErrNonMonotonic  = errors.New("runlog: span sequence is not monotonic")
	ErrWriterFailure = errors.New("runlog: writer is in a failed state")
)

type WriterOptions struct {
	Dir            string
	JSONLName      string
	ManifestName   string
	AppendExisting bool
	RunID          string
	Profile        string
	ToolVersion    string
	TargetPIDs     []int
	Libraries      []LibraryRef
	Warnings       []string
	Operator       string
	Command        string
	Host           string
	Kernel         string
	CannVersion    string
	StartedAt      time.Time
	BufferSize     int
}

// Writer is a concurrency-safe JSONL sink. Every accepted span is appended as
// one JSON object plus newline; Flush makes all accepted spans durable before
// advancing manifest event_count.
type Writer struct {
	mu       sync.Mutex
	manifest *ManifestStore
	file     *os.File
	buffer   *bufio.Writer
	jsonl    string
	runID    string
	profile  string
	closed   bool
	closeErr error
	failed   error
	lastSeq  uint64
	accepted uint64
	durable  uint64
}

func NewWriter(options WriterOptions) (*Writer, error) {
	if options.Dir == "" {
		return nil, fmt.Errorf("output directory is empty")
	}
	if options.RunID == "" {
		return nil, fmt.Errorf("run_id is empty")
	}
	if options.Profile == "" {
		return nil, fmt.Errorf("profile is empty")
	}
	if options.ToolVersion == "" {
		return nil, fmt.Errorf("tool_version is empty")
	}
	if len(options.TargetPIDs) == 0 {
		return nil, fmt.Errorf("target_pids is empty")
	}
	if options.JSONLName == "" {
		options.JSONLName = "spans.jsonl"
	}
	if options.ManifestName == "" {
		options.ManifestName = "manifest.json"
	}
	if filepath.Base(options.JSONLName) != options.JSONLName {
		return nil, fmt.Errorf("JSONL name must not contain a directory: %q", options.JSONLName)
	}
	if filepath.Base(options.ManifestName) != options.ManifestName {
		return nil, fmt.Errorf("manifest name must not contain a directory: %q", options.ManifestName)
	}
	if options.BufferSize <= 0 {
		options.BufferSize = 128 * 1024
	}
	if options.StartedAt.IsZero() {
		options.StartedAt = time.Now()
	}
	if err := ensureDir(options.Dir); err != nil {
		return nil, err
	}

	jsonlPath := filepath.Join(options.Dir, options.JSONLName)
	manifestPath := filepath.Join(options.Dir, options.ManifestName)
	_, statErr := os.Stat(jsonlPath)
	preExisting := statErr == nil
	flags := os.O_CREATE | os.O_WRONLY | os.O_APPEND
	if !options.AppendExisting {
		flags |= os.O_EXCL
	}
	file, err := os.OpenFile(jsonlPath, flags, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("%w: %s", ErrOutputExists, jsonlPath)
		}
		return nil, fmt.Errorf("open JSONL: %w", err)
	}

	manifest := Manifest{
		RunID:       options.RunID,
		ToolVersion: options.ToolVersion,
		Status:      ManifestStatusRunning,
		Profile:     options.Profile,
		Host:        options.Host,
		Kernel:      options.Kernel,
		CannVersion: options.CannVersion,
		TargetPIDs:  append([]int(nil), options.TargetPIDs...),
		Libraries:   append([]LibraryRef(nil), options.Libraries...),
		StartedAt:   options.StartedAt,
		Warnings:    append([]string(nil), options.Warnings...),
		Operator:    options.Operator,
		Command:     options.Command,
	}
	store, err := NewManifestStore(manifestPath, manifest)
	if err != nil {
		file.Close()
		if !preExisting {
			os.Remove(jsonlPath)
		}
		return nil, err
	}
	return &Writer{
		manifest: store,
		file:     file,
		buffer:   bufio.NewWriterSize(file, options.BufferSize),
		jsonl:    jsonlPath,
		runID:    options.RunID,
		profile:  options.Profile,
	}, nil
}

func (w *Writer) JSONLPath() string {
	return w.jsonl
}

func (w *Writer) SnapshotManifest() Manifest {
	return w.manifest.Snapshot()
}

// WriteSpan appends one normalized, validated span. A zero Seq is assigned in
// ingestion order; explicit sequences must increase strictly. A zero SpanID is
// derived from immutable identity fields plus Seq. Observed values otherwise
// remain untouched.
func (w *Writer) WriteSpan(span Span) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return ErrWriterClosed
	}
	if w.failed != nil {
		return fmt.Errorf("%w: %v", ErrWriterFailure, w.failed)
	}

	span.Normalize(w.runID, w.profile)
	if span.RunID != w.runID {
		return fmt.Errorf("span run_id %q does not match writer run_id %q", span.RunID, w.runID)
	}
	if span.Profile != w.profile {
		return fmt.Errorf("span profile %q does not match writer profile %q", span.Profile, w.profile)
	}
	if span.Seq == 0 {
		if w.lastSeq == ^uint64(0) {
			return fmt.Errorf("sequence exhausted")
		}
		span.Seq = w.lastSeq + 1
	} else if span.Seq <= w.lastSeq {
		return fmt.Errorf("%w: got %d after %d", ErrNonMonotonic, span.Seq, w.lastSeq)
	}
	if span.SpanID == "" {
		span.SpanID = deriveSpanID(span)
	}
	if err := span.Validate(); err != nil {
		return fmt.Errorf("validate span: %w", err)
	}
	line, err := json.Marshal(span)
	if err != nil {
		return fmt.Errorf("marshal span: %w", err)
	}
	line = append(line, '\n')
	if _, err := w.buffer.Write(line); err != nil {
		w.failLocked(err)
		return err
	}
	w.lastSeq = span.Seq
	w.accepted++
	return nil
}

func deriveSpanID(span Span) string {
	hash := sha256.New()
	fmt.Fprintf(hash, "%s\x00%s\x00%s\x00%d\x00%d\x00%d\x00%d\x00%d\x00%s",
		span.RunID, span.Profile, span.Function, span.PID, span.TID, span.TsNS,
		span.DurationNS, span.Seq, span.Library)
	return hex.EncodeToString(hash.Sum(nil))
}

// Flush writes buffered spans and fsyncs them before recording the durable
// event count in the manifest.
func (w *Writer) Flush() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return ErrWriterClosed
	}
	if w.failed != nil {
		return fmt.Errorf("%w: %v", ErrWriterFailure, w.failed)
	}
	if err := w.buffer.Flush(); err != nil {
		w.failLocked(err)
		return err
	}
	if err := w.file.Sync(); err != nil {
		w.failLocked(err)
		return err
	}
	w.durable = w.accepted
	if err := w.manifest.Update(func(manifest *Manifest) {
		manifest.EventCount = w.durable
	}); err != nil {
		w.failLocked(err)
		return err
	}
	return nil
}

func (w *Writer) AddDropped(count uint64) error {
	if count == 0 {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return ErrWriterClosed
	}
	if w.failed != nil {
		return fmt.Errorf("%w: %v", ErrWriterFailure, w.failed)
	}
	if err := w.manifest.Update(func(manifest *Manifest) {
		manifest.DroppedEvents += count
	}); err != nil {
		w.failLocked(err)
		return err
	}
	return nil
}

func (w *Writer) AddWarning(warning string) error {
	if warning == "" {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return ErrWriterClosed
	}
	if w.failed != nil {
		return fmt.Errorf("%w: %v", ErrWriterFailure, w.failed)
	}
	if err := w.manifest.Update(func(manifest *Manifest) {
		manifest.Warnings = append(manifest.Warnings, warning)
	}); err != nil {
		w.failLocked(err)
		return err
	}
	return nil
}

func (w *Writer) Close() error {
	return w.CloseWithError(nil)
}

// CloseWithError flushes, fsyncs, hashes, and finalizes the manifest. runErr is
// recorded as status=error but is not itself returned; the return value only
// reports I/O or manifest failures encountered while closing.
func (w *Writer) CloseWithError(runErr error) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return w.closeErr
	}
	w.closed = true

	var errs []error
	effectiveErr := runErr
	if w.failed != nil {
		errs = append(errs, w.failed)
		if effectiveErr == nil {
			effectiveErr = w.failed
		}
	}

	flushErr := w.buffer.Flush()
	if flushErr != nil {
		errs = append(errs, fmt.Errorf("flush JSONL: %w", flushErr))
		if effectiveErr == nil {
			effectiveErr = flushErr
		}
	}
	syncErr := w.file.Sync()
	if syncErr != nil {
		errs = append(errs, fmt.Errorf("sync JSONL: %w", syncErr))
		if effectiveErr == nil {
			effectiveErr = syncErr
		}
	}
	closeFileErr := w.file.Close()
	if closeFileErr != nil {
		errs = append(errs, fmt.Errorf("close JSONL: %w", closeFileErr))
		if effectiveErr == nil {
			effectiveErr = closeFileErr
		}
	}
	if flushErr == nil && syncErr == nil {
		w.durable = w.accepted
	}

	segments := []SegmentRef(nil)
	if closeFileErr == nil {
		size, digest, hashErr := fileSHA256(w.jsonl)
		if hashErr != nil {
			errs = append(errs, fmt.Errorf("hash JSONL: %w", hashErr))
			if effectiveErr == nil {
				effectiveErr = hashErr
			}
		} else {
			segments = []SegmentRef{{Path: w.jsonl, Bytes: size, SHA256: digest}}
		}
	}
	updateErr := w.manifest.Update(func(manifest *Manifest) {
		manifest.EventCount = w.durable
		if len(segments) > 0 {
			manifest.Segments = segments
		}
	})
	if updateErr != nil {
		errs = append(errs, updateErr)
		if effectiveErr == nil {
			effectiveErr = updateErr
		}
	}
	finishErr := w.manifest.Finish(time.Now(), effectiveErr)
	if finishErr != nil {
		errs = append(errs, finishErr)
	}
	w.closeErr = errors.Join(errs...)
	return w.closeErr
}

func (w *Writer) failLocked(err error) {
	if w.failed == nil {
		w.failed = err
	}
}

func fileSHA256(path string) (int64, string, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, "", err
	}
	hash := sha256.New()
	bytes, copyErr := io.Copy(hash, file)
	closeErr := file.Close()
	if copyErr != nil {
		return bytes, "", copyErr
	}
	if closeErr != nil {
		return bytes, "", closeErr
	}
	return bytes, hex.EncodeToString(hash.Sum(nil)), nil
}
