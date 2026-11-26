# A6：Prometheus exporter

日期：2026-11-26
状态：代码 + 单测 + V4 证据验证完成。

## 1. 用法

```bash
# 单次输出（可交给 node_exporter textfile collector）
./cann-ebpf-linux-arm64-m3 metrics --input runs/ebpf-xxx
./cann-ebpf-linux-arm64-m3 metrics --input runs/ebpf-xxx --out /var/lib/node_exporter/text/cann-ebpf.prom

# 直接暴露 /metrics；每次抓取时重新读取最新完成的 run
./cann-ebpf-linux-arm64-m3 metrics --input runs --listen 127.0.0.1:9101
curl -s http://127.0.0.1:9101/metrics
```

`--input` 是单个 run（含 `spans.jsonl`）或一个包含多个 run 的父目录；父目录模式选择
`manifest.json` 状态为 `complete` 且 `ended_at` 最新的 run，写入中的 run 会被跳过。

## 2. 指标

| 指标 | 标签 | 含义 |
|---|---|---|
| `cann_ebpf_run_info` | run_id/profile/host/status | 运行身份，值固定 1 |
| `cann_ebpf_spans_total` | run_id | 观测到的 host span 数 |
| `cann_ebpf_dropped_events_total` | run_id | ringbuf 丢失事件 |
| `cann_ebpf_function_duration_seconds` | function/category/quantile=0.5\|0.95\|1.0 | 函数耗时分位（nearest-rank） |
| `cann_ebpf_category_spans_total` | category | 类别 span 数 |
| `cann_ebpf_category_duration_seconds_total` | category | 类别累计耗时 |
| `cann_ebpf_blocking_measured` | — | 是否存在 off-CPU/futex 采样 |
| `cann_ebpf_blocking_offcpu_seconds_total` / `futex_seconds_total` | — | host 阻塞累计 |
| `cann_ebpf_blocking_offcpu_ratio` / `futex_ratio` | — | 阻塞比例 |
| `cann_ebpf_blocking_invalid_samples_total` | — | 被排除的物理不可能样本 |
| `cann_ebpf_hardware_available` | — | 是否有硬件采样 |
| `cann_ebpf_hardware_hbm_free_percent_min` | — | 全设备最低 HBM 余量 |
| `cann_ebpf_hardware_device_hbm_free_percent` | device/name | 每设备 HBM 余量 |
| `cann_ebpf_hardware_device_aicore_percent_max` | device/name | 每设备 AICore 峰值 |
| `cann_ebpf_hardware_device_power_watts` / `temp_celsius` | device/name | 每设备功耗/温度 |
| `cann_ebpf_device_match_rate` / `slow_sync_match_rate` | — | device join 匹配率 |
| `cann_ebpf_device_span_busy_seconds_total` | device | 每设备 busy 并集 |
| `cann_ebpf_verdict` | verdict | 当前 run 的判定（值 1） |
| `cann_ebpf_unknowns_total` / `warnings_total` | — | unknown/警告数量 |

标签基数受控：function 约 37、category 约 8、device 8、verdict 少于 10；不含 request_id/batch_id。

## 3. 验证

V4 真实 run（7,794 spans）：

- `metrics --input <run>` 输出 8KB，包含 run_info/spans/function/category 等；
- `--input runs` 选到最新 `complete` run；
- `--listen 127.0.0.1:19101` 返回 HTTP 200，`grep -c '^cann_ebpf_'` = 69；
- 单测覆盖 label 转义、LatestRun 选择（跳过 running）、空 run 输出。

## 4. 边界

- 只暴露“最新完成 run”的派生值，不做历史时间序列；Prometheus 负责存储与 rate。
- 没有 request 级指标；batch/request 不可拆成请求延迟。
- `device_*` 指标只在存在真实 `device.jsonl` 时非零；合成数据不要用于生产面板。
- 每次抓取会重新读取 spans.jsonl；超大 run 建议用 `diagnose --json` 预生成或给 exporter 加缓存（后续）。
