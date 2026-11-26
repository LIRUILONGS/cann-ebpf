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


if __name__ == "__main__":
    unittest.main()
