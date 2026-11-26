package tracectx

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestValidateIDs(t *testing.T) {
	if err := ValidateTraceID(strings.Repeat("a", 32)); err != nil {
		t.Fatal(err)
	}
	if err := ValidateSpanID(strings.Repeat("b", 16)); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"", strings.Repeat("a", 31), strings.Repeat("A", 32), strings.Repeat("0", 32)} {
		if err := ValidateTraceID(value); err == nil {
			t.Fatalf("trace_id %q should fail", value)
		}
	}
	for _, value := range []string{"", strings.Repeat("b", 15), strings.Repeat("B", 16), strings.Repeat("0", 16)} {
		if err := ValidateSpanID(value); err == nil {
			t.Fatalf("span_id %q should fail", value)
		}
	}
}

func TestContextValidateAndNormalize(t *testing.T) {
	context := Context{
		TraceID:           "  " + strings.Repeat("A", 32) + "  ",
		SpanID:            strings.Repeat("B", 16),
		ParentSpanID:      strings.Repeat("C", 16),
		BatchParentSpanID: strings.Repeat("D", 16),
		TraceState:        " vendor=value ",
		Source:            " plugin ",
	}
	normalized := context.Normalize()
	if normalized.TraceID != strings.Repeat("a", 32) || normalized.SpanID != strings.Repeat("b", 16) || normalized.BatchParentSpanID != strings.Repeat("d", 16) {
		t.Fatalf("normalize = %+v", normalized)
	}
	if normalized.TraceState != "vendor=value" || normalized.Source != "plugin" {
		t.Fatalf("normalize strings = %+v", normalized)
	}
	if err := context.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (Context{}).Validate(); err != nil {
		t.Fatalf("empty context should be valid: %v", err)
	}
	if err := (Context{SpanID: strings.Repeat("b", 16)}).Validate(); err == nil {
		t.Fatal("span without trace_id must fail")
	}
	if err := (Context{BatchParentSpanID: strings.Repeat("b", 16)}).Validate(); err == nil {
		t.Fatal("batch parent without trace_id must fail")
	}
	if err := (Context{TraceID: strings.Repeat("a", 32)}).ValidateBatch(); err == nil {
		t.Fatal("batch without span_id must fail")
	}
}

func TestChildIsDeterministicAndKeepsTrace(t *testing.T) {
	parent := Context{
		TraceID:    strings.Repeat("a", 32),
		SpanID:     strings.Repeat("b", 16),
		TraceState: "k=v",
		Source:     "vllm_general_plugin",
	}
	first := parent.Child("pid=1|tid=2|start=3")
	second := parent.Child("pid=1|tid=2|start=3")
	if first != second {
		t.Fatalf("child is not deterministic: %+v vs %+v", first, second)
	}
	if first.TraceID != parent.TraceID || first.ParentSpanID != parent.SpanID {
		t.Fatalf("child parentage = %+v", first)
	}
	if first.BatchParentSpanID != "" {
		t.Fatalf("child unexpectedly inherited a batch parent: %+v", first)
	}
	if first.SpanID == parent.SpanID {
		t.Fatal("child reused parent span_id")
	}
	if err := first.Validate(); err != nil {
		t.Fatal(err)
	}
	if child := (Context{}).Child("x"); !child.IsZero() {
		t.Fatalf("empty parent produced %+v", child)
	}
}

func TestChildPreservesBatchUpstreamParent(t *testing.T) {
	parent := Context{
		TraceID:      strings.Repeat("a", 32),
		SpanID:       strings.Repeat("b", 16),
		ParentSpanID: strings.Repeat("c", 16),
	}
	child := parent.Child("span-1")
	if child.ParentSpanID != parent.SpanID || child.BatchParentSpanID != parent.ParentSpanID {
		t.Fatalf("child hierarchy = %+v", child)
	}
	if err := child.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestNewContextAndJSON(t *testing.T) {
	context := NewContext("collector_local_batch")
	if err := context.ValidateBatch(); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(context)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Context
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded != context {
		t.Fatalf("round trip = %+v, want %+v", decoded, context)
	}
}
