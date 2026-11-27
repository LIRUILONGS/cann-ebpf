# B3：V4 固定负载门控（2026-11-27，V4 不停机）

状态：**严格 p95 阈值不通过，但样本太小、生产流量干扰，不能归因为 agent 开销。**

## 1. 背景

当前 V4 生产负载（测前 10 秒）：

- `num_requests_running=9`，`num_requests_waiting=0`
- `request_success_total` +5 / 10s ≈ **0.5 req/s**
- `generation_tokens_total` +1,486 / 10s ≈ 149 tok/s
- `prompt_tokens_total` +58,767 / 10s

生产流量在波动，不能直接当固定 baseline；因此叠加 1 并发固定请求。

## 2. 方法

- V4 保持运行，不重启、不注入 collector；
- 1 并发、2 条短 prompt、`max_tokens=32`、30 秒；
- off/on 各 3 轮；
- 每轮 attach（off 不采阻塞，on 采 off-CPU/futex）；
- health 前后均 200，dropped 0。

## 3. 结果

| 轮次 | client rps | p50 | p95 | agent CPU | spans | health |
|---|---:|---:|---:|---:|---:|---|
| off#1 | 0.500 | 2.765s | 4.363s | 1.20% | 4,202 | 200 |
| off#2 | 0.400 | 3.227s | 5.150s | 1.33% | 4,160 | 200 |
| off#3 | 0.400 | 3.203s | 4.574s | 1.03% | 3,380 | 200 |
| on#1 | 0.400 | 3.259s | 5.264s | 1.10% | 3,484 | 200 |
| on#2 | 0.433 | 3.292s | 3.760s | 1.13% | 3,400 | 200 |
| on#3 | 0.366 | 3.433s | 5.193s | 1.13% | 3,498 | 200 |

客户端中位数：

| 指标 | off | on | 变化 | 阈值 | 结果 |
|---|---:|---:|---:|---:|---|
| client rps | 0.400 | 0.400 | **0%** | 下降 ≤3% | 通过 |
| client p95 | 4.574s | 5.193s | **+13.5%** | 回归 ≤5% | **不通过** |
| agent CPU | 1.20% | 1.13% | -0.07pp | — | 无上升 |
| dropped | 0 | 0 | — | — | 通过 |

服务端计数（`vllm:request_success_total`）off 21/20/19、on 18/17/20，受生产流量影响，不能作为门禁。

## 4. 结论

- **严格按客户端 p95 +5%，本次不通过；但 30 秒 ×1 并发只有 11–15 个请求，p95 不稳定，且 agent CPU 没有上升。**
- 不能据此说 cann-ebpf 造成 13.5% 延迟回归；也不能说 ≤3% 通过。
- 正式门控需要：固定/隔离流量、≥5 轮、每轮 ≥60 秒、2–4 并发，最好交替 off/on 消除时间漂移。
- 当前 V4 生产负载下，这个测试只能算 smoke，不计入验收。

## 5. 证据

- CSV：`/root/cann-ebpf/m2-test-20261126T151851Z/runs/overhead-v4-gate2/overhead.csv`
- 每轮 run 目录：`.../overhead-v4-gate2/{off,on}-*`
