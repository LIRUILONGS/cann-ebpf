# 交互式图表报告

打开 [`all-charts.html`](all-charts.html) 即可在浏览器中切换全部报告。每个报告是 `cann-ebpf report` 生成的单文件 HTML，内嵌 `DATA` JSON、Canvas/SVG 和证据门禁；不依赖 CDN，也不会联网。

| 文件 | 数据来源 | 说明 |
|---|---|---|
| `report-vllm-attach.html` | 真实 vLLM-Ascend attach run（28,905 spans） | 固定图表 + vLLM 十视图；无 device/trace 时保持 unavailable |
| `report-host-device.html` | final7 M3 join 验证（11,859 host spans + 148,466 device spans） | 设备四象限、设备忙率、CDF；设备侧为合成 ACL，仅验证 join/UI |
| `report-vllm-sweep.html` | 真实六档并发扫描（c1/c2/c4/c8/c16/c32） | `load_latency` 为 partial，背景负载存在，不计算容量 knee |
| `report-real36-metrics.html` | 36 机器真实 Orinfer 8 请求轻载（16,006 点、541 series） | metrics-only；多指标小倍数、统计表、histogram 窗口分位 |

本地打开：

```bash
# Windows
start docs/demo/all-charts.html

# Linux
xdg-open docs/demo/all-charts.html
```

这些 HTML 只读展示已有报告，不修改 `spans.jsonl`、`device.jsonl`、`hardware.jsonl` 或 `metrics.jsonl`。