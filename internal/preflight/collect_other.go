//go:build !linux

package preflight

import (
	"os"
	"runtime"
)

// Collect on non-Linux hosts reports only what is knowable; Evaluate will mark
// the missing BPF prerequisites as failures/warnings instead of pretending the
// platform is ready.
func Collect() Environment {
	host, _ := os.Hostname()
	return Environment{
		Hostname: host, Kernel: runtime.GOOS, Arch: runtime.GOARCH,
		TracepointIDs: map[string]string{},
	}
}
