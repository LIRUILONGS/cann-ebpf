#!/usr/bin/env python3
"""Fixed-duration vLLM load generator for concurrency sweep evidence.

The client records only request outcomes and wall-clock latency. Throughput and
histogram percentiles should come from the server-side metrics captured by
``cann-ebpf sample``. The bearer token is read from VLLM_API_KEY (or --api-key)
and is never written to the output.
"""

import argparse
import json
import os
import sys
import threading
import time
import urllib.error
import urllib.request
from datetime import datetime, timezone


def parse_args():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base-url", default="http://127.0.0.1:8905")
    parser.add_argument("--model", default="deepseek-v4-flash")
    parser.add_argument("--api-key", default=os.environ.get("VLLM_API_KEY", ""))
    parser.add_argument("--prompt", default="Summarize why operators should use fixed workload sweeps.")
    parser.add_argument("--concurrency", type=int, required=True)
    parser.add_argument("--duration", type=int, default=30)
    parser.add_argument("--max-tokens", type=int, default=64)
    parser.add_argument("--request-timeout", type=int, default=180)
    parser.add_argument("--out", required=True)
    return parser.parse_args()


def utc_now():
    return datetime.now(timezone.utc).isoformat().replace("+00:00", "Z")


def chat_once(args, prompt):
    body = json.dumps({
        "model": args.model,
        "messages": [{"role": "user", "content": prompt}],
        "max_tokens": args.max_tokens,
        "temperature": 0,
    }).encode()
    request = urllib.request.Request(
        args.base_url.rstrip("/") + "/v1/chat/completions",
        data=body,
        method="POST",
        headers={
            "Content-Type": "application/json",
            "Authorization": "Bearer " + args.api_key,
        },
    )
    started = time.monotonic()
    with urllib.request.urlopen(request, timeout=args.request_timeout) as response:
        payload = json.loads(response.read().decode("utf-8"))
        status = response.status
    return status, time.monotonic() - started, payload


class LoadState:
    def __init__(self):
        self.lock = threading.Lock()
        self.ok = 0
        self.errors = 0
        self.latencies = []
        self.error_samples = []
        self.completion_tokens = 0

    def success(self, status, latency, payload):
        usage = payload.get("usage") or {}
        with self.lock:
            self.ok += 1
            self.latencies.append(latency)
            self.completion_tokens += int(usage.get("completion_tokens") or 0)

    def failure(self, error):
        with self.lock:
            self.errors += 1
            if len(self.error_samples) < 5:
                self.error_samples.append(str(error)[:500])


def worker(args, prompt, deadline, state):
    while time.monotonic() < deadline:
        try:
            status, latency, payload = chat_once(args, prompt)
        except (urllib.error.URLError, TimeoutError, OSError, ValueError) as error:
            state.failure(error)
            continue
        state.success(status, latency, payload)


def percentile(values, fraction):
    if not values:
        return 0.0
    ordered = sorted(values)
    index = min(len(ordered) - 1, max(0, int(fraction * len(ordered))))
    return ordered[index]


def main():
    args = parse_args()
    if args.concurrency <= 0 or args.duration <= 0 or args.max_tokens <= 0:
        raise SystemExit("concurrency, duration and max-tokens must be positive")
    if not args.api_key:
        raise SystemExit("VLLM_API_KEY or --api-key is required")

    state = LoadState()
    started_wall = utc_now()
    started = time.monotonic()
    deadline = started + args.duration
    threads = [
        threading.Thread(target=worker, args=(args, args.prompt, deadline, state), daemon=True)
        for _ in range(args.concurrency)
    ]
    for thread in threads:
        thread.start()
    for thread in threads:
        thread.join()
    elapsed = time.monotonic() - started

    result = {
        "schema": "cann.vllm.loadclient.v1",
        "started_at": started_wall,
        "finished_at": utc_now(),
        "base_url": args.base_url,
        "model": args.model,
        "prompt_chars": len(args.prompt),
        "concurrency": args.concurrency,
        "duration_s": args.duration,
        "max_tokens": args.max_tokens,
        "elapsed_s": round(elapsed, 3),
        "ok": state.ok,
        "errors": state.errors,
        "client_rps": round(state.ok / elapsed, 3) if elapsed > 0 else 0.0,
        "completion_tokens": state.completion_tokens,
        "latency_p50_s": round(percentile(state.latencies, 0.50), 4),
        "latency_p95_s": round(percentile(state.latencies, 0.95), 4),
        "error_samples": state.error_samples,
    }
    with open(args.out, "w", encoding="utf-8") as handle:
        json.dump(result, handle, indent=2, ensure_ascii=False)
        handle.write("\n")
    print(json.dumps({key: result[key] for key in ("concurrency", "ok", "errors", "client_rps", "latency_p95_s")}, ensure_ascii=False))
    return 0 if state.errors == 0 else 3


if __name__ == "__main__":
    sys.exit(main())
