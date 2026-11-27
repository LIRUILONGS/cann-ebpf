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
