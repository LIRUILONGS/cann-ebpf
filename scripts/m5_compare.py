#!/usr/bin/env python3
"""M5 A/B compare for two cann-ebpf runs.

Runs `cann-ebpf diagnose --json` on each run (read-only) and renders a compare
report. A/B conclusions are only valid for the same workload/concurrency/
duration; the report states that explicitly and never invents request latency.
"""

import argparse
import json
import os
import subprocess
import sys
import tempfile


def parse_args():
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("--run-a", required=True, help="run directory for A (e.g. V4)")
    p.add_argument("--run-b", required=True, help="run directory for B (e.g. Orinfer)")
    p.add_argument("--label-a", default="A", help="label for A")
    p.add_argument("--label-b", default="B", help="label for B")
    p.add_argument("--binary", default="./cann-ebpf-linux-arm64-m3", help="cann-ebpf binary")
    p.add_argument("--out", default="compare.md", help="output markdown path")
    p.add_argument("--dry-run", action="store_true", help="validate inputs without running diagnose")
    return p.parse_args()


def diagnose(binary, run_dir):
    with tempfile.NamedTemporaryFile(suffix=".json", delete=False) as tmp:
        path = tmp.name
    try:
        subprocess.run([binary, "diagnose", "--input", run_dir, "--json", "--out", path],
                       check=True, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
        with open(path, encoding="utf-8") as handle:
            return json.load(handle)
    finally:
        try:
            os.unlink(path)
        except OSError:
            pass


def fmt(value, digits=2):
    if value is None:
        return "unknown"
    if isinstance(value, float):
        return f"{value:.{digits}f}"
    return str(value)


def render(a, b, label_a, label_b):
    lines = []
    lines.append(f"# M5 A/B 对比：{label_a} vs {label_b}\n")
    lines.append("> 仅当两边使用相同 workload、并发、max_tokens、duration 时，本对比才有效。")
    lines.append("> 本报告来自 `diagnose` 派生数据，原始值以各自 `spans.jsonl`/`manifest.json` 为准。\n")

    lines.append("## 运行信息\n")
    lines.append("| 项 | " + label_a + " | " + label_b + " |")
    lines.append("|---|---|---|")
    for key, getter in [
        ("run_id", lambda r: r.get("run", {}).get("run_id")),
        ("profile", lambda r: r.get("run", {}).get("profile")),
        ("host", lambda r: r.get("run", {}).get("host")),
        ("kernel", lambda r: r.get("run", {}).get("kernel")),
        ("spans", lambda r: r.get("span_count")),
        ("matched_spans", lambda r: r.get("matched_spans")),
        ("dropped_events", lambda r: r.get("run", {}).get("dropped_events")),
        ("window_ns", lambda r: r.get("window", {}).get("duration_ns")),
    ]:
        lines.append(f"| {key} | {fmt(getter(a))} | {fmt(getter(b))} |")

    lines.append("\n## 类别分布\n")
    lines.append(f"| category | {label_a} count | {label_a} total_ns | {label_b} count | {label_b} total_ns |")
    lines.append("|---|---:|---:|---:|---:|")
    cats = {}
    for report, prefix in ((a, "a"), (b, "b")):
        for cat in report.get("categories") or []:
            entry = cats.setdefault(cat["category"], {})
            entry[prefix] = (cat["count"], cat["total_ns"])
    for name in sorted(cats):
        ca = cats[name].get("a", (0, 0))
        cb = cats[name].get("b", (0, 0))
        lines.append(f"| `{name}` | {ca[0]} | {ca[1]} | {cb[0]} | {cb[1]} |")

    lines.append("\n## Top functions（按 total_ns）\n")
    lines.append(f"| function | {label_a} count | {label_a} p50 ns | {label_a} p95 ns | {label_b} count | {label_b} p50 ns | {label_b} p95 ns |")
    lines.append("|---|---:|---:|---:|---:|---:|---:|")
    funcs = {}
    for report, prefix in ((a, "a"), (b, "b")):
        for fn in report.get("functions") or []:
            funcs.setdefault(fn["function"], {})[prefix] = fn
    ordered = sorted(funcs, key=lambda name: -(funcs[name].get("a", {}).get("total_ns", 0) + funcs[name].get("b", {}).get("total_ns", 0)))
    for name in ordered[:10]:
        fa = funcs[name].get("a", {})
        fb = funcs[name].get("b", {})
        lines.append(f"| `{name}` | {fa.get('count',0)} | {fa.get('p50_ns',0)} | {fa.get('p95_ns',0)} | {fb.get('count',0)} | {fb.get('p50_ns',0)} | {fb.get('p95_ns',0)} |")

    lines.append("\n## host 阻塞\n")
    lines.append(f"| 指标 | {label_a} | {label_b} |")
    lines.append("|---|---:|---:|")
    for key in ("measured", "offcpu_ratio", "futex_ratio", "offcpu_ns", "futex_ns"):
        lines.append(f"| {key} | {fmt((a.get('blocking') or {}).get(key))} | {fmt((b.get('blocking') or {}).get(key))} |")

    lines.append("\n## 硬件层\n")
    lines.append(f"| 指标 | {label_a} | {label_b} |")
    lines.append("|---|---:|---:|")
    for key in ("available", "hbm_free_min_pct", "aicore_max_pct"):
        lines.append(f"| {key} | {fmt((a.get('hardware') or {}).get(key))} | {fmt((b.get('hardware') or {}).get(key))} |")

    lines.append("\n## 设备时间线 join\n")
    dev_a = a.get("device") or {}
    dev_b = b.get("device") or {}
    lines.append(f"| 指标 | {label_a} | {label_b} |")
    lines.append("|---|---:|---:|")
    for key in ("matched_host_spans", "match_rate", "slow_sync_match_rate", "clock_offset_ns", "time_window_fallback"):
        lines.append(f"| {key} | {fmt(dev_a.get(key))} | {fmt(dev_b.get(key))} |")

    lines.append("\n## 判定\n")
    for label, report in ((label_a, a), (label_b, b)):
        verdicts = report.get("verdicts") or []
        text = ", ".join(f"`{v['verdict']}`({fmt(v.get('confidence'))})" for v in verdicts) or "无"
        lines.append(f"- {label}：{text}")

    lines.append("\n## 限制\n")
    lines.append("- 没有单 request 延迟；batch 是调度窗口，不是逐请求执行时长。")
    lines.append("- 没有 `execute_model` 函数级耗时（插件未激活，按约定不做）。")
    lines.append("- 设备结论只在有真实 `device.jsonl` 且 join 匹配时成立；合成数据不能作为性能结论。")
    lines.append("- 吞吐门禁需要固定负载（见 `scripts/overhead_fixed_load.py`），本报告不做 ≤3% 判定。")
    for label, report in ((label_a, a), (label_b, b)):
        unknowns = report.get("unknowns") or []
        if unknowns:
            lines.append(f"- {label} unknowns：{'；'.join(unknowns[:3])}")
    return "\n".join(lines) + "\n"


def main():
    args = parse_args()
    for path in (args.run_a, args.run_b):
        if not os.path.isdir(path) or not os.path.isfile(os.path.join(path, "spans.jsonl")):
            raise SystemExit(f"run directory missing spans.jsonl: {path}")
    if not os.path.exists(args.binary):
        raise SystemExit(f"binary not found: {args.binary}")
    print(f"plan: A={args.run_a} B={args.run_b} label={args.label_a}/{args.label_b} out={args.out}")
    if args.dry_run:
        print("dry-run ok: diagnose not executed")
        return 0
    report_a = diagnose(args.binary, args.run_a)
    report_b = diagnose(args.binary, args.run_b)
    markdown = render(report_a, report_b, args.label_a, args.label_b)
    with open(args.out, "w", encoding="utf-8") as handle:
        handle.write(markdown)
    print(f"written {args.out} ({len(markdown)} bytes)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
