package runlog

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSpanUnknownFieldsRoundTrip(t *testing.T) {
	input := `{
		"schema":"cann.span.v1",
		"run_id":"run-1",
		"profile":"vllm-ascend",
		"ts_ns":100,
		"pid":42,
		"tid":43,
		"tgid":42,
		"function":"aclrtSynchronizeStream",
		"library":"/usr/local/Ascend/lib64/libascendcl.so",
		"category":"sync",
		"duration_ns":10,
		"span_id":"span-1",
		"seq":1,
		"legacy":{"nested":true},
		"note":"kept"
	}`
	var span Span
	if err := json.Unmarshal([]byte(input), &span); err != nil {
		t.Fatal(err)
	}
	if len(span.UnknownFields) != 2 {
		t.Fatalf("unknown fields = %+v, want 2", span.UnknownFields)
	}
	if span.UnknownFields[0].Name != "legacy" || span.UnknownFields[1].Name != "note" {
		t.Fatalf("unknown fields not sorted: %+v", span.UnknownFields)
	}
	if string(span.UnknownFields[0].Value) != `{"nested":true}` {
		t.Fatalf("legacy raw value = %s", span.UnknownFields[0].Value)
	}

	encoded, err := json.Marshal(span)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	if !strings.Contains(text, `"unknown_fields"`) || !strings.Contains(text, `"legacy"`) {
		t.Fatalf("unknown fields were not explicit in output: %s", text)
	}
}

func TestSpanRejectsReservedUnknownField(t *testing.T) {
	input := `{"unknown_fields":[{"name":"pid","value":1}]}`
	var span Span
	if err := json.Unmarshal([]byte(input), &span); err == nil {
		t.Fatal("expected reserved unknown field to be rejected")
	}
}

func TestSpanValidateOwnershipAndRequiredFields(t *testing.T) {
	valid := Span{
		Schema:     SpanSchemaV1,
		RunID:      "run-1",
		Profile:    "orinfer",
		TsNS:       1,
		PID:        100,
		TID:        101,
		TGID:       100,
		Function:   "aclrtSynchronizeStream",
		Library:    "/usr/local/Ascend/lib64/libascendcl.so",
		Category:   "sync",
		DurationNS: 12,
		SpanID:     "span-1",
		Seq:        1,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid span: %v", err)
	}
	invalid := valid
	invalid.TGID = 999
	if err := invalid.Validate(); err == nil {
		t.Fatal("expected mismatched tgid to fail")
	}
	invalid = valid
	invalid.DurationNS = -1
	if err := invalid.Validate(); err == nil {
		t.Fatal("expected negative duration to fail")
	}
}

func TestSpanValidateContextAttribution(t *testing.T) {
	span := Span{
		Schema:     SpanSchemaV1,
		RunID:      "run-1",
		Profile:    "vllm-ascend",
		TsNS:       100,
		StartNS:    100,
		EndNS:      200,
		PID:        100,
		TID:        101,
		TGID:       100,
		Function:   "aclrtSynchronizeStream",
		APIName:    "aclrtSynchronizeStream",
		Category:   "sync",
		DurationNS: 100,
		BatchID:    "batch-1",
		RequestIDs: []string{"request-1"},
		Attribution: &Attribution{
			Level: "batch", Source: "vllm_general_plugin", Confidence: 1, Verdict: "batch_window",
		},
		SpanID: "span-1",
		Seq:    1,
	}
	if err := span.Validate(); err != nil {
		t.Fatalf("valid contextual span: %v", err)
	}
	invalid := span
	invalid.Attribution = &Attribution{Level: "batch", Confidence: 2}
	if err := invalid.Validate(); err == nil {
		t.Fatal("expected attribution confidence outside [0,1] to fail")
	}
	invalid = span
	invalid.EndNS = 99
	if err := invalid.Validate(); err == nil {
		t.Fatal("expected reversed span window to fail")
	}
	invalid = span
	invalid.RequestIDs = []string{"request-1", "  "}
	if err := invalid.Validate(); err == nil {
		t.Fatal("expected blank request_id to fail")
	}
}

func TestReadSpansPreservesUnknownAndReportsLine(t *testing.T) {
	body := `{"schema":"cann.span.v1","function":"f","category":"sync","duration_ns":1,"extra":{"x":1}}

{"schema":"cann.span.v1","function":"g","category":"submit","duration_ns":2}
`
	spans, err := ReadSpans(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(spans) != 2 {
		t.Fatalf("spans = %d, want 2", len(spans))
	}
	if len(spans[0].UnknownFields) != 1 || spans[0].UnknownFields[0].Name != "extra" {
		t.Fatalf("unknown fields = %+v", spans[0].UnknownFields)
	}

	_, err = ReadSpans(strings.NewReader(`{"function":"ok"}
bad-json
`))
	if err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("expected line 2 error, got %v", err)
	}
}
