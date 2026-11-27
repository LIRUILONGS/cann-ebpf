//go:build linux

package metrics

import (
	"time"

	"golang.org/x/sys/unix"
)

func systemNow() (int64, int64) {
	var ts unix.Timespec
	wall := time.Now().UnixNano()
	if err := unix.ClockGettime(unix.CLOCK_MONOTONIC, &ts); err != nil {
		return wall, wall
	}
	return int64(ts.Sec)*int64(time.Second) + int64(ts.Nsec), wall
}
