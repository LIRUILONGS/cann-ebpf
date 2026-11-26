package hardware

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var memPairRE = regexp.MustCompile(`(\d+)\s*/\s*(\d+)`)

// ParseNPUSmiInfo parses the two-line-per-device table printed by
// `npu-smi info`. Missing metrics stay nil; a malformed row is skipped rather
// than guessed. It returns an error only when no device row could be parsed.
func ParseNPUSmiInfo(data []byte) ([]Sample, error) {
	lines := strings.Split(string(data), "\n")
	var out []Sample
	var pending *Sample
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "|") {
			continue
		}
		cells := splitCells(line)
		if len(cells) < 3 {
			continue
		}
		// Chip row: second cell is a PCI bus id.
		if strings.Contains(cells[1], ":") {
			if pending == nil {
				continue
			}
			applyChipRow(pending, cells)
			out = append(out, *pending)
			pending = nil
			continue
		}
		// NPU row: first cell has "<id> <name>", second is health.
		fields := strings.Fields(cells[0])
		if len(fields) < 2 {
			continue
		}
		if !isHealth(cells[1]) {
			// npu-smi info also prints a process table at the end; its rows
			// carry a pid in the health column and must not become devices.
			continue
		}
		if _, err := strconv.Atoi(fields[1]); err == nil {
			continue
		}
		id, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		sample := Sample{
			Schema: SampleSchemaV1, DeviceID: id, Name: fields[1],
			Health: cells[1], Source: "npu-smi",
		}
		if len(cells) >= 3 {
			numbers := strings.Fields(cells[2])
			if len(numbers) >= 1 {
				if v, err := strconv.ParseFloat(numbers[0], 64); err == nil {
					sample.PowerW = floatPtr(v)
				}
			}
			if len(numbers) >= 2 {
				if v, err := strconv.ParseFloat(numbers[1], 64); err == nil {
					sample.TempC = floatPtr(v)
				}
			}
		}
		pending = &sample
	}
	if pending != nil {
		out = append(out, *pending)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("npu-smi: no device row parsed")
	}
	return out, nil
}

func isHealth(value string) bool {
	switch value {
	case "OK", "Warning", "Alarm", "Critical", "Unknown":
		return true
	default:
		return false
	}
}

func splitCells(line string) []string {
	parts := strings.Split(line, "|")
	if len(parts) < 3 {
		return nil
	}
	cells := make([]string, 0, len(parts)-2)
	for i := 1; i < len(parts)-1; i++ {
		cells = append(cells, strings.TrimSpace(parts[i]))
	}
	return cells
}

func applyChipRow(sample *Sample, cells []string) {
	if len(cells) < 3 {
		return
	}
	numbers := strings.Fields(cells[2])
	if len(numbers) >= 1 {
		if v, err := strconv.ParseFloat(numbers[0], 64); err == nil {
			sample.AICorePct = floatPtr(v)
		}
	}
	pairs := memPairRE.FindAllStringSubmatch(cells[2], -1)
	if len(pairs) >= 1 {
		used, err1 := strconv.ParseInt(pairs[0][1], 10, 64)
		total, err2 := strconv.ParseInt(pairs[0][2], 10, 64)
		if err1 == nil && err2 == nil {
			sample.MemoryUsedMB = intPtr(used)
			sample.MemoryTotalMB = intPtr(total)
		}
	}
	if len(pairs) >= 2 {
		used, err1 := strconv.ParseInt(pairs[1][1], 10, 64)
		total, err2 := strconv.ParseInt(pairs[1][2], 10, 64)
		if err1 == nil && err2 == nil {
			sample.HBMUsedMB = intPtr(used)
			sample.HBMTotalMB = intPtr(total)
			sample.HBMFreeMB = intPtr(total - used)
		}
	}
}
