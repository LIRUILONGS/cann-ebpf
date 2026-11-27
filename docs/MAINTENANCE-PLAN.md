# 停机维护窗口：任务分配与执行顺序

日期：2026-11-27
前提：V4 可以停机；外部推理流量需先排空。当前 V4 运行中、health 200，Orinfer 单元已存在但 disabled。

## 1. 角色与所有权

| 角色 | 负责 | 文件/范围 |
|---|---|---|
| **我（窗口负责人）** | 停机/启动、health/回滚、B1 host 采集、B3 固定负载、B2 采集对比、最终证据与提交 | `internal/probes/**`、`internal/hardware/**`、`deploy/**`、`scripts/**`、docs |
| **另一任务（collector/device）** | B1 collector 产物完整性、B4 HCCL 语义与 correlationId、B2 device join 复核 | `m3/**`、`internal/device/**`、`internal/diagnose/**` |
| **共同** | go/no-go、每阶段验收、异常回滚决策 | 只读检查与证据 |

规则：只提交自己名下文件；禁止 `git add -A`；每阶段结束记录时间、命令、结果。

## 2. 阶段与分工

### P0 T-30min：窗口前检查（不停机）

| 任务 | 负责人 | 命令/验收 |
|---|---|---|
| 排空外部流量，确认无新请求 | 我 | 网关 QPS→0；`/metrics` running/waiting 归零 |
| 备份配置、记录现状 | 我 | `/etc/vllm-ds4.env`、unit、health、HBM、uprobe 残留 |
| 校验 Orinfer 可启动 | 另一任务 | `orinfer-v41.service`、`orinfer-cann920` 容器、`start-orinfer.sh`、端口/模型/health |
| 校验 B4 工具 | 另一任务 | `hccl_test` 构建或 torch_npu 方案可执行；独占卡确认 |
| 校验 collector 已 stage | 我 | `/data/cache/cann-ebpf/libcannmspti.so`、`/etc/cann-ebpf/collector.env` |

### P1 停 V4（约 2–3 分钟）

```bash
systemctl stop vllm-ds4-8905.service
docker ps --filter name=deepseek-v4-flash-8905
npu-smi info   # 确认 HBM 释放
```

负责人：我；另一任务复核容器退出、无残留进程。

### P2 B4：真实 HCCL（V4 停机、卡空闲）

| 任务 | 负责人 | 验收 |
|---|---|---|
| 启动 2/8 rank HCCL（`hccl_test` 或 torchrun）并加载 collector | 另一任务 | 测试进程正常运行，`device-<pid>.jsonl` 产生 |
| 合并 device 文件、统计 kind | 我 | `b1_merge_device.py` 输出 records/kinds |
| 检查 `Communication`/`HCCL` 的 correlationId | 另一任务 | 明确有无 correlationId；无则只能时间窗回退 |
| host attach 测试进程 + 导入 device | 我 | span 与设备记录可 join；不伪造精确关联 |

### P3 启动 V4 + collector，B1 真实设备时间线

```bash
sh deploy/b1/stage-collector.sh --apply   # 追加 marked block，备份 env
systemctl start vllm-ds4-8905.service
# 等待 health 200（health-start-period=45m，实际耗时待测）
```

| 任务 | 负责人 | 验收 |
|---|---|---|
| 等待 V4 ready、health 200 | 我 | `/health` 200，service active，容器 healthy |
| 确认 `/data/cache/cann-ebpf/device-*.jsonl` 生成 | 我 | 多 Worker 文件、meta 正常 |
| 合并 device、host attach 10–30s | 我 | 真实 V4 `device.jsonl` + spans |
| diagnose join 复核 | 另一任务 | 可匹配率、慢 Sync 匹配率、reason、verdict；无证据不下结论 |

### P4 B3：固定负载门禁（V4 仅测试流量）

| 任务 | 负责人 | 验收 |
|---|---|---|
| 3 轮 off/on、2–4 并发、固定 prompt/max_tokens | 我 | `overhead.csv`、客户端吞吐/p95、agent CPU/RSS、dropped |
| 门禁判定 | 我 | 吞吐下降 ≤3%、p95 回归 ≤5%、dropped 0、health 200 |
| 服务健康观察 | 另一任务 | 无 5xx/OOM，service 稳定 |

### P5 B2：Orinfer A/B

```bash
systemctl stop vllm-ds4-8905.service
systemctl start orinfer-v41.service
# 等待 Orinfer health（端口/模型/auth 以 P0 检查为准）
```

| 任务 | 负责人 | 验收 |
|---|---|---|
| 启动 Orinfer、等待 ready | 我 | health 200、容器 healthy |
| 采 Orinfer run（同 workload/时长） | 我 | `m5_capture.sh` 证据 |
| V4/Orinfer `m5_compare.py` | 我 | compare.md，字段完整 |
| device join/判定复核 | 另一任务 | 无 device 证据时明确 unknown |

### P6 恢复 V4（正常模式）

```bash
systemctl stop orinfer-v41.service
sh deploy/b1/rollback-collector.sh --apply   # 移除 collector block
systemctl start vllm-ds4-8905.service
# 等待 health 200
```

| 任务 | 负责人 | 验收 |
|---|---|---|
| 回滚 collector env、启动 V4 | 我 | `/etc/vllm-ds4.env` 无 block；health 200；service active |
| 残留检查 | 我 | uprobe/kprobe 残留 0；无 device 文件继续增长 |
| 外部流量恢复 | 我 | health 200、5xx 正常、运行 10 分钟观察 |

### P7 收尾

- 更新 `M3-VALIDATION`、`B1-PREP`、`B3-ONLINE-OVERHEAD`、`B-STATUS`、`HANDOFF`；
- 证据目录、CSV、compare.md 路径写入文档；
- 各自提交自己文件；我最终 push；更新 release SHA（若二进制变化）。

## 3. 时间估算

| 阶段 | 预计 |
|---|---|
| P0 检查 | 15–30min |
| P1 停 V4 | 2–3min |
| P2 B4 | 20–40min（含构建） |
| P3 启动 V4 + B1 | 10–45min（模型加载未知，health-start-period 45m） |
| P4 B3 | 15–25min |
| P5 Orinfer + B2 | 20–50min（启动时间待测） |
| P6 恢复 V4 | 10–45min |
| 合计 | **约 1.5–3 小时** |

## 4. 回滚

- V4 起不来：保持停机，`rollback-collector.sh --apply` 移除 env block，`systemctl start` 正常启动；仍失败则恢复 `/etc/vllm-ds4.env` 备份。
- Orinfer 起不来/health 不 200：停止 Orinfer，回滚 collector（如需），启动 V4。
- B4 影响 HBM/健康：立即停止测试进程，确认 HBM 释放后再继续。
- 任何阶段 health 非 200 或出现 OOM：按 P6 回滚，不继续后续阶段。

## 5. 需要确认

- 外部流量是否已排空、窗口从几点开始；
- Orinfer 的端口/模型/API key/health 路径（P0 由另一任务确认）；
- 是否接受 V4 两次启动（B1 后切 Orinfer，再恢复 V4）；若只做 B1/B3/B4，可少一次启动。
