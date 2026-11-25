# cann-ebpf 开销报告

## 1. 实验条件

| 项 | 值 |
|---|---|
| run_id | `{{run_id}}` |
| 基线 run | `{{baseline_run_id}}` |
| 负载文件 | `{{workload_file}}` |
| 负载 hash | `{{workload_sha256}}` |
| 并发 | `{{concurrency}}` |
| max_tokens | `{{max_tokens}}` |
| warmup | `{{warmup_s}}` s |
| 每轮时长 | `{{duration_s}}` s |
| 重复次数 | `{{repeats}}` |
| 取样方式 | 中位数 |

## 2. 结果

| 指标 | 基线 | 探针 | 变化 | 门槛 | 判定 |
|---|---:|---:|---:|---:|---|
| 吞吐 req/s | `{{base_rps}}` | `{{probe_rps}}` | `{{delta_pct}}` | ≤3% | `{{verdict}}` |
| p50 ms | `{{base_p50}}` | `{{probe_p50}}` | `{{delta_pct}}` | ≤5% | `{{verdict}}` |
| p95 ms | `{{base_p95}}` | `{{probe_p95}}` | `{{delta_pct}}` | ≤5% | `{{verdict}}` |
| p99 ms | `{{base_p99}}` | `{{probe_p99}}` | `{{delta_pct}}` | ≤5% | `{{verdict}}` |
| agent CPU % | — | `{{agent_cpu_pct}}` | — | ≤`{{max_cpu_pct}}` | `{{verdict}}` |
| agent RSS MB | — | `{{agent_rss_mb}}` | — | ≤`{{max_rss_mb}}` | `{{verdict}}` |
| ringbuf loss % | — | `{{loss_pct}}` | — | ≤0.1% | `{{verdict}}` |

## 3. 自保护动作

| 时间 | 动作 | 原因 | 结果 |
|---|---|---|---|
| `{{ts}}` | `{{action}}` | `{{reason}}` | `{{result}}` |

## 4. 结论

- 是否通过：`{{pass_fail}}`
- 如果不通过：已经执行的降级/关闭动作：`{{mitigation}}`
- 不能省略的 caveat：`{{caveats}}`
