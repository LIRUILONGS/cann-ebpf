package tracefs

import "testing"

func TestParseTraceLine(t *testing.T) {
	line, ok := parseTraceLine(" vllm-1452  [004] d... 123.456789: e001: (0x7fff)")
	if !ok {
		t.Fatal("line not parsed")
	}
	if line.TID != 1452 || line.Event != "e001" {
		t.Fatalf("line = %+v", line)
	}
	if line.TimestampNS != 123456789000 {
		t.Fatalf("timestamp = %d", line.TimestampNS)
	}
}

func TestParseTraceLineGroupPrefix(t *testing.T) {
	line, ok := parseTraceLine(" worker-99 [001] .... 1.000001: cann_1_2/r000: (0x0)")
	if !ok || line.Event != "r000" {
		t.Fatalf("line = %+v ok=%v", line, ok)
	}
}
