# B3：V4.1 固定负载开销门控（2026-11-27）

状态：客户端口径 **PASS**，但重复次数少、噪声大，不能证明严格 <3%。

## 1. 环境与方法

- V4.1（Orinfer）`http://127.0.0.1:8000`，模型 `deepseek-v4.1`；
- 未停/未启/未重启任何服务；同一 `orinfer` 进程 2477883 前后存活；
- 2 并发、2 条短 prompt、`max_tokens=32`、每轮 30 秒、off/on 各 2 轮；
- `attach --blocking off/on`，preflight pass=15 warn=0 fail=0；
- off 轮 `blocking attached=0`；on 轮 `attached=4`，offcpu/futex=true；
- health monitor 151 次采样全部 200；每轮客户端错误 0、dropped 0。

## 2. 每轮结果

| 轮次 | client rps | p50 | p95 | agent CPU | RSS | spans | health |
|---|---:|---:|---:|---:|---:|---:|---|
| off#1 | 9.632 | 0.2897s | 0.3428s | 0.83% | 26.3MB | 2,667 | 200→200 |
| off#2 | 7.600 | 0.3296s | 0.4894s | 0.77% | 44.9MB | 2,164 | 200→200 |
| on#1 | 8.126 | 0.3129s | 0.4700s | 0.70% | 42.8MB | 2,045 | 200→200 |
| on#2 | 10.166 | 0.2851s | 0.3462s | 0.77% | 28.2MB | 2,363 | 200→200 |

## 3. 中位数与门控

| 指标 | off | on | 变化 | 阈值 | 结果 |
|---|---:|---:|---:|---:|---|
| client rps | 8.616 | 9.146 | **-6.15%（on 略快）** | 下降 ≤3% | PASS |
| client p95 | 0.4161s | 0.4081s | **-1.92%** | 回归 ≤5% | PASS |
| client p50 | 0.3096s | 0.2990s | -3.42% | — | — |
| agent CPU | 0.80% | 0.735% | -0.065pp | — | — |
| agent RSS | 35.6MB | 35.5MB | 持平 | — | — |

## 4. 重要限制

1. **Orinfer 指标前缀是 `orinfer_*`，不是 `vllm:*`**；脚本的
   `vllm_requests/req_per_s/tokens_per_s/e2e/ttft` 列为 0，内置“服务端 req/s”门控退化。
   上表门控使用客户端 `client_rps` 与 `client_p95`，是直接测量值。
2. **噪声大**：client_rps 的 stdev/median ≈ 11.8%（off 9.632 vs 7.600），
   2 轮中位数无法分辨 3% 的真实开销。**这是 nominal PASS，不是严格 <3% 证明。**
3. 若要硬门控，需要更多轮次（≥5）和/或更长窗口，并固定输入/输出长度、缓存冷热。

## 5. 证据

- CSV：`/opt/cann-ebpf/m2-test-20261126T151851Z/runs/overhead-v41-gate/overhead.csv`
- 汇总：`.../overhead-v41-gate/client_gate_summary.md` / `.json`
- 每轮目录：`.../overhead-v41-gate/{off-1,off-2,on-1,on-2}-*`
