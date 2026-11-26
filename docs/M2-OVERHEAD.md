# M2 开销基线（V4，2026-11-26）

目标：回答 M2 blocking 探针的 host 开销，而不是重复网关吞吐统计。

## 1. 方法

- 主机 `bms-84865758`，V4 生产服务，`--hardware off`；
- 每轮 60 秒采集，`--blocking off` 与 `--blocking on` 各 2 轮；
- 业务吞吐从 vLLM 自身 `/metrics` 读取（`request_success_total`、`prompt_tokens_total`、
  e2e/TTFT 的 count/sum），窗口严格取 `capture_start` → `capture_stop`；
- agent 开销从 `/proc/<attach pid>/stat` 与 `VmRSS` 每 0.5 秒采样；
- health/service 前后检查。

注意：`capture_end` 之后还有进程退出清理（见第 4 节），不计入采集窗口。

## 2. 结果

| mode | window | requests | tokens | req/s | tokens/s | e2e avg | TTFT avg | agent CPU% (单核) | RSS max | spans | dropped |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| off-1 | 60.03s | 6 | 475,669 | 0.100 | 7,924 | 24.70s | 4.55s | 1.35 | 48.4MB | 8,299 | 0 |
| off-2 | 60.01s | 4 | 369,896 | 0.067 | 6,164 | 77.75s | 8.40s | 1.38 | 49.8MB | 8,022 | 0 |
| on-1 | 60.01s | 6 | 101,385 | 0.100 | 1,690 | 51.09s | 2.00s | 1.68 | 49.3MB | 8,164 | 0 |
| on-2 | 60.01s | 9 | 72,480 | 0.150 | 1,208 | 31.30s | 1.14s | 1.60 | 51.5MB | 7,696 | 0 |

中位数：

- `agent_cpu_pct`：off **1.365%**，on **1.64%**，增加 **0.28 个百分点**（相对单核）；
- `agent_rss_max_mb`：off 49.1MB，on 50.4MB，增加约 1.3MB；
- `dropped_events`：全部 0；health 200/200；service active/active；
- spans：off 8,160 / on 7,930，量级一致（差异来自业务流量，不是探针丢事件）。

## 3. 业务吞吐为什么不作结论

V4 是生产服务，60 秒内只有 4–9 个请求，且 prompt/generation 长度差异很大
（tokens 72k–476k）。因此 req/s 与 tokens/s 的波动来自负载本身，不能归因于探针：

- req/s 中位数 off 0.084 → on 0.125（+49.7%）；
- tokens/s 中位数 off 7,044 → on 1,449（-79.4%）。

两者方向相反且样本极少，说明**没有固定负载时 ≤3% 吞吐门禁不可测**。当前结论只能落在
agent 自身开销：+0.28pp 单核 CPU、+1.3MB RSS、0 丢事件，符合低开销预期。

要做正式的 ≤3% 门禁，需要：固定 prompt 集、固定并发、固定 max_tokens、warmup 后
对 on/off 各跑 ≥5 分钟并取中位数（见 `docs/PLAN.md` 14.1）。这需要业务侧配合或维护窗口。

## 4. 运维成本：进程退出清理

在 V4 上，10 个目标 × 37 函数 × 2 probe = 740 个 uprobe 链接：

- 采集窗口本身正常（60 秒）；
- `capture_stop` 后 `closeLinks` 有界等待 1 秒返回；
- 但**进程退出**时内核仍需关闭剩余 perf fd，约 40 秒；期间 health 200、service active、
  uprobe/kprobe 残留最终为 0；
- 因此 `wall_s ≈ setup 1.2s + capture 60s + teardown 1.5s + exit ~48s`。

这不是采集开销，也不会卡住业务，但会影响“命令返回时间”。后续若要缩短，可：
减少 attach 目标（frontend/EngineCore 在本窗口无 span）或改成按库系统级 attach + BPF 侧
PID 过滤（37×库数 而不是 37×进程数）。

## 5. 结论

- M2 blocking 的 agent 开销：**+0.28pp 单核 CPU、+1.3MB RSS、0 丢事件**；
- 业务吞吐门禁未完成：生产流量不可控，必须用固定负载复测；
- 采集窗口、health、service、残留均正常；
- 进程退出清理约 40 秒，是运维体验问题，不是业务影响。

证据：`/root/cann-ebpf/m2-test-20261126T151851Z/overhead-20261126T093729Z.csv` 及 `runs/overhead-*/`。
