package probes

import (
	"bytes"
	"encoding/binary"
	"fmt"

	"cann-ebpf/internal/config"

	"github.com/cilium/ebpf"
)

const bpfFunctionSlots = 16

type probeConfig struct {
	Enabled       uint32
	SampleRate    uint32
	MinDurationNS uint64
}

type probeStats struct {
	Entries              uint64
	Exits                uint64
	Emitted              uint64
	Filtered             uint64
	RingbufLost          uint64
	MissingInflight      uint64
	Disabled             uint64
	InflightUpdateErrors uint64
}

type rawSpanEvent struct {
	StartNS  uint64
	DurNS    uint64
	PID      uint32
	TID      uint32
	Function uint32
	Flags    uint32
}

func decodeSpanEvent(sample []byte) (rawSpanEvent, error) {
	var event rawSpanEvent
	if len(sample) != binary.Size(event) {
		return event, fmt.Errorf("event size %d, want %d", len(sample), binary.Size(event))
	}
	if err := binary.Read(bytes.NewReader(sample), binary.LittleEndian, &event); err != nil {
		return event, err
	}
	return event, nil
}

func configureProbeMaps(collection *ebpf.Collection, profile *config.Document, overrideMinNS uint64) error {
	cfgMap, ok := collection.Maps["probe_cfg"]
	if !ok {
		return fmt.Errorf("BPF map probe_cfg not found")
	}
	for slot := uint32(0); slot < bpfFunctionSlots; slot++ {
		cfg := probeConfig{}
		if fn, ok := functionByID(slot); ok {
			enabled, rate, minNS := probeConfigFor(profile, fn)
			if enabled {
				cfg.Enabled = 1
				cfg.SampleRate = rate
				cfg.MinDurationNS = minNS
				if overrideMinNS > minNS {
					cfg.MinDurationNS = overrideMinNS
				}
			}
		}
		if err := cfgMap.Update(slot, cfg, ebpf.UpdateAny); err != nil {
			return fmt.Errorf("update probe_cfg[%d]: %w", slot, err)
		}
	}
	statsMap, ok := collection.Maps["probe_stats"]
	if !ok {
		return fmt.Errorf("BPF map probe_stats not found")
	}
	if err := statsMap.Update(uint32(0), probeStats{}, ebpf.UpdateAny); err != nil {
		return fmt.Errorf("reset probe_stats: %w", err)
	}
	return nil
}

func readProbeStats(collection *ebpf.Collection) (probeStats, error) {
	var stats probeStats
	mapRef, ok := collection.Maps["probe_stats"]
	if !ok {
		return stats, fmt.Errorf("BPF map probe_stats not found")
	}
	if err := mapRef.Lookup(uint32(0), &stats); err != nil {
		return stats, err
	}
	return stats, nil
}
