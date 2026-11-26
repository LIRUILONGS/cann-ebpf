package contextfeed

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"cann-ebpf/internal/runlog"
	"cann-ebpf/internal/tracectx"
)

const (
	EventTypeBatchBegin   = "batch_begin"
	EventTypeBatchEnd     = "batch_end"
	EventTypeRequestBegin = "request_begin"
	EventTypeRequestEnd   = "request_end"
	EventTypeHeartbeat    = "heartbeat"
)

const (
	DefaultSource     = "vllm_general_plugin"
	defaultRetention  = 2 * time.Minute
	maxBatchesPerPID  = 512
	maxRequestIDs     = 4096
	maxWarningEntries = 32
)

// Event is the newline-delimited context protocol emitted by the optional
// vLLM plugin. Payloads contain identifiers only; prompt and response text are
// explicitly outside this contract.
type Event struct {
	V            int      `json:"v"`
	Type         string   `json:"type"`
	TsNS         int64    `json:"ts_ns"`
	PID          int      `json:"pid"`
	BatchID      string   `json:"batch_id,omitempty"`
	TraceID      string   `json:"trace_id,omitempty"`
	SpanID       string   `json:"span_id,omitempty"`
	ParentSpanID string   `json:"parent_span_id,omitempty"`
	TraceState   string   `json:"trace_state,omitempty"`
	TraceSource  string   `json:"trace_source,omitempty"`
	Phase        string   `json:"phase,omitempty"`
	RequestIDs   []string `json:"request_ids,omitempty"`
	Model        string   `json:"model,omitempty"`
	Source       string   `json:"source,omitempty"`
}

type batchWindow struct {
	id         string
	startNS    int64
	endNS      int64
	phase      string
	requestIDs []string
	source     string
	trace      tracectx.Context
	receivedAt time.Time
}

// Feed receives batch/request context from a framework plugin and applies it
// to host spans. It is safe to call Apply and Enrich concurrently.
type Feed struct {
	mu        sync.Mutex
	byPID     map[int][]*batchWindow
	conns     map[net.Conn]struct{}
	allowed   map[uint32]struct{}
	warnings  []string
	lastWarn  string
	listener  net.Listener
	done      chan struct{}
	wg        sync.WaitGroup
	closed    bool
	retention time.Duration
	now       func() time.Time
	accepted  uint64
	rejected  uint64
}

// New creates a context store without starting a listener. It is useful for
// deterministic unit tests and for callers that receive events elsewhere.
func New() *Feed {
	return &Feed{
		byPID:     map[int][]*batchWindow{},
		conns:     map[net.Conn]struct{}{},
		done:      make(chan struct{}),
		retention: defaultRetention,
		now:       time.Now,
	}
}

// StartOptions controls optional Unix-socket access. AllowedUIDs is required
// when the plugin runs as a non-root container user. OwnerUID/OwnerGID, when
// Chown is true, make the socket group/owner match the target process so a
// container can connect without weakening it to world-writable.
type StartOptions struct {
	AllowedUIDs []uint32
	OwnerUID    uint32
	OwnerGID    uint32
	Chown       bool
}

// Start creates a Unix-domain socket listener. A listen failure is returned as
// a warning by the caller's opt-in policy; the store remains usable with
// thread-level fallback. The caller must Close the returned feed.
func Start(socketPath string) (*Feed, []string, error) {
	return StartWithOptions(socketPath, StartOptions{})
}

// StartWithOptions is Start with explicit peer UID and socket ownership
// controls. The collector UID is always allowed in addition to AllowedUIDs.
func StartWithOptions(socketPath string, opts StartOptions) (*Feed, []string, error) {
	f := New()
	f.allowed = make(map[uint32]struct{}, len(opts.AllowedUIDs)+1)
	f.allowed[uint32(os.Geteuid())] = struct{}{}
	for _, uid := range opts.AllowedUIDs {
		f.allowed[uid] = struct{}{}
	}
	if strings.TrimSpace(socketPath) == "" {
		return f, nil, nil
	}
	listener, warnings, err := listenUnix(socketPath, opts)
	if err != nil {
		return f, warnings, err
	}
	f.listener = listener
	f.wg.Add(1)
	go f.acceptLoop()
	if len(warnings) > 0 {
		f.mu.Lock()
		f.warnings = append(f.warnings, warnings...)
		f.mu.Unlock()
	}
	return f, nil, nil
}

func (f *Feed) acceptLoop() {
	defer f.wg.Done()
	for {
		conn, err := f.listener.Accept()
		if err != nil {
			select {
			case <-f.done:
				return
			default:
				f.addWarning("context socket accept: " + err.Error())
				return
			}
		}
		f.wg.Add(1)
		if !f.addConn(conn) {
			f.wg.Done()
			continue
		}
		go func(c net.Conn) {
			defer f.wg.Done()
			defer c.Close()
			defer f.removeConn(c)
			if err := authorizePeer(c, f.allowed); err != nil {
				f.addWarning(err.Error())
				return
			}
			f.readConn(c)
		}(conn)
	}
}

func (f *Feed) addConn(conn net.Conn) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		_ = conn.Close()
		return false
	}
	f.conns[conn] = struct{}{}
	return true
}

func (f *Feed) removeConn(conn net.Conn) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.conns, conn)
}

func (f *Feed) readConn(conn net.Conn) {
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 4096), 64*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		if err := f.ApplyJSON(line); err != nil {
			f.mu.Lock()
			f.rejected++
			f.mu.Unlock()
			f.addWarning("context protocol: " + err.Error())
		}
	}
	if err := scanner.Err(); err != nil {
		f.addWarning("context socket read: " + err.Error())
	}
}

// ApplyJSON decodes one protocol line. Invalid records are rejected rather
// than partially applied, so malformed plugin input cannot corrupt context.
func (f *Feed) ApplyJSON(line []byte) error {
	var event Event
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&event); err != nil {
		return err
	}
	if dec.More() {
		return fmt.Errorf("trailing JSON data")
	}
	f.mu.Lock()
	f.accepted++
	f.mu.Unlock()
	return f.Apply(event)
}

// Apply updates the in-memory batch windows from one validated event.
func (f *Feed) Apply(event Event) error {
	if event.V != 0 && event.V != 1 {
		return fmt.Errorf("unsupported context version %d", event.V)
	}
	if event.PID <= 0 {
		return fmt.Errorf("pid must be positive")
	}
	switch event.Type {
	case EventTypeBatchBegin, EventTypeBatchEnd, EventTypeRequestBegin, EventTypeRequestEnd, EventTypeHeartbeat:
	default:
		return fmt.Errorf("unknown event type %q", event.Type)
	}
	if event.Type == EventTypeHeartbeat {
		return nil
	}
	if event.BatchID == "" {
		return fmt.Errorf("%s requires batch_id", event.Type)
	}
	if event.TsNS <= 0 {
		return fmt.Errorf("%s requires positive ts_ns", event.Type)
	}
	if len(event.RequestIDs) > maxRequestIDs {
		return fmt.Errorf("too many request_ids: %d", len(event.RequestIDs))
	}
	for i, id := range event.RequestIDs {
		if strings.TrimSpace(id) == "" {
			return fmt.Errorf("request_ids[%d] is empty", i)
		}
	}
	source := event.Source
	if source == "" {
		source = DefaultSource
	}
	trace := tracectx.Context{
		TraceID: event.TraceID, SpanID: event.SpanID,
		ParentSpanID: event.ParentSpanID, TraceState: event.TraceState,
		Source: event.TraceSource,
	}.Normalize()
	if trace.Source == "" && trace.TraceID != "" {
		trace.Source = source
	}
	if err := trace.ValidateBatch(); err != nil {
		return fmt.Errorf("trace context: %w", err)
	}
	now := f.now()
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return fmt.Errorf("context feed closed")
	}
	f.pruneLocked(now)
	switch event.Type {
	case EventTypeBatchBegin:
		if trace.TraceID == "" {
			trace = tracectx.NewContext("collector_local_batch")
		}
		window := &batchWindow{
			id: event.BatchID, startNS: event.TsNS, phase: event.Phase,
			requestIDs: uniqueIDs(event.RequestIDs), source: source,
			trace: trace, receivedAt: now,
		}
		windows := f.byPID[event.PID]
		windows = append(windows, window)
		if len(windows) > maxBatchesPerPID {
			windows = windows[len(windows)-maxBatchesPerPID:]
		}
		f.byPID[event.PID] = windows
	case EventTypeBatchEnd:
		windows := f.byPID[event.PID]
		found := false
		for i := len(windows) - 1; i >= 0; i-- {
			if windows[i].id != event.BatchID {
				continue
			}
			found = true
			if windows[i].endNS == 0 || event.TsNS > windows[i].endNS {
				windows[i].endNS = event.TsNS
			}
			if trace.TraceID != "" {
				if windows[i].trace.TraceID != "" &&
					(windows[i].trace.TraceID != trace.TraceID || windows[i].trace.SpanID != trace.SpanID) {
					return fmt.Errorf("batch_end trace does not match batch_begin: %s", event.BatchID)
				}
				if windows[i].trace.ParentSpanID != "" && trace.ParentSpanID != "" &&
					windows[i].trace.ParentSpanID != trace.ParentSpanID {
					return fmt.Errorf("batch_end parent_span_id does not match batch_begin: %s", event.BatchID)
				}
				// batch_end normally carries only trace_id/span_id. Merge
				// instead of replacing, otherwise the begin-side upstream
				// parent and trace_state would be silently lost.
				windows[i].trace.TraceID = trace.TraceID
				windows[i].trace.SpanID = trace.SpanID
				if trace.ParentSpanID != "" {
					windows[i].trace.ParentSpanID = trace.ParentSpanID
				}
				if trace.TraceState != "" {
					windows[i].trace.TraceState = trace.TraceState
				}
				if windows[i].trace.Source == "" {
					windows[i].trace.Source = trace.Source
				}
			}
			break
		}
		if !found {
			return fmt.Errorf("batch_end without batch_begin: %s", event.BatchID)
		}
	case EventTypeRequestBegin, EventTypeRequestEnd:
		// Request-level events are accepted and retained as protocol evidence,
		// but current attribution is batch-level. Do not invent request spans.
	}
	return nil
}

func uniqueIDs(ids []string) []string {
	if len(ids) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func (f *Feed) pruneLocked(now time.Time) {
	if f.retention <= 0 {
		f.retention = defaultRetention
	}
	for pid, windows := range f.byPID {
		kept := windows[:0]
		for _, window := range windows {
			if now.Sub(window.receivedAt) <= f.retention {
				kept = append(kept, window)
			}
		}
		if len(kept) == 0 {
			delete(f.byPID, pid)
			continue
		}
		f.byPID[pid] = kept
	}
}

// Enrich returns a copy of span with either a batch attribution derived from
// the plugin context or an explicit thread fallback. Observed fields are not
// changed. An empty context store therefore produces the honest
// attribution.level=thread label rather than pretending a batch match.
func (f *Feed) Enrich(span runlog.Span) runlog.Span {
	if span.StartNS == 0 {
		span.StartNS = span.TsNS
	}
	if span.EndNS == 0 && span.DurationNS >= 0 {
		span.EndNS = span.StartNS + span.DurationNS
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if window := f.matchLocked(span.PID, span.StartNS, span.EndNS); window != nil {
		span.BatchID = window.id
		span.RequestIDs = append([]string(nil), window.requestIDs...)
		span.Attribution = &runlog.Attribution{
			Level: "batch", Source: window.source, Confidence: 1.0,
			Verdict: "batch_window",
		}
		if window.trace.TraceID != "" {
			seed := fmt.Sprintf("%s|%s|%d|%d|%s|%d|%d",
				window.trace.TraceID, window.id, span.PID, span.TID,
				span.Function, span.StartNS, span.EndNS)
			child := window.trace.Child(seed)
			span.Trace = &child
		}
		return span
	}
	span.Attribution = &runlog.Attribution{
		Level: "thread", Source: "ebpf_tid", Confidence: 0.3,
		Verdict: "no_plugin_context",
	}
	return span
}

func (f *Feed) matchLocked(pid int, startNS, endNS int64) *batchWindow {
	windows := f.byPID[pid]
	if len(windows) == 0 || startNS <= 0 {
		return nil
	}
	if endNS <= 0 {
		endNS = startNS
	}
	var best *batchWindow
	for _, window := range windows {
		if window.startNS <= 0 {
			continue
		}
		windowEnd := window.endNS
		if windowEnd == 0 {
			windowEnd = endNS
		}
		if startNS > windowEnd || endNS < window.startNS {
			continue
		}
		if best == nil || window.startNS > best.startNS ||
			(window.startNS == best.startNS && window.receivedAt.After(best.receivedAt)) {
			best = window
		}
	}
	return best
}

// Snapshot is a read-only operational summary for manifest/report consumers.
type Snapshot struct {
	ActiveBatches int      `json:"active_batches"`
	TotalBatches  int      `json:"total_batches"`
	RequestIDs    int      `json:"request_ids"`
	PIDs          []int    `json:"pids"`
	Warnings      []string `json:"warnings,omitempty"`
}

func (f *Feed) Snapshot() Snapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := Snapshot{PIDs: make([]int, 0, len(f.byPID))}
	seenRequests := map[string]struct{}{}
	for pid, windows := range f.byPID {
		out.PIDs = append(out.PIDs, pid)
		out.TotalBatches += len(windows)
		for _, window := range windows {
			if window.endNS == 0 {
				out.ActiveBatches++
			}
			for _, id := range window.requestIDs {
				seenRequests[id] = struct{}{}
			}
		}
	}
	out.RequestIDs = len(seenRequests)
	sort.Ints(out.PIDs)
	out.Warnings = append([]string(nil), f.warnings...)
	return out
}

func (f *Feed) Warnings() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.warnings...)
}

func (f *Feed) addWarning(message string) {
	if message == "" {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if message == f.lastWarn {
		return
	}
	f.lastWarn = message
	f.warnings = append(f.warnings, message)
	if len(f.warnings) > maxWarningEntries {
		f.warnings = f.warnings[len(f.warnings)-maxWarningEntries:]
	}
}

// Close stops accepting context events and waits for readers to exit.
func (f *Feed) Close() error {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return nil
	}
	f.closed = true
	listener := f.listener
	conns := make([]net.Conn, 0, len(f.conns))
	for conn := range f.conns {
		conns = append(conns, conn)
	}
	f.conns = map[net.Conn]struct{}{}
	f.mu.Unlock()
	close(f.done)
	if listener != nil {
		_ = listener.Close()
	}
	for _, conn := range conns {
		_ = conn.Close()
	}
	f.wg.Wait()
	return nil
}

// ResetForTest makes time deterministic in tests without exposing production
// hooks through the public CLI.
func (f *Feed) resetForTest(now func() time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = now
}
