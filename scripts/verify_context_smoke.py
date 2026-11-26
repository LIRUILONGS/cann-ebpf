#!/usr/bin/env python3
"""Fail closed when context-protocol smoke evidence is missing."""

import argparse
import json
import os
import sys


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--output", required=True)
    parser.add_argument("--batch-id", required=True)
    parser.add_argument("--pid", required=True, type=int)
    parser.add_argument("--trace-id", default="5" * 32)
    parser.add_argument("--span-id", default="6" * 16)
    args = parser.parse_args()

    with open(os.path.join(args.output, "summary.json"), encoding="utf-8") as handle:
        summary = json.load(handle)
    coverage = summary.get("context_coverage", {})
    batch_rows = 0
    sample = None
    with open(os.path.join(args.output, "spans.jsonl"), encoding="utf-8") as handle:
        for line in handle:
            item = json.loads(line)
            if item.get("batch_id") == args.batch_id:
                batch_rows += 1
                if sample is None:
                    sample = item

    failures = []
    if coverage.get("batch_spans", 0) <= 0:
        failures.append("summary.context_coverage.batch_spans is zero")
    if batch_rows <= 0:
        failures.append("spans.jsonl has no batch rows")
    if sample is None:
        failures.append("no sample row")
    else:
        if sample.get("pid") != args.pid:
            failures.append("sample pid does not match target")
        attribution = sample.get("attribution") or {}
        if attribution.get("level") != "batch" or attribution.get("source") != "protocol_smoke":
            failures.append("sample attribution is not batch/protocol_smoke")
        if sample.get("request_ids") != ["req-smoke-1", "req-smoke-2"]:
            failures.append("sample request_ids do not match")
        trace = sample.get("trace") or {}
        if trace.get("trace_id") != args.trace_id:
            failures.append("sample trace_id does not match")
        if trace.get("parent_span_id") != args.span_id:
            failures.append("sample trace parent_span_id does not match")
        if not trace.get("span_id"):
            failures.append("sample trace span_id is empty")
        if trace.get("source") != "protocol_smoke":
            failures.append("sample trace source is not protocol_smoke")
    trace_coverage = summary.get("trace_coverage", {})
    if trace_coverage.get("traced_spans", 0) <= 0:
        failures.append("summary.trace_coverage.traced_spans is zero")
    if trace_coverage.get("unique_traces", 0) != 1:
        failures.append("summary.trace_coverage.unique_traces is not 1")
    top_traces = summary.get("top_slow_traces") or []
    if not any(item.get("trace_id") == args.trace_id for item in top_traces):
        failures.append("trace is absent from top_slow_traces")
    top_batches = summary.get("top_slow_batches") or []
    if not any(item.get("batch_id") == args.batch_id for item in top_batches):
        failures.append("batch is absent from top_slow_batches")

    report = {
        "batch_id": args.batch_id,
        "target_pid": args.pid,
        "batch_rows": batch_rows,
        "context_coverage": coverage,
        "trace_coverage": trace_coverage,
        "attribution_levels": summary.get("attribution_levels", []),
        "sample": sample,
        "failures": failures,
        "ok": not failures,
    }
    with open(os.path.join(args.output, "context-smoke-validation.json"), "w", encoding="utf-8") as handle:
        json.dump(report, handle, ensure_ascii=False, indent=2)
        handle.write("\n")
    print(json.dumps(report, ensure_ascii=False, indent=2))
    if failures:
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
