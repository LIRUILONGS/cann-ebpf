package runlog

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

const SpanSchemaV1 = "cann.span.v1"

var spanKnownFields = map[string]struct{}{
	"schema":         {},
	"run_id":         {},
	"profile":        {},
	"ts_ns":          {},
	"start_ns":       {},
	"end_ns":         {},
	"pid":            {},
	"tid":            {},
	"tgid":           {},
	"comm":           {},
	"thread_name":    {},
	"function":       {},
	"api":            {},
	"library":        {},
	"category":       {},
	"duration_ns":    {},
	"ret":            {},
	"phase":          {},
	"dev_id":         {},
	"batch_id":       {},
	"request_ids":    {},
	"attribution":    {},
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

// Attribution records how a span was associated with a higher-level unit.
// The observed fields remain untouched; this is an explicitly derived context
// annotation.
type Attribution struct {
	Level      string  `json:"level"`
	Source     string  `json:"source,omitempty"`
	Confidence float64 `json:"confidence,omitempty"`
	Verdict    string  `json:"verdict,omitempty"`
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
	StartNS       int64          `json:"start_ns,omitempty"`
	EndNS         int64          `json:"end_ns,omitempty"`
	PID           int            `json:"pid"`
	TID           int            `json:"tid"`
	TGID          int            `json:"tgid"`
	Comm          string         `json:"comm,omitempty"`
	ThreadName    string         `json:"thread_name,omitempty"`
	Function      string         `json:"function"`
	APIName       string         `json:"api,omitempty"`
	Library       string         `json:"library"`
	Category      string         `json:"category"`
	DurationNS    int64          `json:"duration_ns"`
	Ret           *int64         `json:"ret,omitempty"`
	Phase         string         `json:"phase,omitempty"`
	DevID         int            `json:"dev_id,omitempty"`
	BatchID       string         `json:"batch_id,omitempty"`
	RequestIDs    []string       `json:"request_ids,omitempty"`
	Attribution   *Attribution   `json:"attribution,omitempty"`
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
	if s.StartNS < 0 || s.EndNS < 0 {
		return fmt.Errorf("start_ns and end_ns must not be negative")
	}
	if s.EndNS > 0 && s.StartNS > 0 && s.EndNS < s.StartNS {
		return fmt.Errorf("end_ns %d precedes start_ns %d", s.EndNS, s.StartNS)
	}
	if s.DevID < 0 {
		return fmt.Errorf("dev_id must not be negative")
	}
	if s.Attribution != nil {
		if s.Attribution.Level == "" {
			return fmt.Errorf("attribution.level is empty")
		}
		if s.Attribution.Confidence < 0 || s.Attribution.Confidence > 1 {
			return fmt.Errorf("attribution.confidence must be in [0,1]")
		}
	}
	for i, requestID := range s.RequestIDs {
		if strings.TrimSpace(requestID) == "" {
			return fmt.Errorf("request_ids[%d] is empty", i)
		}
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
