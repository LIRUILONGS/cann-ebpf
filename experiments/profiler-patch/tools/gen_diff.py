#!/usr/bin/env python3
"""Regenerate the two .diff files in patches/vllm-ascend-profiler/.

The diff headers use the real in-container / on-host target paths so the patch
is self-describing; it is not applied by path from this repo (the apply.sh
script installs the patched files directly).
"""
from __future__ import annotations

import difflib
import pathlib

HERE = pathlib.Path(__file__).resolve().parent.parent

PAIRS = [
    (
        "orig/torch_npu_profiler.py",
        "files/torch_npu_profiler.py",
        "vllm-ascend-profiler.diff",
        "vllm_ascend/profiler/torch_npu_profiler.py",
    ),
    (
        "orig/run_vllm_ds4_8905.sh",
        "files/run_vllm_ds4_8905.sh",
        "run_vllm_ds4_8905.sh.diff",
        "usr/local/libexec/run_vllm_ds4_8905.sh",
    ),
]


def main() -> None:
    for orig, patched, out_name, target in PAIRS:
        a = (HERE / orig).read_text(encoding="utf-8").splitlines(keepends=True)
        b = (HERE / patched).read_text(encoding="utf-8").splitlines(keepends=True)
        diff = difflib.unified_diff(
            a,
            b,
            fromfile=f"a/{target}",
            tofile=f"b/{target}",
            n=3,
        )
        text = "".join(diff)
        (HERE / out_name).write_text(text, encoding="utf-8", newline="\n")
        print(f"wrote {out_name}: +{sum(1 for l in text.splitlines() if l.startswith('+') and not l.startswith('+++'))} "
              f"-{sum(1 for l in text.splitlines() if l.startswith('-') and not l.startswith('---'))} lines")


if __name__ == "__main__":
    main()
