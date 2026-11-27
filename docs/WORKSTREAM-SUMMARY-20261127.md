# 四条线汇总（2026-11-27）

对象：V4 真实报告、HBM 能力、`ops.jsonl`、collector 限制。  
约束：V4 未停机；所有探测只读；未发压测；未改 tracked 代码。

## 0. 一句话结论

> **报告链路能用，但缺图主要是“证据没采到”，不是 UI 没写；**
> **能立刻修的是报告质量与 HBM 小字段；device 三视图和真实 Roofline 都需要维护窗口。**

## 1. 四份详细文档

| 线 | 文档 | 结论 |
|---|---|---|
| A1 HBM | `docs/HBM-CAPABILITY-PROBE.md` | HBM 只有利用率 %，没有绝对 GB/s；ECC/频率/电压/PCIe 错误可采；HCCS 计数可采；风扇不支持 |
| A2 Ops | `docs/OPS-FEASIBILITY.md` | 条件可做，需维护窗口 + vLLM-Ascend profiler 两处补丁；`roofline.go:105` 有单位 bug |
| A3 报告 | `docs/REPORT-QUALITY-FIXLIST.md` | 17 条修复项；P0 是 demo 产物过期，P1 是 off-CPU 4MB/截断不可审计 |
| A4 Collector | `docs/COLLECTOR-GATING-VALIDATION.md` | `MAX_BYTES` 有效；`ONLY_CMDLINE` 无法只命中 Worker；需父进程过滤 |

## 2. P0：现在就做，不需要 V4 停机

| # | 事项 | 位置 | 验收 |
|---|---|---|---|
| 1 | **修 Roofline ridge 单位**：`peakTFLOPS / peakGBPS` 少 ×1000 | `internal/report/roofline.go:105` | 400 TFLOPS / 1600 GB/s → 250 FLOPs/byte；加边界测试 |
| 2 | **重生成 demo 产物**：sweep demo 仍有 41 条 `_created`，attach 仍 11.8MB/28,905 点 | `docs/demo/report-*.html` | 逐份断言 `created/info=0`、点数/体积符合当前默认 |
| 3 | **off-CPU 按 lane 聚合**：20,000 行只为画 14 条，占 DATA 47.8% | `internal/report/offcpu.go`、`build_views_v2.go`、`template.html` | 40,457 行→约 18 行；报告 <6MB |
| 4 | **截断可审计**：950→256 无丢弃名单，字典序把 `proc_sched_running_seconds` 切成 5/18 | `internal/report/build_metrics.go`、`metrics.go` | `DroppedSeries` 名单 + 家族配额；计数一致 |
| 5 | **rebase 后重算计数**：裁点不重算 Rendered/Truncated/Coverage | `internal/report/build_metrics.go` | 页面计数与实际 series 一致 |
| 6 | **collector env 收敛**：`ONLY_CMDLINE` 不设；`MAX_BYTES=64MiB`；`AUTO_STOP=0` | `deploy/b1/stage-collector.sh` | 8 Worker ×64MiB ≈512MiB；不静默漏采 |

## 3. P1：需要小开发或配置

| # | 事项 | 说明 |
|---|---|---|
| 7 | 频率/ECC/电压/PCIe 错误采集 | 纯 `npu-smi -t` 文本解析，4×S；只读先做 |
| 8 | HBM 带宽 % | 只能画 utilization，不是 GB/s；不要硬编码峰值冒充绝对值 |
| 9 | HCCS 计数差分 | 7 lane tx/rx bytes/packets/retry/error；M |
| 10 | `ONLY_PARENT_COMM=VLLM::EngineCor` | 当前 `ONLY_CMDLINE` 在 spawn Worker 上 0 命中；父进程过滤可精确命中 8 Worker |
| 11 | 报告覆盖摘要 | 顶部显示 `4/10 视图、256/950 series、694 dropped`；不能让人以为全有图 |
| 12 | 默认 Top-N / MaxSeries 策略 | 541/950 series 一次全画过重；按 source/家族分组，可展开 |

## 4. P2：需要维护窗口

| 事项 | 为什么必须窗口 |
|---|---|
| `critical_path` / `summed_delay` / `slack_cdf` | collector 必须随 V4 进程启动注入；运行中不能安全热插；需 host+device 同窗口 |
| 真实 `ops.jsonl` | V4 启动脚本未开 profiler；`msprof --pid` 在 CANN 9.0.1 段错误；需重启 + profiler 补丁 + 5–10s 采集 |
| HCCS 带宽主动 profiling | `-t hccs-bw` 需要 `-time` 窗口 |
| 干净 B3 / A-B | 需要排空生产流量或独立实例 |

## 5. 不做 / 不能做

- **风扇**：本机型被动散热，`pwm-mode`/`pwm-duty-ratio` 不支持；
- **降频原因**：无直接字段，只能低置信度推断，不建议做因果图；
- **HBM 绝对 GB/s**：位宽/理论峰值不暴露，只能画利用率 %；
- **逐 request TTFT/ITL/全链路**：按用户要求挂起，跨网关/引擎/OTel，暂不设计；
- **给 unavailable 视图画假图**：不做。

## 6. 需要纠正的旧结论

1. `internal/report/bundle.go`、`bundle_template.html`、`RenderBundle`、`docs/demo/all-reports.html` **在当前仓库和 git 历史里都不存在**；此前基于未跟踪 WIP 的 “XSS/两个画廊重复” 结论作废。
2. `all-charts.html` 仍依赖同目录 4 份 `report-*.html`，而 README 某处写“页面内嵌”，这是真实文档/产物不一致。
3. 视图口径：新 sweep 报告 **4/10 可用**；旧 batch 报告是 3/10，不能混用。
4. 体积口径：18MB 是 batch 旧产物；`.analysis/server-live-20261127/report.html` 是 40.9MB/199,789 span/100k 点；新默认约 8.65MB。

## 7. 建议顺序

1. **P0-1** 修 Roofline ridge（一行 + 测试）；
2. **P0-2** 重生成 demo（否则对外展示仍是旧 bug）；
3. **P0-3/4/5** 报告体积与截断审计；
4. **P0-6** collector env 收敛，先不加 `ONLY_CMDLINE`；
5. **P1** HBM 小字段 + 父进程过滤；
6. **P2** 维护窗口再碰 device/ops。
