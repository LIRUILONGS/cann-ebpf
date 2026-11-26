package probes

import (
	"os"
	"path/filepath"
	"testing"
)

func TestContextSocketOptionsReadsTargetCredentials(t *testing.T) {
	procRoot := t.TempDir()
	writeStatus := func(pid, uid, gid string) {
		t.Helper()
		dir := filepath.Join(procRoot, pid)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		body := "Name:\tvllm\nState:\tS (sleeping)\nUid:\t" + uid + "\t" + uid + "\t" + uid + "\t" + uid + "\nGid:\t" + gid + "\t" + gid + "\t" + gid + "\t" + gid + "\n"
		if err := os.WriteFile(filepath.Join(dir, "status"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeStatus("100", "0", "0")
	writeStatus("200", "982", "979")
	writeStatus("300", "982", "979")

	options, credentials := contextSocketOptions(procRoot, []int{100, 200, 300, 999})
	if len(credentials) != 3 {
		t.Fatalf("credentials = %+v", credentials)
	}
	if !options.Chown || options.OwnerUID != 982 || options.OwnerGID != 979 {
		t.Fatalf("owner options = %+v", options)
	}
	if len(options.AllowedUIDs) != 2 || options.AllowedUIDs[0] != 0 || options.AllowedUIDs[1] != 982 {
		t.Fatalf("allowed uids = %+v", options.AllowedUIDs)
	}
}
