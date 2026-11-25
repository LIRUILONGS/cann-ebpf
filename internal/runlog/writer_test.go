package runlog

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestWriterConcurrentJSONLManifestAndHash(t *testing.T) {
	dir := t.TempDir()
	started := time.Now().Add(-time.Minute)
	library := LibraryRef{
		Path:   "/usr/local/Ascend/cann-9.2.0/aarch64-linux/lib64/libascendcl.so",
		Device: 8,
		Inode:  424242,
		Size:   8192,
	}
	writer, err := NewWriter(WriterOptions{
		Dir:         dir,
		RunID:       "run-concurrent",
		Profile:     "vllm-ascend",
		ToolVersion: "cann-ebpf/test",
		TargetPIDs:  []int{4321},
		Libraries:   []LibraryRef{library},
		Warnings:    []string{"initial warning"},
		Operator:    "codex",
		Command:     "cann-ebpf capture",
		StartedAt:   started,
		BufferSize:  128,
	})
	if err != nil {
		t.Fatal(err)
	}

	initial := readManifestAt(t, filepath.Join(dir, "manifest.json"))
	if initial.Status != ManifestStatusRunning || initial.EventCount != 0 {
		t.Fatalf("initial manifest = %+v", initial)
	}
	if !initial.StartedAt.Equal(started) || len(initial.Libraries) != 1 || initial.Libraries[0].Inode != 424242 {
		t.Fatalf("initial evidence metadata = %+v", initial)
	}

	const writers = 8
	const perWriter = 40
	var group sync.WaitGroup
	errs := make(chan error, writers)
	for worker := 0; worker < writers; worker++ {
		group.Add(1)
		go func(worker int) {
			defer group.Done()
			for index := 0; index < perWriter; index++ {
				ret := int64(index % 3)
				err := writer.WriteSpan(Span{
					TsNS:       started.UnixNano() + int64(worker*perWriter+index),
					PID:        4321,
					TID:        5000 + worker,
					Comm:       "worker",
					ThreadName: fmt.Sprintf("thread-%d", worker),
					Function:   "aclrtSynchronizeStream",
					Library:    library.Path,
					Category:   "sync",
					DurationNS: int64((worker + 1) * (index + 1)),
					Ret:        &ret,
					Phase:      "exit",
				})
				if err != nil {
					errs <- err
					return
				}
			}
		}(worker)
	}
	group.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}

	if err := writer.AddDropped(3); err != nil {
		t.Fatal(err)
	}
	if err := writer.AddWarning("late warning"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Flush(); err != nil {
		t.Fatal(err)
	}
	if snapshot := writer.SnapshotManifest(); snapshot.EventCount != writers*perWriter {
		t.Fatalf("flushed event_count = %d", snapshot.EventCount)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	manifest := readManifestAt(t, filepath.Join(dir, "manifest.json"))
	if manifest.Status != ManifestStatusComplete || manifest.EndedAt == nil {
		t.Fatalf("manifest status = %+v", manifest)
	}
	if manifest.EventCount != writers*perWriter || manifest.DroppedEvents != 3 {
		t.Fatalf("manifest counters = %+v", manifest)
	}
	if len(manifest.Warnings) != 2 || manifest.Warnings[1] != "late warning" {
		t.Fatalf("manifest warnings = %+v", manifest.Warnings)
	}
	if manifest.EndedAt.Before(manifest.StartedAt) {
		t.Fatalf("ended_at before started_at: %+v", manifest)
	}
	if len(manifest.Segments) != 1 {
		t.Fatalf("segments = %+v", manifest.Segments)
	}

	data, err := os.ReadFile(writer.JSONLPath())
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	wantHash := hex.EncodeToString(digest[:])
	segment := manifest.Segments[0]
	if segment.Path != writer.JSONLPath() || segment.Bytes != int64(len(data)) || segment.SHA256 != wantHash {
		t.Fatalf("segment = %+v, want bytes=%d sha=%s", segment, len(data), wantHash)
	}

	spans, err := ReadSpans(strings.NewReader(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	if len(spans) != writers*perWriter {
		t.Fatalf("spans = %d, want %d", len(spans), writers*perWriter)
	}
	sequences := make(map[uint64]struct{}, len(spans))
	for _, span := range spans {
		if span.Schema != SpanSchemaV1 || span.RunID != "run-concurrent" || span.Profile != "vllm-ascend" {
			t.Fatalf("span identity = %+v", span)
		}
		if span.PID != 4321 || span.TGID != 4321 || span.TID < 5000 {
			t.Fatalf("span ownership = %+v", span)
		}
		if span.SpanID == "" || span.Seq == 0 {
			t.Fatalf("span missing derived identity = %+v", span)
		}
		if _, duplicate := sequences[span.Seq]; duplicate {
			t.Fatalf("duplicate sequence %d", span.Seq)
		}
		sequences[span.Seq] = struct{}{}
	}
}

func TestWriterFlushMakesRecordDurable(t *testing.T) {
	dir := t.TempDir()
	writer, err := NewWriter(WriterOptions{
		Dir:         dir,
		RunID:       "run-flush",
		Profile:     "orinfer",
		ToolVersion: "cann-ebpf/test",
		TargetPIDs:  []int{7},
		StartedAt:   time.Now(),
		BufferSize:  1 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteSpan(testSpan()); err != nil {
		t.Fatal(err)
	}
	if err := writer.Flush(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(writer.JSONLPath())
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Count(string(data), "\n"); lines != 1 {
		t.Fatalf("flushed lines = %d, want 1; data=%q", lines, data)
	}
	if err := writer.AddDropped(4); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	manifest := readManifestAt(t, filepath.Join(dir, "manifest.json"))
	if manifest.EventCount != 1 || manifest.DroppedEvents != 4 {
		t.Fatalf("manifest = %+v", manifest)
	}
}

func TestWriterCloseWithErrorPreservesCountsAndIntegrity(t *testing.T) {
	dir := t.TempDir()
	writer, err := NewWriter(WriterOptions{
		Dir:         dir,
		RunID:       "run-error",
		Profile:     "orinfer",
		ToolVersion: "cann-ebpf/test",
		TargetPIDs:  []int{7},
		StartedAt:   time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteSpan(testSpan()); err != nil {
		t.Fatal(err)
	}
	runErr := errors.New("target process exited")
	if err := writer.CloseWithError(runErr); err != nil {
		t.Fatal(err)
	}
	manifest := readManifestAt(t, filepath.Join(dir, "manifest.json"))
	if manifest.Status != ManifestStatusError || manifest.Error != runErr.Error() {
		t.Fatalf("manifest error status = %+v", manifest)
	}
	if manifest.EventCount != 1 || len(manifest.Segments) != 1 {
		t.Fatalf("manifest evidence = %+v", manifest)
	}
}

func TestWriterRejectsIdentityMismatchAndExistingOutput(t *testing.T) {
	dir := t.TempDir()
	options := WriterOptions{
		Dir:         dir,
		RunID:       "run-strict",
		Profile:     "orinfer",
		ToolVersion: "cann-ebpf/test",
		TargetPIDs:  []int{7},
		StartedAt:   time.Now(),
	}
	writer, err := NewWriter(options)
	if err != nil {
		t.Fatal(err)
	}
	span := testSpan()
	span.RunID = "different-run"
	if err := writer.WriteSpan(span); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("identity mismatch error = %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := NewWriter(options); !errors.Is(err, ErrOutputExists) {
		t.Fatalf("existing output error = %v, want ErrOutputExists", err)
	}
}

func testSpan() Span {
	return Span{
		TsNS:       1000,
		PID:        7,
		TID:        8,
		Comm:       "orinfer",
		Function:   "aclrtSynchronizeStream",
		Library:    "/usr/local/Ascend/lib64/libascendcl.so",
		Category:   "sync",
		DurationNS: 123,
		Phase:      "exit",
	}
}
