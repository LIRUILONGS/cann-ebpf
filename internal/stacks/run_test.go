package stacks

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testRunProfile() *Profile {
	return &Profile{
		Stacks: []ResolvedStack{{
			Frames: []Frame{{Name: "schedule_root"}, {Name: "futex_leaf"}},
			Count:  5,
		}},
		Total: 5,
	}
}

func TestWriteAndLoadRun(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "run")
	meta := Meta{Host: "host-a", Kernel: "6.6-test", FrequencyHz: 77}
	samples := []Sample{{PID: 12, TID: 13, Count: 5, Addrs: []uint64{0x1000, 0x1200}}}
	profile := testRunProfile()

	if err := WriteRun(dir, meta, samples, profile, true); err != nil {
		t.Fatalf("WriteRun: %v", err)
	}

	for _, name := range []string{FoldedFileName, MetaFileName, RawFileName} {
		path := filepath.Join(dir, name)
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		if got := info.Mode().Perm(); got != 0o644 {
			t.Fatalf("%s mode = %o, want 644", name, got)
		}
	}

	metaData, err := os.ReadFile(filepath.Join(dir, MetaFileName))
	if err != nil {
		t.Fatalf("read meta: %v", err)
	}
	wantMeta := meta
	wantMeta.Schema = SchemaV1
	wantMetaBytes, err := json.MarshalIndent(wantMeta, "", "  ")
	if err != nil {
		t.Fatalf("marshal expected meta: %v", err)
	}
	wantMetaBytes = append(wantMetaBytes, '\n')
	if !bytes.Equal(metaData, wantMetaBytes) {
		t.Fatalf("meta bytes = %q, want %q", metaData, wantMetaBytes)
	}

	foldedData, err := os.ReadFile(filepath.Join(dir, FoldedFileName))
	if err != nil {
		t.Fatalf("read folded: %v", err)
	}
	if got, want := string(foldedData), "schedule_root;futex_leaf 5\n"; got != want {
		t.Fatalf("folded = %q, want %q", got, want)
	}

	rawFile, err := os.Open(filepath.Join(dir, RawFileName))
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	defer rawFile.Close()
	scanner := bufio.NewScanner(rawFile)
	rawSamples := make([]Sample, 0)
	for scanner.Scan() {
		var sample Sample
		if err := json.Unmarshal(scanner.Bytes(), &sample); err != nil {
			t.Fatalf("decode raw sample: %v", err)
		}
		rawSamples = append(rawSamples, sample)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan raw: %v", err)
	}
	if len(rawSamples) != 1 || rawSamples[0].PID != 12 || rawSamples[0].Count != 5 {
		t.Fatalf("raw samples = %+v", rawSamples)
	}

	run, err := LoadRun(dir)
	if err != nil {
		t.Fatalf("LoadRun dir: %v", err)
	}
	if run.Profile == nil || run.Profile.Total != 5 {
		t.Fatalf("loaded run = %+v", run)
	}
	if got := frameNames(run.Profile.Stacks[0].Frames); !equalStrings(got, []string{"schedule_root", "futex_leaf"}) {
		t.Fatalf("loaded frames = %v", got)
	}
	if run.Meta.Host != "host-a" {
		t.Fatalf("loaded host = %q", run.Meta.Host)
	}
	if run.Dir != filepath.Clean(dir) {
		t.Fatalf("run dir = %q, want %q", run.Dir, filepath.Clean(dir))
	}

	fromFolded, err := LoadRun(filepath.Join(dir, FoldedFileName))
	if err != nil {
		t.Fatalf("LoadRun folded path: %v", err)
	}
	if fromFolded.Profile == nil || fromFolded.Profile.Total != 5 {
		t.Fatalf("loaded folded profile = %+v", fromFolded.Profile)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read run dir: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			t.Fatalf("temporary file left behind: %s", entry.Name())
		}
	}

	originalFolded := append([]byte(nil), foldedData...)
	if err := WriteRun(dir, meta, samples, profile, true); err == nil {
		t.Fatalf("second WriteRun unexpectedly succeeded")
	}
	afterSecond, err := os.ReadFile(filepath.Join(dir, FoldedFileName))
	if err != nil {
		t.Fatalf("read folded after refusal: %v", err)
	}
	if !bytes.Equal(afterSecond, originalFolded) {
		t.Fatalf("original folded evidence changed")
	}
}

func TestLoadRunMissingFoldedReturnsNilProfile(t *testing.T) {
	dir := t.TempDir()
	if err := WriteRun(dir, Meta{Host: "host-b"}, nil, testRunProfile(), false); err != nil {
		t.Fatalf("WriteRun: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, RawFileName)); !os.IsNotExist(err) {
		t.Fatalf("raw file should not exist when raw=false: %v", err)
	}
	if err := os.Remove(filepath.Join(dir, FoldedFileName)); err != nil {
		t.Fatalf("remove folded: %v", err)
	}

	run, err := LoadRun(dir)
	if err != nil {
		t.Fatalf("LoadRun: %v", err)
	}
	if run.Profile != nil {
		t.Fatalf("profile = %+v, want nil", run.Profile)
	}
	if run.Meta.Host != "host-b" {
		t.Fatalf("meta host = %q", run.Meta.Host)
	}
}

func TestLoadRunRejectsMissingOrBadMeta(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		if _, err := LoadRun(t.TempDir()); err == nil {
			t.Fatalf("expected missing meta error")
		}
	})

	t.Run("corrupt", func(t *testing.T) {
		dir := t.TempDir()
		mustWriteTestFile(t, dir, MetaFileName, "{")
		if _, err := LoadRun(dir); err == nil {
			t.Fatalf("expected corrupt meta error")
		}
	})

	t.Run("no schema", func(t *testing.T) {
		dir := t.TempDir()
		mustWriteTestFile(t, dir, MetaFileName, "{}\n")
		if _, err := LoadRun(dir); err == nil {
			t.Fatalf("expected unsupported schema error")
		}
	})
}

func TestWriteRunRefusesAnyExistingEvidenceFile(t *testing.T) {
	for _, existing := range []string{FoldedFileName, MetaFileName, RawFileName} {
		t.Run(existing, func(t *testing.T) {
			dir := t.TempDir()
			marker := "do-not-overwrite\n"
			mustWriteTestFile(t, dir, existing, marker)
			if err := WriteRun(dir, Meta{}, nil, testRunProfile(), false); err == nil {
				t.Fatalf("expected refusal for existing %s", existing)
			}
			data, err := os.ReadFile(filepath.Join(dir, existing))
			if err != nil {
				t.Fatalf("read existing %s: %v", existing, err)
			}
			if string(data) != marker {
				t.Fatalf("existing %s was modified", existing)
			}
		})
	}
}

func TestWriteRunCleansTemporaryFilesOnError(t *testing.T) {
	dir := t.TempDir()
	badProfile := &Profile{Stacks: []ResolvedStack{{Count: 1}}}
	if err := WriteRun(dir, Meta{}, nil, badProfile, true); err == nil {
		t.Fatalf("expected folded validation error")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("temporary or partial files left behind: %v", entries)
	}
}
