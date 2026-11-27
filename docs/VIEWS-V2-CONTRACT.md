# vLLM 十视图 JSON 契约（M0）

状态：M0 契约已冻结；M1–M4 已实现并完成真实 vLLM 接入。实现代码：`internal/report/views_v2.go`、`internal/report/build_views_v2.go`。
真实验收记录：`docs/M4-VIEWS2-VALIDATION.md`。

## 1. 顶层对象

`report.Data` 新增字段：

```json
{
  "views_v2": {
    "load_latency": {},
    "critical_path": {},
    "summed_delay": {},
    "slack_cdf": {},
    "off_cpu": {},
    "operator_treemap": {},
    "roofline": {},
    "ab_diff": {},
    "change_point": {},
    "service_topology": {}
  }
}
```

字段名只增不改；删除或改变语义必须升 `views_v2` 版本。每个视图都带：

```json
{
  "status": {
    "available": false,
    "reason": "not implemented",
    "source": "none",
    "confidence": ""
  }
}
```

`confidence` 只允许 `exact`、`inferred`、`partial` 或空。`available=false` 时不能声称 confidence。

## 2. 视图字段

| JSON key | 关键字段 | 主要证据 | 不可用条件 |
|---|---|---|---|
| `load_latency` | `workload_id`、`knee_concurrency`、`points[]`（concurrency、qps、ttft_p50/p95、latency_p50/p95/p99、running_max、waiting_max、kv_cache_max、npu_aicore_mean） | `sweep.json` + 每档 run 的 metrics | 少于 4 个并发档位或 workload_id 不一致 |
| `critical_path` | `batch_id`、`request_id`、`trace_id`、`nodes[]`、`critical_path[]`、`total_ms`、`coverage` | spans + device + batch/request context | 无 batch/request/trace 上下文或无可关联 device |
| `summed_delay` | `sum_ms`、`critical_path_ms`、`overlap_ms`、`parallelism`、`nodes_by_category[]` | 同 Critical Path DAG | Critical Path 不可用 |
| `slack_cdf` | `points[]`（slack_ms、count、cumulative）、`min_ms`、`p50_ms`、`p95_ms`、`negative_count` | Critical Path DAG 的 earliest/latest start | DAG 不完整或样本小于 30 |
| `off_cpu` | `mode`（aggregate/exact）、`rows[]`（lane、start_ms、end_ms、on_cpu_ms、off_cpu_ms、futex_ms、segments[]） | span 内 `offcpu_ns/futex_ns`；精确模式需要 off-CPU 区间 | 无 blocking 采样 |
| `operator_treemap` | `root`（name、category、kind、device、stream、duration_ms、count、children） | msPTI KERNEL/MEMCPY/HCCL；无 device 时 host fallback | 无任何可聚合调用数据 |
| `roofline` | `points[]`（op、category、duration_ms、flops、bytes、arithmetic_intensity、achieved_tflops、achieved_gbps、bound）、`peak_tflops`、`peak_gbps`、`dtype` | `ops.jsonl` + 910B2 峰值参数 | 无 per-op FLOPs/bytes；禁止用理论值补点 |
| `ab_diff` | `a_run`、`b_run`、`workload_id`、`metrics[]`、`functions[]`、`significant[]` | 两个完整 run 的 spans/metrics | workload_id 不一致或 run 不完整 |
| `change_point` | `series[]`、`points[]`（time_ms、metric、before、after、delta_pct、score、method）、`events[]`、`method`、`min_samples` | 长时序 metrics + `events.jsonl` | run 小于 5 分钟或有效点小于 30 |
| `service_topology` | `scope`、`root_service`、`coverage`、`nodes[]`、`edges[]` | OTel/W3C trace 或网关/LiteLLM 日志 | 无上游 trace 时只能 `scope=process`，不能宣称端到端 |

## 3. 数据源边界

- `spans.jsonl`：host span、`batch_id`、`request_ids`、`trace`、`offcpu_ns`、`futex_ns`。
- `device.jsonl`：msPTI 的 `API|KERNEL|MEMCPY|HCCL` 时间线、`correlation_id`、`stream_id`。
- `metrics.jsonl`：Prometheus/vLLM/NPU 时序；vLLM 指标可能是 `vllm:num_requests_running` 冒号形式，报告加载器必须优先保留。
- `sweep.json`：多并发档位的 run 索引，字段见 `MULTI-AGENT-PLAN-VLLM-VIEWS.md`。
- `ops.jsonl`：per-op FLOPs/bytes，只用于 Roofline。
- `events.jsonl`：部署、扩缩容、故障等变更标记，只用于变化点注释。
- `otel.jsonl`：跨服务 trace；缺失时拓扑只看进程内。
- `offcpu.jsonl`：可选的精确 off-CPU 区间；没有时只能做 aggregate。

## 4. 实现规则（M0 冻结，M4 更新）

- 每个 builder 只返回有证据的视图；缺少证据时写 `status.available=false` 和可复核的 `status.reason`，不能生成示意点、示意曲线或猜测的依赖边。
- 数组字段初始化为空数组，不写 `null`。
- 报告模板必须显示十张卡片及其不可用原因；没有数据时不出现空坐标轴。
- `status.confidence` 必须与证据一致：msPTI/精确区间为 `exact`，host fallback 为 `inferred`，窗口/聚合或背景负载下为 `partial`。
- histogram 首帧是累计快照，不能作为窗口样本；后续帧只输出相邻累计差值，并从同一差值计算 P50/P95/P99。`_created` gauge 不参与延迟统计。
- sweep 的每档 run 优先使用 `load.json` 的 wall-clock 起止窗口；窗口前最后一个累计 histogram 快照只作基线。检测到运行中请求数高于客户端并发时，不能声称干净容量拐点。
- `views_v2` 与旧 `views` 并存，旧视图不回归。
