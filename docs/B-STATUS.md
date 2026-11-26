# B1-B4 当前状态（2026-11-26）

约束：**推理服务（V4）不能停**。以下按此约束执行。

## B1 真实 V4/Orinfer 设备时间线

- **未执行**：msPTI collector 必须在 ACL 初始化前 LD_PRELOAD/入口 wrapper；对运行中的 V4
  不能安全注入，必然需要一次 V4 重启或备用实例。
- 已准备：`m3/mspti_collector` 可从当前源码构建并通过 selftest；`attach --device-jsonl`
  可导入 collector 产物、抓 pidmap 并封存 artifact。
- 进一步准备（2026-11-26）：`deploy/b1/stage-collector.sh --stage` 已把
  `libcannmspti.so` 放到 `/data/cache/cann-ebpf/` 并生成 `/etc/cann-ebpf/collector.env`；
  **未改 `/etc/vllm-ds4.env`、未重启 V4**。激活/回滚步骤见 `docs/B1-PREP.md`。
- 需要：一次计划重启（把 wrapper 永久加到服务入口）或一台备用节点。

## B2 V4/Orinfer M5 A/B

- V4 基线已采：10 秒、blocking+hardware on、1,765 spans、8 Worker、health 200→200、service active；
  证据在服务器 `runs/m5/v4-baseline-20261126T125051Z`。
- **Orinfer 未运行**，本机也没有额外空闲卡；启动 Orinfer 会与 V4 争 8 卡/HBM，不能在不影响
  V4 的前提下做真实 A/B。
- 需要：Orinfer 专用节点/实例，或维护窗口切换；V4 基线可复用。

## B3 固定负载吞吐门禁

- **在线 smoke 已完成**：客户端吞吐 +6.7%、p95 -34.6%、agent CPU +0.06pp、0 dropped、health 200；
  详见 `docs/B3-ONLINE-OVERHEAD.md`。
- 正式 ≤3% 门禁仍建议隔离/严格低峰、多轮中位数；在线服务端总计数受同机流量干扰，不作为判据。

## B4 真实 HCCL correlationId

- **未执行**：容器内有 `hccl_test` 源码，但没有现成二进制；跑 HCCL 需要多 rank 集合通信。
- 当前 HBM 余量仅 ~6.3%（约 4GB），在 V4 正在推理的同一组卡上跑 HCCL 有 OOM/影响线上请求风险，
  因此不在共享卡上执行。
- 需要：备用节点/独占卡测试环境，或维护窗口内与 B1 一起做；同时验证
  `msptiActivityHccl` 无 correlationId、只有 `Communication` 有的降级路径。
- 方案与命令模板已写入 `docs/B4-HCCL-PLAN.md`，未执行。

## 可以在不停机下继续做的

- A7 DCMI 直连已完成并验证（`--hardware-backend dcmi`）。
- 继续完善 Grafana/Prometheus dashboard、发布包、文档。
- Orinfer 配置校验、wrapper 脚本和回滚脚本的离线准备。
