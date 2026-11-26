"""Optional vLLM-Ascend context adapter.

The adapter intentionally sends identifiers only. It never reads or transmits
prompt text, generated text, token tensors, or model outputs. The Go collector
uses the batch window to annotate host-side CANN spans; it does not replace the
observed eBPF values.

Loading happens through the vLLM general plugin entry point at process start.
Copying this package into a running container is therefore not enough to
activate it.
"""

from __future__ import annotations

import atexit
import json
import logging
import os
import queue
import socket
import threading
import time
import uuid
from typing import Any

LOG = logging.getLogger(__name__)
DEFAULT_SOCKET = "/run/cann-ebpf/vllm-ascend.sock"
DEFAULT_SOURCE = "vllm_general_plugin"
QUEUE_SIZE = 2048
RECONNECT_SECONDS = 0.5
MAX_REQUEST_IDS = 4096
TRACE_ID_HEX = 32
SPAN_ID_HEX = 16

_state_lock = threading.Lock()
_registered = False
_sender: "_Sender | None" = None


def _env_int(name: str, default: int) -> int:
    try:
        return int(os.environ.get(name, str(default)))
    except (TypeError, ValueError):
        return default


def _request_ids_from_scheduler_output(scheduler_output: Any) -> list[str]:
    """Extract request identifiers without touching prompt or output payloads."""
    if scheduler_output is None:
        return []
    ids: list[str] = []
    seen: set[str] = set()

    def add(value: Any) -> None:
        if value is None:
            return
        text = str(value)
        if not text or text in seen or len(ids) >= MAX_REQUEST_IDS:
            return
        seen.add(text)
        ids.append(text)

    # vLLM v1 scheduler output: {request_id: number_of_tokens}.
    tokens = getattr(scheduler_output, "num_scheduled_tokens", None)
    if isinstance(tokens, dict):
        for request_id in tokens.keys():
            add(request_id)

    for attr in ("scheduled_new_reqs", "scheduled_cached_reqs"):
        values = getattr(scheduler_output, attr, None)
        if values is None:
            continue
        try:
            iterable = values.values() if isinstance(values, dict) else values
            for request in iterable:
                if isinstance(request, dict):
                    add(request.get("req_id") or request.get("request_id"))
                else:
                    add(getattr(request, "req_id", None) or getattr(request, "request_id", None))
        except TypeError:
            continue

    # Some versions expose a flat list instead of the v1 fields.
    for attr in ("request_ids", "req_ids"):
        values = getattr(scheduler_output, attr, None)
        if isinstance(values, (list, tuple, set)):
            for request_id in values:
                add(request_id)
    return ids


def _phase_from_scheduler_output(scheduler_output: Any) -> str:
    phase = getattr(scheduler_output, "phase", None)
    if isinstance(phase, str) and phase:
        return phase
    new_reqs = getattr(scheduler_output, "scheduled_new_reqs", None)
    if new_reqs:
        return "prefill"
    return "decode"


def _model_name(instance: Any) -> str:
    model_config = getattr(instance, "model_config", None)
    model = getattr(model_config, "model", None)
    if isinstance(model, str) and model:
        return model
    return os.environ.get("CANN_EBPF_VLLM_MODEL", "")


class _Sender:
    """Non-blocking bounded sender. Inference must never wait on the socket."""

    def __init__(self, socket_path: str, source: str) -> None:
        self.socket_path = socket_path
        self.source = source
        self.queue: "queue.Queue[dict[str, Any] | None]" = queue.Queue(maxsize=QUEUE_SIZE)
        self._stop = threading.Event()
        self._thread = threading.Thread(target=self._run, name="cann-ebpf-vllm-sender", daemon=True)
        self._dropped = 0
        self._send_errors = 0
        self._last_error_at = 0.0
        self._thread.start()

    def emit(self, event: dict[str, Any]) -> None:
        try:
            self.queue.put_nowait(event)
        except queue.Full:
            self._dropped += 1

    def _run(self) -> None:
        sock: socket.socket | None = None
        while not self._stop.is_set():
            try:
                event = self.queue.get(timeout=0.25)
            except queue.Empty:
                continue
            if event is None:
                break
            if sock is None:
                sock = self._connect()
            if sock is None:
                # Keep the newest event rather than blocking inference. The
                # collector will truthfully fall back to thread attribution.
                continue
            try:
                line = json.dumps(event, separators=(",", ":"), ensure_ascii=True) + "\n"
                sock.sendall(line.encode("utf-8"))
            except OSError as exc:
                self._send_errors += 1
                now = time.monotonic()
                if now - self._last_error_at > 30:
                    self._last_error_at = now
                    LOG.warning("cann-ebpf context send failed: %s", exc)
                try:
                    sock.close()
                except OSError:
                    pass
                sock = None
        if sock is not None:
            try:
                sock.close()
            except OSError:
                pass

    def _connect(self) -> socket.socket | None:
        sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        sock.settimeout(1.0)
        try:
            sock.connect(self.socket_path)
        except OSError as exc:
            self._send_errors += 1
            now = time.monotonic()
            if now - self._last_error_at > 30:
                self._last_error_at = now
                LOG.warning("cann-ebpf context socket unavailable: %s", exc)
            try:
                sock.close()
            except OSError:
                pass
            time.sleep(RECONNECT_SECONDS)
            return None
        return sock

    def close(self) -> None:
        self._stop.set()
        try:
            self.queue.put_nowait(None)
        except queue.Full:
            pass
        self._thread.join(timeout=2.0)

    @property
    def dropped(self) -> int:
        return self._dropped

    @property
    def send_errors(self) -> int:
        return self._send_errors


def _parse_traceparent(value: str) -> tuple[str, str, str] | None:
    """Parse a W3C traceparent header without accepting malformed IDs."""
    parts = value.strip().split("-")
    if len(parts) != 4:
        return None
    version, trace_id, parent_id, flags = parts
    if len(version) != 2 or version == "ff":
        return None
    if len(trace_id) != TRACE_ID_HEX or len(parent_id) != SPAN_ID_HEX:
        return None
    if trace_id == "0" * TRACE_ID_HEX or parent_id == "0" * SPAN_ID_HEX:
        return None
    if len(flags) != 2:
        return None
    if any(ch not in "0123456789abcdef" for ch in trace_id + parent_id + flags):
        return None
    return trace_id, parent_id, flags


def _trace_context() -> dict[str, str]:
    """Return a local batch trace, optionally rooted in CANN_EBPF_TRACEPARENT."""
    trace_id = uuid.uuid4().hex
    parent_span_id = ""
    trace_source = DEFAULT_SOURCE
    trace_state = os.environ.get("CANN_EBPF_TRACESTATE", "").strip()
    traceparent = os.environ.get("CANN_EBPF_TRACEPARENT", "").strip()
    parsed = _parse_traceparent(traceparent) if traceparent else None
    if parsed is not None:
        trace_id, parent_span_id, _flags = parsed
        trace_source = DEFAULT_SOURCE + "+traceparent"
    return {
        "trace_id": trace_id,
        "span_id": uuid.uuid4().hex[:SPAN_ID_HEX],
        "parent_span_id": parent_span_id,
        "trace_state": trace_state,
        "trace_source": trace_source,
    }


def _new_batch_id(pid: int) -> str:
    return f"{pid}-{time.monotonic_ns()}-{uuid.uuid4().hex[:12]}"


def _event(kind: str, ts_ns: int, pid: int, batch_id: str, **extra: Any) -> dict[str, Any]:
    # Keep the protocol deliberately closed. In particular, do not forward
    # arbitrary keyword arguments: a caller must not be able to turn this into
    # a prompt or output exfiltration path.
    allowed = {
        "phase", "request_ids", "model", "source",
        "trace_id", "span_id", "parent_span_id", "trace_state", "trace_source",
    }
    event: dict[str, Any] = {
        "v": 1,
        "type": kind,
        "ts_ns": int(ts_ns),
        "pid": int(pid),
        "batch_id": batch_id,
    }
    for key in allowed:
        value = extra.get(key)
        if value not in (None, "", [], {}):
            event[key] = value
    return event


def _wrap_execute_model(original: Any) -> Any:
    if getattr(original, "__cann_ebpf_wrapped__", False):
        return original

    def execute_model(self: Any, *args: Any, **kwargs: Any) -> Any:
        scheduler_output = kwargs.get("scheduler_output")
        if scheduler_output is None and args:
            scheduler_output = args[0]
        request_ids = _request_ids_from_scheduler_output(scheduler_output)
        if not request_ids:
            return original(self, *args, **kwargs)

        sender = _sender
        if sender is None:
            return original(self, *args, **kwargs)
        pid = os.getpid()
        batch_id = _new_batch_id(pid)
        started = time.monotonic_ns()
        trace = _trace_context()
        begin = _event(
            "batch_begin", started, pid, batch_id,
            phase=_phase_from_scheduler_output(scheduler_output),
            request_ids=request_ids,
            model=_model_name(self),
            source=sender.source,
            **trace,
        )
        try:
            sender.emit(begin)
        except Exception:  # never let observability break inference
            LOG.exception("cann-ebpf context begin failed")
        try:
            return original(self, *args, **kwargs)
        finally:
            try:
                sender.emit(_event(
                    "batch_end", time.monotonic_ns(), pid, batch_id,
                    source=sender.source,
                    trace_id=trace["trace_id"],
                    span_id=trace["span_id"],
                    trace_source=trace["trace_source"],
                ))
            except Exception:
                LOG.exception("cann-ebpf context end failed")

    execute_model.__cann_ebpf_wrapped__ = True  # type: ignore[attr-defined]
    execute_model.__wrapped__ = original  # type: ignore[attr-defined]
    return execute_model


def _load_target() -> tuple[Any, str] | None:
    # Prefer the Ascend runner because it overrides the base execute_model.
    candidates = (
        ("vllm_ascend.worker.model_runner_v1", "NPUModelRunner"),
        ("vllm_ascend.worker.model_runner", "NPUModelRunner"),
        ("vllm.v1.worker.gpu_model_runner", "GPUModelRunner"),
    )
    for module_name, class_name in candidates:
        try:
            module = __import__(module_name, fromlist=[class_name])
            target = getattr(module, class_name)
        except (ImportError, AttributeError):
            continue
        if hasattr(target, "execute_model"):
            return target, f"{module_name}.{class_name}"
    return None


def register() -> bool:
    """vLLM entry point. Returns True if a runner was patched."""
    global _registered, _sender
    with _state_lock:
        if _registered:
            return True
        if os.environ.get("CANN_EBPF_VLLM_DISABLE", "").lower() in {"1", "true", "yes", "on"}:
            LOG.info("cann-ebpf vLLM plugin disabled by environment")
            return False
        socket_path = os.environ.get("CANN_EBPF_VLLM_SOCKET", DEFAULT_SOCKET)
        source = os.environ.get("CANN_EBPF_VLLM_SOURCE", DEFAULT_SOURCE)
        target = _load_target()
        if target is None:
            LOG.warning("cann-ebpf vLLM plugin could not find a model runner")
            return False
        cls, target_name = target
        original = cls.execute_model
        cls.execute_model = _wrap_execute_model(original)
        _sender = _Sender(socket_path, source)
        atexit.register(_shutdown)
        _registered = True
        LOG.info("cann-ebpf vLLM plugin attached to %s socket=%s", target_name, socket_path)
        return True


def _shutdown() -> None:
    global _sender
    sender = _sender
    if sender is not None:
        sender.close()
        _sender = None


def plugin_status() -> dict[str, Any]:
    sender = _sender
    return {
        "registered": _registered,
        "socket": sender.socket_path if sender else None,
        "dropped": sender.dropped if sender else 0,
        "send_errors": sender.send_errors if sender else 0,
    }


__all__ = ["register", "plugin_status", "_request_ids_from_scheduler_output", "_phase_from_scheduler_output"]
