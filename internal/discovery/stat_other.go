//go:build !linux

package discovery

import "os"

func inodeOf(info os.FileInfo) uint64 { return 0 }
