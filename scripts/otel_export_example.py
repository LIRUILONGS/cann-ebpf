#!/usr/bin/env python3
"""Safe example exporter for the service_topology `otel.jsonl` input.

This script is a deliberately small reference implementation. It converts
JSONL records that a gateway, LiteLLM proxy, or vLLM sidecar already logs into
the exact fields consumed by report.BuildServiceTopology:

    trace_id, span_id, parent_span_id, service, name, kind, status,
    start_ns, end_ns

Safety properties:

* Standard library only; it never performs network I/O and never talks to an
  OTLP endpoint, LiteLLM endpoint, or vLLM server.
* It accepts no credential flags and reads no environment variables.
* Output is built from a field whitelist, so Authorization headers, API keys,
  cookies, and other credential-like input fields are dropped rather than
  copied.
* Output files are written atomically with mode 0600 and the script refuses to
  overwrite its own input file.
* Missing trace/span IDs stay missing. The exporter never invents parent links;
  spans without upstream propagation remain valid process-scope evidence.

Usage:

    python scripts/otel_export_example.py --demo
    python scripts/otel_export_example.py --input gateway-spans.jsonl --output otel.jsonl

The `--demo` mode emits synthetic data and says so on stderr. It must never be
presented as a real trace.
"""

from __future__ import annotations

import argparse
import json
import os
import re
import sys
import tempfile
from typing import Any, Iterable

OUTPUT_FIELDS = (
    "trace_id",
    "span_id",
    "parent_span_id",
    "service",
    "name",
    "kind",
    "status",
    "start_ns",
    "end_ns",
)

STRING_FIELDS = {
    "trace_id",
    "span_id",
    "parent_span_id",
    "service",
    "name",
    "kind",
    "status",
}

SENSITIVE_FIELDS = {
    "authorization",
    "proxy-authorization",
    "x-api-key",
    "api_key",
    "apikey",
    "access_token",
    "refresh_token",
    "token",
    "secret",
    "password",
    "cookie",
    "set-cookie",
}

TRACE_ID_RE = re.compile(r"^[0-9a-f]{32}$")
SPAN_ID_RE = re.compile(r"^[0-9a-f]{16}$")


class ExportError(ValueError):
    """Input record cannot be exported safely."""


def _clean_id(value: Any, field: str, pattern: re.Pattern[str], required: bool) -> str:
    if value is None or value == "":
        if required:
            raise ExportError(f"{field} is required")
        return ""
    if not isinstance(value, str):
        raise ExportError(f"{field} must be a string")
    normalized = value.strip().lower()
    if not pattern.fullmatch(normalized) or set(normalized) == {"0"}:
        raise ExportError(f"{field} is not a valid lowercase W3C identifier")
    return normalized


def _clean_int(value: Any, field: str) -> int | None:
    if value is None or value == "":
        return None
    if isinstance(value, bool) or not isinstance(value, int):
        raise ExportError(f"{field} must be an integer nanosecond timestamp")
    if value < 0:
        raise ExportError(f"{field} must not be negative")
    return value


def normalize_record(raw: Any) -> tuple[dict[str, Any], bool]:
    """Return one safe otel.jsonl record and whether credential fields were seen."""
    if not isinstance(raw, dict):
        raise ExportError("record must be a JSON object")

    lowered = {str(key).lower() for key in raw}
    saw_sensitive = bool(lowered & SENSITIVE_FIELDS)
    record: dict[str, Any] = {}

    for field in OUTPUT_FIELDS:
        value = raw.get(field)
        if value is None or value == "":
            continue
        if field in STRING_FIELDS:
            if not isinstance(value, str):
                raise ExportError(f"{field} must be a string")
            value = value.strip()
            if value:
                record[field] = value
            continue
        cleaned = _clean_int(value, field)
        if cleaned is not None:
            record[field] = cleaned

    if not record.get("service"):
        raise ExportError("service is required")

    trace_id = record.get("trace_id", "")
    span_id = record.get("span_id", "")
    parent_id = record.get("parent_span_id", "")
    if trace_id:
        record["trace_id"] = _clean_id(trace_id, "trace_id", TRACE_ID_RE, True)
    if span_id:
        record["span_id"] = _clean_id(span_id, "span_id", SPAN_ID_RE, True)
    if parent_id:
        record["parent_span_id"] = _clean_id(parent_id, "parent_span_id", SPAN_ID_RE, False)
    if record.get("trace_id") and not record.get("span_id"):
        raise ExportError("span_id is required when trace_id is present")

    start_ns = record.get("start_ns")
    end_ns = record.get("end_ns")
    if start_ns is not None and end_ns is not None and end_ns < start_ns:
        raise ExportError("end_ns precedes start_ns")
    return record, saw_sensitive


def read_records(stream: Iterable[str]) -> tuple[list[dict[str, Any]], int]:
    records: list[dict[str, Any]] = []
    sensitive_records = 0
    for line_no, line in enumerate(stream, start=1):
        line = line.strip()
        if not line:
            continue
        try:
            raw = json.loads(line)
            record, saw_sensitive = normalize_record(raw)
        except (json.JSONDecodeError, ExportError) as exc:
            raise ExportError(f"line {line_no}: {exc}") from exc
        records.append(record)
        if saw_sensitive:
            sensitive_records += 1
    return records, sensitive_records


def demo_records() -> list[dict[str, Any]]:
    """Synthetic three-service chain for local testing only."""
    return [
        {
            "trace_id": "1" * 32,
            "span_id": "a" * 16,
            "service": "gateway",
            "name": "POST /v1/chat/completions",
            "kind": "server",
            "status": "OK",
            "start_ns": 1_000_000,
            "end_ns": 11_000_000,
        },
        {
            "trace_id": "1" * 32,
            "span_id": "b" * 16,
            "parent_span_id": "a" * 16,
            "service": "litellm",
            "name": "chat.completions",
            "kind": "client",
            "status": "OK",
            "start_ns": 2_000_000,
            "end_ns": 8_000_000,
        },
        {
            "trace_id": "1" * 32,
            "span_id": "c" * 16,
            "parent_span_id": "b" * 16,
            "service": "vllm",
            "name": "vllm.generate",
            "kind": "server",
            "status": "OK",
            "start_ns": 3_000_000,
            "end_ns": 7_000_000,
        },
    ]


def write_records(records: list[dict[str, Any]], output: str) -> None:
    if output == "-":
        for record in records:
            print(json.dumps(record, ensure_ascii=False, separators=(",", ":")))
        return

    output_path = os.path.realpath(output)
    directory = os.path.dirname(output_path) or "."
    os.makedirs(directory, exist_ok=True)
    fd, temp_path = tempfile.mkstemp(prefix=".otel-export-", suffix=".tmp", dir=directory)
    try:
        if hasattr(os, "fchmod"):
            os.fchmod(fd, 0o600)
        with os.fdopen(fd, "w", encoding="utf-8", newline="\n") as handle:
            for record in records:
                handle.write(json.dumps(record, ensure_ascii=False, separators=(",", ":")) + "\n")
        os.replace(temp_path, output_path)
    except Exception:
        try:
            os.unlink(temp_path)
        except FileNotFoundError:
            pass
        raise


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        description="Safely normalize gateway/LiteLLM/vLLM span logs into otel.jsonl.",
    )
    parser.add_argument("--input", default="-", help="input JSONL path, or - for stdin (default)")
    parser.add_argument("--output", default="-", help="output otel.jsonl path, or - for stdout (default)")
    parser.add_argument("--demo", action="store_true", help="emit a synthetic three-service example and exit")
    return parser


def main(argv: list[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    if args.input != "-" and args.output != "-":
        if os.path.realpath(args.input) == os.path.realpath(args.output):
            print("error: refusing to overwrite the input file", file=sys.stderr)
            return 2

    try:
        if args.demo:
            records = demo_records()
            print("note: --demo emits synthetic topology data, not a real trace", file=sys.stderr)
            sensitive_records = 0
        else:
            if args.input == "-":
                records, sensitive_records = read_records(sys.stdin)
            else:
                with open(args.input, "r", encoding="utf-8") as handle:
                    records, sensitive_records = read_records(handle)
        if not records:
            print("error: no records to export", file=sys.stderr)
            return 1
        write_records(records, args.output)
    except (OSError, ExportError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2

    if sensitive_records:
        print(
            f"warning: dropped credential-like fields from {sensitive_records} input record(s)",
            file=sys.stderr,
        )
    print(f"exported: {len(records)} record(s)", file=sys.stderr)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
