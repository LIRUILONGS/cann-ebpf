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
