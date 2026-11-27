package metrics

import (
	"context"
	"errors"
	"testing"
	"time"

	"cann-ebpf/internal/hardware"
)

func TestDCMISourceConvertsLongFormat(t *testing.T) {
	raw := []byte(`{"schema":"cann.hardware.v1","ts_ns":1,"device_id":0,"name":"910B2","health":"OK","aicore_pct":25,"hbm_used_mb":61404,"hbm_total_mb":65536,"power_w":130.4,"temp_c":42,"source":"dcmi"}`)
	source := NewDCMISource("dcmi_probe", time.Second, time.Second)
	source.Runner = func(context.Context, string, ...string) ([]byte, error) { return raw, nil }
	points, warnings, err := source.Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings=%v", warnings)
	}
	assertPointValue(t, points, "npu_aicore_utilization_ratio", 0.25)
	assertPointValue(t, points, "npu_power_watts", 130.4)
	assertPointValue(t, points, "npu_temperature_celsius", 42)
	for _, point := range points {
		if point.Source != "dcmi" || point.Device != "NPU-0" {
			t.Fatalf("identity: %+v", point)
		}
	}
}

func TestDCMISourceParseError(t *testing.T) {
	source := NewDCMISource("dcmi_probe", time.Second, time.Second)
	source.Runner = func(context.Context, string, ...string) ([]byte, error) { return []byte("bad\n"), nil }
	if _, _, err := source.Collect(t.Context()); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestNPUSmiSourceConvertsLongFormat(t *testing.T) {
	fixture := `+------------------------------------------------------------------------------------------------+
| npu-smi 25.2.1                   Version: 25.2.1                                               |
+---------------------------+---------------+----------------------------------------------------+
| NPU   Name                | Health        | Power(W)    Temp(C)           Hugepages-Usage(page)|
| Chip                      | Bus-Id        | AICore(%)   Memory-Usage(MB)  HBM-Usage(MB)        |
+===========================+===============+====================================================+
| 0     910B2               | OK            | 130.4       42                0    / 0             |
| 0                         | 0000:C1:00.0  | 25          0    / 0          61404/ 65536         |
+===========================+===============+====================================================+
`
	source := NewNPUSmiSource("npu-smi", time.Second, time.Second)
	source.Runner = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "npu-smi" || len(args) != 1 || args[0] != "info" {
			return nil, errors.New("unexpected command")
		}
		return []byte(fixture), nil
	}
	points, _, err := source.Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	assertPointValue(t, points, "npu_aicore_utilization_ratio", 0.25)
	assertPointValue(t, points, "npu_hbm_used_bytes", 61404*1024*1024)
	if source.Interval() < 5*time.Second {
		t.Fatalf("npu-smi interval=%v, want >=5s", source.Interval())
	}
}

func TestHardwareSamplesToPointsHealth(t *testing.T) {
	percent := 25.0
	points := hardwareSamplesToPoints("dcmi", []hardware.Sample{{
		Schema: hardware.SampleSchemaV1, DeviceID: 0, Name: "910B2", Health: "OK", AICorePct: &percent,
	}})
	assertPointValue(t, points, "npu_health", 1)
}

func float64Value(v float64) *float64 { return &v }
func int64Value(v int64) *int64       { return &v }

func TestHardwareSamplesToPointsDCMIExtended(t *testing.T) {
	sample := hardware.Sample{
		Schema: hardware.SampleSchemaV1, DeviceID: 2, Name: "910B2",
		AIVectorPct:        float64Value(12.5),
		AICPUPct:           float64Value(3),
		CtrlCPUPct:         float64Value(4),
		NPUPct:             float64Value(8),
		VectorCoreClockMHz: float64Value(1000),
		CtrlCPUClockMHz:    float64Value(2000),
		AICoreClockRatio:   float64Value(0.9),
		HBMTempC:           float64Value(41),
		SocTempC:           float64Value(42),
		NDieTempC:          float64Value(43),
		SFPTempC:           float64Value(39),
		HealthErrorCount:   int64Value(2),
	}
	points := hardwareSamplesToPoints("dcmi", []hardware.Sample{sample})
	assertPointValue(t, points, "npu_aivector_utilization_ratio", 0.125)
	assertPointValue(t, points, "npu_aicpu_utilization_ratio", 0.03)
	assertPointValue(t, points, "npu_ctrlcpu_utilization_ratio", 0.04)
	assertPointValue(t, points, "npu_utilization_ratio", 0.08)
	assertPointValue(t, points, "npu_vectorcore_clock_mhz", 1000)
	assertPointValue(t, points, "npu_ctrlcpu_clock_mhz", 2000)
	assertPointValue(t, points, "npu_aicore_clock_ratio", 0.9)
	assertPointValue(t, points, "npu_hbm_temperature_celsius", 41)
	assertPointValue(t, points, "npu_soc_temperature_celsius", 42)
	assertPointValue(t, points, "npu_ndie_temperature_celsius", 43)
	assertPointValue(t, points, "npu_sfp_temperature_celsius", 39)
	assertPointValue(t, points, "npu_health_error_count", 2)
	for _, point := range points {
		if point.Source != "dcmi" || point.Device != "NPU-2" {
			t.Fatalf("identity = %+v", point)
		}
	}
}

func TestHardwareSamplesToPointsUnknownIsNotZero(t *testing.T) {
	points := hardwareSamplesToPoints("dcmi", []hardware.Sample{{
		Schema: hardware.SampleSchemaV1, DeviceID: 3,
	}})
	if len(points) != 0 {
		t.Fatalf("unknown sample emitted %v, want no fabricated zero values", pointNames(points))
	}
}

func TestDCMISourceExtendedUsesExtendedFlag(t *testing.T) {
	raw := []byte("{\"schema\":\"cann.hardware.v1\",\"ts_ns\":1,\"device_id\":0,\"health\":\"OK\",\"aivector_pct\":10,\"vectorcore_clock_mhz\":1200,\"source\":\"dcmi\"}\n")
	source := NewDCMISourceExtended("dcmi_probe", 10*time.Second, 10*time.Second)
	var calledArgs []string
	source.Runner = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "dcmi_probe" {
			return nil, errors.New("unexpected command")
		}
		calledArgs = append([]string(nil), args...)
		return raw, nil
	}
	points, warnings, err := source.Collect(t.Context())
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v", warnings)
	}
	if len(calledArgs) != 1 || calledArgs[0] != "--extended" {
		t.Fatalf("args = %v, want --extended", calledArgs)
	}
	assertPointValue(t, points, "npu_aivector_utilization_ratio", 0.1)
	assertPointValue(t, points, "npu_vectorcore_clock_mhz", 1200)
}
