//go:build !linux

package metrics

import (
	"context"
	"fmt"
	"runtime"
	"time"
)

// DriverEBPFSource is a non-Linux stub. It preserves the metrics source
// contract and reports every driver metric as unavailable instead of silently
// omitting the data.
type DriverEBPFSource struct {
	PIDs     []int
	ProcRoot string
	Every    time.Duration
	Deadline time.Duration
}

func NewDriverEBPFSource(pids []int, interval, timeout time.Duration) *DriverEBPFSource {
	if interval <= 0 {
		interval = time.Second
	}
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	return &DriverEBPFSource{PIDs: append([]int(nil), pids...), ProcRoot: "/proc", Every: interval, Deadline: timeout}
}

func (s *DriverEBPFSource) Name() string            { return driverSourceName }
func (s *DriverEBPFSource) Interval() time.Duration { return s.Every }
func (s *DriverEBPFSource) Timeout() time.Duration  { return s.Deadline }
func (s *DriverEBPFSource) Close() error            { return nil }

func (s *DriverEBPFSource) Collect(ctx context.Context) ([]Point, []string, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	reason := fmt.Sprintf("driver-ebpf requires linux/arm64 (running %s/%s)", runtime.GOOS, runtime.GOARCH)
	info := Gauge("npu_driver_source_info", "count", 1, map[string]string{
		"status": "unavailable", "reason": reason, "arch": runtime.GOARCH,
		"pid": "all", "tid": "all", "cgroup": "all", "device": "host", "confidence": "probe",
	})
	info.Source = driverSourceName
	info.Device = "host"
	points := []Point{info}
	for _, name := range driverRequestedMetrics {
		point := Gauge("npu_driver_metric_status", "count", 1, map[string]string{
			"metric": name, "status": "unavailable", "reason": reason,
			"pid": "all", "tid": "all", "cgroup": "all", "device": "host", "confidence": "probe",
		})
		point.Source = driverSourceName
		point.Device = "host"
		points = append(points, point)
	}
	return points, []string{"driver-ebpf: " + reason}, nil
}

// DriverProbeSymbols is empty on non-Linux hosts.
func DriverProbeSymbols() []string { return nil }
