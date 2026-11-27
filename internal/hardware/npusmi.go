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

// ---------------------------------------------------------------------------
// P0 extended `npu-smi info -t <type> -i N` parsing
//
// These subcommands print a flat "Key : Value" block for one device instead of
// the summary table handled by ParseNPUSmiInfo. Every parser returns a partial
// Sample (only the fields that command exposes) and a non-nil error when none of
// its expected fields could be parsed, so a broken command never silently
// reports zero. The metrics package merges the per-command samples with
// MergeSample.
// ---------------------------------------------------------------------------

var numberRE = regexp.MustCompile(`-?\d+(?:\.\d+)?`)
var integerRE = regexp.MustCompile(`-?\d+`)

type npusmiKV struct {
	values map[string]string
	keys   []string
}

// parseNPUSmiKV extracts normalized "key -> value" pairs from a `-t` block. It
// tolerates optional box-drawing (`|`, `+--`) so the same parser works whether
// npu-smi prints a plain block or a bordered table.
func parseNPUSmiKV(data []byte) *npusmiKV {
	kv := &npusmiKV{values: make(map[string]string)}
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		line = strings.TrimPrefix(line, "|")
		line = strings.TrimSuffix(line, "|")
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-") {
			continue
		}
		idx := strings.Index(line, ":")
		if idx <= 0 {
			continue
		}
		key := normalizeNPUSmiKey(line[:idx])
		if key == "" {
			continue
		}
		if _, seen := kv.values[key]; !seen {
			kv.keys = append(kv.keys, key)
		}
		kv.values[key] = strings.TrimSpace(line[idx+1:])
	}
	return kv
}

// normalizeNPUSmiKey lowercases and strips spaces, hyphens and unit suffixes so
// "HBM Clock Speed(MHz)" and "HBM Clock Speed" resolve to the same key. The
// parenthesised unit is removed first so its letters do not leak into the key.
func normalizeNPUSmiKey(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(stripParenthetical(s)) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// stripParenthetical removes "(...)" / "（...）" unit suffixes from a key.
func stripParenthetical(s string) string {
	var b strings.Builder
	depth := 0
	for _, r := range s {
		switch r {
		case '(', '（':
			depth++
		case ')', '）':
			if depth > 0 {
				depth--
			}
		default:
			if depth == 0 {
				b.WriteRune(r)
			}
		}
	}
	return b.String()
}

// exact returns the value for the first alias with a direct (normalized) match.
func (kv *npusmiKV) exact(aliases ...string) (string, bool) {
	for _, alias := range aliases {
		if value, ok := kv.values[alias]; ok {
			return value, true
		}
	}
	return "", false
}

// exactOrSuffix prefers an exact match, then falls back to a key ending with an
// alias. The fallback covers optional prefixes such as "PCIe" while the exact
// pass prevents "Single Bit Error Count" from matching the aggregate total key.
func (kv *npusmiKV) exactOrSuffix(aliases ...string) (string, bool) {
	if value, ok := kv.exact(aliases...); ok {
		return value, true
	}
	for _, alias := range aliases {
		for _, key := range kv.keys {
			if strings.HasSuffix(key, alias) {
				return kv.values[key], true
			}
		}
	}
	return "", false
}

func parseNPUSmiFloat(raw string) (float64, bool) {
	match := numberRE.FindString(raw)
	if match == "" {
		return 0, false
	}
	value, err := strconv.ParseFloat(match, 64)
	if err != nil {
		return 0, false
	}
	return value, true
}

func parseNPUSmiInt(raw string) (int64, bool) {
	match := integerRE.FindString(raw)
	if match == "" {
		return 0, false
	}
	value, err := strconv.ParseInt(match, 10, 64)
	if err != nil {
		return 0, false
	}
	return value, true
}

func parseNPUSmiBool(raw string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "true", "yes", "enabled", "on", "1":
		return true, true
	case "false", "no", "disabled", "off", "0":
		return false, true
	}
	return false, false
}

// npusmiBase seeds a sample with schema/source/device identity. The device id
// comes from the "NPU ID" line; parsers that lack it leave DeviceID at 0 and the
// caller (which passed -i N) owns the real identity.
func npusmiBase(kv *npusmiKV) Sample {
	sample := Sample{Schema: SampleSchemaV1, Source: "npu-smi"}
	if raw, ok := kv.exact("npuid", "npid"); ok {
		if id, ok := parseNPUSmiInt(raw); ok {
			sample.DeviceID = int(id)
		}
	}
	if raw, ok := kv.exact("chipname"); ok {
		sample.Name = strings.TrimSpace(raw)
	}
	return sample
}

// ParseNPUSmiUsages parses `npu-smi info -t usages -i N`. It extracts the
// whole-card HBM bandwidth utilisation (the only absolute-bandwidth signal the
// driver exposes) plus the AICore usage percentage reported alongside it.
func ParseNPUSmiUsages(data []byte) (Sample, error) {
	kv := parseNPUSmiKV(data)
	sample := npusmiBase(kv)
	found := false
	if raw, ok := kv.exactOrSuffix("hbmbandwidthusagerate"); ok {
		if value, ok := parseNPUSmiFloat(raw); ok {
			sample.HBMBandwidthUtilPct = floatPtr(value)
			found = true
		}
	}
	if raw, ok := kv.exactOrSuffix("aicoreusagerate"); ok {
		if value, ok := parseNPUSmiFloat(raw); ok {
			sample.AICorePct = floatPtr(value)
			found = true
		}
	}
	if !found {
		return Sample{}, fmt.Errorf("npu-smi usages: HBM bandwidth usage rate not found")
	}
	return sample, nil
}

// ParseNPUSmiCommon parses `npu-smi info -t common -i N` for the rated
// (Aicore Freq) and current (Aicore curFreq) AICore clocks.
func ParseNPUSmiCommon(data []byte) (Sample, error) {
	kv := parseNPUSmiKV(data)
	sample := npusmiBase(kv)
	found := false
	if raw, ok := kv.exact("aicorefreq", "aicorefrequency"); ok {
		if value, ok := parseNPUSmiFloat(raw); ok {
			sample.AICoreClockMaxMHz = floatPtr(value)
			found = true
		}
	}
	if raw, ok := kv.exact("aicorecurfreq"); ok {
		if value, ok := parseNPUSmiFloat(raw); ok {
			sample.AICoreClockMHz = floatPtr(value)
			found = true
		}
	}
	if !found {
		return Sample{}, fmt.Errorf("npu-smi common: Aicore frequency not found")
	}
	return sample, nil
}

// ParseNPUSmiMemory parses `npu-smi info -t memory -i N` for the HBM clock.
func ParseNPUSmiMemory(data []byte) (Sample, error) {
	kv := parseNPUSmiKV(data)
	sample := npusmiBase(kv)
	raw, ok := kv.exact("hbmclockspeed", "hbmclock")
	if !ok {
		return Sample{}, fmt.Errorf("npu-smi memory: HBM clock not found")
	}
	value, ok := parseNPUSmiFloat(raw)
	if !ok {
		return Sample{}, fmt.Errorf("npu-smi memory: invalid HBM clock %q", raw)
	}
	sample.HBMClockMHz = floatPtr(value)
	return sample, nil
}

// ParseNPUSmiECC parses `npu-smi info -t ecc -i N`. The eight counters map to
// the dcmi_ecc_info fields: current single/double bit errors, aggregate totals,
// isolated pages and next isolated pages.
func ParseNPUSmiECC(data []byte) (Sample, error) {
	kv := parseNPUSmiKV(data)
	sample := npusmiBase(kv)
	found := false
	setInt := func(dst **int64, aliases ...string) {
		raw, ok := kv.exact(aliases...)
		if !ok {
			return
		}
		value, ok := parseNPUSmiInt(raw)
		if !ok {
			return
		}
		*dst = intPtr(value)
		found = true
	}
	setInt(&sample.ECCSingleBitErrors, "singlebiterrorcount", "hbmsinglebiterrorcount")
	setInt(&sample.ECCDoubleBitErrors, "doublebiterrorcount", "hbmdoublebiterrorcount")
	setInt(&sample.ECCSingleBitTotal, "aggregatetotalsinglebiterrorcount", "hbmsinglebitaggregatetotalerrcnt")
	setInt(&sample.ECCDoubleBitTotal, "aggregatetotaldoublebiterrorcount", "hbmdoublebitaggregatetotalerrcnt")
	setInt(&sample.ECCSingleBitIsolatedPages, "singlebitisolatedpagescount", "hbmsinglebitisolatedpagescount", "aggregatetotalsinglebitisolatedpagescount")
	setInt(&sample.ECCDoubleBitIsolatedPages, "doublebitisolatedpagescount", "hbmdoublebitisolatedpagescount", "aggregatetotaldoublebitisolatedpagescount")
	setInt(&sample.ECCSingleBitNextIsolatedPages, "singlebitnextisolatedpagescount", "hbmsinglebitnextisolatedpagescount")
	setInt(&sample.ECCDoubleBitNextIsolatedPages, "doublebitnextisolatedpagescount", "hbmdoublebitnextisolatedpagescount")
	if !found {
		return Sample{}, fmt.Errorf("npu-smi ecc: no ECC counter found")
	}
	return sample, nil
}

// ParseNPUSmiECCEnable parses `npu-smi info -t ecc-enable -i N`.
func ParseNPUSmiECCEnable(data []byte) (Sample, error) {
	kv := parseNPUSmiKV(data)
	sample := npusmiBase(kv)
	raw, ok := kv.exact("hbmeccenablestatus", "eccenablestatus", "eccenable")
	if !ok {
		return Sample{}, fmt.Errorf("npu-smi ecc-enable: ECC-Enable status not found")
	}
	value, ok := parseNPUSmiBool(raw)
	if !ok {
		return Sample{}, fmt.Errorf("npu-smi ecc-enable: invalid status %q", raw)
	}
	sample.ECCEnabled = boolPtr(value)
	return sample, nil
}

// ParseNPUSmiVolt parses `npu-smi info -t volt -i N` for the chip voltage.
func ParseNPUSmiVolt(data []byte) (Sample, error) {
	kv := parseNPUSmiKV(data)
	sample := npusmiBase(kv)
	raw, ok := kv.exact("voltage", "voltagev")
	if !ok {
		return Sample{}, fmt.Errorf("npu-smi volt: voltage not found")
	}
	value, ok := parseNPUSmiFloat(raw)
	if !ok {
		return Sample{}, fmt.Errorf("npu-smi volt: invalid voltage %q", raw)
	}
	sample.VoltageV = floatPtr(value)
	return sample, nil
}

// ParseNPUSmiPCIeErr parses `npu-smi info -t pcie-err -i N`. npu-smi exposes
// only these five cumulative error counters, never PCIe throughput.
func ParseNPUSmiPCIeErr(data []byte) (Sample, error) {
	kv := parseNPUSmiKV(data)
	sample := npusmiBase(kv)
	found := false
	setInt := func(dst **int64, aliases ...string) {
		raw, ok := kv.exactOrSuffix(aliases...)
		if !ok {
			return
		}
		value, ok := parseNPUSmiInt(raw)
		if !ok {
			return
		}
		*dst = intPtr(value)
		found = true
	}
	setInt(&sample.PCIeTXErrors, "pcietxerrorcount", "txerrorcount")
	setInt(&sample.PCIeRXErrors, "pcierxerrorcount", "rxerrorcount")
	setInt(&sample.PCIeLCRCErrors, "pcielcrcerrorcount", "lcrcerrorcount", "lcrc")
	setInt(&sample.PCIeECRCErrors, "pcieecrcerrorcount", "ecrcerrorcount", "ecrc")
	setInt(&sample.PCIeRetry, "pciertrycount", "retrycount")
	if !found {
		return Sample{}, fmt.Errorf("npu-smi pcie-err: no PCIe error counter found")
	}
	return sample, nil
}

// MergeSample copies every metric that dst does not already have from src. It
// lets the metrics package assemble one cann.hardware.v1 sample per device from
// several `-t` outputs: first writer wins, nil/unknown stays distinct from zero.
func MergeSample(dst *Sample, src Sample) {
	if dst.Schema == "" {
		dst.Schema = src.Schema
	}
	if dst.Name == "" {
		dst.Name = src.Name
	}
	if dst.Health == "" {
		dst.Health = src.Health
	}
	if dst.Source == "" {
		dst.Source = src.Source
	}
	for _, pair := range []struct{ dst, src **float64 }{
		{&dst.AICorePct, &src.AICorePct},
		{&dst.AICoreClockMHz, &src.AICoreClockMHz},
		{&dst.AICoreClockMaxMHz, &src.AICoreClockMaxMHz},
		{&dst.HBMClockMHz, &src.HBMClockMHz},
		{&dst.HBMBandwidthUtilPct, &src.HBMBandwidthUtilPct},
		{&dst.PowerW, &src.PowerW},
		{&dst.TempC, &src.TempC},
		{&dst.VoltageV, &src.VoltageV},
	} {
		if *pair.dst == nil {
			*pair.dst = *pair.src
		}
	}
	for _, pair := range []struct{ dst, src **int64 }{
		{&dst.MemoryUsedMB, &src.MemoryUsedMB},
		{&dst.MemoryTotalMB, &src.MemoryTotalMB},
		{&dst.HBMUsedMB, &src.HBMUsedMB},
		{&dst.HBMTotalMB, &src.HBMTotalMB},
		{&dst.HBMFreeMB, &src.HBMFreeMB},
		{&dst.ECCSingleBitErrors, &src.ECCSingleBitErrors},
		{&dst.ECCDoubleBitErrors, &src.ECCDoubleBitErrors},
		{&dst.ECCSingleBitTotal, &src.ECCSingleBitTotal},
		{&dst.ECCDoubleBitTotal, &src.ECCDoubleBitTotal},
		{&dst.ECCSingleBitIsolatedPages, &src.ECCSingleBitIsolatedPages},
		{&dst.ECCDoubleBitIsolatedPages, &src.ECCDoubleBitIsolatedPages},
		{&dst.ECCSingleBitNextIsolatedPages, &src.ECCSingleBitNextIsolatedPages},
		{&dst.ECCDoubleBitNextIsolatedPages, &src.ECCDoubleBitNextIsolatedPages},
		{&dst.PCIeTXErrors, &src.PCIeTXErrors},
		{&dst.PCIeRXErrors, &src.PCIeRXErrors},
		{&dst.PCIeLCRCErrors, &src.PCIeLCRCErrors},
		{&dst.PCIeECRCErrors, &src.PCIeECRCErrors},
		{&dst.PCIeRetry, &src.PCIeRetry},
	} {
		if *pair.dst == nil {
			*pair.dst = *pair.src
		}
	}
	if dst.ECCEnabled == nil {
		dst.ECCEnabled = src.ECCEnabled
	}
}
