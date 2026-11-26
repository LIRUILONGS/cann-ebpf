# 下一阶段工作与分工（M3 合成验证之后）

日期：2026-11-26
前提：vLLM 插件与 `execute_model` 函数级耗时**不做**；不重启 V4、不注入运行中的服务，除非维护窗口。
当前 HEAD：`4be88ba`；M3 collector 已提交（`5e2fc93`），合成 ACL final7 慢 Sync 305/305（100%）。

## 1. 现在可做（无维护窗口）

| ID | 工作 | 负责人 | 产出/验收 | 依赖 |
|---|---|---|---|---|
| A1 | **M3 join 质量**：修复 `api_name_mismatch`（final7 有 5,320 条）；区分“可匹配设备调用的 host span”与全量分母；把 reason 细分到报告 | 另一个任务 | final7 重跑：慢 Sync 保持 100%，可匹配函数的匹配率 ≥95%；diagnose 展示 reason 明细；golden 测试 | `docs/M3-VALIDATION.md` |
| A2 | **M3 collector 加固**：回调 buffer 生命周期、stop 幂等/死锁、时钟偏移定期重估、线程新建后的 tid 映射刷新、JSON 转义 | 另一个任务 | 容器内 `synthetic_acl` 复跑通过；新增单测/压力测试；meta 记录 clock drift | m3 代码 |
| A3 | **uprobe detach/规模优化**：740 个 uprobe 退出清理约 40s。改为系统级 attach + BPF `target_pids` 过滤（37×库数），或只挂 Worker 目标 | 我 | 退出去抖后 <10s；uprobe 残留 0；V4 span 数量/覆盖不下降 | `internal/probes/**` |
| A4 | **固定负载开销脚本**：固定 prompt/并发/max_tokens，on/off 多轮，读 vLLM `/metrics` + agent CPU/RSS + dropped | 我 | `scripts/overhead_fixed_load.py` + runbook；维护窗口可直接跑；dry-run 校验参数 | 现有 `/metrics`、`attach` |
| A5 | **M6 交付骨架**：systemd unit、install/uninstall/upgrade、preflight 前置、release tar + `SHA256SUMS`、runbook | 我 | 一次性安装/卸载/回滚脚本可 dry-run；包内二进制 SHA 与 HANDOFF 一致 | 当前 CLI |
| A6 | **Prometheus exporter 设计/实现**：把 summary/hardware/blocking 暴露为 `cann_ebpf_*` 指标（只读文件或随 attach 输出） | 我（P1） | `/metrics` 文本可被 Prometheus 抓取；label 基数受控 | A5 |
| A7 | **DCMI/短间隔硬件**：DCMI 后端或 npu-smi 1–2s 间隔开销评估 | 我（P2） | 与 npu-smi 数值对齐；开销可接受 | E 层代码 |
| A8 | **M5 采集/对比脚本**：V4 稳态与 Orinfer 窗口的 capture/compare 模板 | 我 | `scripts/m5-ab.sh` + `compare.md` 自动填充；等待 Orinfer 窗口 | M5 窗口 |

优先级：**A1/A3/A4 → A5 → A2 → A6/A7/A8**。

## 2. 需要维护窗口

| ID | 工作 | 负责人 | 验收 |
|---|---|---|---|
| B1 | **V4 真实设备时间线**：入口 wrapper/LD_PRELOAD 注入 collector，10–30 秒 | 另一个任务主导，我负责 host attach/health/残留/证据导入 | 真实 V4：慢 Sync 匹配 ≥95%，时钟偏差 <100µs；`/health` 200、service active、残留 0 |
| B2 | **M5 V4/Orinfer A/B** | 我主导，另一个任务保证 device join | 同 workload/并发/时长；`compare.md` 含 host/device/verdict |
| B3 | **固定负载开销门禁** | 我 | 吞吐下降 ≤3%、p95 回归 ≤5%、ringbuf 丢 <0.1% |
| B4 | **真实 HCCL correlation** | 另一个任务 | 多卡集合通信下有 `Communication` correlationId；`msptiActivityHccl` 无 correlationId 的降级标注正确 |

## 3. 明确不做

- vLLM general plugin 安装/重启激活、`execute_model` 函数级耗时；
- 逐 request 延迟拆分（batch 仍是窗口归因）；
- 未授权窗口重启 V4/Orinfer、修改 CANN/驱动。

## 4. 建议节奏

```text
Week 1（无窗口）
  D1-D2  A3 teardown 优化 + A4 固定负载脚本
  D2-D3  A1 M3 join 质量 + A2 collector 加固
  D4-D5  A5 交付骨架；A6 exporter 设计
Week 2（申请窗口）
  B1 V4 真实设备时间线 → B3 开销门禁 → B2 M5 A/B
  之后    B4 HCCL；A7/A8 收尾
```

## 5. 协作规则

文件所有权、接口（`device.jsonl`/`meta`/`pidmap`/artifacts）和合并顺序见 `docs/WORK-SPLIT.md`；
提交时只 `git add` 自己名下路径，禁止 `git add -A`。
