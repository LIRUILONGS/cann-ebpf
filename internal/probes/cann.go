package probes

import (
	"bytes"
	"encoding/binary"
	"fmt"

	"cann-ebpf/internal/config"

	"github.com/cilium/ebpf"
)

const bpfFunctionSlots = 64

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
	OffCPUNS uint64
	FutexNS  uint64
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
	for slot := uint32(0); slot < bpfFunctionSlots; slot++ {
		if err := statsMap.Update(slot, probeStats{}, ebpf.UpdateAny); err != nil {
			return fmt.Errorf("reset probe_stats[%d]: %w", slot, err)
		}
	}
	return nil
}

func readProbeStats(collection *ebpf.Collection) (probeStats, error) {
	var total probeStats
	mapRef, ok := collection.Maps["probe_stats"]
	if !ok {
		return total, fmt.Errorf("BPF map probe_stats not found")
	}
	for slot := uint32(0); slot < bpfFunctionSlots; slot++ {
		var stats probeStats
		if err := mapRef.Lookup(slot, &stats); err != nil {
			return total, fmt.Errorf("lookup probe_stats[%d]: %w", slot, err)
		}
		total.Entries += stats.Entries
		total.Exits += stats.Exits
		total.Emitted += stats.Emitted
		total.Filtered += stats.Filtered
		total.RingbufLost += stats.RingbufLost
		total.MissingInflight += stats.MissingInflight
		total.Disabled += stats.Disabled
		total.InflightUpdateErrors += stats.InflightUpdateErrors
	}
	return total, nil
}
