package device

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestContainerIDFromCgroup(t *testing.T) {
	cases := map[string]string{
		"12:devices:/docker/abc123\n11:blkio:/docker/abc123\n": "abc123",
		"1:name=systemd:/docker-abc123.scope\n":                "abc123",
		"0::/user.slice/session.scope":                         "",
	}
	for input, want := range cases {
		if got := containerIDFromCgroup(input); got != want {
			t.Fatalf("containerIDFromCgroup(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestPIDMapRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pidmap.json")
	in := &PIDMapArtifact{
		Schema:      PIDMapSchemaV1,
		ContainerID: "abc123",
		HostPIDs:    []int{100},
		PIDMap:      map[int]int{10: 100},
		TIDMap:      map[int]int{11: 101},
	}
	if err := WritePIDMap(path, in); err != nil {
		t.Fatal(err)
	}
	out, err := ReadPIDMap(path)
	if err != nil {
		t.Fatal(err)
	}
	if out.PIDMap[10] != 100 || out.TIDMap[11] != 101 || out.ContainerID != "abc123" {
		t.Fatalf("artifact = %+v", out)
	}
}

func TestCapturePIDMapsCurrentProcess(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("requires linux /proc")
	}
	artifact, err := CapturePIDMaps([]int{os.Getpid()})
	if err != nil {
		t.Fatal(err)
	}
	if len(artifact.PIDMap) == 0 {
		t.Fatalf("artifact = %+v", artifact)
	}
}
