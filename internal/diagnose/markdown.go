package diagnose

import (
	"fmt"
	"strings"
)

// Markdown renders the human-readable (Chinese) diagnosis. It states the
// evidence boundary explicitly so the report is never mistaken for device-side
// or per-request analysis.
func (r Report) Markdown() string {
	var b strings.Builder
	b.WriteString("# CANN eBPF 低层定位报告\n\n")
	fmt.Fprintf(&b, "- Schema：`%s`\n", ReportSchemaV1)
	fmt.Fprintf(&b, "- 状态：`%s`\n", r.Status)
	fmt.Fprintf(&b, "- 生成时间：`%s`\n", r.GeneratedAt)
	fmt.Fprintf(&b, "- 输入：`%s`\n", r.InputPath)
	b.WriteString("- 原始值以 `spans.jsonl` / `manifest.json` 为准；本文件仅为只读派生报告，不修改原始证据。\n")

	b.WriteString("\n## 运行信息\n\n")
	fmt.Fprintf(&b, "- run_id：`%s`，profile：`%s`，tool：`%s`\n", orUnknown(r.Run.RunID), orUnknown(r.Run.Profile), orUnknown(r.Run.ToolVersion))
	fmt.Fprintf(&b, "- 主机：`%s`，内核：`%s`，manifest 状态：`%s`\n", orUnknown(r.Run.Host), orUnknown(r.Run.Kernel), orUnknown(r.Run.Status))
	fmt.Fprintf(&b, "- 目标 PID：%s\n", joinInts(r.Run.TargetPIDs))
	fmt.Fprintf(&b, "- manifest 事件数：%d，dropped_events：%d\n", r.Run.EventCount, r.Run.DroppedEvents)

	b.WriteString("\n## 时间窗与过滤\n\n")
	fmt.Fprintf(&b, "- 时间窗（monotonic ns，与 span 同域）：`%d` – `%d`（时长 `%d` ns，来源 `%s`）\n",
		r.Window.StartNS, r.Window.EndNS, r.Window.DurationNS, r.Window.Source)
	fmt.Fprintf(&b, "- 过滤：PID=%s TID=%s batch_id=%s request_id=%s trace_id=%s\n",
		joinInts(r.Filters.PIDs), joinInts(r.Filters.TIDs),
		orAny(r.Filters.BatchID), orAny(r.Filters.RequestID), orAny(r.Filters.TraceID))
	fmt.Fprintf(&b, "- 总 span：`%d`，命中 span：`%d`\n", r.SpanCount, r.MatchedSpans)

	if r.Status == StatusEmpty {
		b.WriteString("\n## 数据状态\n\n")
		b.WriteString("没有 span 匹配当前时间窗和过滤条件；未生成任何 count、p50、p95、max 或结论。\n")
		writeUnknowns(&b, r.Unknowns)
		writeWarnings(&b, r.Warnings)
		return b.String()
	}

	b.WriteString("\n## 命中概览\n\n")
	fmt.Fprintf(&b, "- 命中 PID：`%d`，命中 TID：`%d`，唯一 batch：`%d`，唯一 request_id：`%d`\n",
		len(r.PIDs), len(r.TIDs), len(r.Batches), len(r.RequestIDs))
	if len(r.PIDs) > 0 {
		b.WriteString("\n| pid | spans | batch spans | threads | total ns | max ns | thread names |\n|---:|---:|---:|---:|---:|---:|---|\n")
		for _, pid := range r.PIDs {
			fmt.Fprintf(&b, "| %d | %d | %d | %d | %d | %d | %s |\n",
				pid.PID, pid.Spans, pid.BatchSpans, pid.Threads, pid.TotalNS, pid.MaxNS,
				escape(strings.Join(pid.ThreadNames, ", ")))
		}
	}
	if len(r.TIDs) > 0 {
		b.WriteString("\n| tid | pid | spans | total ns | max ns |\n|---:|---:|---:|---:|---:|\n")
		for i, tid := range r.TIDs {
			if i >= 10 {
				break
			}
			fmt.Fprintf(&b, "| %d | %d | %d | %d | %d |\n", tid.TID, tid.PID, tid.Spans, tid.TotalNS, tid.MaxNS)
		}
	}
	if len(r.Batches) > 0 {
		b.WriteString("\n| batch_id | spans | total ns | max ns | requests | pids |\n|---|---:|---:|---:|---:|---|\n")
		for _, batch := range r.Batches {
			fmt.Fprintf(&b, "| `%s` | %d | %d | %d | %s | %s |\n",
				escape(batch.BatchID), batch.Spans, batch.TotalNS, batch.MaxNS,
				escape(strings.Join(batch.RequestIDs, ",")), joinInts(batch.PIDs))
		}
	}
	if len(r.RequestIDs) > 0 {
		fmt.Fprintf(&b, "\n- 命中 request_id：%s\n", escape(strings.Join(r.RequestIDs, ", ")))
	}

	b.WriteString("\n## Top slow function / category\n\n")
	if len(r.Functions) == 0 {
		b.WriteString("未发现 function 统计。\n")
	} else {
		b.WriteString("| function | category | count | p50 ns | p95 ns | max ns | total ns | offcpu ns | futex ns |\n|---|---|---:|---:|---:|---:|---:|---:|---:|\n")
		for _, fn := range r.Functions {
			fmt.Fprintf(&b, "| `%s` | `%s` | %d | %d | %d | %d | %d | %d | %d |\n",
				escape(fn.Function), escape(fn.Category), fn.Count, fn.P50NS, fn.P95NS, fn.MaxNS, fn.TotalNS, fn.OffCPUNS, fn.FutexNS)
		}
	}
	if len(r.Categories) > 0 {
		b.WriteString("\n| category | count | span % | total ns |\n|---|---:|---:|---:|\n")
		for _, cat := range r.Categories {
			fmt.Fprintf(&b, "| `%s` | %d | %.2f%% | %d |\n", escape(cat.Category), cat.Count, cat.SpanPct, cat.TotalNS)
		}
	}

	b.WriteString("\n## host 阻塞迹象（off-CPU / futex）\n\n")
	blk := r.Blocking
	if !blk.Measured {
		b.WriteString("- 本窗口没有 offcpu_ns / futex_ns 采样，无法判断 host 是否被阻塞（原因见“当前无法判定”）。\n")
	} else {
		fmt.Fprintf(&b, "- 统计范围：`%s` span，样本 span `%d/%d`\n", blk.Population, blk.SampledSpans, blk.Spans)
		fmt.Fprintf(&b, "- host wall time：`%d` ns，off-CPU：`%d` ns（%.1f%%），futex：`%d` ns（%.1f%%）\n",
			blk.WallNS, blk.OffCPUNS, blk.OffCPURatio*100, blk.FutexNS, blk.FutexRatio*100)
		if blk.NestedWarning != "" {
			fmt.Fprintf(&b, "- 注意：%s\n", blk.NestedWarning)
		}
		if len(blk.Evidence) > 0 {
			fmt.Fprintf(&b, "- 证据 span：%s\n", escape(strings.Join(blk.Evidence, ", ")))
		}
	}

	b.WriteString("\n## 判定\n\n")
	if len(r.Verdicts) == 0 {
		b.WriteString("无判定。\n")
	} else {
		b.WriteString("| verdict | confidence | reason |\n|---|---:|---|\n")
		for _, verdict := range r.Verdicts {
			fmt.Fprintf(&b, "| `%s` | %.2f | %s |\n", escape(verdict.Verdict), verdict.Confidence, escape(verdict.Reason))
		}
	}

	b.WriteString("\n## 证据 span（慢调用）\n\n")
	if len(r.Evidence) == 0 {
		b.WriteString("没有超过慢阈值的 span。\n")
	} else {
		b.WriteString("| span_id | function | category | pid | tid | start_ns | end_ns | duration_ns | offcpu_ns | futex_ns |\n|---|---|---|---:|---:|---:|---:|---:|---:|---:|\n")
		for _, ev := range r.Evidence {
			fmt.Fprintf(&b, "| `%s` | `%s` | `%s` | %d | %d | %d | %d | %d | %s | %s |\n",
				escape(ev.SpanID), escape(ev.Function), escape(ev.Category), ev.PID, ev.TID,
				ev.StartNS, ev.EndNS, ev.DurationNS, formatOptional(ev.OffCPUNS), formatOptional(ev.FutexNS))
		}
	}

	writeUnknowns(&b, r.Unknowns)
	writeWarnings(&b, r.Warnings)
	return b.String()
}

func writeUnknowns(b *strings.Builder, unknowns []string) {
	b.WriteString("\n## 当前无法判定\n\n")
	if len(unknowns) == 0 {
		b.WriteString("- 无。\n")
		return
	}
	for _, item := range unknowns {
		fmt.Fprintf(b, "- %s\n", item)
	}
}

func writeWarnings(b *strings.Builder, warnings []string) {
	b.WriteString("\n## 数据完整性与警告\n\n")
	if len(warnings) == 0 {
		b.WriteString("- 无。\n")
		return
	}
	for _, item := range warnings {
		fmt.Fprintf(b, "- %s\n", item)
	}
}

func formatOptional(value *int64) string {
	if value == nil {
		return "未测量"
	}
	return fmt.Sprintf("%d", *value)
}

func joinInts(values []int) string {
	if len(values) == 0 {
		return "全部"
	}
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, fmt.Sprintf("%d", value))
	}
	return strings.Join(parts, ",")
}

func orUnknown(value string) string {
	if value == "" {
		return "unknown"
	}
	return value
}

func orAny(value string) string {
	if value == "" {
		return "全部"
	}
	return value
}

func escape(value string) string {
	return strings.ReplaceAll(value, "|", "\\|")
}
