#!/usr/bin/env python3
"""Send a synthetic batch window over the documented context protocol."""

import argparse
import json
import os
import socket
import time


def send(sock, event):
    wire = json.dumps(event, separators=(",", ":")).encode("utf-8") + b"\n"
    sock.sendall(wire)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--socket", required=True)
    parser.add_argument("--pid", required=True, type=int)
    parser.add_argument("--batch-id", default="ctx-smoke-1")
    parser.add_argument("--trace-id", default="5" * 32)
    parser.add_argument("--span-id", default="6" * 16)
    parser.add_argument("--hold-seconds", default=1.2, type=float)
    parser.add_argument("--uid", type=int)
    parser.add_argument("--gid", type=int)
    args = parser.parse_args()

    if args.gid is not None:
        os.setgroups([])
        os.setgid(args.gid)
    if args.uid is not None:
        os.setuid(args.uid)

    sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    last_error = None
    for _ in range(80):
        try:
            sock.connect(args.socket)
            break
        except OSError as exc:
            last_error = exc
            time.sleep(0.1)
    else:
        raise SystemExit("connect failed: %s" % last_error)

    begin = {
        "v": 1,
        "type": "batch_begin",
        "ts_ns": time.monotonic_ns(),
        "pid": args.pid,
        "batch_id": args.batch_id,
        "phase": "decode",
        "request_ids": ["req-smoke-1", "req-smoke-2"],
        "source": "protocol_smoke",
        "trace_id": args.trace_id,
        "span_id": args.span_id,
        "trace_source": "protocol_smoke",
    }
    send(sock, begin)
    time.sleep(args.hold_seconds)
    end = {
        "v": 1,
        "type": "batch_end",
        "ts_ns": time.monotonic_ns(),
        "pid": args.pid,
        "batch_id": args.batch_id,
        "source": "protocol_smoke",
        "trace_id": args.trace_id,
        "span_id": args.span_id,
        "trace_source": "protocol_smoke",
    }
    send(sock, end)
    time.sleep(0.2)
    sock.close()


if __name__ == "__main__":
    main()
