package probes

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestImportDeviceEvidenceCopiesAndSeals(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "collector", "device.jsonl")
	if err := os.MkdirAll(filepath.Dir(source), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte(`{"schema":"cann.device.v1","kind":"KERNEL","start_ns":1,"end_ns":2}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source+".meta.json", []byte(`{"schema":"cann.mspti.collector.v1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "run")
	if err := os.MkdirAll(out, 0o700); err != nil {
		t.Fatal(err)
	}
	evidence := importDeviceEvidence(out, source, []int{os.Getpid()})
	if !evidence.Imported {
		t.Fatalf("not imported: %+v", evidence)
	}
	if evidence.DevicePath != filepath.Join(out, "device.jsonl") {
		t.Fatalf("device path = %q", evidence.DevicePath)
	}
	if evidence.MetaPath != filepath.Join(out, "device.jsonl.meta.json") {
		t.Fatalf("meta path = %q", evidence.MetaPath)
	}
	data, err := os.ReadFile(evidence.DevicePath)
	if err != nil || !strings.Contains(string(data), "cann.device.v1") {
		t.Fatalf("copied device = %q err=%v", data, err)
	}
	// On a non-Linux host the pidmap capture is expected to warn; it must never
	// fail the import.
	if evidence.PIDMapPath == "" && len(evidence.Warnings) == 0 {
		t.Fatal("missing pidmap should carry a warning")
	}

	second := importDeviceEvidence(out, source, []int{os.Getpid()})
	if second.Imported {
		t.Fatal("second import must not overwrite existing evidence")
	}
	if !containsWarning(second.Warnings, "refusing to overwrite") {
		t.Fatalf("warnings = %v", second.Warnings)
	}
}

func TestImportDeviceEvidenceMissingSource(t *testing.T) {
	out := t.TempDir()
	evidence := importDeviceEvidence(out, filepath.Join(out, "missing.jsonl"), nil)
	if evidence.Imported || len(evidence.Warnings) == 0 {
		t.Fatalf("evidence = %+v", evidence)
	}
}

func TestImportDeviceEvidenceNoOutputDir(t *testing.T) {
	evidence := importDeviceEvidence("", "x.jsonl", nil)
	if evidence.Imported || len(evidence.Warnings) == 0 {
		t.Fatalf("evidence = %+v", evidence)
	}
}

func containsWarning(warnings []string, substr string) bool {
	for _, warning := range warnings {
		if strings.Contains(warning, substr) {
			return true
		}
	}
	return false
}
