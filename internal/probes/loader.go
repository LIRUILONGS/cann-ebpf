package probes

import (
	"bytes"
	_ "embed"
	"fmt"

	"github.com/cilium/ebpf"
)

//go:embed bpf/cann.bpf.o
var bpfObject []byte

func loadCollectionSpec() (*ebpf.CollectionSpec, error) {
	if len(bpfObject) == 0 {
		return nil, fmt.Errorf("embedded BPF object is empty")
	}
	spec, err := ebpf.LoadCollectionSpecFromReader(bytes.NewReader(bpfObject))
	if err != nil {
		return nil, fmt.Errorf("parse embedded BPF object: %w", err)
	}
	return spec, nil
}

func blockingProgramNames() map[string]struct{} {
	names := make(map[string]struct{}, len(blockingPrograms))
	for _, bp := range blockingPrograms {
		names[bp.name] = struct{}{}
	}
	return names
}

// loadCollection loads the host uprobe programs only. M2 blocking programs are
// pruned here so that a verifier/kernel incompatibility in the optional
// sched_switch/futex code can never break the primary host span path.
func loadCollection() (*ebpf.Collection, error) {
	spec, err := loadCollectionSpec()
	if err != nil {
		return nil, err
	}
	for name := range blockingProgramNames() {
		delete(spec.Programs, name)
	}
	collection, err := ebpf.NewCollection(spec)
	if err != nil {
		return nil, fmt.Errorf("load BPF collection: %w", err)
	}
	return collection, nil
}

// loadBlockingCollection loads only the M2 blocking programs and reuses the
// main collection's tid_state map. A failure here is reported by the caller as
// a warning; blocking attribution is then simply absent from spans.
func loadBlockingCollection(main *ebpf.Collection) (*ebpf.Collection, error) {
	spec, err := loadCollectionSpec()
	if err != nil {
		return nil, err
	}
	names := blockingProgramNames()
	for name := range spec.Programs {
		if _, keep := names[name]; !keep {
			delete(spec.Programs, name)
		}
	}
	for name := range spec.Maps {
		if name != "tid_state" {
			delete(spec.Maps, name)
		}
	}
	stateMap, ok := main.Maps["tid_state"]
	if !ok {
		return nil, fmt.Errorf("main collection is missing tid_state map")
	}
	collection, err := ebpf.NewCollectionWithOptions(spec, ebpf.CollectionOptions{
		MapReplacements: map[string]*ebpf.Map{"tid_state": stateMap},
	})
	if err != nil {
		return nil, fmt.Errorf("load blocking BPF collection: %w", err)
	}
	return collection, nil
}
