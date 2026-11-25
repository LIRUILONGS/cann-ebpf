package runlog

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
)

const SpanSchemaV1 = "cann.span.v1"

var spanKnownFields = map[string]struct{}{
	"schema":         {},
	"run_id":         {},
	"profile":        {},
	"ts_ns":          {},
	"pid":            {},
	"tid":            {},
	"tgid":           {},
	"comm":           {},
	"thread_name":    {},
	"function":       {},
	"library":        {},
	"category":       {},
	"duration_ns":    {},
	"ret":            {},
	"phase":          {},
	"span_id":        {},
	"seq":            {},
	"unknown_fields": {},
}

// UnknownField preserves a JSON field that is not part of the stable Span
// contract. Unknown data is never silently dropped from JSONL.
type UnknownField struct {
	Name  string          `json:"name"`
	Value json.RawMessage `json:"value"`
}

// Span is the stable JSONL record for one host-observed CANN event.
//
// PID is the process ID (Linux thread-group ID), TID is the thread ID, and
// TGID repeats the process ID explicitly so consumers do not have to guess
// the Linux naming convention. TsNS is the event/start timestamp.
type Span struct {
	Schema        string         `json:"schema"`
	RunID         string         `json:"run_id"`
	Profile       string         `json:"profile"`
	TsNS          int64          `json:"ts_ns"`
	PID           int            `json:"pid"`
	TID           int            `json:"tid"`
	TGID          int            `json:"tgid"`
	Comm          string         `json:"comm,omitempty"`
	ThreadName    string         `json:"thread_name,omitempty"`
	Function      string         `json:"function"`
	Library       string         `json:"library"`
	Category      string         `json:"category"`
	DurationNS    int64          `json:"duration_ns"`
	Ret           *int64         `json:"ret,omitempty"`
	Phase         string         `json:"phase,omitempty"`
	SpanID        string         `json:"span_id"`
	Seq           uint64         `json:"seq"`
	UnknownFields []UnknownField `json:"unknown_fields,omitempty"`
}

// Normalize fills writer-owned defaults without changing observed values.
func (s *Span) Normalize(runID, profile string) {
	if s.Schema == "" {
		s.Schema = SpanSchemaV1
	}
	if s.RunID == "" {
		s.RunID = runID
	}
	if s.Profile == "" {
		s.Profile = profile
	}
	if s.TGID == 0 {
		s.TGID = s.PID
	}
}

// Validate rejects records that cannot be tied to a stable process/thread,
// function, category, run, and profile. Library may be empty when it could not
// be resolved, but all observed numeric fields are preserved verbatim.
func (s Span) Validate() error {
	if s.Schema != SpanSchemaV1 {
		return fmt.Errorf("schema: got %q, want %q", s.Schema, SpanSchemaV1)
	}
	if s.RunID == "" {
		return fmt.Errorf("run_id is empty")
	}
	if s.Profile == "" {
		return fmt.Errorf("profile is empty")
	}
	if s.TsNS <= 0 {
		return fmt.Errorf("ts_ns must be positive")
	}
	if s.PID <= 0 || s.TID <= 0 {
		return fmt.Errorf("pid and tid must be positive")
	}
	if s.TGID != s.PID {
		return fmt.Errorf("tgid %d does not match pid %d", s.TGID, s.PID)
	}
	if s.Function == "" {
		return fmt.Errorf("function is empty")
	}
	if s.Category == "" {
		return fmt.Errorf("category is empty")
	}
	if s.DurationNS < 0 {
		return fmt.Errorf("duration_ns must not be negative")
	}
	if s.SpanID == "" {
		return fmt.Errorf("span_id is empty")
	}
	if err := validateUnknownFields(s.UnknownFields); err != nil {
		return err
	}
	return nil
}

func validateUnknownFields(fields []UnknownField) error {
	seen := make(map[string]struct{}, len(fields))
	for i, field := range fields {
		if field.Name == "" {
			return fmt.Errorf("unknown_fields[%d]: name is empty", i)
		}
		if _, reserved := spanKnownFields[field.Name]; reserved {
			return fmt.Errorf("unknown_fields[%d]: %q is a reserved field", i, field.Name)
		}
		if _, duplicate := seen[field.Name]; duplicate {
			return fmt.Errorf("unknown_fields[%d]: duplicate name %q", i, field.Name)
		}
		if len(field.Value) == 0 || !json.Valid(field.Value) {
			return fmt.Errorf("unknown_fields[%d] (%s): invalid JSON value", i, field.Name)
		}
		seen[field.Name] = struct{}{}
	}
	return nil
}

// UnmarshalJSON preserves any unrecognized top-level fields under
// unknown_fields. This keeps forward-compatible data visible instead of
// silently discarding it.
func (s *Span) UnmarshalJSON(data []byte) error {
	type spanAlias Span
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	var decoded spanAlias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*s = Span(decoded)
	for name, value := range raw {
		if _, known := spanKnownFields[name]; known {
			continue
		}
		s.UnknownFields = append(s.UnknownFields, UnknownField{
			Name:  name,
			Value: append(json.RawMessage(nil), value...),
		})
	}
	sort.Slice(s.UnknownFields, func(i, j int) bool {
		return s.UnknownFields[i].Name < s.UnknownFields[j].Name
	})
	if err := validateUnknownFields(s.UnknownFields); err != nil {
		return err
	}
	return nil
}

// ReadSpans decodes JSONL while retaining unknown fields. Blank lines are
// ignored; malformed records fail with their one-based line number.
func ReadSpans(r io.Reader) ([]Span, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	var spans []Span
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var span Span
		if err := json.Unmarshal(line, &span); err != nil {
			return spans, fmt.Errorf("line %d: %w", lineNo, err)
		}
		spans = append(spans, span)
	}
	if err := scanner.Err(); err != nil {
		return spans, err
	}
	return spans, nil
}
