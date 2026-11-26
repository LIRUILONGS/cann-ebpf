package cpython

import (
	"bytes"
	"encoding/binary"
	"os"
	"testing"
	"time"

	"github.com/cilium/ebpf"
)

func TestBatchBinaryLayouts(t *testing.T) {
	if got := binary.Size(BatchEvent{}); got != BatchEventSize {
		t.Fatalf("BatchEvent size = %d, want %d", got, BatchEventSize)
	}
	if got := binary.Size(BatchState{}); got != 96 {
		t.Fatalf("BatchState size = %d, want 96", got)
	}
	if got := binary.Size(BatchStats{}); got != 120 {
		t.Fatalf("BatchStats size = %d, want 120", got)
	}
}

func TestBatchProbeObjectLayout(t *testing.T) {
	data, err := os.ReadFile("bpf/batch_probe.bpf.o")
	if err != nil {
		t.Fatal(err)
	}
	spec, err := ebpf.LoadCollectionSpecFromReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"enter_pyobject_getattr", "exit_pyobject_getattr"} {
		if spec.Programs[name] == nil {
			t.Fatalf("missing BPF program %s", name)
		}
	}
	if got := spec.Maps["current"].ValueSize; got != 96 {
		t.Fatalf("current map value size = %d, want 96", got)
	}
	if got := spec.Maps["stats"].ValueSize; got != 120 {
		t.Fatalf("stats map value size = %d, want 120", got)
	}
	if got := spec.Maps["events"].Type; got != ebpf.RingBuf {
		t.Fatalf("events map type = %v, want RingBuf", got)
	}
}

func TestBuildBatchReport(t *testing.T) {
	offsets, err := LoadEmbeddedOffsets()
	if err != nil {
		t.Fatal(err)
	}
	events := []BatchEvent{
		batchBegin(0x100, 0x10, 100, "SchedulerOutput"),
		batchID(0x100, 0x10, 100, 110, "req-1"),
		batchID(0x100, 0x10, 100, 120, "req-2"),
		batchID(0x100, 0x10, 100, 125, "req-1"),
		batchEnd(0x100, 0x10, 100, 190),
		batchBegin(0x200, 0x20, 200, "SchedulerOutput"),
		batchID(0x200, 0x20, 200, 210, "req-3"),
	}
	report := BuildBatchReport(BatchReportInput{
		PID:         1452,
		Lib:         "/usr/local/python3.12.13/lib/libpython3.12.so.1.0",
		Duration:    6 * time.Second,
		Events:      events,
		OpenBatches: []OpenBatch{{PIDTID: 1452, State: BatchState{Dict: 0x200, Object: 0x20, BeginNS: 200, LastSeenNS: 300, TypeName: asciiArray("SchedulerOutput")}}},
		Offsets:     offsets,
		RequireType: "SchedulerOutput",
	})
	if report.Status != "ok" {
		t.Fatalf("status = %q, want ok; warnings=%v", report.Status, report.Warnings)
	}
	if report.BatchCount != 2 || report.RequestCount != 3 {
		t.Fatalf("batches=%d requests=%d, want 2/3", report.BatchCount, report.RequestCount)
	}
	if report.CompleteBatches != 1 || report.OpenBatchCount != 1 {
		t.Fatalf("complete=%d open=%d, want 1/1", report.CompleteBatches, report.OpenBatchCount)
	}
	if report.Batches[0].DurationNS != 90 || report.Batches[0].RequestCount != 2 {
		t.Fatalf("first batch = %+v", report.Batches[0])
	}
	if report.Batches[1].DurationNS != 100 || report.Batches[1].RequestCount != 1 {
		t.Fatalf("second batch = %+v", report.Batches[1])
	}
}

func TestBuildBatchReportTypeMismatch(t *testing.T) {
	offsets, err := LoadEmbeddedOffsets()
	if err != nil {
		t.Fatal(err)
	}
	report := BuildBatchReport(BatchReportInput{
		PID: 1,
		Events: []BatchEvent{
			batchBegin(0x100, 0x10, 100, "dict"),
			batchID(0x100, 0x10, 100, 110, "req-1"),
		},
		Offsets:     offsets,
		RequireType: "SchedulerOutput",
	})
	if report.Status != "ok_type_mismatch" {
		t.Fatalf("status = %q, want ok_type_mismatch", report.Status)
	}
	if report.TypeConfirmed {
		t.Fatal("TypeConfirmed = true, want false")
	}
}

func TestLoadEmbeddedOffsets(t *testing.T) {
	offsets, err := LoadEmbeddedOffsets()
	if err != nil {
		t.Fatal(err)
	}
	if offsets.CPython.PyASCIIObjectSize != 40 || offsets.CPython.PyTypeTPNameOffset != 24 {
		t.Fatalf("unexpected offsets: %+v", offsets.CPython)
	}
	if offsets.VLLM.Boundary != "scheduler_output_dict_identity" {
		t.Fatalf("unexpected boundary: %q", offsets.VLLM.Boundary)
	}
}

func batchBegin(dict, object, begin uint64, typeName string) BatchEvent {
	return BatchEvent{
		TsNS:         begin,
		Dict:         dict,
		Object:       object,
		BatchBeginNS: begin,
		EventType:    EventBatchBegin,
		TypeNameRaw:  asciiArray(typeName),
	}
}

func batchID(dict, object, begin, ts uint64, requestID string) BatchEvent {
	event := BatchEvent{
		TsNS:         ts,
		Dict:         dict,
		Object:       object,
		BatchBeginNS: begin,
		EventType:    EventID,
	}
	copy(event.ID[:], requestID)
	return event
}

func batchEnd(dict, object, begin, ts uint64) BatchEvent {
	return BatchEvent{
		TsNS:         ts,
		Dict:         dict,
		Object:       object,
		BatchBeginNS: begin,
		EventType:    EventBatchEnd,
	}
}

func asciiArray(value string) [64]byte {
	var result [64]byte
	copy(result[:], value)
	return result
}
