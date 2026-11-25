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

func loadCollection() (*ebpf.Collection, error) {
	spec, err := loadCollectionSpec()
	if err != nil {
		return nil, err
	}
	collection, err := ebpf.NewCollection(spec)
	if err != nil {
		return nil, fmt.Errorf("load BPF collection: %w", err)
	}
	return collection, nil
}
