# cann-ebpf 运行摘要

> 由 `cann-ebpf report` 生成。原始数值以 `spans.jsonl` 和 `manifest.json` 为准，本文件只做汇总，不修改证据。

## 1. 运行信息

| 项 | 值 |
|---|---|
| run_id | `{{run_id}}` |
| profile | `{{profile}}` |
| 目标 pid | `{{pid}}` |
| 进程身份 | `{{pid_starttime}}` / boot `{{boot_id}}` |
| 开始/结束 | `{{started_at}}` / `{{ended_at}}` |
| 时长 | `{{duration_s}}` s |
| 合并配置 hash | `{{config_sha256}}` |
| operator | `{{operator}}` |
| 命令 | `{{command}}` |

## 2. 环境与基线

| 项 | 值 |
|---|---|
| 主机 | `{{host}}` |
| 内核 | `{{kernel}}` |
| CANN / 驱动 | `{{cann_version}}` / `{{driver_version}}` |
| NPU | `{{npu_model}}` × `{{npu_count}}` |
| 容器 | `{{container_name}}` / `{{container_image}}` |
| 目标库 | `{{library_path}}`（inode `{{library_inode}}`） |
| 负载 | `{{workload_name}}`，hash `{{workload_sha256}}` |
| 基线窗口 | `{{baseline_start}}`–`{{baseline_end}}` |

## 3. 开销

| 指标 | 基线 | 开探针 | 变化 | 判定 |
|---|---:|---:|---:|---|
| 吞吐 req/s | `{{base_rps}}` | `{{probe_rps}}` | `{{rps_delta_pct}}` | `{{rps_verdict}}` |
| p50 ms | `{{base_p50}}` | `{{probe_p50}}` | `{{p50_delta_pct}}` | `{{p50_verdict}}` |
| p95 ms | `{{base_p95}}` | `{{probe_p95}}` | `{{p95_delta_pct}}` | `{{p95_verdict}}` |
| p99 ms | `{{base_p99}}` | `{{probe_p99}}` | `{{p99_delta_pct}}` | `{{p99_verdict}}` |
| agent CPU % | — | `{{agent_cpu_pct}}` | — | `{{cpu_verdict}}` |
| agent RSS MB | — | `{{agent_rss_mb}}` | — | `{{rss_verdict}}` |
| ringbuf loss % | — | `{{ringbuf_loss_pct}}` | — | `{{loss_verdict}}` |

结论：`{{overhead_verdict}}`

## 4. Top 慢 span

| # | api | p50 | p95 | max | count | verdict | 备注 |
|---|---|---|---|---|---|---|---|
| 1 | `{{api}}` | `{{p50}}` | `{{p95}}` | `{{max}}` | `{{count}}` | `{{verdict}}` | `{{note}}` |

## 5. Verdict 分布

| verdict | 数量 | 占比 | 主要证据 |
|---|---:|---:|---|
| `host_lock_contention` | `{{n}}` | `{{pct}}` | futex/off-CPU |
| `device_bound` | `{{n}}` | `{{pct}}` | msPTI kernel |
| `launch_starvation` | `{{n}}` | `{{pct}}` | device idle |
| `hbm_pressure` | `{{n}}` | `{{pct}}` | GetMemInfo/DCMI |
| `copy_bound` | `{{n}}` | `{{pct}}` | msPTI memcpy |
| `queue_backpressure` | `{{n}}` | `{{pct}}` | ret/inflight |
| `communication_bound` | `{{n}}` | `{{pct}}` | msPTI HCCL |
| `timeout` | `{{n}}` | `{{pct}}` | ret/timeout |

## 6. 典型 case

| 项 | 值 |
|---|---|
| span_id | `{{span_id}}` |
| JSONL 行号 | `{{line_no}}` |
| manifest hash | `{{manifest_sha256}}` |
| attribution.level | `{{attribution_level}}` |
| batch/request | `{{batch_id}}` / `{{request_ids}}` |
| 证据 | `{{evidence}}` |
| 结论 | `{{verdict}}`（confidence `{{confidence}}`） |

## 7. 结论与限制

- 结论：`{{conclusion}}`
- 归因级别：`{{attribution_level}}`；没有 request 上下文时不得写成 request 级。
- 未匹配/丢失：`{{unmatched}}` / `{{lost}}`
- 已知限制：`{{limitations}}`
