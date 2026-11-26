// Package hardware samples accelerator-level metrics that do not require
// device-side profiling: AICore utilisation, HBM usage, power and temperature.
//
// It is deliberately a coarse "E layer": it can support hbm_pressure or a rough
// device-busy signal, but it must never be presented as kernel/memcpy/HCCL
// execution time.
package hardware

const SampleSchemaV1 = "cann.hardware.v1"

// Sample is one point-in-time reading for one device. Pointer fields stay nil
// when the source did not report the metric, so "unknown" is distinct from 0.
type Sample struct {
	Schema        string   `json:"schema"`
	TsNS          int64    `json:"ts_ns"`
	DeviceID      int      `json:"device_id"`
	Name          string   `json:"name,omitempty"`
	Health        string   `json:"health,omitempty"`
	AICorePct     *float64 `json:"aicore_pct,omitempty"`
	MemoryUsedMB  *int64   `json:"memory_used_mb,omitempty"`
	MemoryTotalMB *int64   `json:"memory_total_mb,omitempty"`
	HBMUsedMB     *int64   `json:"hbm_used_mb,omitempty"`
	HBMTotalMB    *int64   `json:"hbm_total_mb,omitempty"`
	HBMFreeMB     *int64   `json:"hbm_free_mb,omitempty"`
	PowerW        *float64 `json:"power_w,omitempty"`
	TempC         *float64 `json:"temp_c,omitempty"`
	Source        string   `json:"source"`
}

func floatPtr(v float64) *float64 { return &v }
func intPtr(v int64) *int64       { return &v }

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
