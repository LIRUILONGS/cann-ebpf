package hardware

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const npuSmiFixture = `+------------------------------------------------------------------------------------------------+
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
| NPU   Chip                | Process id    | Process name             | Process memory(MB)      |
+---------------------------+---------------+--------------------------+-------------------------+
| 0       0                 | 1452          | VLLMWorker_TP            | 57999                   |
| 1       0                 | 3962          | VLLMWorker_TP            | 57998                   |
+===========================+===============+====================================================+
`

func TestParseNPUSmiInfo(t *testing.T) {
	samples, err := ParseNPUSmiInfo([]byte(npuSmiFixture))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(samples) != 2 {
		t.Fatalf("samples = %d, want 2", len(samples))
	}
	first := samples[0]
	if first.DeviceID != 0 || first.Name != "910B2" || first.Health != "OK" {
		t.Fatalf("device row = %+v", first)
	}
	if first.AICorePct == nil || *first.AICorePct != 25 {
		t.Fatalf("aicore = %v", first.AICorePct)
	}
	if first.HBMUsedMB == nil || *first.HBMUsedMB != 61404 || first.HBMTotalMB == nil || *first.HBMTotalMB != 65536 {
		t.Fatalf("hbm = %v/%v", first.HBMUsedMB, first.HBMTotalMB)
	}
	if first.HBMFreeMB == nil || *first.HBMFreeMB != 4132 {
		t.Fatalf("hbm free = %v", first.HBMFreeMB)
	}
	if pct, ok := first.HBMFreePct(); !ok || pct < 6 || pct > 7 {
		t.Fatalf("hbm free pct = %v %v", pct, ok)
	}
	if first.PowerW == nil || *first.PowerW != 130.4 || first.TempC == nil || *first.TempC != 42 {
		t.Fatalf("power/temp = %v/%v", first.PowerW, first.TempC)
	}
	if samples[1].DeviceID != 1 || samples[1].AICorePct == nil || *samples[1].AICorePct != 24 {
		t.Fatalf("second = %+v", samples[1])
	}
}

func TestParseNPUSmiInfoRejectsGarbage(t *testing.T) {
	if _, err := ParseNPUSmiInfo([]byte("no table here\n")); err == nil {
		t.Fatal("expected error for no device rows")
	}
}

func TestCollectorWritesJSONL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hardware.jsonl")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1000, 0)
	c := &Collector{
		opts: collectorOptions{
			interval: time.Hour,
			path:     path,
			now:      func() time.Time { return now },
			runner: func(ctx context.Context, name string, args ...string) ([]byte, error) {
				if name != "npu-smi" || len(args) != 1 || args[0] != "info" {
					t.Fatalf("unexpected command %s %v", name, args)
				}
				return []byte(npuSmiFixture), nil
			},
		},
		file:   file,
		writer: bufio.NewWriter(file),
		done:   make(chan struct{}),
	}
	c.sampleOnce(context.Background())
	c.writer.Flush()
	file.Close()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var samples []Sample
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		var sample Sample
		if err := json.Unmarshal(scanner.Bytes(), &sample); err != nil {
			t.Fatalf("line: %v", err)
		}
		samples = append(samples, sample)
	}
	if len(samples) != 2 {
		t.Fatalf("lines = %d", len(samples))
	}
	if samples[0].TsNS != now.UnixNano() || samples[0].Schema != SampleSchemaV1 {
		t.Fatalf("sample = %+v", samples[0])
	}
}

func TestCollectorResultAvailability(t *testing.T) {
	c := &Collector{opts: collectorOptions{interval: 10 * time.Second}}
	if result := c.resultLocked(); result.Available || !result.Enabled {
		t.Fatalf("empty result = %+v", result)
	}
	c.samples = 3
	c.devices = 8
	result := c.resultLocked()
	if !result.Available || result.Source != "npu-smi" || result.Devices != 8 {
		t.Fatalf("result = %+v", result)
	}
}
