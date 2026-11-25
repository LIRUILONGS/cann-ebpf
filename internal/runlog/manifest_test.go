package runlog

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestManifestStoreWritesImmediatelyAndFinishes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "manifest.json")
	started := time.Date(2026, 11, 26, 8, 0, 0, 0, time.UTC)
	finished := started.Add(30 * time.Second)
	libraryTime := started.Add(-time.Hour)
	store, err := NewManifestStore(path, Manifest{
		RunID:       "run-1",
		ToolVersion: "cann-ebpf/test",
		Profile:     "vllm-ascend",
		TargetPIDs:  []int{1234},
		Libraries: []LibraryRef{{
			Path:   "/usr/local/Ascend/lib64/libascendcl.so",
			Device: 8,
			Inode:  777,
			Size:   4096,
			MTime:  &libraryTime,
		}},
		StartedAt: started,
		Warnings:  []string{"initial warning"},
	})
	if err != nil {
		t.Fatal(err)
	}

	initial := readManifestAt(t, path)
	if initial.Schema != ManifestSchemaV1 || initial.Status != ManifestStatusRunning {
		t.Fatalf("initial manifest = %+v", initial)
	}
	if initial.EventCount != 0 || initial.DroppedEvents != 0 {
		t.Fatalf("initial counters = %+v", initial)
	}
	if len(initial.TargetPIDs) != 1 || initial.TargetPIDs[0] != 1234 {
		t.Fatalf("target pids = %+v", initial.TargetPIDs)
	}
	if len(initial.Libraries) != 1 || initial.Libraries[0].Inode != 777 {
		t.Fatalf("libraries = %+v", initial.Libraries)
	}

	if err := store.Update(func(manifest *Manifest) {
		manifest.EventCount = 5
		manifest.DroppedEvents = 2
	}); err != nil {
		t.Fatal(err)
	}
	if got := store.Snapshot(); got.EventCount != 5 || got.DroppedEvents != 2 {
		t.Fatalf("snapshot = %+v", got)
	}
	if err := store.Finish(finished, nil); err != nil {
		t.Fatal(err)
	}
	final := readManifestAt(t, path)
	if final.Status != ManifestStatusComplete || final.EndedAt == nil {
		t.Fatalf("final manifest = %+v", final)
	}
	if !final.EndedAt.Equal(finished) || final.EventCount != 5 || final.DroppedEvents != 2 {
		t.Fatalf("final values = %+v", final)
	}

	snapshot := store.Snapshot()
	snapshot.Warnings[0] = "mutated by caller"
	if store.Snapshot().Warnings[0] != "initial warning" {
		t.Fatal("snapshot did not deep-copy warnings")
	}
	if err := store.Finish(finished, nil); !errors.Is(err, ErrManifestClosed) {
		t.Fatalf("second finish error = %v, want ErrManifestClosed", err)
	}
}

func TestManifestStoreSerializesEmptySlices(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manifest.json")
	if _, err := NewManifestStore(path, Manifest{
		RunID:       "run-empty",
		ToolVersion: "cann-ebpf/test",
		Profile:     "orinfer",
		TargetPIDs:  []int{99},
		StartedAt:   time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"libraries", "warnings", "segments"} {
		if string(raw[field]) != "[]" {
			t.Fatalf("%s = %s, want []", field, raw[field])
		}
	}
}

func TestManifestStoreRefusesToOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(path, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := NewManifestStore(path, Manifest{
		RunID:       "run-1",
		ToolVersion: "cann-ebpf/test",
		Profile:     "orinfer",
		TargetPIDs:  []int{1},
		StartedAt:   time.Now(),
	})
	if !errors.Is(err, ErrManifestExists) {
		t.Fatalf("error = %v, want ErrManifestExists", err)
	}
}

func TestManifestErrorStatusRequiresAndPreservesErrorText(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manifest.json")
	store, err := NewManifestStore(path, Manifest{
		RunID:       "run-1",
		ToolVersion: "cann-ebpf/test",
		Profile:     "orinfer",
		TargetPIDs:  []int{1},
		StartedAt:   time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	runErr := errors.New("upstream capture failed")
	if err := store.Finish(time.Now(), runErr); err != nil {
		t.Fatal(err)
	}
	manifest := readManifestAt(t, path)
	if manifest.Status != ManifestStatusError || manifest.Error != runErr.Error() {
		t.Fatalf("error manifest = %+v", manifest)
	}
}

func readManifestAt(t *testing.T, path string) Manifest {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	return manifest
}
