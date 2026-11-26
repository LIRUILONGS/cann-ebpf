package cpython

import (
	"encoding/json"
	"fmt"
	"sort"

	"cann-ebpf/internal/runlog"
)

// BatchSpanFunction is the stable function name used when a pure-eBPF batch
// observation is written into the normal cann.span.v1 evidence stream. It is a
// derived observation, not a CANN API call.
const BatchSpanFunction = "vllm.scheduler.batch"

// StandardSpans converts the batch-only report into the same canonical span
// records used by the CANN collector. The observed batch window is preserved
// verbatim; attribution.source makes its pure-eBPF origin explicit.
func StandardSpans(pid int, lib string, report BatchReport) ([]runlog.Span, []string) {
	spans := make([]runlog.Span, 0, len(report.Batches))
	var warnings []string
	for _, batch := range report.Batches {
		if batch.BeginNS == 0 {
			warnings = append(warnings, fmt.Sprintf("skip batch %s: begin_ns is zero", batch.BatchID))
			continue
		}
		spanPID := pid
		if len(batch.PIDs) > 0 && int(batch.PIDs[0]) > 0 {
			spanPID = int(batch.PIDs[0])
		}
		if spanPID <= 0 {
			warnings = append(warnings, fmt.Sprintf("skip batch %s: no positive pid", batch.BatchID))
			continue
		}
		tid := spanPID
		if len(batch.TIDs) > 0 && int(batch.TIDs[0]) > 0 {
			tid = int(batch.TIDs[0])
		}
		endNS := batch.EndNS
		if endNS < batch.BeginNS {
			endNS = batch.BeginNS
		}
		requestIDs := append([]string(nil), batch.RequestIDs...)
		sort.Strings(requestIDs)
		batchID := fmt.Sprintf("pid-%d-%s", spanPID, batch.BatchID)
		verdict := "batch_window"
		if batch.Open {
			verdict = "batch_window_open"
		}
		spans = append(spans, runlog.Span{
			Schema:     runlog.SpanSchemaV1,
			TsNS:       int64(batch.BeginNS),
			StartNS:    int64(batch.BeginNS),
			EndNS:      int64(endNS),
			PID:        spanPID,
			TID:        tid,
			TGID:       spanPID,
			Function:   BatchSpanFunction,
			APIName:    "SchedulerOutput.num_scheduled_tokens",
			Library:    lib,
			Category:   "batch",
			DurationNS: int64(endNS - batch.BeginNS),
			Phase:      "batch_window",
			BatchID:    batchID,
			RequestIDs: requestIDs,
			Attribution: &runlog.Attribution{
				Level:      "batch",
				Source:     "ebpf_scheduler_output_dict",
				Confidence: 0.7,
				Verdict:    verdict,
			},
			UnknownFields: []runlog.UnknownField{
				rawBatchField("batch_boundary", report.Boundary),
				rawBatchField("window_semantics", report.WindowSemantics),
				rawBatchField("layout", report.Layout),
				rawBatchField("batch_dict", fmt.Sprintf("0x%x", batch.Dict)),
				rawBatchField("batch_object", fmt.Sprintf("0x%x", batch.Object)),
				rawBatchField("batch_type_name", batch.TypeName),
				rawBatchField("batch_open", batch.Open),
				rawBatchField("batch_truncated", batch.Truncated),
				rawBatchField("dict_used", batch.DictUsed),
				rawBatchField("observed_id_events", batch.ObservedIDEvents),
				rawBatchField("request_id_count", batch.RequestCount),
			},
		})
	}
	return spans, warnings
}

func rawBatchField(name string, value any) runlog.UnknownField {
	data, err := json.Marshal(value)
	if err != nil {
		data = []byte(`null`)
	}
	return runlog.UnknownField{Name: name, Value: json.RawMessage(data)}
}
