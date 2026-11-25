# Orinfer vs vLLM-Ascend 对比报告

## 1. 可比性

| 项 | Orinfer | vLLM-Ascend | 是否一致 |
|---|---|---|---|
| 模型 | `{{model}}` | `{{model}}` | `{{same}}` |
| 量化 | `{{quant}}` | `{{quant}}` | `{{same}}` |
| 负载 hash | `{{workload_sha256}}` | `{{workload_sha256}}` | `{{same}}` |
| 并发 | `{{concurrency}}` | `{{concurrency}}` | `{{same}}` |
| max_tokens | `{{max_tokens}}` | `{{max_tokens}}` | `{{same}}` |
| 采集时长 | `{{duration_s}}` | `{{duration_s}}` | `{{same}}` |

不同 workload 的对比不成立；任一不一致项必须在结论里标红。

## 2. Host 侧

| 指标 | Orinfer | vLLM-Ascend | 差异 | 备注 |
|---|---:|---:|---:|---|
| Sync p95 ms | `{{x}}` | `{{y}}` | `{{delta}}` | `{{note}}` |
| submit p95 µs | `{{x}}` | `{{y}}` | `{{delta}}` | `{{note}}` |
| off-CPU % | `{{x}}` | `{{y}}` | `{{delta}}` | `{{note}}` |
| futex % | `{{x}}` | `{{y}}` | `{{delta}}` | `{{note}}` |

## 3. Device 侧

| 指标 | Orinfer | vLLM-Ascend | 差异 | 备注 |
|---|---:|---:|---:|---|
| kernel busy % | `{{x}}` | `{{y}}` | `{{delta}}` | `{{note}}` |
| memcpy ns | `{{x}}` | `{{y}}` | `{{delta}}` | `{{note}}` |
| HCCL ns | `{{x}}` | `{{y}}` | `{{delta}}` | `{{note}}` |
| HBM free % | `{{x}}` | `{{y}}` | `{{delta}}` | `{{note}}` |

## 4. Verdict 对比

| verdict | Orinfer | vLLM-Ascend | 说明 |
|---|---:|---:|---|
| `host_lock_contention` | `{{n}}` | `{{n}}` | `{{note}}` |
| `device_bound` | `{{n}}` | `{{n}}` | `{{note}}` |
| `launch_starvation` | `{{n}}` | `{{n}}` | `{{note}}` |
| `queue_backpressure` | `{{n}}` | `{{n}}` | `{{note}}` |

## 5. 结论

- 哪个框架在当前 workload 下更稳：`{{winner}}`
- 差异来自 host、device 还是 batching：`{{root_cause}}`
- 证据引用：`{{run_id}} + JSONL 行号 + manifest hash`
- 不可比/不确定项：`{{caveats}}`
