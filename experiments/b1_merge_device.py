#!/usr/bin/env python3
"""Merge per-rank device-<pid>.jsonl files produced by the B1 LD_PRELOAD setup.

Each vLLM worker writes its own file (CANN_MSPTI_OUTPUT contains {pid}); this
tool concatenates them into one device.jsonl + meta.json that `attach
--device-jsonl` can import.
"""

import argparse
import glob
import json
import os
import sys
from datetime import datetime, timezone


def parse_args():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--input-dir", required=True, help="directory containing device-<pid>.jsonl")
    p.add_argument("--out", required=True, help="output directory for merged device.jsonl")
    p.add_argument("--run-id", default="b1-merged", help="merged run id")
    p.add_argument("--dry-run", action="store_true")
    return p.parse_args()


def main():
    args = parse_args()
    files = sorted(glob.glob(os.path.join(args.input_dir, "device-*.jsonl")))
    if not files:
        raise SystemExit(f"no device-*.jsonl under {args.input_dir}")
    print(f"inputs={len(files)}")
    for path in files:
        print("  " + path)
    if args.dry_run:
        print("dry-run: no output written")
        return 0

    os.makedirs(args.out, exist_ok=True)
    out_path = os.path.join(args.out, "device.jsonl")
    kinds = {}
    records = 0
    skipped = 0
    first_ns = None
    last_ns = None
    with open(out_path, "w", encoding="utf-8") as out:
        for path in files:
            with open(path, encoding="utf-8-sig", errors="replace") as handle:
                for line in handle:
                    line = line.strip()
                    if not line:
                        continue
                    try:
                        record = json.loads(line)
                    except Exception:
                        skipped += 1
                        continue
                    if record.get("schema") != "cann.device.v1":
                        skipped += 1
                        continue
                    out.write(json.dumps(record, ensure_ascii=False) + "\n")
                    records += 1
                    kind = record.get("kind", "?")
                    kinds[kind] = kinds.get(kind, 0) + 1
                    start = record.get("start_ns")
                    end = record.get("end_ns")
                    if isinstance(start, int):
                        first_ns = start if first_ns is None else min(first_ns, start)
                    if isinstance(end, int):
                        last_ns = end if last_ns is None else max(last_ns, end)
    meta = {
        "schema": "cann.mspti.collector.v1",
        "run_id": args.run_id,
        "activities": "api,acl_api,runtime_api,kernel,memcpy,hccl",
        "clock": "monotonic",
        "source_files": [os.path.basename(p) for p in files],
        "records_written": records,
        "records_skipped": skipped,
        "kinds": ",".join(f"{k}={v}" for k, v in sorted(kinds.items())),
        "start_ns": first_ns,
        "stop_ns": last_ns,
        "merged_at": datetime.now(timezone.utc).isoformat(),
        "warnings": "",
    }
    meta_path = out_path + ".meta.json"
    with open(meta_path, "w", encoding="utf-8") as handle:
        json.dump(meta, handle, ensure_ascii=False, indent=2)
        handle.write("\n")
    print(f"wrote {out_path}: records={records} skipped={skipped} kinds={kinds}")
    print(f"wrote {meta_path}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
