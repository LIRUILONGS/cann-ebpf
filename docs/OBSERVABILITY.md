# 可观测性接入（Prometheus / Grafana）

日期：2026-11-26
状态：exporter（A6）+ dashboard/告警规则/unit 已提供；未在客户 Prometheus 实际接入。

## 1. 启动 exporter

```bash
# 读取最新完成 run，暴露 /metrics
/opt/cann-ebpf/bin/cann-ebpf metrics \
  --input /var/log/cann-ebpf/runs \
  --listen 127.0.0.1:9101

# 或交给 node_exporter textfile collector
/opt/cann-ebpf/bin/cann-ebpf metrics \
  --input /var/log/cann-ebpf/runs \
  --out /var/lib/node_exporter/text/cann-ebpf.prom
```

systemd：

```bash
install -m 0644 deploy/cann-ebpf-metrics.service /etc/systemd/system/
systemctl daemon-reload
systemctl enable --now cann-ebpf-metrics.service
curl -s http://127.0.0.1:9101/metrics | head
```

`--input` 是 runs 父目录时，每次抓取选择 `manifest.status=complete` 且 `ended_at` 最新的 run；
写入中的 run 会被跳过。

## 2. Prometheus

```yaml
scrape_configs:
  - job_name: cann-ebpf
    scrape_interval: 30s
    static_configs:
      - targets: ["127.0.0.1:9101"]
```

告警规则：`deploy/prometheus/cann-ebpf-rules.yml`

```yaml
rule_files:
  - /etc/prometheus/rules/cann-ebpf-rules.yml
```

包含：丢事件、HBM 余量 <5%、device 匹配率 <90%、host off-CPU >50%、exporter down。

## 3. Grafana

导入 `deploy/grafana/cann-ebpf-dashboard.json`，选择 Prometheus 数据源即可。面板：

- spans / dropped / device match rate / blocking ratios；
- function p95；
- AICore 峰值、HBM 余量；
- category 分布、verdict、blocking 累计。

标签基数受控：function ~37、category ~8、device 8、verdict <10；不含 request_id/batch_id。

## 4. 注意

- 指标是“最新完成 run”的快照，不是历史原始事件；历史由 Prometheus 存储。
- 定时采集会持续产生 run 目录，当前没有自动清理；生产接入前应加保留策略。
- `cann_ebpf_device_*` 需要真实 `device.jsonl`；没有采集器时为 0/unknown，不要据此告警业务。
- 没有 request 级指标，不能把 batch 当请求延迟。
