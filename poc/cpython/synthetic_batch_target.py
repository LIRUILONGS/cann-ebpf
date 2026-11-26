import os
import time


class SchedulerOutput:
    def __init__(self, request_ids):
        self.num_scheduled_tokens = {request_id: 1 for request_id in request_ids}


def main():
    with open("/tmp/synthetic_batch.pid", "w", encoding="ascii") as handle:
        handle.write(str(os.getpid()))
    deadline = time.time() + 300
    sequence = 0
    while time.time() < deadline:
        for _ in range(2):
            request_ids = [
                f"synthetic-batch-{sequence:04d}-req-{index}"
                for index in range(1, 4)
            ]
            output = SchedulerOutput(request_ids)
            for _ in range(2):
                tokens = getattr(output, "num_scheduled_tokens")
                if len(tokens) != 3:
                    raise SystemExit("unexpected dict")
            sequence += 1
        time.sleep(0.05)


if __name__ == "__main__":
    main()
