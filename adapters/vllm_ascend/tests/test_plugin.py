import json
import os
import sys
import unittest
from types import SimpleNamespace

sys.path.insert(0, os.path.dirname(os.path.dirname(__file__)))

from cann_ebpf_vllm import plugin


class FakeSchedulerOutput:
    def __init__(self, request_ids, phase="prefill"):
        self.num_scheduled_tokens = {request_id: 1 for request_id in request_ids}
        self.scheduled_new_reqs = []
        self.scheduled_cached_reqs = []
        self.phase = phase


class PluginTests(unittest.TestCase):
    def test_extracts_request_ids_without_payload(self):
        output = FakeSchedulerOutput(["r-1", "r-2"])
        output.prompt = "must never be read"
        self.assertEqual(plugin._request_ids_from_scheduler_output(output), ["r-1", "r-2"])

    def test_extracts_cached_requests(self):
        output = SimpleNamespace(
            num_scheduled_tokens={},
            scheduled_new_reqs=[],
            scheduled_cached_reqs=[SimpleNamespace(req_id="cached-1")],
        )
        self.assertEqual(plugin._request_ids_from_scheduler_output(output), ["cached-1"])

    def test_phase(self):
        self.assertEqual(plugin._phase_from_scheduler_output(FakeSchedulerOutput(["a"])), "prefill")
        decode = FakeSchedulerOutput([], phase="decode")
        self.assertEqual(plugin._phase_from_scheduler_output(decode), "decode")

    def test_event_has_no_payload_fields(self):
        event = plugin._event(
            "batch_begin", 100, 42, "b-1",
            request_ids=["r-1"], source="test", prompt="secret", output="secret",
        )
        # _event only copies the explicit keyword fields supplied by the
        # adapter; arbitrary caller fields are not accepted here.
        self.assertNotIn("prompt", event)
        self.assertNotIn("output", event)
        self.assertEqual(event["type"], "batch_begin")
        json.dumps(event)

    def test_wrapped_execute_model_sends_begin_and_end(self):
        original_sender = plugin._sender
        emitted = []

        class Sender:
            socket_path = "/tmp/test.sock"
            source = "test"
            dropped = 0
            send_errors = 0

            def emit(self, event):
                emitted.append(event)

        class Runner:
            def execute_model(self, scheduler_output):
                return "result"

        plugin._sender = Sender()
        try:
            Runner.execute_model = plugin._wrap_execute_model(Runner.execute_model)
            result = Runner().execute_model(FakeSchedulerOutput(["r-1", "r-2"]))
            self.assertEqual(result, "result")
        finally:
            plugin._sender = original_sender
        self.assertEqual([event["type"] for event in emitted], ["batch_begin", "batch_end"])
        self.assertEqual(emitted[0]["request_ids"], ["r-1", "r-2"])


    def test_traceparent_parser(self):
        trace_id = "a" * 32
        parent_id = "b" * 16
        parsed = plugin._parse_traceparent(f"00-{trace_id}-{parent_id}-01")
        self.assertEqual(parsed, (trace_id, parent_id, "01"))
        for value in (
            "",
            "00-" + trace_id + "-" + parent_id,
            "ff-" + trace_id + "-" + parent_id + "-01",
            "00-" + "0" * 32 + "-" + parent_id + "-01",
            "00-" + trace_id + "-" + "0" * 16 + "-01",
            "00-" + trace_id.upper() + "-" + parent_id + "-01",
        ):
            with self.subTest(value=value):
                self.assertIsNone(plugin._parse_traceparent(value))

    def test_trace_context_uses_traceparent(self):
        trace_id = "c" * 32
        parent_id = "d" * 16
        old_traceparent = os.environ.get("CANN_EBPF_TRACEPARENT")
        old_tracestate = os.environ.get("CANN_EBPF_TRACESTATE")
        os.environ["CANN_EBPF_TRACEPARENT"] = f"00-{trace_id}-{parent_id}-01"
        os.environ["CANN_EBPF_TRACESTATE"] = "vendor=value"
        try:
            context = plugin._trace_context()
        finally:
            if old_traceparent is None:
                os.environ.pop("CANN_EBPF_TRACEPARENT", None)
            else:
                os.environ["CANN_EBPF_TRACEPARENT"] = old_traceparent
            if old_tracestate is None:
                os.environ.pop("CANN_EBPF_TRACESTATE", None)
            else:
                os.environ["CANN_EBPF_TRACESTATE"] = old_tracestate
        self.assertEqual(context["trace_id"], trace_id)
        self.assertEqual(context["parent_span_id"], parent_id)
        self.assertEqual(context["trace_state"], "vendor=value")
        self.assertEqual(context["trace_source"], "vllm_general_plugin+traceparent")
        self.assertEqual(len(context["span_id"]), 16)

    def test_wrapped_execute_model_emits_trace_fields(self):
        original_sender = plugin._sender
        emitted = []

        class Sender:
            socket_path = "/tmp/test.sock"
            source = "test"

            def emit(self, event):
                emitted.append(event)

        class Runner:
            def execute_model(self, scheduler_output):
                return "result"

        plugin._sender = Sender()
        try:
            Runner.execute_model = plugin._wrap_execute_model(Runner.execute_model)
            Runner().execute_model(FakeSchedulerOutput(["r-1"]))
        finally:
            plugin._sender = original_sender
        begin, end = emitted
        self.assertEqual(begin["trace_id"], end["trace_id"])
        self.assertEqual(begin["span_id"], end["span_id"])
        self.assertEqual(len(begin["trace_id"]), 32)
        self.assertEqual(len(begin["span_id"]), 16)
        self.assertEqual(begin["trace_source"], "vllm_general_plugin")
        self.assertNotIn("prompt", begin)
        self.assertNotIn("output", begin)


if __name__ == "__main__":
    unittest.main()
