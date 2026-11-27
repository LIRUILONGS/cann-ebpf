//go:build linux

package stacks

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"

	"github.com/cilium/ebpf"
)

// ErrUnsupported is returned when kernel stack sampling is unavailable on the
// current platform or kernel. The non-Linux build defines the same sentinel so
// callers can always use errors.Is.
var ErrUnsupported = errors.New("stacks: kernel stack sampling is not supported on this platform")

const (
	// stackProgramName is the SEC("perf_event") function name in
	// bpf/stack_probe.bpf.c. The loader addresses programs by this name.
	stackProgramName = "stack_probe"

	targetPIDsMapName  = "target_pids"
	stackTracesMapName = "stack_traces"
	stackCountsMapName = "stack_counts"
	stackErrorsMapName = "stack_errors"
	stackStatsMapName  = "stats"

	// stackDepth is PERF_MAX_STACK_DEPTH, the value size the kernel expects
	// for a stack-trace map.
	stackDepth = 127
)

// stackProbeObject is built from bpf/stack_probe.bpf.c with:
//
//	clang --target=bpfel -mcpu=v1 -O2 -g -c bpf/stack_probe.bpf.c -o bpf/stack_probe.bpf.o
//
// bpfel bytecode is architecture-neutral, so one object serves both the
// amd64 development host and the aarch64 CANN server.
//
//go:embed bpf/stack_probe.bpf.o
var stackProbeObject []byte

func loadCollectionSpec() (*ebpf.CollectionSpec, error) {
	if len(stackProbeObject) == 0 {
		return nil, fmt.Errorf("stacks: embedded BPF object is empty")
	}
	spec, err := ebpf.LoadCollectionSpecFromReader(bytes.NewReader(stackProbeObject))
	if err != nil {
		return nil, fmt.Errorf("stacks: parse embedded BPF object: %w", err)
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
		return nil, fmt.Errorf("stacks: load perf_event stack BPF collection: %w", err)
	}
	return collection, nil
}
