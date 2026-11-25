package discovery

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cann-ebpf/internal/config"
)

func TestParseStat(t *testing.T) {
	comm, ppid, start, err := parseStat("123 (a b) S 42 1 1 0 -1 4194560 0 0 0 0 0 0 0 0 20 0 1 0 999 0")
	if err != nil {
		t.Fatalf("parseStat: %v", err)
	}
	if comm != "a b" || ppid != 42 || start != 999 {
		t.Fatalf("parsed comm=%q ppid=%d start=%d", comm, ppid, start)
	}
}

func TestParseStatMalformed(t *testing.T) {
	if _, _, _, err := parseStat("not a stat line"); err == nil {
		t.Fatal("expected malformed stat error")
	}
}

func TestReadMaps(t *testing.T) {
	path := filepath.Join(t.TempDir(), "maps")
	body := "00400000-00452000 r-xp 00000000 08:02 173521 /usr/bin/worker\n" +
		"7f000000-7f001000 rw-s 00001000 00:00 0 /dev/shm/a file with spaces\n" +
		"7f002000-7f003000 rw-p 00000000 00:00 0 [anonymous]\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	maps := readMaps(path)
	if len(maps) != 2 {
		t.Fatalf("maps = %d, want 2: %+v", len(maps), maps)
	}
	if maps[1].Path != "/dev/shm/a file with spaces" {
		t.Fatalf("path with spaces = %q", maps[1].Path)
	}
	if maps[1].Offset != 0x1000 || maps[1].Inode != 0 {
		t.Fatalf("map fields = %+v", maps[1])
	}
}

func TestParseNetTCP(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tcp")
	body := "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n" +
		"   0: 0100007F:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 12345 1 0000000000000000 100 0 0 10 0\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got := parseNetTCP(path)
	if got[12345] != 8080 {
		t.Fatalf("listen ports = %#v, want inode 12345 -> 8080", got)
	}
}

func TestMatchExprAndEvaluateMatch(t *testing.T) {
	p := &Process{
		Comm:           "vllm",
		Cmdline:        "vllm serve /model",
		ContainerName:  "vllm",
		ContainerImage: "ds4/vllm-ascend:v0.23.0rc1",
		Maps:           []MapEntry{{Path: "/usr/local/Ascend/lib64/libascendcl.so"}},
		listens:        map[int]bool{8905: true},
	}
	if !matchExpr(config.MatchExpr{Comm: "vllm", CmdlineContains: "serve", MapsContains: "libascendcl.so", ContainerName: "/vllm", ContainerImageContains: "vllm-ascend", ListenPort: 8905}, p) {
		t.Fatal("expected match expression to match")
	}
	if matchExpr(config.MatchExpr{Comm: "orinfer"}, p) {
		t.Fatal("unexpected comm match")
	}
	if matchExpr(config.MatchExpr{}, p) {
		t.Fatal("empty expression must not match")
	}

	m := &config.Match{
		Any:     []config.MatchExpr{{Comm: "vllm"}, {Comm: "orinfer"}},
		All:     []config.MatchExpr{{MapsContains: "libascendcl.so"}},
		Exclude: []config.MatchExpr{{CmdlineContains: "start-orinfer.sh"}},
	}
	got := EvaluateMatch(m, p)
	if !got.Matched || got.Excluded || len(got.MatchedRules) != 2 {
		t.Fatalf("match result = %+v", got)
	}
	excluded := EvaluateMatch(&config.Match{Any: []config.MatchExpr{{Comm: "vllm"}}, Exclude: []config.MatchExpr{{Comm: "vllm"}}}, p)
	if !excluded.Excluded || excluded.Matched {
		t.Fatalf("exclude result = %+v", excluded)
	}
}

func TestSelectBestPriorityAndTie(t *testing.T) {
	low := &config.Document{Metadata: config.Metadata{Name: "low"}, Spec: config.Spec{Match: &config.Match{Priority: 1}}}
	high := &config.Document{Metadata: config.Metadata{Name: "high"}, Spec: config.Spec{Match: &config.Match{Priority: 9}}}
	otherHigh := &config.Document{Metadata: config.Metadata{Name: "other-high"}, Spec: config.Spec{Match: &config.Match{Priority: 9}}}
	matched := MatchResult{Matched: true}
	best, err := SelectBest([]Candidate{
		{Profile: low, Path: "low.yaml", Match: matched},
		{Profile: high, Path: "high.yaml", Match: matched},
	})
	if err != nil || best.Profile.Metadata.Name != "high" {
		t.Fatalf("best = %+v, err=%v", best, err)
	}
	if _, err := SelectBest([]Candidate{
		{Profile: high, Path: "high.yaml", Match: matched},
		{Profile: otherHigh, Path: "other.yaml", Match: matched},
	}); err == nil || !strings.Contains(err.Error(), "tie") {
		t.Fatalf("expected tie error, got %v", err)
	}
}

func TestFindLibrariesInProcessRoot(t *testing.T) {
	root := t.TempDir()
	rel := "usr/local/Ascend/cann-9.2.0/aarch64-linux/lib64/libascendcl.so"
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not-an-elf"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := &Process{
		PID:  4242,
		Root: root,
		Maps: []MapEntry{{Path: "/" + rel}},
	}
	libs := FindLibraries(p, nil)
	if len(libs) != 1 {
		t.Fatalf("libraries = %+v", libs)
	}
	if libs[0].Note == "" || libs[0].Size == 0 {
		t.Fatalf("library not resolved in process root: %+v", libs[0])
	}
}

func TestGlobLibraryRecursive(t *testing.T) {
	root := t.TempDir()
	rel := "usr/local/Ascend/cann-9.2.0/aarch64-linux/lib64/libascendcl.so"
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := globLibrary(root, "/usr/local/Ascend/**/lib64/libascendcl.so")
	if len(got) != 1 || !strings.HasSuffix(filepath.ToSlash(got[0]), "/lib64/libascendcl.so") {
		t.Fatalf("glob result = %#v", got)
	}
}
