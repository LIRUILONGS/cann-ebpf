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
