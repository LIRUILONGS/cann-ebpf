package metrics

import (
	"context"
	"errors"
	"testing"
	"time"
)

const extUsagesFixture = `        NPU ID                         : 0
        Aicore Usage Rate(%)           : 29
        HBM Usage Rate(%)              : 93
        HBM Bandwidth Usage Rate(%)    : 15
`

const extCommonFixture = `        NPU ID                         : 0
        Aicore Freq(MHZ)               : 1800
        Aicore curFreq(MHZ)            : 1750
`

const extMemoryFixture = `        NPU ID                         : 0
        HBM Clock Speed(MHz)           : 1600
`

const extECCFixture = `        NPU ID                         : 0
        Single Bit Error Count         : 1
        Double Bit Error Count         : 2
        Aggregate Total Single-bit Error Count : 11
        Aggregate Total Double-bit Error Count : 22
        Aggregate Total Single-bit Isolated Pages Count : 3
        Aggregate Total Double-bit Isolated Pages Count : 4
        Single-bit Next Isolated Pages Count : 5
        Double-bit Next Isolated Pages Count : 6
`

const extECCEnableFixture = `        NPU ID                         : 0
        HBM ECC-Enable Status          : True
`

const extVoltFixture = `        NPU ID                         : 0
        Voltage(V)                     : 0.81
`

const extPCIeFixture = `        NPU ID                         : 0
        TX Error Count                 : 1
        RX Error Count                 : 2
        Lcrc Error Count               : 3
        Ecrc Error Count               : 4
        Retry Count                    : 5
`

const extSummaryFixture = `+------------------------------------------------------------------------------------------------+
| npu-smi 25.2.1                   Version: 25.2.1                                               |
+---------------------------+---------------+----------------------------------------------------+
| NPU   Name                | Health        | Power(W)    Temp(C)           Hugepages-Usage(page)|
| Chip                      | Bus-Id        | AICore(%)   Memory-Usage(MB)  HBM-Usage(MB)        |
+===========================+===============+====================================================+
| 0     910B2               | OK            | 130.4       42                0    / 0             |
| 0                         | 0000:C1:00.0  | 25          0    / 0          61404/ 65536         |
+===========================+===============+====================================================+
| 1     910B2               | OK            | 129.7       44                0    / 0             |
| 0                         | 0000:01:00.0  | 24          0    / 0          61405/ 65536         |
+===========================+===============+====================================================+
`

func extFixtureFor(subcommand string) string {
	switch subcommand {
	case "usages":
		return extUsagesFixture
	case "common":
		return extCommonFixture
	case "memory":
		return extMemoryFixture
	case "ecc":
		return extECCFixture
	case "ecc-enable":
		return extECCEnableFixture
	case "volt":
		return extVoltFixture
	case "pcie-err":
		return extPCIeFixture
	}
	return ""
}

func extRunner(t *testing.T) CommandRunner {
	return func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "npu-smi" {
			return nil, errors.New("unexpected command")
		}
		if len(args) == 1 && args[0] == "info" {
			return []byte(extSummaryFixture), nil
		}
		if len(args) == 5 && args[0] == "info" && args[1] == "-t" {
			fixture := extFixtureFor(args[2])
			if fixture == "" {
				return nil, errors.New("unexpected type " + args[2])
			}
			return []byte(fixture), nil
		}
		return nil, errors.New("unexpected args")
	}
}

func assertPointKind(t *testing.T, points []Point, name string, kind Kind) {
	t.Helper()
	for _, point := range points {
		if point.Name == name {
			if point.Kind != kind {
				t.Fatalf("%s kind = %s, want %s", name, point.Kind, kind)
			}
			return
		}
	}
	t.Fatalf("point %s not found in %v", name, pointNames(points))
}

func TestNPUSmiExtendedSourceCollects(t *testing.T) {
	source := NewNPUSmiExtendedSource("npu-smi", []int{0}, nil, time.Minute, time.Minute)
	source.Runner = extRunner(t)
	points, warnings, err := source.Collect(t.Context())
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v", warnings)
	}
	assertPointValue(t, points, "npu_aicore_clock_mhz", 1750)
	assertPointValue(t, points, "npu_aicore_clock_max_mhz", 1800)
	assertPointValue(t, points, "npu_hbm_clock_mhz", 1600)
	assertPointValue(t, points, "npu_hbm_bandwidth_util_ratio", 0.15)
	assertPointValue(t, points, "npu_voltage_volts", 0.81)
	assertPointValue(t, points, "npu_hbm_ecc_single_bit_errors", 1)
	assertPointValue(t, points, "npu_hbm_ecc_double_bit_errors", 2)
	assertPointValue(t, points, "npu_hbm_ecc_single_bit_total", 11)
	assertPointValue(t, points, "npu_hbm_ecc_double_bit_total", 22)
	assertPointValue(t, points, "npu_hbm_ecc_single_bit_isolated_pages", 3)
	assertPointValue(t, points, "npu_hbm_ecc_double_bit_next_isolated_pages", 6)
	assertPointValue(t, points, "npu_hbm_ecc_enabled", 1)
	assertPointValue(t, points, "npu_pcie_tx_errors_total", 1)
	assertPointValue(t, points, "npu_pcie_rx_errors_total", 2)
	assertPointValue(t, points, "npu_pcie_lcrc_errors_total", 3)
	assertPointValue(t, points, "npu_pcie_ecrc_errors_total", 4)
	assertPointValue(t, points, "npu_pcie_retry_total", 5)
	// Aggregate ECC totals and PCIe error counts are cumulative -> counter.
	assertPointKind(t, points, "npu_hbm_ecc_single_bit_total", KindCounter)
	assertPointKind(t, points, "npu_pcie_tx_errors_total", KindCounter)
	assertPointKind(t, points, "npu_voltage_volts", KindGauge)
	for _, point := range points {
		if point.Source != "npu-smi" || point.Device != "NPU-0" {
			t.Fatalf("identity: %+v", point)
		}
	}
}

func TestNPUSmiExtendedSourceDiscoversDevices(t *testing.T) {
	source := NewNPUSmiExtendedSource("npu-smi", nil, []string{"volt"}, time.Minute, time.Minute)
	var sawDiscovery bool
	base := extRunner(t)
	source.Runner = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if len(args) == 1 && args[0] == "info" {
			sawDiscovery = true
		}
		return base(ctx, name, args...)
	}
	points, _, err := source.Collect(t.Context())
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if !sawDiscovery {
		t.Fatal("expected device discovery via `npu-smi info`")
	}
	devices := make(map[string]bool)
	for _, point := range points {
		devices[point.Device] = true
	}
	if !devices["NPU-0"] || !devices["NPU-1"] {
		t.Fatalf("devices = %v", devices)
	}
}

func TestNPUSmiExtendedSourcePartialFailureIsWarning(t *testing.T) {
	source := NewNPUSmiExtendedSource("npu-smi", []int{0}, []string{"volt", "ecc"}, time.Minute, time.Minute)
	source.Runner = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if len(args) == 5 && args[2] == "ecc" {
			return nil, errors.New("device does not support querying ecc")
		}
		if len(args) == 5 && args[2] == "volt" {
			return []byte(extVoltFixture), nil
		}
		return nil, errors.New("unexpected args")
	}
	points, warnings, err := source.Collect(t.Context())
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(warnings) != 1 {
		t.Fatalf("warnings = %v, want 1", warnings)
	}
	assertPointValue(t, points, "npu_voltage_volts", 0.81)
}

func TestNPUSmiExtendedSourceNoData(t *testing.T) {
	source := NewNPUSmiExtendedSource("npu-smi", []int{0}, nil, time.Minute, time.Minute)
	source.Runner = func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return []byte("This device does not support querying\n"), nil
	}
	if _, _, err := source.Collect(t.Context()); err == nil {
		t.Fatal("expected error when no metrics parse")
	}
}
