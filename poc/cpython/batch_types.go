package cpython

import (
	"bytes"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	BatchEventSize = 184
	BatchSchema    = "cann.cpython.vllm_batch_poc.v1"
	BatchMaxIDs    = 64

	EventID         uint32 = 1
	EventBatchBegin uint32 = 2
	EventBatchEnd   uint32 = 3
)

// BatchEvent mirrors struct batch_event in batch_probe.bpf.c.
type BatchEvent struct {
	TsNS         uint64
	Dict         uint64
	Object       uint64
	BatchBeginNS uint64
	PID          uint32
	TID          uint32
	EventType    uint32
	DictUsed     uint32
	Index        uint32
	Total        uint32
	Nentries     uint32
	Kind         uint32
	Truncated    uint32
	IDLen        uint32
	ID           [48]byte
	TypeNameRaw  [64]byte
}

func (e BatchEvent) RequestID() string {
	return trimNUL(e.ID[:])
}

func (e BatchEvent) TypeName() string {
	return trimNUL(e.TypeNameRaw[:])
}

func trimNUL(value []byte) string {
	if index := bytes.IndexByte(value, 0); index >= 0 {
		value = value[:index]
	}
	return string(value)
}

// BatchState mirrors struct batch_state in batch_probe.bpf.c.
type BatchState struct {
	Dict       uint64
	Object     uint64
	BeginNS    uint64
	LastSeenNS uint64
	TypeName   [64]byte
}

func (s BatchState) TypeNameString() string {
	return trimNUL(s.TypeName[:])
}

type OpenBatch struct {
	PIDTID uint64
	State  BatchState
}

// BatchStats mirrors struct batch_stats in batch_probe.bpf.c.
type BatchStats struct {
	Entries        uint64 `json:"entries"`
	NameMatches    uint64 `json:"name_matches"`
	Returns        uint64 `json:"returns"`
	NullReturns    uint64 `json:"null_returns"`
	Parsed         uint64 `json:"parsed"`
	Emitted        uint64 `json:"emitted"`
	Skipped        uint64 `json:"skipped"`
	ReadErrors     uint64 `json:"read_errors"`
	SplitDict      uint64 `json:"split_dict"`
	NonUnicodeDict uint64 `json:"non_unicode_dict"`
	EmptyDict      uint64 `json:"empty_dict"`
	RingbufLost    uint64 `json:"ringbuf_lost"`
	BatchesStarted uint64 `json:"batches_started"`
	BatchesClosed  uint64 `json:"batches_closed"`
	TypeReadErrors uint64 `json:"type_read_errors"`
}

type Versions struct {
	Python     string `json:"python"`
	VLLM       string `json:"vllm"`
	VLLMAscend string `json:"vllm_ascend"`
}

type Batch struct {
	BatchID          string   `json:"batch_id"`
	Dict             uint64   `json:"dict"`
	Object           uint64   `json:"object"`
	TypeName         string   `json:"type_name"`
	BeginNS          uint64   `json:"begin_ns"`
	EndNS            uint64   `json:"end_ns"`
	DurationNS       uint64   `json:"duration_ns"`
	Open             bool     `json:"open"`
	RequestIDs       []string `json:"request_ids"`
	RequestCount     int      `json:"request_count"`
	ObservedIDEvents int      `json:"observed_id_events"`
	DictUsed         uint32   `json:"dict_used"`
	Truncated        bool     `json:"truncated"`
	PIDs             []uint32 `json:"pids"`
	TIDs             []uint32 `json:"tids"`
}

type BatchReport struct {
	Schema          string       `json:"schema"`
	Status          string       `json:"status"`
	PID             int          `json:"pid"`
	Lib             string       `json:"lib"`
	Duration        string       `json:"duration"`
	Layout          string       `json:"layout"`
	Boundary        string       `json:"boundary"`
	WindowSemantics string       `json:"window_semantics"`
	Versions        Versions     `json:"versions"`
	Offsets         OffsetBundle `json:"offsets"`
	BatchCount      int          `json:"batch_count"`
	CompleteBatches int          `json:"complete_batches"`
	OpenBatchCount  int          `json:"open_batch_count"`
	RequestCount    int          `json:"request_count"`
	RequestIDs      []string     `json:"request_ids"`
	TypeNames       []string     `json:"type_names"`
	Batches         []Batch      `json:"batches"`
	Stats           BatchStats   `json:"stats"`
	Warnings        []string     `json:"warnings,omitempty"`
	Note            string       `json:"note"`
	RequireType     string       `json:"require_type"`
	TypeConfirmed   bool         `json:"type_confirmed"`
}

type BatchReportInput struct {
	PID         int
	Lib         string
	Duration    time.Duration
	Events      []BatchEvent
	OpenBatches []OpenBatch
	Stats       BatchStats
	Warnings    []string
	Offsets     OffsetBundle
	RequireType string
}

type BatchOptions struct {
	PID       int
	Duration  time.Duration
	LibPath   string
	MaxEvents int
	Offsets   OffsetBundle
}

type BatchResult struct {
	PID         int
	LibPath     string
	Events      []BatchEvent
	OpenBatches []OpenBatch
	Stats       BatchStats
	Warnings    []string
}

type batchAccumulator struct {
	batch    Batch
	idSet    map[string]struct{}
	typeName string
}

func BuildBatchReport(in BatchReportInput) BatchReport {
	events := append([]BatchEvent(nil), in.Events...)
	sort.SliceStable(events, func(i, j int) bool {
		if events[i].TsNS != events[j].TsNS {
			return events[i].TsNS < events[j].TsNS
		}
		return eventRank(events[i].EventType) < eventRank(events[j].EventType)
	})

	accumulators := make([]*batchAccumulator, 0)
	byDict := make(map[uint64][]*batchAccumulator)
	var lastTs uint64
	for _, event := range events {
		if event.TsNS > lastTs {
			lastTs = event.TsNS
		}
		switch event.EventType {
		case EventBatchBegin:
			acc := findOpenBatch(byDict[event.Dict])
			if acc == nil {
				acc = newAccumulator(event)
				accumulators = append(accumulators, acc)
				byDict[event.Dict] = append(byDict[event.Dict], acc)
			} else {
				mergeBatchMeta(acc, event)
			}
		case EventID:
			acc := findOpenBatch(byDict[event.Dict])
			if acc == nil {
				acc = newAccumulator(event)
				acc.batch.Open = true
				accumulators = append(accumulators, acc)
				byDict[event.Dict] = append(byDict[event.Dict], acc)
			}
			mergeBatchMeta(acc, event)
			acc.batch.ObservedIDEvents++
			if event.DictUsed > acc.batch.DictUsed {
				acc.batch.DictUsed = event.DictUsed
			}
			if event.Truncated != 0 {
				acc.batch.Truncated = true
			}
			if requestID := event.RequestID(); requestID != "" {
				acc.idSet[requestID] = struct{}{}
			}
			acc.batch.PIDs = appendUniqueUint32(acc.batch.PIDs, event.PID)
			acc.batch.TIDs = appendUniqueUint32(acc.batch.TIDs, event.TID)
		case EventBatchEnd:
			acc := findEndBatch(byDict[event.Dict], event.BatchBeginNS)
			if acc == nil {
				acc = newAccumulator(event)
				accumulators = append(accumulators, acc)
				byDict[event.Dict] = append(byDict[event.Dict], acc)
			}
			if event.TsNS > acc.batch.EndNS {
				acc.batch.EndNS = event.TsNS
			}
			acc.batch.Open = false
			acc.batch.PIDs = appendUniqueUint32(acc.batch.PIDs, event.PID)
			acc.batch.TIDs = appendUniqueUint32(acc.batch.TIDs, event.TID)
		}
	}

	for _, open := range in.OpenBatches {
		for _, acc := range byDict[open.State.Dict] {
			if acc.batch.BeginNS != open.State.BeginNS && !acc.batch.Open {
				continue
			}
			if open.State.LastSeenNS > acc.batch.EndNS {
				acc.batch.EndNS = open.State.LastSeenNS
			}
			if acc.typeName == "" {
				acc.typeName = open.State.TypeNameString()
			}
		}
	}

	for _, acc := range accumulators {
		if acc.batch.EndNS == 0 {
			acc.batch.EndNS = lastTs
			acc.batch.Open = true
		}
		if acc.batch.EndNS < acc.batch.BeginNS {
			acc.batch.EndNS = acc.batch.BeginNS
		}
		acc.batch.DurationNS = acc.batch.EndNS - acc.batch.BeginNS
		acc.batch.TypeName = acc.typeName
		acc.batch.RequestIDs = sortedKeys(acc.idSet)
		acc.batch.RequestCount = len(acc.batch.RequestIDs)
		sort.Slice(acc.batch.PIDs, func(i, j int) bool { return acc.batch.PIDs[i] < acc.batch.PIDs[j] })
		sort.Slice(acc.batch.TIDs, func(i, j int) bool { return acc.batch.TIDs[i] < acc.batch.TIDs[j] })
	}

	sort.SliceStable(accumulators, func(i, j int) bool {
		if accumulators[i].batch.BeginNS != accumulators[j].batch.BeginNS {
			return accumulators[i].batch.BeginNS < accumulators[j].batch.BeginNS
		}
		return accumulators[i].batch.Dict < accumulators[j].batch.Dict
	})

	report := BatchReport{
		Schema:   BatchSchema,
		PID:      in.PID,
		Lib:      in.Lib,
		Duration: in.Duration.String(),
		Layout:   "cpython-3.12.13-aarch64/vllm-0.23.0rc1",
		Boundary: in.Offsets.VLLM.Boundary,
		WindowSemantics: "observed interval between SchedulerOutput.num_scheduled_tokens dict identities; " +
			"not request latency, model execution time, or device kernel time",
		Versions:    in.Offsets.Versions(),
		Offsets:     in.Offsets,
		Stats:       in.Stats,
		Warnings:    append([]string(nil), in.Warnings...),
		RequireType: in.RequireType,
		Note: "Pure eBPF: CPython 3.12.13 pinned object layout, combined or split unicode dict, compact ASCII keys, max 64 IDs per access. " +
			"Batch identity is the SchedulerOutput.num_scheduled_tokens dict pointer; the final open batch is closed at the last observed event or live map last_seen. " +
			"This is not a function-level execute_model boundary. No prompt, output, or token tensor is read.",
	}
	report.RequestIDs = make([]string, 0)
	report.Batches = make([]Batch, 0, len(accumulators))
	report.TypeNames = make([]string, 0)
	allIDs := make(map[string]struct{})
	typeNames := make(map[string]struct{})
	for index, acc := range accumulators {
		acc.batch.BatchID = fmt.Sprintf("batch-%04d", index+1)
		report.Batches = append(report.Batches, acc.batch)
		if acc.batch.RequestCount > 0 {
			for _, requestID := range acc.batch.RequestIDs {
				allIDs[requestID] = struct{}{}
			}
		}
		if acc.batch.TypeName != "" {
			typeNames[acc.batch.TypeName] = struct{}{}
		}
		if acc.batch.Open {
			report.OpenBatchCount++
		} else if acc.batch.EndNS > acc.batch.BeginNS {
			report.CompleteBatches++
		}
	}
	report.BatchCount = len(report.Batches)
	report.RequestIDs = sortedKeys(allIDs)
	report.RequestCount = len(report.RequestIDs)
	report.TypeNames = sortedKeys(typeNames)
	requireType := strings.TrimSpace(in.RequireType)
	if requireType == "" {
		requireType = "SchedulerOutput"
	}
	for _, typeName := range report.TypeNames {
		if strings.Contains(typeName, requireType) {
			report.TypeConfirmed = true
			break
		}
	}
	if report.RequireType == "" {
		report.RequireType = requireType
	}
	switch {
	case report.BatchCount == 0 && report.Stats.NameMatches > 0:
		report.Status = "matched_but_unparsed"
	case report.BatchCount == 0:
		report.Status = "no_match"
	case report.RequestCount > 0 && report.TypeConfirmed:
		report.Status = "ok"
	case report.RequestCount > 0 && len(report.TypeNames) > 0:
		report.Status = "ok_type_mismatch"
	case report.RequestCount > 0:
		report.Status = "ok_type_unconfirmed"
	default:
		report.Status = "ok_no_ids"
	}
	if !report.TypeConfirmed && report.BatchCount > 0 {
		report.Warnings = append(report.Warnings,
			fmt.Sprintf("batch owner type does not contain %q; treat attribution as unconfirmed", requireType))
	}
	if report.OpenBatchCount > 0 {
		report.Warnings = append(report.Warnings,
			fmt.Sprintf("%d batch(es) were still open at capture end; end_ns is last observed/last_seen, not a function return", report.OpenBatchCount))
	}
	if report.Warnings == nil {
		report.Warnings = []string{}
	}
	return report
}

func eventRank(eventType uint32) int {
	switch eventType {
	case EventBatchBegin:
		return 0
	case EventID:
		return 1
	case EventBatchEnd:
		return 2
	default:
		return 3
	}
}

func newAccumulator(event BatchEvent) *batchAccumulator {
	begin := event.BatchBeginNS
	if begin == 0 {
		begin = event.TsNS
	}
	acc := &batchAccumulator{
		batch: Batch{
			Dict:     event.Dict,
			Object:   event.Object,
			BeginNS:  begin,
			Open:     event.EventType != EventBatchEnd,
			DictUsed: event.DictUsed,
		},
		idSet:    make(map[string]struct{}),
		typeName: event.TypeName(),
	}
	if event.EventType == EventBatchEnd {
		acc.batch.EndNS = event.TsNS
		acc.batch.Open = false
	}
	return acc
}

func mergeBatchMeta(acc *batchAccumulator, event BatchEvent) {
	if event.BatchBeginNS != 0 && (acc.batch.BeginNS == 0 || event.BatchBeginNS < acc.batch.BeginNS) {
		acc.batch.BeginNS = event.BatchBeginNS
	}
	if event.Object != 0 {
		acc.batch.Object = event.Object
	}
	if typeName := event.TypeName(); typeName != "" {
		acc.typeName = typeName
	}
	if event.DictUsed > acc.batch.DictUsed {
		acc.batch.DictUsed = event.DictUsed
	}
	if event.TsNS > acc.batch.EndNS {
		acc.batch.EndNS = event.TsNS
	}
}

func findOpenBatch(batches []*batchAccumulator) *batchAccumulator {
	for index := len(batches) - 1; index >= 0; index-- {
		if batches[index].batch.Open {
			return batches[index]
		}
	}
	return nil
}

func findEndBatch(batches []*batchAccumulator, begin uint64) *batchAccumulator {
	for index := len(batches) - 1; index >= 0; index-- {
		if !batches[index].batch.Open {
			continue
		}
		if begin != 0 && batches[index].batch.BeginNS != begin {
			continue
		}
		return batches[index]
	}
	return findOpenBatch(batches)
}

func appendUniqueUint32(values []uint32, value uint32) []uint32 {
	if value == 0 {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func sortedKeys(values map[string]struct{}) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
