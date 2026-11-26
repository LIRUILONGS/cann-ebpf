// Package device defines the normalized device-side timeline that M3 joins
// against host spans. The producer (msPTI helper or msprof parser) is separate;
// this package owns the schema, the join algorithm and the join report so both
// producers share one contract.
package device

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

const SchemaV1 = "cann.device.v1"

// Activity kinds. API records are the bridge from a host span to a device
// correlation id; KERNEL/MEMCPY/HCCL are the actual device intervals.
const (
	KindAPI    = "API"
	KindKernel = "KERNEL"
	KindMemcpy = "MEMCPY"
	KindHCCL   = "HCCL"
)

// Span is one normalized device-side record.
type Span struct {
	Schema        string `json:"schema"`
	RunID         string `json:"run_id,omitempty"`
	DeviceID      int    `json:"device_id,omitempty"`
	StreamID      int64  `json:"stream_id,omitempty"`
	Kind          string `json:"kind"`
	Name          string `json:"name,omitempty"`
	PID           int    `json:"pid,omitempty"`
	TID           int    `json:"tid,omitempty"`
	StartNS       int64  `json:"start_ns"`
	EndNS         int64  `json:"end_ns"`
	DurationNS    int64  `json:"duration_ns"`
	CorrelationID int64  `json:"correlation_id,omitempty"`
	Source        string `json:"source,omitempty"`
}

func (s *Span) Normalize(runID string) {
	if s.Schema == "" {
		s.Schema = SchemaV1
	}
	if s.RunID == "" {
		s.RunID = runID
	}
	if s.DurationNS == 0 && s.EndNS > s.StartNS {
		s.DurationNS = s.EndNS - s.StartNS
	}
}

func (s Span) Validate() error {
	if s.Schema != SchemaV1 {
		return fmt.Errorf("schema: got %q, want %q", s.Schema, SchemaV1)
	}
	switch s.Kind {
	case KindAPI, KindKernel, KindMemcpy, KindHCCL:
	default:
		return fmt.Errorf("kind %q is invalid", s.Kind)
	}
	if s.StartNS <= 0 {
		return fmt.Errorf("start_ns must be positive")
	}
	if s.EndNS < s.StartNS {
		return fmt.Errorf("end_ns %d precedes start_ns %d", s.EndNS, s.StartNS)
	}
	if s.DurationNS < 0 {
		return fmt.Errorf("duration_ns must not be negative")
	}
	if s.DeviceID < 0 {
		return fmt.Errorf("device_id must not be negative")
	}
	return nil
}

// ReadSpans decodes device JSONL. Malformed lines fail with a one-based line
// number; blank lines are ignored.
func ReadSpans(r io.Reader) ([]Span, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	var spans []Span
	line := 0
	for scanner.Scan() {
		line++
		raw := bytes.TrimSpace(scanner.Bytes())
		if len(raw) == 0 {
			continue
		}
		var span Span
		if err := json.Unmarshal(raw, &span); err != nil {
			return spans, fmt.Errorf("line %d: %w", line, err)
		}
		spans = append(spans, span)
	}
	if err := scanner.Err(); err != nil {
		return spans, err
	}
	sort.SliceStable(spans, func(i, j int) bool {
		if spans[i].StartNS == spans[j].StartNS {
			return spans[i].Kind < spans[j].Kind
		}
		return spans[i].StartNS < spans[j].StartNS
	})
	return spans, nil
}

// Summary counts records by kind, used in join reports and warnings.
func Summary(spans []Span) map[string]int {
	counts := map[string]int{}
	for _, span := range spans {
		counts[span.Kind]++
	}
	return counts
}

func summaryString(counts map[string]int) string {
	if len(counts) == 0 {
		return "none"
	}
	kinds := make([]string, 0, len(counts))
	for kind := range counts {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	parts := make([]string, 0, len(kinds))
	for _, kind := range kinds {
		parts = append(parts, fmt.Sprintf("%s=%d", kind, counts[kind]))
	}
	return strings.Join(parts, ",")
}
