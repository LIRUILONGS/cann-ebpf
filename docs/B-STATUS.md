# B1-B4 当前状态（2026-11-27 更新）

约束变化：2026-11-27 已允许停机；窗口方案见 `docs/MAINTENANCE-PLAN.md`，执行结果见下文。

## B1 真实 V4/Orinfer 设备时间线

- **根因已定位并修复（2026-11-27）**：不是 vLLM/Orinfer、也不是 msPTI API 缺失，而是
  collector 的 `__attribute__((constructor))` 在全局 `std::string` 构造前执行，写 `g_run_id`
  空指针导致 SIGSEGV。已改为文件末尾 `AutoLifecycle` 对象；9.0.1/9.2 构建+selftest 通过，
  bash/python `LD_PRELOAD` 不再崩溃；**真实 torch_npu 进程已采到 109 API + 1 MEMCPY**。
  剩余：进程退出析构 SIGSEGV、真实 V4 验证。详见 `docs/B1-COLLECTOR-FIX.md`。
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
- 方案与命令模板已写入 `docs/B4-HCCL-PLAN.md`；2026-11-27 窗口在 `orinfer-cann920`
  容器内尝试 2-rank，HCCL 初始化失败（hostname/IPv6 解析报错、ERR99999），**未得到 correlationId 证据**；
  即使不加载 collector 也失败，属容器/网络配置问题，不是 collector 结论。

## 2026-11-27 停机窗口执行结果

- **B1（真实 V4 设备时间线）失败**：把 `libcannmspti.so` 通过 V4 `--env-file` 的 `LD_PRELOAD`
  注入后，容器入口 `bash` 先 SIGSEGV；加 `CANN_MSPTI_ONLY_COMM` 过滤后 shell 正常，但
  vLLM 的 `python3` 进程仍 SIGSEGV，V4 反复 139 退出。已回滚 collector env，V4 恢复正常。
  结论：当前 collector 与 V4/CANN 9.0.1 + vLLM 运行时存在兼容性问题，不能直接 LD_PRELOAD；
  需要改为“只包住目标进程”的入口 wrapper 或修复 collector 构造/初始化路径，再重试。
- **B4（真实 HCCL correlationId）部分成功/collector 未完成**：
  - 2-rank HCCL 本身已跑通：修复容器 `/etc/hosts` 的 hostname 解析 + 每个 rank
    `torch.npu.set_device(local_rank)` 后，`all_reduce` 成功，两个 rank 输出 `sum=32`；
    根因是缺 device 绑定导致两个 rank 抢同一 device，不是 HCCL 网络不可用。
  - collector 仍失败：带 `LD_PRELOAD` 或 HCCL 初始化后 `dlopen` 均使 Python SIGSEGV，
    没有生成 device 记录，**未得到 correlationId 证据**。这是 collector 生命周期问题。
- **B3（固定负载门禁）V4.1 客户端口径 PASS，但置信度低**：2 并发、30 秒、off/on
  各 2 轮；client rps off 8.616→on 9.146（-6.15%，on 略快）、client p95 -1.92%、
  agent CPU -0.065pp、dropped 0、health 151/151=200。噪声 stdev/median≈11.8%，
  2 轮不能证明严格 <3%；详见 `docs/B3-V41-GATE.md`。
- **B3 V4 固定负载（不停机）**：1 并发、30 秒、off/on 各 3 轮；client rps 0.400→0.400（0%），
  client p95 4.574→5.193s（+13.5%，严格阈值不通过），agent CPU 1.20%→1.13%、dropped 0；
  样本仅 11–15 个请求且生产流量干扰，**不能归因**。详见 `docs/B3-V4-GATE.md`。
- **V4.1 切换完成**：V4 已停，`orinfer-v41` active，`127.0.0.1:8000/health` 200，
  `/v1/models` 返回 `deepseek-v4.1`；collector env block 已移除，无 device 文件。
- **B2 Orinfer 采集（有负载）成功**：20 秒、2 并发固定请求，`spans=584`，包含
  `event/handle/memory/sync` 和 **6 条 HCCL**（`HcclAllReduce` 3 次）；health 200→200，
  `dropped=0`，`compare-v4-v41-load.md` 已生成。V4 基线为 1,765 spans，但两边不是同一
  workload，且缺少 device 时间线，**只能作为 host 侧行为对比，不能下生产 A/B 结论**。
  （早先无负载的 0-span 采集已作废。）

## 可以在不停机下继续做的

- A7 DCMI 直连已完成并验证（`--hardware-backend dcmi`）。
- 继续完善 Grafana/Prometheus dashboard、发布包、文档。
- Orinfer 配置校验、wrapper 脚本和回滚脚本的离线准备。
