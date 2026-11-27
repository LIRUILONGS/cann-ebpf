package hardware

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var hccsLineRE = regexp.MustCompile(`(?i)^\s*hccs\s+([a-z0-9 _-]+?)\s*:\s*(.*?)\s*$`)

// ParseNPUSmiHCCS parses `npu-smi info -t hccs -i N -c 0`.
//
// The command returns one block with bracketed arrays. The array length is
// driver/device dependent (910B2 currently reports 7 HCCS macros), so the
// parser derives lane count from the output instead of hard-coding 7.
// Link speed and lane mode remain raw values; the CLI documentation does not
// define a unit for them and they must not be converted to GB/s.
func ParseNPUSmiHCCS(data []byte) (Sample, error) {
	values := make(map[string]string)
	for _, raw := range strings.Split(string(data), "\n") {
		match := hccsLineRE.FindStringSubmatch(strings.TrimSpace(raw))
		if match == nil {
			continue
		}
		key := normalizeNPUSmiKey(match[1])
		if key == "" {
			continue
		}
		values[key] = match[2]
	}

	health := strings.TrimSpace(values["healthstatus"])
	laneMode := parseHCCSArray(values["lanemode"])
	linkLane := parseHCCSArray(values["linklanelist"])
	linkSpeed := parseHCCSArray(values["linkspeed"])
	txPackets := parseHCCSArray(values["txpackets"])
	txBytes := parseHCCSArray(values["txbytes"])
	rxPackets := parseHCCSArray(values["rxpackets"])
	rxBytes := parseHCCSArray(values["rxbytes"])
	retry := parseHCCSArray(values["retrycount"])
	errCount := parseHCCSArray(values["errorcount"])

	laneCount := maxInt(
		len(laneMode), len(linkLane), len(linkSpeed), len(txPackets), len(txBytes),
		len(rxPackets), len(rxBytes), len(retry), len(errCount),
	)
	if laneCount == 0 {
		return Sample{}, fmt.Errorf("npu-smi hccs: no HCCS lane arrays found")
	}
	sample := Sample{Schema: SampleSchemaV1, Source: "npu-smi", HCCSHealth: health}
	for lane := 0; lane < laneCount; lane++ {
		entry := HCCSLane{Lane: lane}
		entry.LinkMode = hccsIntAt(laneMode, lane)
		entry.LinkSpeed = hccsIntAt(linkSpeed, lane)
		if lane < len(linkLane) {
			entry.LinkLane = linkLane[lane]
		}
		entry.TXBytes = hccsIntAt(txBytes, lane)
		entry.RXBytes = hccsIntAt(rxBytes, lane)
		entry.TXPackets = hccsIntAt(txPackets, lane)
		entry.RXPackets = hccsIntAt(rxPackets, lane)
		entry.Retry = hccsIntAt(retry, lane)
		entry.Error = hccsIntAt(errCount, lane)
		sample.HCCSLanes = append(sample.HCCSLanes, entry)
	}
	return sample, nil
}

// parseHCCSArray removes the surrounding brackets and splits on whitespace or
// commas. An empty or "NA" value yields no entries, which the caller keeps as
// unknown rather than zero.
func parseHCCSArray(raw string) []string {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "[")
	raw = strings.TrimSuffix(raw, "]")
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.EqualFold(raw, "NA") {
		return nil
	}
	return strings.FieldsFunc(raw, func(r rune) bool {
		return r == ' ' || r == '\t' || r == ',' || r == ';'
	})
}

func hccsIntAt(values []string, index int) *int64 {
	if index < 0 || index >= len(values) {
		return nil
	}
	value, err := strconv.ParseInt(strings.TrimSpace(values[index]), 10, 64)
	if err != nil {
		return nil
	}
	return &value
}

func maxInt(values ...int) int {
	maximum := 0
	for _, value := range values {
		if value > maximum {
			maximum = value
		}
	}
	return maximum
}
