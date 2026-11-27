# `diagnose` 低层定位命令

状态：代码与单元测试完成（`internal/diagnose` + `cmd/cann-ebpf/diagnose.go`）。本命令只读已有证据，
不 attach、不重启、不修改原始文件，因此在任何环境都可以直接验证，不需要 NPU 授权窗口。

已做过的只读验证：对 M4 交付包里的 V4 真实 evidence 运行通过——10 秒 run（6,991 spans、8 个 Worker）
和 batch 上下文 run（164 spans、1 个 batch、2 个 request ID）都返回 `status=ok`，且旧数据没有
`offcpu_ns`/`futex_ns` 时正确给出 `blocking_unknown` 和 5 条“无法判定”说明。

## 1. 它解决什么

网关负责发现异常并给出时间窗、PID、trace/request 线索；`diagnose` 把这些线索转成低层定位：

- 命中的 Worker / PID / TID；
- 命中的 batch 与 request_id；
- top slow function / category；
- sync/event/submit/memory/hccl 分布；
- 是否存在 host 阻塞迹象（需要 M2 的 off-CPU/futex 采样）；
- 证据 span_id、start_ns/end_ns；
- 当前无法判定的原因（缺少 off-CPU、缺少 msPTI 等）。

它不重复实现网关的 HTTP 延迟、TTFT、QPS 统计。

## 2. 输入

```text
<run 目录>/
  spans.jsonl    # 原始 host span，一行一个对象
  manifest.json  # 运行环境、目标 PID、库 inode、统计与 warnings（可缺，缺则告警）
  hardware.jsonl # 可选：npu-smi 硬件采样（run 级，不逐调用 join）
  device.jsonl   # 可选：M3 设备时间线；有它才输出 device 侧结论
```

`--input` 可以是 run 目录，也可以直接指向 `spans.jsonl`。命令只读取 span 元数据与 request_id，
不读取 prompt、响应、token tensor 或任何业务文本。

## 3. 命令行

```bash
# 整个 run 的默认报告（Markdown 打印到 stdout）
./cann-ebpf diagnose --input runs/ebpf-20261126T000000Z

# 输出到文件
./cann-ebpf diagnose --input runs/ebpf-... --out runs/ebpf-.../diagnose.md

# JSON 报告
./cann-ebpf diagnose --input runs/ebpf-... --json --out runs/ebpf-.../diagnose.json

# 只看某个 Worker 的最后 2 秒
./cann-ebpf diagnose --input runs/ebpf-... --pid 1452 --last 2s

# 只看某个 batch / request / trace
./cann-ebpf diagnose --input runs/ebpf-... --batch-id b-3
./cann-ebpf diagnose --input runs/ebpf-... --request-id chatcmpl-xxxx
./cann-ebpf diagnose --input runs/ebpf-... --trace-id <trace_id>
```

### 参数

| 参数 | 说明 | 默认 |
|---|---|---|
| `--input` / `--run` | run 目录或 `spans.jsonl` | 必填 |
| `--start-ns` / `--end-ns` | 时间窗，单调时钟 ns，与 span 的 `start_ns`/`end_ns` 同域 | 全run |
| `--last` | 以最新 span 结束时间为基准，向前取一段窗口 | 关闭 |
| `--pid` / `--tid` | 逗号分隔 | 全部 |
| `--batch-id` | 精确匹配 `batch_id` | 全部 |
| `--request-id` | `request_ids` 包含该值 | 全部 |
| `--trace-id` | 精确匹配 `trace.trace_id` | 全部 |
| `--top` | 每个排名最多行数 | 10 |
| `--slow-threshold` | 进入证据列表的最小耗时 | 1ms |
| `--offcpu-ratio` | `host_lock_contention` 的 off-CPU/wall 阈值 | 0.5 |
| `--futex-ratio` | `host_lock_contention` 的 futex/wall 阈值 | 0.3 |
| `--hbm-free-pct` | `hbm_pressure` 的 HBM free% 阈值 | 5 |
| `--device-tolerance` | host/device API start 最大偏差 | 100µs |
| `--json` | 输出 JSON（`cann.diagnose.report.v1`） | Markdown |
| `--out` | 写入文件 | stdout |

### 时间窗语义（重要）

span 的 `ts_ns`/`start_ns`/`end_ns` 来自 `bpf_ktime_get_ns()`，是**单调时钟**，不是墙钟。因此：

- `--start-ns`/`--end-ns` 使用与 span 相同的单调时钟数值，不能用 `2026-11-26T12:00:00Z`；
- `--last 2s` 以最新 span 的结束时间向前取 2 秒；
- 时间窗按“区间重叠”判断，跨窗口边界的慢 span 也会被包含。

这样可以避免把墙钟时间误当单调时间导致窗口全错。

## 4. 输出结构（JSON）

Schema 固定为 `cann.diagnose.report.v1`，主要字段：

| 字段 | 内容 |
|---|---|
| `status` | `ok` / `empty` |
| `run` | run_id、profile、tool_version、host、kernel、target_pids、event_count、dropped_events、started/ended |
| `window` | start_ns / end_ns / duration_ns / source（run / explicit / last / empty） |
| `filters` | 实际生效的 PID/TID/batch/request/trace 过滤 |
| `span_count` / `matched_spans` | 总数与命中数 |
| `pids[]` | 每个 PID 的 span 数、batch span 数、线程数、总耗时、最大耗时、线程名 |
| `tids[]` | 每个 TID 的 span 数、总耗时、最大耗时 |
| `batches[]` | batch_id、span 数、耗时、request_ids、PIDs、示例 span_id |
| `request_ids[]` | 命中的 request_id（仅关联键） |
| `functions[]` | function、category、count、p50/p95/max/total、offcpu_ns、futex_ns、blocking_samples |
| `categories[]` | category 分布（count / span_pct / total_ns） |
| `blocking` | off-CPU/futex 采样范围、wall、offcpu、futex、比例、证据 span、嵌套提示 |
| `hardware` | 可选：npu-smi 的每设备 AICore/HBM/功耗/温度与最低 HBM free% |
| `device` | 可选：M3 join 的可匹配设备调用匹配率（`match_rate`）、全量 host 匹配率（`all_host_match_rate`）、时钟偏移、reason 明细、每设备 busy、device verdict 计数 |
| `verdicts[]` | verdict、reason、confidence、evidence |
| `evidence[]` | 慢 span 的 span_id、function、pid/tid、start/end、duration、offcpu/futex |
| `unknowns[]` | 当前无法判定的原因 |
| `warnings[]` | 数据完整性/采集警告 |

Markdown 报告包含同样的信息，并固定声明：原始值以 `spans.jsonl` / `manifest.json` 为准，本文件为派生报告。

## 5. 判定规则

`diagnose` 只在**有 off-CPU/futex 采样**时才给出 host 阻塞判定（M2 数据）。统计范围优先取 `sync`
类别（真正阻塞 host 的调用），没有 sync 时退回全部 span。

| 条件 | verdict |
|---|---|
| 没有 offcpu_ns/futex_ns 采样 | `blocking_unknown` |
| off-CPU/wall ≥ 0.5 且 futex/wall ≥ 0.3 | `host_lock_contention` |
| off-CPU/wall ≥ 0.5 且 futex/wall < 0.3 | `host_blocked_non_futex` |
| off-CPU/wall < 0.2 | `host_cpu_running` |
| 其它 | `host_blocking_mixed` |

硬件层：最低 HBM free% < `--hbm-free-pct` 且存在超过慢阈值的 `category=memory` span 时，
追加 `hbm_pressure`（confidence 0.6）。它只是线索，需要设备侧证据确认。

设备侧：只有存在 `device.jsonl` 且 join 匹配成功时才输出
`device_bound` / `launch_starvation` / `copy_bound` / `communication_bound`；
具体阈值和 reason 见 `docs/M3-DEVICE-JOIN.md`。可匹配设备调用匹配率 <95% 或退化为时间窗时，
结论会降置信度并写入 unknown；`handle`/`memory`/`event` 类 host span 计入 `not_applicable`，不进入分母。

没有 `device.jsonl` 或 join 未匹配时，下列结论**不会**输出，只写入 `unknowns`：

- `device_bound` / `launch_starvation` / `copy_bound` / `communication_bound`；
- 单 request 延迟（batch 是 `SchedulerOutput.num_scheduled_tokens` 窗口）；
- `execute_model` 的 Python 函数级耗时（插件未激活）。

## 6. 退出码

| 码 | 含义 |
|---|---|
| 0 | 成功（包括命中 0 条、`status=empty`，此时会说明原因，不补造数据） |
| 2 | 参数错误（缺 `--input`、非法 PID/TID、负时间窗） |
| 3 | 报告构建失败（如 `--end-ns` 早于 `--start-ns`） |
| 4 | 读取失败（路径不存在、JSONL 无法解析） |

## 7. 边界

- 只读：不 attach、不修改 `spans.jsonl` / `manifest.json`，不覆盖旧证据目录。
- `summary` 之类派生数字只来自原始 span；空数据/缺字段明确报 `unknown`。
- 不把 batch 窗口写成 `execute_model` 函数边界，不把 `duration_ns` 写成 request 延迟或设备时间。
- 嵌套 CANN span 的 off-CPU 时间会重叠，`blocking` 里显式提示不能跨嵌套函数直接相加；
  M2 只对最外层 span 采样，嵌套 span 没有 `offcpu_ns`/`futex_ns` 是设计行为。
- `blocking.invalid_samples` 统计 `offcpu_ns`/`futex_ns` 大于 span `duration_ns` 的物理不可能样本；
  这些值不会进入比例和函数汇总，并会写入 `unknowns`。当前实现实测为 0 条。

## 8. 测试与验证

单元测试：

```powershell
$env:CGO_ENABLED = "0"
go test ./internal/diagnose ./cmd/cann-ebpf
```

覆盖：时间窗（run/explicit/last/非法）、PID/TID/batch/request/trace 过滤、空结果、分类聚合、
阻塞判定四种分支、Markdown 边界声明、run 目录加载与缺 manifest 告警、CLI 写 Markdown/JSON 与退出码。

服务器只读验证（不需要 attach）：

```bash
./cann-ebpf-linux-arm64-m2 diagnose --input /opt/cann-ebpf/runs/<已有 evidence> 
./cann-ebpf-linux-arm64-m2 diagnose --input /opt/cann-ebpf/runs/<...> --json --out /tmp/diagnose.json
```

本机已用 M4 交付包中的真实 evidence 验证过同样的命令：

```powershell
go run ./cmd/cann-ebpf diagnose --input dist/cann-ebpf-m4-delivery-20261126/evidence/runs/ebpf-auto-10s-final-20261126 --out diag-10s.md
go run ./cmd/cann-ebpf diagnose --input dist/cann-ebpf-m4-delivery-20261126/evidence/runs/context-test-final-20261126 --json --out diag-ctx.json
```

服务器（`target-host`）上已用同一个二进制验证：

```bash
./cann-ebpf-linux-arm64-m2 diagnose --input /opt/cann-ebpf/m4-20261126/runs/ebpf-auto-10s-final-20261126 --out diag-m4.md
./cann-ebpf-linux-arm64-m2 diagnose --input /opt/cann-ebpf/m2-test-20261126T151851Z/runs/m2-blocking-10s-20261126T073824Z --pid 1452 --last 2s
```

结果：历史 6,991-span run 与 164-span batch run 都是 `status=ok`；最新 M2 run 上
`--pid 1452 --last 2s` 命中 67 条 span，窗口来源为 `last`。详见 `docs/M2-VALIDATION.md`。
