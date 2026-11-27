// Package hardware samples accelerator-level metrics that do not require
// device-side profiling: AICore utilisation, HBM usage, power and temperature.
//
// It is deliberately a coarse "E layer": it can support hbm_pressure or a rough
// device-busy signal, but it must never be presented as kernel/memcpy/HCCL
// execution time.
package hardware

const SampleSchemaV1 = "cann.hardware.v1"

// HCCSLane is one HCCS macro/lane entry reported by npu-smi info -t hccs.
// LinkSpeed and LinkMode are raw driver values: npu-smi does not document their
// unit, so they must not be relabelled as GB/s or a percentage. Counters are
// cumulative since device boot/clear.
type HCCSLane struct {
	Lane      int    `json:"lane"`
	LinkMode  *int64 `json:"link_mode,omitempty"`
	LinkLane  string `json:"link_lane,omitempty"`
	LinkSpeed *int64 `json:"link_speed,omitempty"`
	TXBytes   *int64 `json:"tx_bytes,omitempty"`
	RXBytes   *int64 `json:"rx_bytes,omitempty"`
	TXPackets *int64 `json:"tx_packets,omitempty"`
	RXPackets *int64 `json:"rx_packets,omitempty"`
	Retry     *int64 `json:"retry,omitempty"`
	Error     *int64 `json:"error,omitempty"`
}

// Sample is one point-in-time reading for one device. Pointer fields stay nil
// when the source did not report the metric, so "unknown" is distinct from 0.
type Sample struct {
	Schema        string   `json:"schema"`
	TsNS          int64    `json:"ts_ns"`
	DeviceID      int      `json:"device_id"`
	Name          string   `json:"name,omitempty"`
	Health        string   `json:"health,omitempty"`
	HealthCode    *int64   `json:"health_code,omitempty"`
	AICorePct     *float64 `json:"aicore_pct,omitempty"`
	MemoryUsedMB  *int64   `json:"memory_used_mb,omitempty"`
	MemoryTotalMB *int64   `json:"memory_total_mb,omitempty"`
	HBMUsedMB     *int64   `json:"hbm_used_mb,omitempty"`
	HBMTotalMB    *int64   `json:"hbm_total_mb,omitempty"`
	HBMFreeMB     *int64   `json:"hbm_free_mb,omitempty"`
	PowerW        *float64 `json:"power_w,omitempty"`
	TempC         *float64 `json:"temp_c,omitempty"`
	Source        string   `json:"source"`

	// --- P0 extended NPU metrics (parsed from npu-smi info -t type) ---
	//
	// All extra fields are pointers with omitempty so that an older parser
	// (single npu-smi info table) still emits a valid cann.hardware.v1
	// record and old JSONL files remain readable.

	// Frequency. AICoreClockMHz is the current AICore clock (-t common,
	// Aicore curFreq); AICoreClockMaxMHz is the rated/max AICore clock
	// (-t common, Aicore Freq); HBMClockMHz comes from -t memory.
	AICoreClockMHz    *float64 `json:"aicore_clock_mhz,omitempty"`
	AICoreClockMaxMHz *float64 `json:"aicore_clock_max_mhz,omitempty"`
	HBMClockMHz       *float64 `json:"hbm_clock_mhz,omitempty"`

	// HBM bandwidth is only available as a whole-card utilisation percentage;
	// the driver does not expose absolute GB/s (see docs/HBM-CAPABILITY-PROBE.md).
	HBMBandwidthUtilPct *float64 `json:"hbm_bandwidth_util_pct,omitempty"`

	// HBM ECC. The "Errors" fields are the current window counters, "Total"
	// are the aggregate (since clear/reboot) counters, and the isolated pages
	// counters track pages retired after correction.
	ECCSingleBitErrors            *int64 `json:"ecc_single_bit_errors,omitempty"`
	ECCDoubleBitErrors            *int64 `json:"ecc_double_bit_errors,omitempty"`
	ECCSingleBitTotal             *int64 `json:"ecc_single_bit_total,omitempty"`
	ECCDoubleBitTotal             *int64 `json:"ecc_double_bit_total,omitempty"`
	ECCSingleBitIsolatedPages     *int64 `json:"ecc_single_bit_isolated_pages,omitempty"`
	ECCDoubleBitIsolatedPages     *int64 `json:"ecc_double_bit_isolated_pages,omitempty"`
	ECCSingleBitNextIsolatedPages *int64 `json:"ecc_single_bit_next_isolated_pages,omitempty"`
	ECCDoubleBitNextIsolatedPages *int64 `json:"ecc_double_bit_next_isolated_pages,omitempty"`
	ECCEnabled                    *bool  `json:"ecc_enabled,omitempty"`

	// Voltage is a single chip-level value (-t volt); no per-rail limits.
	VoltageV *float64 `json:"voltage_v,omitempty"`

	// PCIe error counters (-t pcie-err). There is no PCIe throughput in
	// npu-smi, only these five cumulative error counts.
	PCIeTXErrors   *int64 `json:"pcie_tx_errors_total,omitempty"`
	PCIeRXErrors   *int64 `json:"pcie_rx_errors_total,omitempty"`
	PCIeLCRCErrors *int64 `json:"pcie_lcrc_errors_total,omitempty"`
	PCIeECRCErrors *int64 `json:"pcie_ecrc_errors_total,omitempty"`
	PCIeRetry      *int64 `json:"pcie_retry_total,omitempty"`

	// --- P1 DCMI extended scalar metrics ---
	//
	// These are direct DCMI values, primarily from dcmi_probe --extended.
	// AIVector/AICPU/CtrlCPU/NPU are whole-device utilisation percentages;
	// they cannot be attributed to a single Worker or request.
	AIVectorPct        *float64 `json:"aivector_pct,omitempty"`
	AICPUPct           *float64 `json:"aicpu_pct,omitempty"`
	CtrlCPUPct         *float64 `json:"ctrlcpu_pct,omitempty"`
	NPUPct             *float64 `json:"npu_pct,omitempty"`
	VectorCoreClockMHz *float64 `json:"vectorcore_clock_mhz,omitempty"`
	CtrlCPUClockMHz    *float64 `json:"ctrlcpu_clock_mhz,omitempty"`
	// AICoreClockRatio is AICoreClockMHz / AICoreClockMaxMHz. A value below 1
	// is only a hint of possible throttling; it is not a throttling reason.
	AICoreClockRatio *float64 `json:"aicore_clock_ratio,omitempty"`
	HBMTempC         *float64 `json:"hbm_temp_c,omitempty"`
	SocTempC         *float64 `json:"soc_temp_c,omitempty"`
	NDieTempC        *float64 `json:"ndie_temp_c,omitempty"`
	SFPTempC         *float64 `json:"sfp_temp_c,omitempty"`
	HealthErrorCount *int64   `json:"health_error_count,omitempty"`

	// --- HCCS passive counters (npu-smi info -t hccs -i N -c 0) ---
	HCCSHealth string     `json:"hccs_health,omitempty"`
	HCCSLanes  []HCCSLane `json:"hccs_lanes,omitempty"`
}

func floatPtr(v float64) *float64 { return &v }
func intPtr(v int64) *int64       { return &v }
func boolPtr(v bool) *bool        { return &v }

// HBMFreePct returns the free percentage when both used/total are known.
func (s Sample) HBMFreePct() (float64, bool) {
	if s.HBMUsedMB == nil || s.HBMTotalMB == nil || *s.HBMTotalMB <= 0 {
		return 0, false
	}
	free := float64(*s.HBMTotalMB-*s.HBMUsedMB) * 100 / float64(*s.HBMTotalMB)
	if free < 0 {
		free = 0
	}
	return free, true
}
