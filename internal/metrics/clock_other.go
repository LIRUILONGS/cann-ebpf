//go:build !linux

package metrics

import "time"

var processStart = time.Now()

func systemNow() (int64, int64) {
	wall := time.Now().UnixNano()
	return int64(time.Since(processStart)), wall
}
