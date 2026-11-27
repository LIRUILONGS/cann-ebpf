package metrics

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"cann-ebpf/internal/hardware"
)

// NPUSmiHCCSSource samples passive HCCS lane counters from
// npu-smi info -t hccs -i N -c 0. It does not use the active hccs-bw
// profiling command and never writes to the device.
//
// Each card costs one npu-smi process; the default interval is 30s. A 10s
// interval is allowed only when explicitly selected for diagnosis.
type NPUSmiHCCSSource struct {
	Command  string
	Devices  []int
	Every    time.Duration
	Deadline time.Duration
	Runner   CommandRunner
}

func NewNPUSmiHCCSSource(command string, devices []int, interval, timeout time.Duration) *NPUSmiHCCSSource {
	if command == "" {
		command = "npu-smi"
	}
	if interval <= 0 || interval < 10*time.Second {
		interval = 30 * time.Second
	}
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	return &NPUSmiHCCSSource{
		Command: command, Devices: append([]int(nil), devices...),
		Every: interval, Deadline: timeout, Runner: execRunner,
	}
}

func (s *NPUSmiHCCSSource) Name() string            { return "npu-smi-hccs" }
func (s *NPUSmiHCCSSource) Interval() time.Duration { return s.Every }
func (s *NPUSmiHCCSSource) Timeout() time.Duration  { return s.Deadline }

func (s *NPUSmiHCCSSource) Collect(ctx context.Context) ([]Point, []string, error) {
	runner := s.Runner
	if runner == nil {
		runner = execRunner
	}
	devices := append([]int(nil), s.Devices...)
	if len(devices) == 0 {
		raw, err := runner(ctx, s.Command, "info")
		if err != nil {
			return nil, nil, fmt.Errorf("npu-smi info: %w", err)
		}
		summary, err := hardware.ParseNPUSmiInfo(raw)
		if err != nil {
			return nil, nil, fmt.Errorf("npu-smi info parse: %w", err)
		}
		for _, sample := range summary {
			devices = append(devices, sample.DeviceID)
		}
	}

	var warnings []string
	var samples []hardware.Sample
	for _, device := range devices {
		raw, err := runner(ctx, s.Command, "info", "-t", "hccs", "-i", strconv.Itoa(device), "-c", "0")
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("npu-smi -t hccs -i %d -c 0: %v", device, err))
			continue
		}
		sample, err := hardware.ParseNPUSmiHCCS(raw)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("npu-smi -t hccs -i %d parse: %v", device, err))
			continue
		}
		sample.DeviceID = device
		samples = append(samples, sample)
	}
	if len(samples) == 0 {
		return nil, warnings, fmt.Errorf("npu-smi hccs: no device metrics parsed")
	}
	return hardwareSamplesToPoints("npu-smi", samples), warnings, nil
}
