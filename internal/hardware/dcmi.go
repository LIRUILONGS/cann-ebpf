package hardware

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
)

// ParseDCMIJSONL parses the one-JSON-object-per-device output of dcmi_probe.
// It validates the schema/device fields but leaves optional metrics as
// pointers, so unknown is distinct from zero.
func ParseDCMIJSONL(raw []byte) ([]Sample, error) {
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var samples []Sample
	line := 0
	for scanner.Scan() {
		line++
		text := bytes.TrimSpace(scanner.Bytes())
		if len(text) == 0 {
			continue
		}
		var sample Sample
		if err := json.Unmarshal(text, &sample); err != nil {
			return nil, fmt.Errorf("dcmi line %d: %w", line, err)
		}
		if sample.Schema != SampleSchemaV1 {
			return nil, fmt.Errorf("dcmi line %d: unexpected schema %q", line, sample.Schema)
		}
		if sample.DeviceID < 0 {
			return nil, fmt.Errorf("dcmi line %d: negative device_id", line)
		}
		if sample.Source == "" {
			sample.Source = "dcmi"
		}
		samples = append(samples, sample)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(samples) == 0 {
		return nil, fmt.Errorf("dcmi probe returned no devices")
	}
	return samples, nil
}
