import time

class SchedulerOutput:
    def __init__(self, ids):
        self.num_scheduled_tokens = {request_id: 1 for request_id in ids}

def main():
    output = SchedulerOutput(["synthetic-req-0001", "synthetic-req-0002", "synthetic-req-0003"])
    deadline = time.time() + 300
    while time.time() < deadline:
        tokens = getattr(output, "num_scheduled_tokens")
        if len(tokens) != 3:
            raise SystemExit("unexpected dict")
        time.sleep(0.01)

if __name__ == "__main__":
    main()
