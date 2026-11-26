//go:build linux && arm64

// Package cpython contains an isolated, read-only eBPF POC for CPython 3.12.
// It is intentionally separate from internal/probes so it cannot change the
// production collector's attach set or output contract.
package cpython

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/link"
	"github.com/cilium/ebpf/ringbuf"
)

//go:embed bpf/cpy_probe.bpf.o
var bpfObject []byte

const (
	EventSize = 96
	MaxIDLen  = 48
	MaxIDs    = 8
)

// Event is the fixed-size ring-buffer record written by cpy_probe.bpf.c.
type Event struct {
	TsNS      uint64
	Dict      uint64
	PID       uint32
	TID       uint32
	Index     uint32
	Total     uint32
	Nentries  uint32
	Kind      uint32
	Truncated uint32
	IDLen     uint32
	ID        [MaxIDLen]byte
}

func (e Event) RequestID() string {
	value := e.ID[:]
	if index := bytes.IndexByte(value, 0); index >= 0 {
		value = value[:index]
	}
	return string(value)
}

// Stats mirrors struct cpy_stats in the BPF object.
type Stats struct {
	Entries        uint64
	NameMatches    uint64
	Returns        uint64
	NullReturns    uint64
	Parsed         uint64
	Emitted        uint64
	Skipped        uint64
	ReadErrors     uint64
	SplitDict      uint64
	NonUnicodeDict uint64
	EmptyDict      uint64
	RingbufLost    uint64
}

type Options struct {
	PID       int
	Duration  time.Duration
	LibPath   string
	MaxEvents int
}

type Result struct {
	PID      int
	LibPath  string
	Events   []Event
	Stats    Stats
	Warnings []string
}

// Run attaches the POC probes to one existing process. It never restarts or
// signals the target.
func Run(ctx context.Context, opts Options) (*Result, error) {
	if opts.PID <= 0 {
		return nil, fmt.Errorf("pid must be positive")
	}
	if opts.Duration <= 0 {
		return nil, fmt.Errorf("duration must be positive")
	}
	result := &Result{PID: opts.PID}
	libPath, err := FindLibPython(opts.PID, opts.LibPath)
	if err != nil {
		return nil, err
	}
	result.LibPath = libPath

	spec, err := ebpf.LoadCollectionSpecFromReader(bytes.NewReader(bpfObject))
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
		done <- readEvents(runCtx, reader, result, opts.MaxEvents)
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

	var stats Stats
	if err := statsMap.Lookup(uint32(0), &stats); err != nil {
		result.Warnings = append(result.Warnings, "read stats: "+err.Error())
	} else {
		result.Stats = stats
	}
	return result, nil
}

func readEvents(ctx context.Context, reader *ringbuf.Reader, result *Result, maxEvents int) error {
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
		if len(record.RawSample) != EventSize {
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("unexpected event size %d, want %d", len(record.RawSample), EventSize))
			continue
		}
		var event Event
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

// FindLibPython resolves the libpython mapped by pid. The returned path is
// suitable for link.OpenExecutable and may point through /proc/<pid>/root when
// the target runs in a container.
func FindLibPython(pid int, override string) (string, error) {
	if strings.TrimSpace(override) != "" {
		return override, nil
	}
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/maps", pid))
	if err != nil {
		return "", fmt.Errorf("read /proc/%d/maps: %w", pid, err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		index := strings.IndexByte(line, '/')
		if index < 0 {
			continue
		}
		path := strings.TrimSpace(line[index:])
		path = strings.TrimSuffix(path, " (deleted)")
		if strings.Contains(path, "libpython3.12.so") {
			if _, statErr := os.Stat(path); statErr == nil {
				return path, nil
			}
			// The target may run in a container whose filesystem is only
			// visible through /proc/<pid>/root.
			rootPath := fmt.Sprintf("/proc/%d/root%s", pid, path)
			if _, statErr := os.Stat(rootPath); statErr == nil {
				return rootPath, nil
			}
			return path, nil
		}
	}
	return "", fmt.Errorf("pid %d does not map libpython3.12.so; pass --lib explicitly", pid)
}
