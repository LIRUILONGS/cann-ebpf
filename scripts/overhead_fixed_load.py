#!/usr/bin/env python3
"""Fixed-load overhead harness for cann-ebpf.

Runs a client-side fixed workload against a vLLM-Ascend endpoint while
cann-ebpf captures with blocking off/on, then reports throughput, latency,
agent CPU/RSS and ring-buffer loss. This generates real requests: run it only
in an authorized window with a fixed prompt set/concurrency/max_tokens.

Dry-run validates inputs and prints the plan without sending requests:

    python3 scripts/overhead_fixed_load.py --dry-run --prompt-file prompts.jsonl
"""

import argparse
import json
import os
import re
import statistics
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.request
from datetime import datetime, timezone

METRIC_RE = re.compile(r"^(vllm:[a-z0-9_]+)(\{[^}]*\})?\s+([0-9.eE+-]+)$")


def parse_args():
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("--base-url", default="http://127.0.0.1:8905", help="vLLM base URL")
    p.add_argument("--model", default="deepseek-v4-flash", help="served model name")
    p.add_argument("--api-key", default=os.environ.get("VLLM_API_KEY", ""), help="bearer token (or VLLM_API_KEY)")
    p.add_argument("--prompt-file", required=True, help="JSONL ({\"prompt\": ...}) or one prompt per line")
    p.add_argument("--concurrency", type=int, default=4, help="client worker threads")
    p.add_argument("--max-tokens", type=int, default=128, help="fixed max_tokens")
    p.add_argument("--duration", type=int, default=60, help="capture seconds per run")
    p.add_argument("--modes", default="off,on", help="comma-separated blocking modes")
    p.add_argument("--repeats", type=int, default=2, help="runs per mode")
    p.add_argument("--binary", default="./cann-ebpf-linux-arm64-m3", help="cann-ebpf binary")
    p.add_argument("--profile", default="vllm-ascend", help="profile name")
    p.add_argument("--profiles-dir", default="profiles", help="profiles directory")
    p.add_argument("--pid", default="auto", help="target pid or auto")
    p.add_argument("--output-root", default="runs/overhead-fixed", help="evidence output root")
    p.add_argument("--request-timeout", type=int, default=180, help="per-request timeout seconds")
    p.add_argument("--max-throughput-drop-pct", type=float, default=3.0)
    p.add_argument("--max-p95-regression-pct", type=float, default=5.0)
    p.add_argument("--enforce", action="store_true", help="exit 2 when thresholds are exceeded")
    p.add_argument("--yes", action="store_true", help="confirm real load generation")
    p.add_argument("--dry-run", action="store_true", help="validate inputs only")
    return p.parse_args()


def load_prompts(path):
    prompts = []
    with open(path, encoding="utf-8") as handle:
        for line in handle:
            line = line.strip()
            if not line:
                continue
            if line.startswith("{"):
                prompts.append(json.loads(line)["prompt"])
            else:
                prompts.append(line)
    if not prompts:
        raise SystemExit(f"no prompts in {path}")
    return prompts


def now_ns():
    return time.time_ns()


def health(base):
    try:
        with urllib.request.urlopen(base + "/health", timeout=5) as resp:
            return resp.status
    except Exception:
        return 0


def metrics(base):
    try:
        with urllib.request.urlopen(base + "/metrics", timeout=5) as resp:
            text = resp.read().decode()
    except Exception:
        return {}
    values = {}
    for line in text.splitlines():
        match = METRIC_RE.match(line.strip())
        if match:
            values[match.group(1)] = values.get(match.group(1), 0.0) + float(match.group(3))
    return values


def proc_ticks(pid):
    try:
        with open(f"/proc/{pid}/stat") as handle:
            fields = handle.read().rsplit(")", 1)[1].split()
        return int(fields[11]) + int(fields[12])
    except Exception:
        return None


def proc_rss_kb(pid):
    try:
        with open(f"/proc/{pid}/status") as handle:
            for line in handle:
                if line.startswith("VmRSS:"):
                    return int(line.split()[1])
    except Exception:
        pass
    return 0


def marker(path, name):
    try:
        with open(path, errors="ignore") as handle:
            for line in handle:
                if not line.startswith(name + "="):
                    continue
                raw = line.strip().split("=", 1)[1].rstrip("Z")
                if "." in raw:
                    base, frac = raw.split(".", 1)
                    raw = base + "." + frac[:6]
                return datetime.fromisoformat(raw).replace(tzinfo=timezone.utc).timestamp()
    except Exception:
        pass
    return None


def chat_once(args, prompt):
    body = json.dumps({
        "model": args.model,
        "messages": [{"role": "user", "content": prompt}],
        "max_tokens": args.max_tokens,
        "temperature": 0,
        "stream": False,
    }).encode()
    request = urllib.request.Request(args.base_url.rstrip("/") + "/v1/chat/completions", data=body)
    request.add_header("Content-Type", "application/json")
    if args.api_key:
        request.add_header("Authorization", "Bearer " + args.api_key)
    start = time.time()
    with urllib.request.urlopen(request, timeout=args.request_timeout) as resp:
        resp.read()
    return time.time() - start


class Load:
    def __init__(self, args, prompts):
        self.args = args
        self.prompts = prompts
        self.stop = threading.Event()
        self.lock = threading.Lock()
        self.ok = 0
        self.err = 0
        self.latencies = []
        self.threads = []

    def _worker(self, index):
        while not self.stop.is_set():
            prompt = self.prompts[index % len(self.prompts)]
            try:
                elapsed = chat_once(self.args, prompt)
                with self.lock:
                    self.ok += 1
                    self.latencies.append(elapsed)
            except Exception:
                with self.lock:
                    self.err += 1
            index += 1

    def start(self):
        for i in range(self.args.concurrency):
            thread = threading.Thread(target=self._worker, args=(i,), daemon=True)
            thread.start()
            self.threads.append(thread)

    def finish(self):
        self.stop.set()
        for thread in self.threads:
            thread.join(timeout=5)


def run_capture(args, mode, repeat, prompts):
    stamp = time.strftime("%Y%m%dT%H%M%SZ", time.gmtime())
    out = os.path.join(args.output_root, f"{mode}-{repeat}-{stamp}")
    os.makedirs(out, exist_ok=False)
    log_path = os.path.join(out, "collector.log")
    before_health = health(args.base_url)
    load = Load(args, prompts)
    load.start()
    time.sleep(2)  # steady state before attach
    log = open(log_path, "w")
    proc = subprocess.Popen([
        args.binary, "attach", "--pid", args.pid, "--profile", args.profile,
        "--profiles-dir", args.profiles_dir, "--output", out, "--preflight", "warn",
        "--duration", f"{args.duration}s", "--blocking", mode,
        "--hardware", "off", "--no-context",
    ], stdout=log, stderr=subprocess.STDOUT)
    capture_metrics = None
    stop_metrics = None
    started = stopped = False
    first_ticks = stop_ticks = last_ticks = None
    max_rss = 0
    while proc.poll() is None:
        if not started and marker(log_path, "capture_start") is not None:
            started = True
            capture_metrics = metrics(args.base_url)
        if started and not stopped and marker(log_path, "capture_stop") is not None:
            stopped = True
            stop_metrics = metrics(args.base_url)
            stop_ticks = proc_ticks(proc.pid)
        if started:
            ticks = proc_ticks(proc.pid)
            if ticks is not None:
                first_ticks = ticks if first_ticks is None else first_ticks
                last_ticks = ticks
            max_rss = max(max_rss, proc_rss_kb(proc.pid))
        time.sleep(0.5)
    rc = proc.wait()
    log.close()
    load.finish()
    after_health = health(args.base_url)
    stop_time = marker(log_path, "capture_stop") or 0
    window = stop_time - (marker(log_path, "capture_start") or 0)
    if window <= 0:
        window = float(args.duration)
    start_m = capture_metrics or {}
    end_m = stop_metrics or {}
    req = end_m.get("vllm:request_success_total", 0) - start_m.get("vllm:request_success_total", 0)
    tok = end_m.get("vllm:prompt_tokens_total", 0) - start_m.get("vllm:prompt_tokens_total", 0)
    e2e_c = end_m.get("vllm:e2e_request_latency_seconds_count", 0) - start_m.get("vllm:e2e_request_latency_seconds_count", 0)
    e2e_s = end_m.get("vllm:e2e_request_latency_seconds_sum", 0) - start_m.get("vllm:e2e_request_latency_seconds_sum", 0)
    ttft_c = end_m.get("vllm:time_to_first_token_seconds_count", 0) - start_m.get("vllm:time_to_first_token_seconds_count", 0)
    ttft_s = end_m.get("vllm:time_to_first_token_seconds_sum", 0) - start_m.get("vllm:time_to_first_token_seconds_sum", 0)
    hz = os.sysconf("SC_CLK_TCK")
    cpu = 0.0
    if first_ticks is not None and stop_ticks is not None and window > 0:
        cpu = (stop_ticks - first_ticks) / hz / window * 100
    lat = sorted(load.latencies)
    p50 = lat[int(len(lat) * 0.5)] if lat else 0.0
    p95 = lat[min(len(lat) - 1, int(len(lat) * 0.95))] if lat else 0.0
    manifest = {}
    manifest_path = os.path.join(out, "manifest.json")
    if os.path.exists(manifest_path):
        manifest = json.load(open(manifest_path))
    return {
        "mode": mode, "repeat": repeat, "rc": rc,
        "window_s": round(window, 2),
        "client_ok": load.ok, "client_err": load.err,
        "client_rps": round(load.ok / window, 3) if window else 0,
        "vllm_requests": int(req), "vllm_tokens": int(tok),
        "req_per_s": round(req / window, 3) if window else 0,
        "tokens_per_s": round(tok / window, 1) if window else 0,
        "e2e_avg_s": round(e2e_s / e2e_c, 4) if e2e_c else 0,
        "ttft_avg_s": round(ttft_s / ttft_c, 4) if ttft_c else 0,
        "client_p50_s": round(p50, 4), "client_p95_s": round(p95, 4),
        "agent_cpu_pct": round(cpu, 2), "agent_rss_max_mb": round(max_rss / 1024, 1),
        "spans": manifest.get("event_count", 0), "dropped": manifest.get("dropped_events", 0),
        "health_before": before_health, "health_after": after_health,
    }


def main():
    args = parse_args()
    prompts = load_prompts(args.prompt_file)
    modes = [m.strip() for m in args.modes.split(",") if m.strip()]
    if not os.path.exists(args.binary):
        raise SystemExit(f"binary not found: {args.binary}")
    if not args.dry_run and not args.api_key:
        raise SystemExit("--api-key or VLLM_API_KEY is required for real load")
    plan = {
        "base_url": args.base_url, "model": args.model, "prompts": len(prompts),
        "concurrency": args.concurrency, "max_tokens": args.max_tokens,
        "duration_s": args.duration, "modes": modes, "repeats": args.repeats,
        "binary": args.binary, "output_root": args.output_root,
    }
    print("plan:", json.dumps(plan, ensure_ascii=False))
    if args.dry_run:
        print("dry-run ok: no request sent")
        return 0
    if not args.yes:
        raise SystemExit("refusing to generate real load without --yes (authorized window required)")
    rows = []
    for mode in modes:
        for repeat in range(1, args.repeats + 1):
            row = run_capture(args, mode, repeat, prompts)
            rows.append(row)
            print(json.dumps(row, ensure_ascii=False), flush=True)
    csv_path = os.path.join(args.output_root, "overhead.csv")
    with open(csv_path, "w") as handle:
        header = list(rows[0].keys())
        handle.write(",".join(header) + "\n")
        for row in rows:
            handle.write(",".join(str(row[k]) for k in header) + "\n")
    base = [r for r in rows if r["mode"] == "off"]
    treat = [r for r in rows if r["mode"] == "on"]
    if base and treat:
        base_rps = statistics.median(r["client_rps"] for r in base)
        treat_rps = statistics.median(r["client_rps"] for r in treat)
        base_p95 = statistics.median(r["client_p95_s"] for r in base)
        treat_p95 = statistics.median(r["client_p95_s"] for r in treat)
        drop = (base_rps - treat_rps) / base_rps * 100 if base_rps else 0
        p95_reg = (treat_p95 - base_p95) / base_p95 * 100 if base_p95 else 0
        print(f"median client req/s off={base_rps} on={treat_rps} drop={drop:.2f}% (gate uses client load)")
        print(f"median client p95 off={base_p95}s on={treat_p95}s regression={p95_reg:.2f}%")
        if args.enforce and (drop > args.max_throughput_drop_pct or p95_reg > args.max_p95_regression_pct):
            print("FAIL: overhead gate exceeded", file=sys.stderr)
            return 2
    print("CSV=" + csv_path)
    return 0


if __name__ == "__main__":
    sys.exit(main())
