package hardware

import (
	"bytes"
	"encoding/json"
	"testing"
)

// Fixed text fixtures modelled on `npu-smi info -t <type> -i 0` output from
// npu-smi 25.2.1 on a 910B2. They are fixed text so the parsers never depend on
// a real device or host.

const npuSmiUsagesFixture = `        NPU ID                         : 0
        Chip ID                        : 0
        Chip Name                      : 910B2
        Aicore Usage Rate(%)           : 29
        Aicore Frequency(MHZ)          : 1800
        DDR Usage Rate(%)              : 0
        DDR Bandwidth Usage Rate(%)    : 0
        HBM Usage Rate(%)              : 93
        HBM Bandwidth Usage Rate(%)    : 15
`

const npuSmiCommonFixture = `        NPU ID                         : 0
        Chip ID                        : 0
        Chip Name                      : 910B2
        Aicore Count                   : 24
        Aicore Freq(MHZ)               : 1800
        Aicore curFreq(MHZ)            : 1750
        Temperature(C)                 : 42
        Power(W)                       : 133.3
`

const npuSmiMemoryFixture = `        NPU ID                         : 0
        Chip ID                        : 0
        DDR Capacity(MB)               : 0
        DDR Clock Speed(MHz)           : 0
        HBM Capacity(MB)               : 65536
        HBM Clock Speed(MHz)           : 1600
        HBM Temperature(C)             : 42
`

// Distinct non-zero values so a wrong field mapping cannot pass by accident.
const npuSmiECCFixture = `        NPU ID                         : 0
        Chip ID                        : 0
        Device Type                    : HBM
        Single Bit Error Count         : 1
        Double Bit Error Count         : 2
        Aggregate Total Single-bit Error Count : 11
        Aggregate Total Double-bit Error Count : 22
        Aggregate Total Single-bit Isolated Pages Count : 3
        Aggregate Total Double-bit Isolated Pages Count : 4
        Single-bit Next Isolated Pages Count : 5
        Double-bit Next Isolated Pages Count : 6
`

const npuSmiECCEnableFixture = `        NPU ID                         : 0
        Chip ID                        : 0
        HBM ECC-Enable Status          : True
`

const npuSmiVoltFixture = `        NPU ID                         : 0
        Chip ID                        : 0
        Voltage(V)                     : 0.81
`

const npuSmiPCIeErrFixture = `        NPU ID                         : 0
        Chip ID                        : 0
        TX Error Count                 : 1
        RX Error Count                 : 2
        Lcrc Error Count               : 3
        Ecrc Error Count               : 4
        Retry Count                    : 5
`

func mustFloat(t *testing.T, ptr *float64, want float64) {
	t.Helper()
	if ptr == nil {
		t.Fatalf("value is nil, want %v", want)
	}
	if *ptr != want {
		t.Fatalf("value = %v, want %v", *ptr, want)
	}
}

func mustInt(t *testing.T, ptr *int64, want int64) {
	t.Helper()
	if ptr == nil {
		t.Fatalf("value is nil, want %v", want)
	}
	if *ptr != want {
		t.Fatalf("value = %v, want %v", *ptr, want)
	}
}

func TestParseNPUSmiUsages(t *testing.T) {
	sample, err := ParseNPUSmiUsages([]byte(npuSmiUsagesFixture))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if sample.Schema != SampleSchemaV1 || sample.Source != "npu-smi" || sample.DeviceID != 0 {
		t.Fatalf("identity = %+v", sample)
	}
	mustFloat(t, sample.HBMBandwidthUtilPct, 15)
	mustFloat(t, sample.AICorePct, 29)
}

func TestParseNPUSmiCommon(t *testing.T) {
	sample, err := ParseNPUSmiCommon([]byte(npuSmiCommonFixture))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	mustFloat(t, sample.AICoreClockMaxMHz, 1800)
	mustFloat(t, sample.AICoreClockMHz, 1750)
}

func TestParseNPUSmiMemory(t *testing.T) {
	sample, err := ParseNPUSmiMemory([]byte(npuSmiMemoryFixture))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	mustFloat(t, sample.HBMClockMHz, 1600)
}

func TestParseNPUSmiECC(t *testing.T) {
	sample, err := ParseNPUSmiECC([]byte(npuSmiECCFixture))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	mustInt(t, sample.ECCSingleBitErrors, 1)
	mustInt(t, sample.ECCDoubleBitErrors, 2)
	mustInt(t, sample.ECCSingleBitTotal, 11)
	mustInt(t, sample.ECCDoubleBitTotal, 22)
	mustInt(t, sample.ECCSingleBitIsolatedPages, 3)
	mustInt(t, sample.ECCDoubleBitIsolatedPages, 4)
	mustInt(t, sample.ECCSingleBitNextIsolatedPages, 5)
	mustInt(t, sample.ECCDoubleBitNextIsolatedPages, 6)
}

func TestParseNPUSmiECCEnable(t *testing.T) {
	sample, err := ParseNPUSmiECCEnable([]byte(npuSmiECCEnableFixture))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if sample.ECCEnabled == nil || !*sample.ECCEnabled {
		t.Fatalf("ecc enabled = %v", sample.ECCEnabled)
	}
}

func TestParseNPUSmiVolt(t *testing.T) {
	sample, err := ParseNPUSmiVolt([]byte(npuSmiVoltFixture))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	mustFloat(t, sample.VoltageV, 0.81)
}

func TestParseNPUSmiPCIeErr(t *testing.T) {
	sample, err := ParseNPUSmiPCIeErr([]byte(npuSmiPCIeErrFixture))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	mustInt(t, sample.PCIeTXErrors, 1)
	mustInt(t, sample.PCIeRXErrors, 2)
	mustInt(t, sample.PCIeLCRCErrors, 3)
	mustInt(t, sample.PCIeECRCErrors, 4)
	mustInt(t, sample.PCIeRetry, 5)
}

// Some npu-smi builds prefix every PCIe error key with "PCIe"; the parser must
// still resolve the counters.
func TestParseNPUSmiPCIeErrPrefixedKeys(t *testing.T) {
	fixture := `        NPU ID                         : 1
        Chip ID                        : 0
        PCIe TX Error Count            : 10
        PCIe RX Error Count            : 20
        PCIe Lcrc Error Count          : 30
        PCIe Ecrc Error Count          : 40
        PCIe Retry Count               : 50
`
	sample, err := ParseNPUSmiPCIeErr([]byte(fixture))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if sample.DeviceID != 1 {
		t.Fatalf("device = %d, want 1", sample.DeviceID)
	}
	mustInt(t, sample.PCIeTXErrors, 10)
	mustInt(t, sample.PCIeRXErrors, 20)
	mustInt(t, sample.PCIeLCRCErrors, 30)
	mustInt(t, sample.PCIeECRCErrors, 40)
	mustInt(t, sample.PCIeRetry, 50)
}

func TestParseNPUSmiExtendedRejectsGarbage(t *testing.T) {
	garbage := []byte("this device does not support querying\n")
	parsers := map[string]func([]byte) (Sample, error){
		"usages":     ParseNPUSmiUsages,
		"common":     ParseNPUSmiCommon,
		"memory":     ParseNPUSmiMemory,
		"ecc":        ParseNPUSmiECC,
		"ecc-enable": ParseNPUSmiECCEnable,
		"volt":       ParseNPUSmiVolt,
		"pcie-err":   ParseNPUSmiPCIeErr,
	}
	for name, parse := range parsers {
		if _, err := parse(garbage); err == nil {
			t.Fatalf("%s: expected error for garbage input", name)
		}
	}
}

// TestSampleJSONOmitsExtendedFields proves the new fields are additive: a
// sample parsed by the legacy single `npu-smi info` table still marshals to the
// old cann.hardware.v1 shape with no extra keys.
func TestSampleJSONOmitsExtendedFields(t *testing.T) {
	sample := Sample{Schema: SampleSchemaV1, DeviceID: 0, Source: "npu-smi", AICorePct: floatPtr(25)}
	data, err := json.Marshal(sample)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		"aicore_clock_mhz", "aicore_clock_max_mhz", "hbm_clock_mhz",
		"hbm_bandwidth_util_pct", "ecc_single_bit_total", "ecc_enabled",
		"voltage_v", "pcie_tx_errors_total", "pcie_retry_total",
	} {
		if bytes.Contains(data, []byte(`"`+key+`"`)) {
			t.Fatalf("unset field %q leaked into JSON: %s", key, data)
		}
	}
}

func TestMergeSampleFillsOnlyMissingFields(t *testing.T) {
	base := Sample{
		Schema:   SampleSchemaV1,
		Source:   "npu-smi",
		TempC:    floatPtr(42),
		VoltageV: floatPtr(0.8),
	}
	src := Sample{
		Schema:      SampleSchemaV1,
		Source:      "npu-smi",
		TempC:       floatPtr(99),  // must not overwrite
		VoltageV:    floatPtr(0.9), // must not overwrite
		HBMClockMHz: floatPtr(1600),
		PCIeRetry:   intPtr(0), // zero is a real value, must be kept
	}
	MergeSample(&base, src)
	if base.TempC == nil || *base.TempC != 42 {
		t.Fatalf("temp overwritten: %v", base.TempC)
	}
	if base.VoltageV == nil || *base.VoltageV != 0.8 {
		t.Fatalf("voltage overwritten: %v", base.VoltageV)
	}
	mustFloat(t, base.HBMClockMHz, 1600)
	if base.PCIeRetry == nil || *base.PCIeRetry != 0 {
		t.Fatalf("pcie retry = %v", base.PCIeRetry)
	}
}
