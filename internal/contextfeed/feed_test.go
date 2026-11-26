package contextfeed

import (
	"encoding/json"
	"net"
	"testing"
	"time"

	"cann-ebpf/internal/runlog"
)

func TestFeedEnrichesBatchAndFallsBackToThread(t *testing.T) {
	feed := New()
	defer feed.Close()
	begin := Event{
		V: 1, Type: EventTypeBatchBegin, TsNS: 1000, PID: 42, BatchID: "b-1",
		Phase: "prefill", RequestIDs: []string{"r-1", "r-2", "r-1"},
	}
	if err := feed.Apply(begin); err != nil {
		t.Fatal(err)
	}
	span := feed.Enrich(runlog.Span{TsNS: 1500, StartNS: 1500, EndNS: 1700, DurationNS: 200, PID: 42, TID: 43})
	if span.BatchID != "b-1" || span.Attribution == nil || span.Attribution.Level != "batch" {
		t.Fatalf("batch attribution = %+v", span)
	}
	if len(span.RequestIDs) != 2 || span.RequestIDs[0] != "r-1" || span.RequestIDs[1] != "r-2" {
		t.Fatalf("request ids = %+v", span.RequestIDs)
	}
	if span.Attribution.Source != DefaultSource || span.Attribution.Confidence != 1 {
		t.Fatalf("attribution = %+v", span.Attribution)
	}
	if err := feed.Apply(Event{V: 1, Type: EventTypeBatchEnd, TsNS: 2000, PID: 42, BatchID: "b-1"}); err != nil {
		t.Fatal(err)
	}

	fallback := feed.Enrich(runlog.Span{TsNS: 3000, StartNS: 3000, EndNS: 3100, DurationNS: 100, PID: 42, TID: 43})
	if fallback.BatchID != "" || fallback.Attribution == nil || fallback.Attribution.Level != "thread" {
		t.Fatalf("thread fallback = %+v", fallback)
	}
	if fallback.Attribution.Confidence != 0.3 {
		t.Fatalf("fallback confidence = %v", fallback.Attribution.Confidence)
	}
}

func TestFeedBatchEndAndOverlap(t *testing.T) {
	feed := New()
	defer feed.Close()
	if err := feed.Apply(Event{V: 1, Type: EventTypeBatchBegin, TsNS: 100, PID: 7, BatchID: "b"}); err != nil {
		t.Fatal(err)
	}
	if err := feed.Apply(Event{V: 1, Type: EventTypeBatchEnd, TsNS: 250, PID: 7, BatchID: "b"}); err != nil {
		t.Fatal(err)
	}
	inside := feed.Enrich(runlog.Span{TsNS: 150, StartNS: 150, EndNS: 180, DurationNS: 30, PID: 7})
	if inside.BatchID != "b" {
		t.Fatalf("inside span = %+v", inside)
	}
	overlap := feed.Enrich(runlog.Span{TsNS: 90, StartNS: 90, EndNS: 120, DurationNS: 30, PID: 7})
	if overlap.BatchID != "b" {
		t.Fatalf("overlap span = %+v", overlap)
	}
	outside := feed.Enrich(runlog.Span{TsNS: 300, StartNS: 300, EndNS: 310, DurationNS: 10, PID: 7})
	if outside.BatchID != "" {
		t.Fatalf("outside span = %+v", outside)
	}
	if err := feed.Apply(Event{V: 1, Type: EventTypeBatchEnd, TsNS: 300, PID: 7, BatchID: "missing"}); err == nil {
		t.Fatal("expected unmatched batch_end error")
	}
}

func TestFeedApplyJSONRejectsMalformedAndUnknown(t *testing.T) {
	feed := New()
	defer feed.Close()
	good := []byte(`{"v":1,"type":"batch_begin","ts_ns":100,"pid":9,"batch_id":"b","request_ids":["r"]}`)
	if err := feed.ApplyJSON(good); err != nil {
		t.Fatal(err)
	}
	if err := feed.ApplyJSON([]byte(`{"v":1,"type":"batch_begin","ts_ns":100,"pid":9,"batch_id":"b","prompt":"secret"}`)); err == nil {
		t.Fatal("expected unknown field rejection")
	}
	if err := feed.ApplyJSON([]byte(`{"v":2,"type":"batch_begin","ts_ns":100,"pid":9,"batch_id":"b"}`)); err == nil {
		t.Fatal("expected version rejection")
	}
	if err := feed.ApplyJSON([]byte(`{"v":1,"type":"batch_begin","ts_ns":100,"pid":9,"batch_id":"b"} trailing`)); err == nil {
		t.Fatal("expected trailing data rejection")
	}
}

func TestFeedSnapshotAndClose(t *testing.T) {
	feed := New()
	if err := feed.Apply(Event{V: 1, Type: EventTypeBatchBegin, TsNS: 100, PID: 11, BatchID: "b", RequestIDs: []string{"r1"}}); err != nil {
		t.Fatal(err)
	}
	if err := feed.Apply(Event{V: 1, Type: EventTypeBatchEnd, TsNS: 200, PID: 11, BatchID: "b"}); err != nil {
		t.Fatal(err)
	}
	snapshot := feed.Snapshot()
	if snapshot.TotalBatches != 1 || snapshot.ActiveBatches != 0 || snapshot.RequestIDs != 1 || len(snapshot.PIDs) != 1 || snapshot.PIDs[0] != 11 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if err := feed.Close(); err != nil {
		t.Fatal(err)
	}
	if err := feed.Apply(Event{V: 1, Type: EventTypeBatchBegin, TsNS: 300, PID: 11, BatchID: "b2"}); err == nil {
		t.Fatal("expected closed feed rejection")
	}
}

func TestFeedProtocolJSONShape(t *testing.T) {
	event := Event{V: 1, Type: EventTypeBatchBegin, TsNS: 123, PID: 42, BatchID: "b", RequestIDs: []string{"r"}}
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"v", "type", "ts_ns", "pid", "batch_id", "request_ids"} {
		if _, ok := raw[key]; !ok {
			t.Fatalf("protocol missing %s: %s", key, data)
		}
	}
}

func TestFeedRetentionUsesReceiptTime(t *testing.T) {
	feed := New()
	defer feed.Close()
	now := time.Unix(1000, 0)
	feed.resetForTest(func() time.Time { return now })
	if err := feed.Apply(Event{V: 1, Type: EventTypeBatchBegin, TsNS: 1, PID: 1, BatchID: "old"}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(3 * time.Minute)
	if err := feed.Apply(Event{V: 1, Type: EventTypeBatchBegin, TsNS: 2, PID: 1, BatchID: "new"}); err != nil {
		t.Fatal(err)
	}
	snapshot := feed.Snapshot()
	if snapshot.TotalBatches != 1 {
		t.Fatalf("retention snapshot = %+v", snapshot)
	}
}

func TestFeedCloseTerminatesActiveConnection(t *testing.T) {
	feed := New()
	server, client := net.Pipe()
	defer client.Close()
	if !feed.addConn(server) {
		t.Fatal("could not track connection")
	}
	feed.wg.Add(1)
	go func() {
		defer feed.wg.Done()
		defer feed.removeConn(server)
		feed.readConn(server)
	}()

	closed := make(chan error, 1)
	go func() { closed <- feed.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Feed.Close blocked on an active plugin connection")
	}
}

func TestFeedPropagatesTraceContext(t *testing.T) {
	feed := New()
	defer feed.Close()
	traceID := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	batchSpanID := "bbbbbbbbbbbbbbbb"
	begin := Event{
		V: 1, Type: EventTypeBatchBegin, TsNS: 1000, PID: 42, BatchID: "b-trace",
		RequestIDs: []string{"r-1"}, TraceID: traceID, SpanID: batchSpanID,
		ParentSpanID: "cccccccccccccccc", TraceState: "vendor=value", TraceSource: "vllm_general_plugin",
	}
	if err := feed.Apply(begin); err != nil {
		t.Fatal(err)
	}
	span := feed.Enrich(runlog.Span{
		TsNS: 1500, StartNS: 1500, EndNS: 1700, DurationNS: 200,
		PID: 42, TID: 43, Function: "aclrtSynchronizeStream",
	})
	if span.Trace == nil {
		t.Fatal("batch span did not receive trace context")
	}
	if span.Trace.TraceID != traceID || span.Trace.ParentSpanID != batchSpanID {
		t.Fatalf("trace parentage = %+v", span.Trace)
	}
	if span.Trace.BatchParentSpanID != "cccccccccccccccc" {
		t.Fatalf("batch upstream parent = %+v", span.Trace)
	}
	if span.Trace.SpanID == "" || span.Trace.SpanID == batchSpanID {
		t.Fatalf("invalid child span id = %+v", span.Trace)
	}
	if span.Trace.TraceState != "vendor=value" || span.Trace.Source != "vllm_general_plugin" {
		t.Fatalf("trace metadata = %+v", span.Trace)
	}
	again := feed.Enrich(runlog.Span{
		TsNS: 1500, StartNS: 1500, EndNS: 1700, DurationNS: 200,
		PID: 42, TID: 43, Function: "aclrtSynchronizeStream",
	})
	if again.Trace == nil || again.Trace.SpanID != span.Trace.SpanID {
		t.Fatalf("trace span id is not deterministic: %+v vs %+v", span.Trace, again.Trace)
	}
	if err := feed.Apply(Event{
		V: 1, Type: EventTypeBatchEnd, TsNS: 2000, PID: 42, BatchID: "b-trace",
		TraceID: traceID, SpanID: batchSpanID,
	}); err != nil {
		t.Fatal(err)
	}
	ended := feed.Enrich(runlog.Span{
		TsNS: 1600, StartNS: 1600, EndNS: 1700, DurationNS: 100,
		PID: 42, TID: 43, Function: "aclrtSynchronizeStream",
	})
	if ended.Trace == nil || ended.Trace.BatchParentSpanID != "cccccccccccccccc" || ended.Trace.TraceState != "vendor=value" {
		t.Fatalf("batch_end erased begin trace metadata: %+v", ended.Trace)
	}
}

func TestFeedGeneratesLocalBatchTraceWhenPluginOmitsIt(t *testing.T) {
	feed := New()
	defer feed.Close()
	if err := feed.Apply(Event{V: 1, Type: EventTypeBatchBegin, TsNS: 100, PID: 7, BatchID: "b-local"}); err != nil {
		t.Fatal(err)
	}
	span := feed.Enrich(runlog.Span{TsNS: 120, StartNS: 120, EndNS: 130, DurationNS: 10, PID: 7, TID: 8, Function: "f"})
	if span.Trace == nil {
		t.Fatal("local batch trace was not generated")
	}
	if span.Trace.Source != "collector_local_batch" {
		t.Fatalf("trace source = %q", span.Trace.Source)
	}
	if err := span.Trace.Validate(); err != nil {
		t.Fatalf("generated trace is invalid: %v", err)
	}
}

func TestFeedRejectsInvalidOrMismatchedTrace(t *testing.T) {
	feed := New()
	defer feed.Close()
	bad := Event{
		V: 1, Type: EventTypeBatchBegin, TsNS: 100, PID: 9, BatchID: "b",
		TraceID: "not-hex", SpanID: "bbbbbbbbbbbbbbbb",
	}
	if err := feed.Apply(bad); err == nil {
		t.Fatal("invalid trace_id must be rejected")
	}
	begin := Event{
		V: 1, Type: EventTypeBatchBegin, TsNS: 100, PID: 9, BatchID: "b",
		TraceID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", SpanID: "bbbbbbbbbbbbbbbb",
	}
	if err := feed.Apply(begin); err != nil {
		t.Fatal(err)
	}
	end := Event{
		V: 1, Type: EventTypeBatchEnd, TsNS: 200, PID: 9, BatchID: "b",
		TraceID: "cccccccccccccccccccccccccccccccc", SpanID: "dddddddddddddddd",
	}
	if err := feed.Apply(end); err == nil {
		t.Fatal("mismatched batch_end trace must be rejected")
	}
}

func TestFeedApplyJSONAcceptsTraceFields(t *testing.T) {
	feed := New()
	defer feed.Close()
	line := []byte("{\"v\":1,\"type\":\"batch_begin\",\"ts_ns\":100,\"pid\":9,\"batch_id\":\"b\"," +
		"\"trace_id\":\"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\",\"span_id\":\"bbbbbbbbbbbbbbbb\"," +
		"\"trace_source\":\"vllm_general_plugin\",\"request_ids\":[\"r\"]}")
	if err := feed.ApplyJSON(line); err != nil {
		t.Fatal(err)
	}
	span := feed.Enrich(runlog.Span{TsNS: 110, StartNS: 110, EndNS: 120, DurationNS: 10, PID: 9, TID: 10, Function: "f"})
	if span.Trace == nil || span.Trace.TraceID != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("trace = %+v", span.Trace)
	}
}
