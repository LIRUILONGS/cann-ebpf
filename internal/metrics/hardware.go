package metrics

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"cann-ebpf/internal/hardware"
)

// CommandRunner lets tests replace an external command.
type CommandRunner func(ctx context.Context, name string, args ...string) ([]byte, error)

func execRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, name, args...)
	return command.Output()
}

// ResolveDCMIProbe returns the configured dcmi_probe helper.
func ResolveDCMIProbe() (string, bool) {
	if probe := strings.TrimSpace(os.Getenv("CANN_DCMI_PROBE")); probe != "" {
		return probe, true
	}
	if probe, err := exec.LookPath("dcmi_probe"); err == nil {
		return probe, true
	}
	return "", false
}

// DCMISource samples the read-only dcmi_probe helper.
type DCMISource struct {
	Probe    string
	Every    time.Duration
	Deadline time.Duration
	Runner   CommandRunner
}

// NewDCMISource creates a DCMI source. The caller normally resolves the probe
// with ResolveDCMIProbe so a missing helper is reported as an exit-code-4
// environment failure rather than silently falling back.
func NewDCMISource(probe string, interval, timeout time.Duration) *DCMISource {
	if interval <= 0 {
		interval = time.Second
	}
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	return &DCMISource{Probe: probe, Every: interval, Deadline: timeout, Runner: execRunner}
}

func (s *DCMISource) Name() string            { return "dcmi" }
func (s *DCMISource) Interval() time.Duration { return s.Every }
func (s *DCMISource) Timeout() time.Duration  { return s.Deadline }

func (s *DCMISource) Collect(ctx context.Context) ([]Point, []string, error) {
	runner := s.Runner
	if runner == nil {
		runner = execRunner
	}
	raw, err := runner(ctx, s.Probe)
	if err != nil {
		return nil, nil, fmt.Errorf("dcmi_probe: %w", err)
	}
	samples, err := hardware.ParseDCMIJSONL(raw)
	if err != nil {
		return nil, nil, fmt.Errorf("dcmi_probe parse: %w", err)
	}
	return hardwareSamplesToPoints("dcmi", samples), nil, nil
}

// NPUSmiSource samples `npu-smi info`. It defaults to a 5s interval because the
// command forks and is much more expensive than DCMI.
type NPUSmiSource struct {
	Command  string
	Every    time.Duration
	Deadline time.Duration
	Runner   CommandRunner
}

// NewNPUSmiSource creates an npu-smi source. command may be empty to use PATH.
func NewNPUSmiSource(command string, interval, timeout time.Duration) *NPUSmiSource {
	if command == "" {
		command = "npu-smi"
	}
	if interval <= 0 || interval < 5*time.Second {
		interval = 5 * time.Second
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return &NPUSmiSource{Command: command, Every: interval, Deadline: timeout, Runner: execRunner}
}

func (s *NPUSmiSource) Name() string            { return "npu-smi" }
func (s *NPUSmiSource) Interval() time.Duration { return s.Every }
func (s *NPUSmiSource) Timeout() time.Duration  { return s.Deadline }

func (s *NPUSmiSource) Collect(ctx context.Context) ([]Point, []string, error) {
	runner := s.Runner
	if runner == nil {
		runner = execRunner
	}
	raw, err := runner(ctx, s.Command, "info")
	if err != nil {
		return nil, nil, fmt.Errorf("npu-smi info: %w", err)
	}
	samples, err := hardware.ParseNPUSmiInfo(raw)
	if err != nil {
		return nil, nil, fmt.Errorf("npu-smi parse: %w", err)
	}
	return hardwareSamplesToPoints("npu-smi", samples), nil, nil
}

func hardwareSamplesToPoints(source string, samples []hardware.Sample) []Point {
	var points []Point
	for _, sample := range samples {
		device := fmt.Sprintf("NPU-%d", sample.DeviceID)
		baseLabels := map[string]string{}
		if sample.Name != "" {
			baseLabels["name"] = sample.Name
		}
		addGauge := func(name, unit string, value *float64, labels map[string]string) {
			if value == nil {
				return
			}
			point := Gauge(name, unit, *value, labels)
			point.Source = source
			point.Device = device
			points = append(points, point)
		}
		addCounter := func(name string, value *int64, labels map[string]string) {
			if value == nil {
				return
			}
			point := Counter(name, "count", float64(*value), labels)
			point.Source = source
			point.Device = device
			points = append(points, point)
		}
		addIntGauge := func(name string, value *int64, labels map[string]string) {
			if value == nil {
				return
			}
			addGauge(name, "count", Float(float64(*value)), labels)
		}
		if sample.AICorePct != nil {
			ratio := *sample.AICorePct / 100
			addGauge("npu_aicore_utilization_ratio", "ratio", &ratio, baseLabels)
		}
		if sample.HBMUsedMB != nil {
			bytes := float64(*sample.HBMUsedMB) * 1024 * 1024
			addGauge("npu_hbm_used_bytes", "bytes", &bytes, baseLabels)
		}
		if sample.HBMTotalMB != nil {
			bytes := float64(*sample.HBMTotalMB) * 1024 * 1024
			addGauge("npu_hbm_total_bytes", "bytes", &bytes, baseLabels)
		}
		if sample.HBMFreeMB != nil {
			bytes := float64(*sample.HBMFreeMB) * 1024 * 1024
			addGauge("npu_hbm_free_bytes", "bytes", &bytes, baseLabels)
		}
		if sample.HBMUsedMB != nil && sample.HBMTotalMB != nil && *sample.HBMTotalMB > 0 {
			ratio := float64(*sample.HBMUsedMB) / float64(*sample.HBMTotalMB)
			addGauge("npu_hbm_used_ratio", "ratio", &ratio, baseLabels)
		}
		if sample.MemoryUsedMB != nil {
			bytes := float64(*sample.MemoryUsedMB) * 1024 * 1024
			addGauge("npu_memory_used_bytes", "bytes", &bytes, baseLabels)
		}
		addGauge("npu_power_watts", "watts", sample.PowerW, baseLabels)
		addGauge("npu_temperature_celsius", "celsius", sample.TempC, baseLabels)
		if sample.Health != "" {
			labels := cloneLabels(baseLabels)
			labels["status"] = sample.Health
			value := 1.0
			addGauge("npu_health", "count", &value, labels)
		}

		// --- P0 extended NPU metrics (frequency / ECC / voltage / PCIe) ---
		// Units stay aligned with the existing catalog: ratios are 0..1,
		// clocks are MHz, voltage is volts, counters use "count".
		addGauge("npu_aicore_clock_mhz", "mhz", sample.AICoreClockMHz, baseLabels)
		addGauge("npu_aicore_clock_max_mhz", "mhz", sample.AICoreClockMaxMHz, baseLabels)
		addGauge("npu_hbm_clock_mhz", "mhz", sample.HBMClockMHz, baseLabels)
		if sample.HBMBandwidthUtilPct != nil {
			ratio := *sample.HBMBandwidthUtilPct / 100
			addGauge("npu_hbm_bandwidth_util_ratio", "ratio", &ratio, baseLabels)
		}
		addGauge("npu_voltage_volts", "volts", sample.VoltageV, baseLabels)

		addIntGauge("npu_hbm_ecc_single_bit_errors", sample.ECCSingleBitErrors, baseLabels)
		addIntGauge("npu_hbm_ecc_double_bit_errors", sample.ECCDoubleBitErrors, baseLabels)
		addCounter("npu_hbm_ecc_single_bit_total", sample.ECCSingleBitTotal, baseLabels)
		addCounter("npu_hbm_ecc_double_bit_total", sample.ECCDoubleBitTotal, baseLabels)
		addIntGauge("npu_hbm_ecc_single_bit_isolated_pages", sample.ECCSingleBitIsolatedPages, baseLabels)
		addIntGauge("npu_hbm_ecc_double_bit_isolated_pages", sample.ECCDoubleBitIsolatedPages, baseLabels)
		addIntGauge("npu_hbm_ecc_single_bit_next_isolated_pages", sample.ECCSingleBitNextIsolatedPages, baseLabels)
		addIntGauge("npu_hbm_ecc_double_bit_next_isolated_pages", sample.ECCDoubleBitNextIsolatedPages, baseLabels)
		if sample.ECCEnabled != nil {
			enabled := 0.0
			if *sample.ECCEnabled {
				enabled = 1
			}
			addGauge("npu_hbm_ecc_enabled", "count", &enabled, baseLabels)
		}

		addCounter("npu_pcie_tx_errors_total", sample.PCIeTXErrors, baseLabels)
		addCounter("npu_pcie_rx_errors_total", sample.PCIeRXErrors, baseLabels)
		addCounter("npu_pcie_lcrc_errors_total", sample.PCIeLCRCErrors, baseLabels)
		addCounter("npu_pcie_ecrc_errors_total", sample.PCIeECRCErrors, baseLabels)
		addCounter("npu_pcie_retry_total", sample.PCIeRetry, baseLabels)
	}
	return points
}
