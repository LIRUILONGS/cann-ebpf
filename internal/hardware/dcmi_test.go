package hardware

import "testing"

func TestParseDCMIJSONL(t *testing.T) {
	raw := []byte(`{"schema":"cann.hardware.v1","ts_ns":1,"device_id":0,"name":"910B2","health":"OK","aicore_pct":42,"hbm_used_mb":61404,"hbm_total_mb":65536,"power_w":130.8,"temp_c":43,"source":"dcmi"}
{"schema":"cann.hardware.v1","ts_ns":2,"device_id":1,"aicore_pct":0,"power_w":128.1}`)
	samples, err := ParseDCMIJSONL(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) != 2 {
		t.Fatalf("samples=%d", len(samples))
	}
	if samples[0].AICorePct == nil || *samples[0].AICorePct != 42 {
		t.Fatalf("aicore=%v", samples[0].AICorePct)
	}
	if samples[1].Source != "dcmi" {
		t.Fatalf("source=%q", samples[1].Source)
	}
	if _, err := ParseDCMIJSONL([]byte("not-json\n")); err == nil {
		t.Fatal("expected parse error")
	}
	if _, err := ParseDCMIJSONL([]byte("")); err == nil {
		t.Fatal("expected empty error")
	}
}

func TestParseDCMIJSONLExtendedFields(t *testing.T) {
	raw := []byte("{\"schema\":\"cann.hardware.v1\",\"ts_ns\":1,\"device_id\":0,\"name\":\"910B2\",\"health\":\"OK\",\"health_code\":0,\"aicore_pct\":25,\"aivector_pct\":1.5,\"aicpu_pct\":2,\"ctrlcpu_pct\":3,\"npu_pct\":4,\"vectorcore_clock_mhz\":1200,\"ctrlcpu_clock_mhz\":2000,\"aicore_clock_ratio\":0.9,\"hbm_temp_c\":41,\"soc_temp_c\":42,\"ndie_temp_c\":43,\"sfp_temp_c\":39,\"health_error_count\":2,\"source\":\"dcmi\"}\n")
	samples, err := ParseDCMIJSONL(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) != 1 {
		t.Fatalf("samples = %d", len(samples))
	}
	sample := samples[0]
	checks := map[string]*float64{
		"aivector": sample.AIVectorPct, "aicpu": sample.AICPUPct,
		"ctrlcpu": sample.CtrlCPUPct, "npu": sample.NPUPct,
		"vectorcore_clock": sample.VectorCoreClockMHz,
		"ctrlcpu_clock":    sample.CtrlCPUClockMHz,
		"aicore_ratio":     sample.AICoreClockRatio,
		"hbm_temp":         sample.HBMTempC, "soc_temp": sample.SocTempC,
		"ndie_temp": sample.NDieTempC, "sfp_temp": sample.SFPTempC,
	}
	want := map[string]float64{
		"aivector": 1.5, "aicpu": 2, "ctrlcpu": 3, "npu": 4,
		"vectorcore_clock": 1200, "ctrlcpu_clock": 2000,
		"aicore_ratio": 0.9, "hbm_temp": 41, "soc_temp": 42,
		"ndie_temp": 43, "sfp_temp": 39,
	}
	for name, ptr := range checks {
		if ptr == nil || *ptr != want[name] {
			t.Fatalf("%s = %v, want %v", name, ptr, want[name])
		}
	}
	if sample.HealthErrorCount == nil || *sample.HealthErrorCount != 2 {
		t.Fatalf("health_error_count = %v", sample.HealthErrorCount)
	}
	if sample.HealthCode == nil || *sample.HealthCode != 0 {
		t.Fatalf("health_code = %v, want 0", sample.HealthCode)
	}
}

func TestParseDCMIJSONLOmitsUnknownFields(t *testing.T) {
	raw := []byte("{\"schema\":\"cann.hardware.v1\",\"ts_ns\":1,\"device_id\":0,\"source\":\"dcmi\"}\n")
	samples, err := ParseDCMIJSONL(raw)
	if err != nil {
		t.Fatal(err)
	}
	if samples[0].AICorePct != nil || samples[0].Health != "" {
		t.Fatalf("unknown fields must stay nil/empty: %+v", samples[0])
	}
}
