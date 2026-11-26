// Package tracectx implements the small, W3C-shaped trace identifiers used by
// cann-ebpf. It deliberately does not implement a full OpenTelemetry SDK: the
// collector only needs stable IDs, validation, and deterministic child IDs.
package tracectx

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

const (
	TraceIDBytes = 16
	SpanIDBytes  = 8
)

// Context is the trace context attached to a batch window and its CANN spans.
// SpanID identifies the batch span; the collector derives a child SpanID for
// each observed CANN span and records the batch span as ParentSpanID.
type Context struct {
	TraceID           string `json:"trace_id,omitempty"`
	SpanID            string `json:"span_id,omitempty"`
	ParentSpanID      string `json:"parent_span_id,omitempty"`
	BatchParentSpanID string `json:"batch_parent_span_id,omitempty"`
	TraceState        string `json:"trace_state,omitempty"`
	Source            string `json:"source,omitempty"`
}

func (c Context) IsZero() bool {
	return c.TraceID == "" && c.SpanID == "" && c.ParentSpanID == "" && c.BatchParentSpanID == "" && c.TraceState == "" && c.Source == ""
}

// Normalize returns a copy with lower-case IDs. It never invents an ID.
func (c Context) Normalize() Context {
	c.TraceID = strings.ToLower(strings.TrimSpace(c.TraceID))
	c.SpanID = strings.ToLower(strings.TrimSpace(c.SpanID))
	c.ParentSpanID = strings.ToLower(strings.TrimSpace(c.ParentSpanID))
	c.BatchParentSpanID = strings.ToLower(strings.TrimSpace(c.BatchParentSpanID))
	c.TraceState = strings.TrimSpace(c.TraceState)
	c.Source = strings.TrimSpace(c.Source)
	return c
}

func (c Context) Validate() error {
	c = c.Normalize()
	if c.TraceID == "" {
		if c.SpanID != "" || c.ParentSpanID != "" || c.BatchParentSpanID != "" {
			return fmt.Errorf("trace_id is required when span_id, parent_span_id, or batch_parent_span_id is set")
		}
		return nil
	}
	if err := ValidateTraceID(c.TraceID); err != nil {
		return err
	}
	if c.SpanID != "" {
		if err := ValidateSpanID(c.SpanID); err != nil {
			return fmt.Errorf("span_id: %w", err)
		}
	}
	if c.ParentSpanID != "" {
		if err := ValidateSpanID(c.ParentSpanID); err != nil {
			return fmt.Errorf("parent_span_id: %w", err)
		}
	}
	if c.BatchParentSpanID != "" {
		if err := ValidateSpanID(c.BatchParentSpanID); err != nil {
			return fmt.Errorf("batch_parent_span_id: %w", err)
		}
	}
	return nil
}

// ValidateBatch is Validate plus the invariant that a batch trace has its own
// batch span. A trace without a batch span cannot anchor child CANN spans.
func (c Context) ValidateBatch() error {
	c = c.Normalize()
	if err := c.Validate(); err != nil {
		return err
	}
	if c.TraceID != "" && c.SpanID == "" {
		return fmt.Errorf("batch trace requires span_id")
	}
	return nil
}

// NewContext creates a local root trace with a fresh trace_id and span_id.
func NewContext(source string) Context {
	return Context{
		TraceID: NewTraceID(),
		SpanID:  NewSpanID(),
		Source:  strings.TrimSpace(source),
	}
}

// Child derives a deterministic child span under c. The parent is c.SpanID.
// An empty trace context stays empty; the collector must not invent a root.
func (c Context) Child(seed string) Context {
	c = c.Normalize()
	if c.TraceID == "" {
		return Context{}
	}
	return Context{
		TraceID:           c.TraceID,
		SpanID:            DeriveSpanID(seed),
		ParentSpanID:      c.SpanID,
		BatchParentSpanID: c.ParentSpanID,
		TraceState:        c.TraceState,
		Source:            c.Source,
	}
}

func ValidateTraceID(value string) error {
	if len(value) != TraceIDBytes*2 {
		return fmt.Errorf("trace_id must be %d hex characters", TraceIDBytes*2)
	}
	if !isLowerHex(value) {
		return fmt.Errorf("trace_id must be lower-case hexadecimal")
	}
	if isAllZero(value) {
		return fmt.Errorf("trace_id must not be all zero")
	}
	return nil
}

func ValidateSpanID(value string) error {
	if len(value) != SpanIDBytes*2 {
		return fmt.Errorf("span_id must be %d hex characters", SpanIDBytes*2)
	}
	if !isLowerHex(value) {
		return fmt.Errorf("span_id must be lower-case hexadecimal")
	}
	if isAllZero(value) {
		return fmt.Errorf("span_id must not be all zero")
	}
	return nil
}

func NewTraceID() string {
	return randomHex(TraceIDBytes)
}

func NewSpanID() string {
	return randomHex(SpanIDBytes)
}

// DeriveSpanID creates a stable 16-hex child ID from observed span fields. It is
// a correlation ID, not a cryptographic secret.
func DeriveSpanID(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(sum[:SpanIDBytes])
}

func randomHex(size int) string {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand failure is exceptional. Derive from a fixed namespace
		// rather than returning an empty or reused all-zero ID.
		sum := sha256.Sum256([]byte("cann-ebpf-trace-fallback"))
		copy(buf, sum[:])
	}
	return hex.EncodeToString(buf)
}

func isLowerHex(value string) bool {
	for _, ch := range value {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			return false
		}
	}
	return value != ""
}

func isAllZero(value string) bool {
	for _, ch := range value {
		if ch != '0' {
			return false
		}
	}
	return true
}
