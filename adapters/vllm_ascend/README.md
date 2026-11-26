# cann-ebpf vLLM-Ascend context adapter

This package is an optional, identifier-only vLLM general plugin. It emits
`batch_begin` / `batch_end` events over a Unix socket so the Go collector can
label host-side CANN spans with `attribution.level=batch`. It does not read prompt
text, output text, token tensors, or request payloads. Request identifiers are
the only per-request values sent.

## Install into the vLLM runtime

The vLLM process loads general plugins at startup. Installing this package into
a running container does not activate it; the service must be restarted or a
new instance must be started after installation. That restart is an explicit
operational action and is not performed by the collector.

Build and install as a normal Python package:

```bash
python3 -m pip install --no-deps adapters/vllm_ascend
```

The collector starts a Unix socket at `/run/cann-ebpf/vllm-ascend.sock` by
default, or at the path in `CANN_EBPF_VLLM_SOCKET`. It reads the target
process's real UID/GID from `/proc/<pid>/status`:

- socket directory: mode `0755`;
- socket default: mode `0600`, owned by the collector;
- if a target is non-root: socket chowned to the target UID/GID and mode `0660`;
- `SO_PEERCRED` allows the collector UID plus every discovered target UID.

For the V4 deployment the target UID/GID is `982/979`, so the socket owner is
`982:979` and the target user can connect. The plugin container and the
collector must see the same socket path; if they use different mount
namespaces, bind-mount `/run/cann-ebpf` (or set `CANN_EBPF_VLLM_SOCKET` to a
shared path) before activation.

If the plugin is absent, or a request has no schedulable request ID, the
collector writes `attribution.level=thread` and does not invent a batch.

## Trace context

Each batch gets a local `trace_id` and batch `span_id`. A host CANN span that
overlaps the batch window receives a child `trace.span_id`, with
`trace.parent_span_id` set to the batch span and
`trace.batch_parent_span_id` preserving the batch's upstream parent when one is
supplied.

Set `CANN_EBPF_TRACEPARENT` to inherit a process-level upstream trace:

```text
CANN_EBPF_TRACEPARENT=00-<32hex trace_id>-<16hex parent_span_id>-01
CANN_EBPF_TRACESTATE=vendor=value
```

This is not per-request HTTP propagation. A complete `HTTP -> vLLM -> CANN`
trace still requires extracting `traceparent` at the HTTP boundary and passing
request-scoped context into the Worker. The general plugin is not activated in
the running V4 service.

## Activation status

This plugin is **not installed or activated in the running V4 service**. vLLM
only loads general plugins at process start; installing files into a running
container is not sufficient. Activation requires a separate maintenance window
and the clock check described in `docs/M4-CONTEXT-PROTOCOL.md`.
