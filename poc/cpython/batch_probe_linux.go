//go:build linux && arm64

package cpython

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/link"
	"github.com/cilium/ebpf/ringbuf"
)

//go:embed bpf/batch_probe.bpf.o
var batchProbeObject []byte

// RunBatch attaches the pinned vLLM batch POC to one existing process. It
// never restarts, signals, or writes to the target.
func RunBatch(ctx context.Context, opts BatchOptions) (*BatchResult, error) {
	if opts.PID <= 0 {
		return nil, fmt.Errorf("pid must be positive")
	}
	if opts.Duration <= 0 {
		return nil, fmt.Errorf("duration must be positive")
	}
	result := &BatchResult{PID: opts.PID}
	libPath, err := FindLibPython(opts.PID, opts.LibPath)
	if err != nil {
		return nil, err
	}
	result.LibPath = libPath

	spec, err := ebpf.LoadCollectionSpecFromReader(bytes.NewReader(batchProbeObject))
	if err != nil {
		return nil, fmt.Errorf("parse embedded BPF object: %w", err)
	}
	collection, err := ebpf.NewCollection(spec)
	if err != nil {
		return nil, fmt.Errorf("load BPF collection: %w", err)
	}
	defer collection.Close()

	entryProg, ok := collection.Programs["enter_pyobject_getattr"]
	if !ok {
		return nil, fmt.Errorf("BPF program enter_pyobject_getattr not found")
	}
	exitProg, ok := collection.Programs["exit_pyobject_getattr"]
	if !ok {
		return nil, fmt.Errorf("BPF program exit_pyobject_getattr not found")
	}
	eventsMap, ok := collection.Maps["events"]
	if !ok {
		return nil, fmt.Errorf("BPF map events not found")
	}
	statsMap, ok := collection.Maps["stats"]
	if !ok {
		return nil, fmt.Errorf("BPF map stats not found")
	}
	currentMap, ok := collection.Maps["current"]
	if !ok {
		return nil, fmt.Errorf("BPF map current not found")
	}

	executable, err := link.OpenExecutable(libPath)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", libPath, err)
	}
	entryLink, err := executable.Uprobe("PyObject_GetAttr", entryProg, &link.UprobeOptions{PID: opts.PID})
	if err != nil {
		return nil, fmt.Errorf("attach uprobe PyObject_GetAttr: %w", err)
	}
	defer entryLink.Close()
	exitLink, err := executable.Uretprobe("PyObject_GetAttr", exitProg, &link.UprobeOptions{PID: opts.PID})
	if err != nil {
		return nil, fmt.Errorf("attach uretprobe PyObject_GetAttr: %w", err)
	}
	defer exitLink.Close()

	reader, err := ringbuf.NewReader(eventsMap)
	if err != nil {
		return nil, fmt.Errorf("open ring buffer: %w", err)
	}

	runCtx, cancel := context.WithTimeout(ctx, opts.Duration)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- readBatchEvents(runCtx, reader, result, opts.MaxEvents)
	}()

	<-runCtx.Done()
	_ = reader.Close()
	select {
	case readErr := <-done:
		if readErr != nil && !errors.Is(readErr, ringbuf.ErrClosed) {
			result.Warnings = append(result.Warnings, "ring buffer: "+readErr.Error())
		}
	case <-time.After(2 * time.Second):
		result.Warnings = append(result.Warnings, "ring buffer reader did not stop within 2s")
	}

	// Stop updates before reading the final stats and live batch map.
	_ = entryLink.Close()
	_ = exitLink.Close()

	var stats BatchStats
	if err := statsMap.Lookup(uint32(0), &stats); err != nil {
		result.Warnings = append(result.Warnings, "read stats: "+err.Error())
	} else {
		result.Stats = stats
	}
	iterator := currentMap.Iterate()
	var key uint64
	var state BatchState
	for iterator.Next(&key, &state) {
		result.OpenBatches = append(result.OpenBatches, OpenBatch{PIDTID: key, State: state})
	}
	if err := iterator.Err(); err != nil {
		result.Warnings = append(result.Warnings, "iterate current batches: "+err.Error())
	}
	return result, nil
}

func readBatchEvents(ctx context.Context, reader *ringbuf.Reader, result *BatchResult, maxEvents int) error {
	capWarned := false
	for {
		record, err := reader.Read()
		if err != nil {
			if errors.Is(err, ringbuf.ErrClosed) {
				return nil
			}
			select {
			case <-ctx.Done():
				return nil
			default:
				return err
			}
		}
		if len(record.RawSample) != BatchEventSize {
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("unexpected event size %d, want %d", len(record.RawSample), BatchEventSize))
			continue
		}
		var event BatchEvent
		if err := binary.Read(bytes.NewReader(record.RawSample), binary.LittleEndian, &event); err != nil {
			result.Warnings = append(result.Warnings, "decode event: "+err.Error())
			continue
		}
		if maxEvents > 0 && len(result.Events) >= maxEvents {
			if !capWarned {
				result.Warnings = append(result.Warnings,
					fmt.Sprintf("event cap %d reached; later events are not retained", maxEvents))
				capWarned = true
			}
			continue
		}
		result.Events = append(result.Events, event)
	}
}
