# report-views（实验）

这些是**不可用视图**的 builder 和测试，已从生产 `internal/report` 移出：

- `criticalpath.go`：关键路径瀑布（需要 host+device 同窗口）
- `slack.go`：Summed Delay / Slack CDF
- `roofline.go`：Roofline（需要真实 `ops.jsonl`）
- `compare.go`：A/B 差分（需要两次同 workload run）
- `changepoint.go`：变化点（需要 ≥5 分钟序列）
- 对应测试

生产报告只保留：

- `load_latency`
- `off_cpu`
- `operator_treemap`（inferred）
- `service_topology`（partial）

这些实验文件不参与 `go build ./...`，不要在生产报告里恢复卡片。
