# Synthetic views_v2 QA fixtures

Every file in this directory is synthetic test data generated for
`internal/report/views_v2_integration_test.go`. None of it is production vLLM
evidence, customer data, or a measured performance result.

- `ops-complete.jsonl`: one complete synthetic Roofline record plus one record
  with a missing field to exercise partial-evidence handling.
- `ops-missing-fields.jsonl`: synthetic records that must never produce
  Roofline points.
- `events.jsonl`: synthetic deployment annotations for timestamp conversion.
- `offcpu-exact.jsonl`: a synthetic exact off-CPU interval inside a test span.
- `otel.jsonl`: synthetic parent/child spans for process topology integration.

Do not copy real evidence into this directory.
