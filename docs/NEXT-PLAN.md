# cann-ebpf 下一步方案（M2 之后）

日期：2026-11-26
基于：M2 已在 `bms-84865758` 完成 V4 与合成锁竞争验证（见 `docs/M2-VALIDATION.md`）。

## 0. 结论先行

下一步不是“直接写 M3 代码”，而是先用 1 天做 **msPTI 可行性验证**，同时并行做两件不需要重启 V4 的事：

1. **M3.0 msPTI 动态 attach 可行性**：目标机容器内 `msprof` 支持 `--pid` 动态 profiling，
   有可能不重启 V4 就拿到 device kernel / memcpy / HCCL 时间线；先验证输出格式、开销和 correlationId。
2. **E 层硬件指标（npu-smi/DCMI）**：目标机 `npu-smi 25.2.1` 可用，能直接读 AICore%、HBM、功耗、温度；
   先补这一层，可以独立支撑 `hbm_pressure` 和粗粒度 `device_busy`，不依赖 msPTI。
3. **M2 收尾**：补 10 秒/长窗口开销基线、真实 futex 竞争窗口观察；否则 M2 的“开销 ≤3%”仍未量化。

之后按 M3.0 的结论二选一：
- 能动态 attach → 做 msprof 输出解析 + host/device join（不需要维护窗口）；
- 不能动态 attach → 写容器内 msPTI in-process helper，等维护窗口激活（与插件同类风险，但只做设备时间线）。

## 1. 目标机现状（2026-11-26 只读侦察）

| 项 | 结果 | 影响 |
|---|---|---|
| 宿主 CANN | 只有驱动（`/usr/local/Ascend` 下无 toolkit） | host 侧没有 `libmspti.so` |
| V4 容器 | `deepseek-v4-flash-8905`，healthy，CANN `9.0.1` | 设备侧能力都在容器内 |
| 容器 msPTI | `tools/mspti/{include,lib64,python,samples}`，`libmspti.so` | API/KERNEL/MEMCPY/HCCL 有实现基础 |
| msPTI headers | `mspti.h`、`mspti_activity.h`、`mspti_callback.h`、`mspti_cbid.h`、`mspti_result.h` | 可编译 C collector |
| msPTI samples | `mspti_activity`、`mspti_correlation`、`mspti_external_correlation`、`mspti_hccl_activity`、`python_monitor` | 存在“外部关联/相关性”能力，需读样例确认用法 |
| `msprof` | `/usr/local/Ascend/ascend-toolkit/latest/bin/msprof`，支持 `--pid`、`--duration`、`--sys-devices` | **可能不重启 PID 动态采集** |
| `msopprof` | 存在，面向单算子 launch 采集 | 不适合在线服务 |
| `npu-smi` | 25.2.1，容器内外都可用 | 可做 E 层 5–10s 采样 |
| DCMI | 宿主 `/usr/local/dcmi/libdcmi.so` + 头文件 | 可替代 npu-smi，字段更细 |
| torch_npu | 2.10.0.post2 | 若有维护窗口，可用 torch_npu profiler 作为备选 |

关键未知（M3.0 要回答）：
- `msprof --pid` 对 V4 这种多进程 TP8 服务，能采到哪些 pid、什么 activity；
- 输出目录里是否有 **kernel/memcpy/HCCL 的 start/end + correlationId**，还是只有 host API 与聚合指标；
- 动态 profiling 对在线服务的影响（延迟/吞吐/HBM）；
- 时间戳基准（是否能与 `bpf_ktime_get_ns` 对齐）。

## 2. 阶段与优先级

### P0-1：M3.0 msPTI 动态 attach 可行性（0.5–1 天，短窗口、可回退）

产出：
- 对 1 个 Worker PID 做 5 秒 `msprof --pid`，记录输出树、文件格式、activity 类型；
- 对 V4 做 5 秒 `msprof --pid <engine/worker 列表> --sys-devices all`，前后 health/service/残留检查；
- 写 `docs/M3-FEASIBILITY.md`：能采到什么、缺什么、开销、是否可 join、下一步选型。

验收：
- 不重启 V4，health 200/200、service active/active；
- 明确回答“correlationId 是否可见”“kernel/memcpy/HCCL 是否可用”“开销是否可接受”；
- 给出选型结论：动态 attach 解析 vs 容器内 in-process collector。

建议命令（先只测一个 worker，5 秒）：

```bash
docker exec deepseek-v4-flash-8905 sh -c '
  /usr/local/Ascend/ascend-toolkit/latest/bin/msprof \
    --pid 1452 --duration 5 --sys-devices 0 \
    --output /tmp/m3probe-$(date +%s)'
```

风险与回退：`msprof --pid` 若需停止/暂停目标或报权限错误，立即终止，不重试强采；不修改容器配置。

### P0-2：E 层硬件指标（1 天，无重启）

目标：补 `hardware` 采样，回答 HBM 压力、粗粒度设备忙碌，不冒充 kernel 时间线。

实现：
- 新增 `internal/hardware`：优先 DCMI（宿主 `libdcmi.so`），回退 `npu-smi info` 解析；
- 5–10 秒采样 AICore%、HBM used/total、power、temp，写入 `hardware.jsonl` 并纳入 manifest artifact；
- `diagnose` 增加 `hardware` 汇总：HBM free%、AICore 均值/峰值；
- 判定新增 `hbm_pressure`（Malloc 慢且 HBM free < 5%），不输出 `device_bound`。

验收：
- V4 上 10 秒采样，字段与 `npu-smi` 手工值一致；
- 采集不改变服务状态；数值缺失时明确 `unknown`，不补造。

### P0-3：M2 收尾（1 天，无重启）

- **开销基线**：固定负载或生产低扰动窗口，采集 `--blocking off` vs `--blocking on` 的
  req/s、p50/p95、agent CPU/RSS、ringbuf dropped；目标吞吐下降 ≤3%、丢事件 <0.1%；
- **真实锁竞争窗口**：持续短窗口观察 `futex_ns > 0` 的样本，找到一次真实锁等待并记录 verdict；
  找不到就明确写“本环境未观察到”，不伪造；
- 可选：`sched_switch` 长时间运行的降采样/自动关闭策略（超阈值自动 `--blocking off`）。

验收：形成 `docs/M2-OVERHEAD.md`，含基线方法、数字、判定；不达标则默认 `--blocking off`。

### P1-1：M3.1–M3.3 设备时间线与 join（3–5 天，取决于 P0-1 结论）

**路线 A：msprof 动态 attach 可解析（优先）**
- `internal/mspti`（或 `internal/device`）解析 msprof 输出：API / KERNEL / MEMCPY / HCCL record；
- 建立 device span：`device_id`、`stream_id`、`kind`、`start/end`、`correlation_id`、`name`；
- host/device 时钟校准：用同一批 API record 与 eBPF span 的中位偏差，按 device 分别记录；
- join：`(pid, tid, api, start±100µs)` 一对一匹配 API record，再用 `(device_id, correlationId)`
  关联 kernel/memcpy/HCCL；
- `join-report.md/json`：对齐率、未匹配数量与原因（no_record/time_skew/api_name_mismatch/duplicate/mspti_off）；
- `diagnose` 增加 `device` 证据；只有 join 成功才输出 `device_bound` / `launch_starvation` /
  `copy_bound` / `communication_bound`，否则明确降级为时间窗。

**路线 B：容器内 in-process msPTI collector（需要维护窗口）**
- 在容器内编译/放置 helper，用 `libmspti.so` 采集 API/KERNEL/MEMCPY/HCCL；
- 通过 Unix socket 把 device span 发给 host collector（沿用现有 contextfeed 的权限模型）；
- 需要重启 V4 或容器入口 wrapper；只在获批维护窗口执行；与 vLLM 插件分开描述、分开开关。

验收（两条路线一致）：
- 在一个已知 kernel 上验证 correlationId 真能关联；
- 95% 慢 Sync 能匹配到 API record，时间偏差 <100µs（不达标则降级为时间窗并写明）；
- join 失败不伪造精确关联；服务状态前后不变。

### P1-2：M5 V4/Orinfer A/B（需要人工窗口）

- 先采 V4 稳态基线（同 workload、同并发、同时长）；
- 授权窗口切 Orinfer，重复采集；
- 输出 `compare.md`：host span、device 时间线（若 M3 已完成）、verdict 分布。

### P2：生产化（M6）

- Prometheus/OTLP exporter、Grafana dashboard；
- systemd/容器 sidecar、安装/升级/回滚、preflight、权限收敛；
- 资源 watchdog、自动降采样/detach、长跑基线；
- `SHA256SUMS` 与交付包。

### 暂不做（明确排除）

- vLLM general plugin 安装/重启激活、`execute_model` 函数级耗时；
- 逐 request 延迟拆分（batch 仍是窗口归因）；
- 修改 CANN/驱动或在未授权窗口重启服务。

## 3. 建议的执行顺序（含依赖）

```text
Week 1
  D1      P0-1 M3.0 动态 attach 可行性（短窗口） + P0-2 E 层指标开工
  D2      P0-2 完成 + P0-3 开销基线
  D3-D5   M3.1 解析/join（按 P0-1 结论走路线 A 或准备路线 B）
Week 2
  D1-D2   M3.2/M3.3 join report + diagnose device 证据
  D3      验收与文档（M3-VALIDATION.md）
  之后    M5 A/B（等窗口）、M6 生产化
```

## 4. 判定与证据硬约束（继续遵守）

- 不重启 V4/Orinfer、不停止服务、不修改目标进程内存；短时只读 attach 才允许。
- 不覆盖旧证据；每次采集前后检查 health、service、uprobe/kprobe 残留。
- 原始值以 `spans.jsonl` / `manifest.json` / 设备原始记录为准；派生报告不覆盖原始事件。
- 没有设备侧证据时，不输出 `device_bound` / `launch_starvation` / `copy_bound` / `communication_bound`。
- 不把 batch 窗口说成 `execute_model` 函数级耗时，不把 host 时间说成 NPU 时间。
- M3.0 如果发现 `msprof --pid` 会影响服务或必须重启，立即停止，转为维护窗口方案。

## 5. 现在就能执行的第一批动作

1. `msprof --pid 1452 --duration 5 --sys-devices 0`，确认输出格式（不改任何配置）。
2. 读取输出目录，`grep`/解析是否存在 `KERNEL`、`MEMCPY`、`HCCL`、`correlationId` 字段。
3. 同一窗口用当前二进制跑 `attach --blocking on`，为后续 host/device 对齐准备 host span。
4. 记录 `/health`、service、残留；如果 msprof 有暂停/阻塞迹象立即终止。
5. 并行开始 E 层 `npu-smi` 采样实现（无重启、风险最低）。

对应验收与证据写入 `docs/M3-FEASIBILITY.md`；M3 主体验收写入 `docs/M3-VALIDATION.md`。
