package metrics

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"cann-ebpf/internal/hardware"
)

// npusmiExtendedTypeOrder is the fixed order the extended source runs the
// per-device `npu-smi info -t <type>` subcommands in. Order matters only for
// determinism of warnings; samples are merged by field.
var npusmiExtendedTypeOrder = []string{
	"usages",
	"common",
	"memory",
	"ecc",
	"ecc-enable",
	"volt",
	"pcie-err",
}

// npusmiExtendedParsers maps a subcommand type to its text parser.
var npusmiExtendedParsers = map[string]func([]byte) (hardware.Sample, error){
	"usages":     hardware.ParseNPUSmiUsages,
	"common":     hardware.ParseNPUSmiCommon,
	"memory":     hardware.ParseNPUSmiMemory,
	"ecc":        hardware.ParseNPUSmiECC,
	"ecc-enable": hardware.ParseNPUSmiECCEnable,
	"volt":       hardware.ParseNPUSmiVolt,
	"pcie-err":   hardware.ParseNPUSmiPCIeErr,
}

// NPUSmiExtendedSource collects the P0 hardware metrics that the single
// `npu-smi info` summary table does not expose: AICore/HBM clocks, HBM
// bandwidth utilisation, the eight ECC counters plus enable flag, voltage, and
// the five PCIe error counters.
//
// It runs several `-t` subcommands per device (about 0.5 s each), so its default
// interval is 30 s rather than the 5 s used by NPUSmiSource. A failure of one
// subcommand is a warning, not a fatal error: the other families are still
// emitted.
//
// It deliberately does not collect HCCS (deferred) or fan speed (the 910B2 has
// no NPU fan), and it never fabricates absolute HBM GB/s from the utilisation
// percentage.
type NPUSmiExtendedSource struct {
	Command  string
	Devices  []int // optional; discovered from `npu-smi info` when empty
	Types    []string
	Every    time.Duration
	Deadline time.Duration
	Runner   CommandRunner
}

// NewNPUSmiExtendedSource creates an extended npu-smi source. command may be
// empty to use PATH; devices may be empty to auto-discover. types may be empty
// to run every supported family.
func NewNPUSmiExtendedSource(command string, devices []int, types []string, interval, timeout time.Duration) *NPUSmiExtendedSource {
	if command == "" {
		command = "npu-smi"
	}
	if interval <= 0 || interval < 10*time.Second {
		interval = 30 * time.Second
	}
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	return &NPUSmiExtendedSource{
		Command: command, Devices: devices, Types: types,
		Every: interval, Deadline: timeout, Runner: execRunner,
	}
}

func (s *NPUSmiExtendedSource) Name() string            { return "npu-smi-ext" }
func (s *NPUSmiExtendedSource) Interval() time.Duration { return s.Every }
func (s *NPUSmiExtendedSource) Timeout() time.Duration  { return s.Deadline }

func (s *NPUSmiExtendedSource) types() []string {
	if len(s.Types) == 0 {
		return npusmiExtendedTypeOrder
	}
	var ordered []string
	for _, name := range npusmiExtendedTypeOrder {
		for _, want := range s.Types {
			if want == name {
				ordered = append(ordered, name)
				break
			}
		}
	}
	return ordered
}

func (s *NPUSmiExtendedSource) Collect(ctx context.Context) ([]Point, []string, error) {
	runner := s.Runner
	if runner == nil {
		runner = execRunner
	}
	types := s.types()
	if len(types) == 0 {
		return nil, nil, fmt.Errorf("npu-smi extended: no supported types requested")
	}
	var warnings []string

	devices := append([]int(nil), s.Devices...)
	if len(devices) == 0 {
		raw, err := runner(ctx, s.Command, "info")
		if err != nil {
			return nil, nil, fmt.Errorf("npu-smi info: %w", err)
		}
		summary, err := hardware.ParseNPUSmiInfo(raw)
		if err != nil {
			return nil, nil, fmt.Errorf("npu-smi info parse: %w", err)
		}
		for _, sample := range summary {
			devices = append(devices, sample.DeviceID)
		}
	}

	var samples []hardware.Sample
	for _, device := range devices {
		base := hardware.Sample{Schema: hardware.SampleSchemaV1, DeviceID: device, Source: "npu-smi"}
		parsedAny := false
		for _, subcommand := range types {
			parse := npusmiExtendedParsers[subcommand]
			raw, err := runner(ctx, s.Command, "info", "-t", subcommand, "-i", strconv.Itoa(device))
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("npu-smi -t %s -i %d: %v", subcommand, device, err))
				continue
			}
			parsed, err := parse(raw)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("npu-smi -t %s -i %d parse: %v", subcommand, device, err))
				continue
			}
			hardware.MergeSample(&base, parsed)
			parsedAny = true
		}
		if parsedAny {
			samples = append(samples, base)
		}
	}
	if len(samples) == 0 {
		return nil, warnings, fmt.Errorf("npu-smi extended: no device metrics parsed")
	}
	return hardwareSamplesToPoints("npu-smi", samples), warnings, nil
}
