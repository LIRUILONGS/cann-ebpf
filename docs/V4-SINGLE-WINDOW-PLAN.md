# V4「唯一一次」重启窗口执行方案

日期：2026-11-27
状态：**方案，未执行**。本文只设计窗口；不执行脚本、不停/启 V4 或 V4.1、不改生产配置、不 `git commit`。
配套脚本：`scripts/v4-maintenance-window.sh`（**默认 dry-run**，仅 `--apply` 才操作主机）。

相关依据：`docs/PREPARED-STATE.md`、`docs/B1-COLLECTOR-FIX.md`、`docs/OPS-FEASIBILITY.md`、
`docs/COLLECTOR-GATING-VALIDATION.md`、`docs/B1-PREP.md`、`docs/B1-DEVICE-EVIDENCE.md`、
`docs/MAINTENANCE-PLAN.md`。

---

## 0. 一句话

> 在**唯一一个**维护窗口内，把「collector device 时间线」与「host span」放进同一时间窗，
> 并（仅当 profiler 补丁就绪时）补采 5–10 秒 profiler；**V4 只做一次 stop、一次 start**，
> 不做任何「health 不过就再重启」的循环；一旦主启动/采集失败，**只回滚一次**到正常 V4。

---

## 1. 范围、命名与「只重启一次」的语义

### 1.1 对象

| 名称 | 含义 | 单元 / 端口 |
|---|---|---|
| **V4**（本窗口目标） | 生产 vLLM，容器 `deepseek-v4-flash-8905`，CANN 9.0.1 | `vllm-ds4-8905.service`；`127.0.0.1:8905/health` |
| **V4.1** | 当前在跑的 Orinfer 验证单元，独占 8 卡 | `orinfer-v41.service`；`127.0.0.1:8000` |
| **正常 V4** | `/etc/vllm-ds4.env` **不含** collector block、启动参数**不含** `--profiler-config` 的 V4 | 同 V4 |
| **验证 V4** | 本窗口临时注入 collector（及可能的 profiler 配置）的 V4 | 同 V4 |

当前准备态（`docs/PREPARED-STATE.md`）：V4.1 在跑、V4 已停；`/etc/vllm-ds4.env` **未**追加 block
（`env_has_block=0`）；修复版 collector 已 stage 到 `/data/cache/cann-ebpf/libcannmspti.so`。

### 1.2 「只重启一次」的严格定义（脚本强制）

把「重启」拆成**主循环（primary）**与**回滚（rollback）**两个预算，各自只允许一次：

- `primary_stop`：对 V4 只 `systemctl stop` **一次**；
- `primary_start`：对 V4 只 `systemctl start` **一次**；
- 主循环内**禁止**任何 `systemctl restart`；health/device 等待是**纯等待循环**，等不到就进入回滚，**绝不**在等待里重启；
- `rollback`：**最多触发一次**，动作 = 还原 env/启动脚本备份 → 停一次 → 启一次正常 V4 → 等 health；无论回滚成败都不再重试；
- 任何函数若被二次调用会直接 `refuse` 退出，从代码层面保证「不循环重启」。

> **U1 已解决（2026-11-27）**：collector 新增 `CANN_MSPTI_DURATION=60s`，采集窗口到期后
> 自动停止写文件，库仍加载但不再工作。因此**成功终态 = V4 正常运行、collector 已 idle**，
> 不需要为了卸载再重启第二次；`--decommission-on-success` 仅作为“必须物理卸载”的备用选项，
> 默认关闭。仍严格只 `primary_start` 一次。

### 1.3 硬边界

- 不注入运行中的 V4：collector 必须在进程启动前加载，所以只能通过一次重启生效（`docs/B1-PREP.md` §5）。
- 8 卡独占：V4 与 V4.1 不能同时跑；窗口开始前 V4.1 必须是 inactive（由 owner 在**窗口外**先停，属「腾卡」，不计入 V4 的 stop 预算）。
- 不压测：profiler 5–10 秒用**正常业务流量**，不额外造压（`docs/OPS-FEASIBILITY.md` §4.2、G7）。

---

## 2. 现状与前置缺口（先把「没做完」写清楚）

### 2.1 已就绪（可直接用于窗口）

| 项 | 证据 |
|---|---|
| 修复版 collector（9.0.1/9.2）已构建、selftest 10/10、`LD_PRELOAD` bash/python exit 0 | `docs/B1-COLLECTOR-FIX.md` §2–§3 |
| 真实 V4 已采到 device 记录（API/KERNEL/MEMCPY/HCCL，HCCL correlation 2,802/2,802） | `docs/B1-DEVICE-EVIDENCE.md` §1–§3 |
| collector 已 stage 到 `/data/cache/cann-ebpf/libcannmspti.so`、`/etc/cann-ebpf/collector.env` 已写 | `docs/PREPARED-STATE.md` §2 |
| `CANN_MSPTI_MAX_BYTES` 硬上限生效（64 MiB/文件） | `docs/COLLECTOR-GATING-VALIDATION.md` §0、§3.3 |
| `attach --device-jsonl` + `b1_merge_device.py` 导入链路可用 | `deploy/b1/validate-v4.sh`、`scripts/b1_merge_device.py` |
| `ops.jsonl` 转换器原型（`kernel_details.csv` → `ops.jsonl` → Roofline）已跑通 | `docs/OPS-FEASIBILITY.md` §5 |

### 2.2 未完成项（**不得**在文档/验收里写成已完成）

| 编号 | 未完成项 | 现状 | 影响本窗口 |
|---|---|---|---|
| **G-P1** | **vLLM-Ascend profiler 两处补丁 + `--profiler-config`** | **已准备 staging，未 apply**：`patches/vllm-ascend-profiler/` 与 `/opt/cann-ebpf/patches/vllm-ascend-profiler/`；窗口前先 `apply.sh --apply`，再唯一一次 stop/start | 未 apply 则 C13/C14 不过，profiler 段 `skipped(patch_not_ready)` |
| **G-P2** | **collector 父进程过滤** `CANN_MSPTI_ONLY_PARENT_COMM` | **已实现**（`b68e995`）：只命中父 comm 为 `VLLM::EngineCor` 的 8 Worker；`CANN_MSPTI_DURATION=60s` 采完自动停 | 窗口内预计 8 个 Worker 采集，不再 28 进程扇出 |
| **G-P3** | **Roofline ridge 单位 bug** | **已修**（提交 `45db78e`）：`roofline.go` 改为 `peakTFLOPS * 1000 / peakGBPS`，含单位/边界测试 | 不再阻塞；`bound` 结论可正常发布 |
| **G-P4** | **host↔device 同窗口 join 验证** | **未验证**（`docs/B1-DEVICE-EVIDENCE.md` §4、§6） | 本窗口的**主目标**；成功判据见 §6 |
| **G-P5** | **collector 优雅退出** | `AUTO_STOP` 默认 0 已使 bash/python exit 0（`docs/B1-COLLECTOR-FIX.md` §4.1），但真实 V4 停服务时仍可能出现 exit 139（数据已周期落盘） | 停 V4 时可能返回 139，**视为可容忍**；只要文件完整、服务能停即可，不因此回滚 |
| **G-P6** | **ops 转换器持久化** | **已处理**：`/opt/cann-ebpf/ops_jsonl.py`，窗口脚本默认指向它 | 若缺失则 profiler→ops 转换跳过（记录 `ops=skipped(converter_missing)`） |

> 结论：**collector 路径基本就绪，profiler 路径（G-P1）尚未完成**。因此本窗口默认是
> 「host span + device 同窗采集 + 可选 profiler」；profiler 段必须由 preflight 的补丁探测决定是否执行，
> **未就绪时明确跳过并写入摘要，绝不假装采到**。

---

## 3. 前置验收清单（go / no-go，逐条打勾才可 `--apply`）

脚本 preflight 会逐条打印 `PASS/FAIL/SKIP`；任一 `FAIL`（阻断类）在 `--apply` 下直接退出 4、不触碰主机。

### 3.1 产物 / 二进制

| # | 检查 | 期望 | 阻断 |
|---|---|---|---|
| C1 | 修复版 collector 源文件存在且 SHA-256 匹配 | `/opt/cann-ebpf/collectors/9.0.1/libcannmspti.so` = `bd94de85…3fb621`（`docs/PREPARED-STATE.md` §1） | 是 |
| C2 | 已 stage 的 collector 与源一致 | `/data/cache/cann-ebpf/libcannmspti.so` 存在且 SHA 同 C1 | 是 |
| C3 | collector env 片段存在 | `/etc/cann-ebpf/collector.env` 且含 `CANN_MSPTI_MAX_BYTES=67108864`、`CANN_MSPTI_AUTO_STOP=0`、**不含** `CANN_MSPTI_ONLY_CMDLINE=VLLM::Worker` | 是 |
| C4 | msPTI 库存在 | `/usr/local/Ascend/cann-9.0.1/tools/mspti/lib64/libmspti.so` | 是 |
| C5 | `cann-ebpf` 二进制可执行 | `/opt/cann-ebpf/bin/cann-ebpf` | 是 |
| C6 | device 合并脚本存在 | `/opt/cann-ebpf/b1_merge_device.py`（或 `scripts/b1_merge_device.py`） | 是 |
| C7 | V4 单元存在 | `/etc/systemd/system/vllm-ds4-8905.service` | 是 |
| C8 | profile 目录存在 | `/opt/cann-ebpf/profiles/vllm-ascend.yaml` | 是 |

### 3.2 独占与状态

| # | 检查 | 期望 | 阻断 |
|---|---|---|---|
| C9 | V4.1 已停（腾卡） | `systemctl is-active orinfer-v41.service` = inactive | 是 |
| C10 | 目标 V4 状态记录 | 记录 `systemctl is-active vllm-ds4-8905.service` 现状（active 则主循环会先 stop 一次） | 否 |
| C11 | 8 卡无他人进程 | `npu-smi info` 无 V4/V4.1 之外的推理进程（人工复核） | 是（人工） |
| C12 | 现有 env 无残留 collector block | `/etc/vllm-ds4.env` 不含 `# BEGIN cann-ebpf collector` | 否（有则先提示，避免重复追加） |

### 3.3 profiler（仅在启用 profiler 时逐条检查）

| # | 检查 | 期望 | 阻断 |
|---|---|---|---|
| C13 | 启动脚本已含 `--profiler-config` | `/usr/local/libexec/run_vllm_ds4_8905.sh` grep 到 `profiler-config` | profiler 启用时阻断 |
| C14 | wrapper 补丁标记存在 | `/opt/cann-ebpf/patches/vllm-ascend-profiler.applied`（operator 打补丁后落标记） | profiler 启用时阻断 |
| C15 | ops 转换器存在 | `/tmp/ops_proto/ops_jsonl.py` | 否（缺失则只跳过 ops 转换） |
| C16 | profiler 输出目录可写 | 容器 `/tmp/vllm_profile` 可建；宿主可 `docker cp` | profiler 启用时阻断 |

> 若 `C13/C14` 不满足：脚本把 profiler 标记为 `skipped(patch_not_ready)`，**继续**做 host+device（若 operator 用 `--require-profiler` 强制则改为阻断）。

---

## 4. 窗口步骤（Phase 0–8）

> 全部通过 `scripts/v4-maintenance-window.sh`；下表是脚本内部顺序，`--apply` 才真正执行。
> `T-` 为窗口前，`T0` 为窗口开始。

### Phase 0（窗口外，owner）腾卡 + 只读快照
- 排空外部流量；`systemctl stop orinfer-v41.service`；确认 inactive、`npu-smi info` HBM 释放。
- `npu-smi info`、`docker ps`、`systemctl status` 存证（脚本也会在 Phase 2 采 before 快照）。

### Phase 1 preflight（脚本，只读）
运行 `v4-maintenance-window.sh`（不带 `--apply`）先看计划与 §3 清单；全部 `PASS` 后加 `--apply`。

### Phase 2 备份（apply）
- 备份 `/etc/vllm-ds4.env`、`/etc/systemd/system/vllm-ds4-8905.service`、`/etc/cann-ebpf/collector.env`、
  `/usr/local/libexec/run_vllm_ds4_8905.sh` 到 `<evidence>/backup/`（带 UTC 时间戳，`cp -a`）；
- 采 `npu-smi-before.txt`、`docker-ps-before.txt`、`systemctl-status-before.txt`。

### Phase 3 注入配置（apply，仅配置、未启动）
- 用 `deploy/b1/stage-collector.sh --apply`（或把 `collector.env` 的 block 追加到 env，幂等、已存在则跳过）；
- 若 profiler 就绪（C13+C14）：把 `--profiler-config` 写入启动参数/包装（**由补丁提供**；脚本只校验、不代打补丁）；
- 不重启。

### Phase 4 主循环：stop 一次（apply）
- `primary_stop`：`systemctl stop vllm-ds4-8905.service`（**一次**）；
- 若因 `G-P5` 返回 139：记录、继续（服务已停即可）。

### Phase 5 主循环：start 一次（apply）
- `primary_start`：`systemctl reset-failed`（可选）+ `systemctl start vllm-ds4-8905.service`（**一次**）。

### Phase 6 等待（apply，纯等待、不重启）
- 循环直到：`/health` = 200 **且** `device-*.jsonl` 至少 1 个非空 **且**（若启用 profiler）vLLM 进程 cmdline 含 `--profiler-config`；
- 超时（默认 `TIMEOUT_HEALTH=1800s`）→ **进入 Phase 8 单次回滚**，不再重启。

### Phase 7 采集（apply）
- **device**：`python3 b1_merge_device.py --input-dir /data/cache/cann-ebpf --out <ev>/device-merged`；
- **host span**：`cann-ebpf attach --pid auto --profile vllm-ascend --profiles-dir … --duration 30s --blocking on --hardware on --device-jsonl <merged>/device.jsonl --output <ev>/run --json`；
- **profiler（条件）**：`POST /start_profile` → 正常流量 5–10s → `POST /stop_profile` → `docker cp` 出 `torch_profiler_dir` → `analyse`；
- **ops（条件）**：`ops_jsonl.py` → `ops.jsonl`（若转换器在）；Roofline `peak` 未验证前只留 `ops.jsonl`，不发布 `bound`；
- **diagnose**：`cann-ebpf diagnose --input <ev>/run --json --out <ev>/diagnose.json`。

### Phase 8 结束
- 成功：写 `<ev>/acceptance.md` 摘要（逐条成功判据 + 证据路径），输出 `WINDOW_OK`；
- 失败：`rollback_once`（见 §5），写摘要，输出 `WINDOW_ROLLED_BACK`，非零退出；
- 成功终态 = 验证 V4 运行（见 §1.2 推论 / U1 决策）。

---

## 5. 失败回滚（**只回滚一次**）

触发条件（任一）：Phase 6 health/device 超时、`systemctl start` 失败、Phase 7 采集致命错误。

回滚动作（`rollback_once`，全流程只执行一次；二次调用直接拒绝）：

1. 还原备份：用 `<ev>/backup/` 覆盖 `/etc/vllm-ds4.env`、`run_vllm_ds4_8905.sh`（若改过）、`collector.env`；
   等同 `deploy/b1/rollback-collector.sh --apply` 的效果（移除 marked block）；
2. 若 V4 正在跑验证配置：`systemctl stop vllm-ds4-8905.service`（一次）；
3. `systemctl start vllm-ds4-8905.service`（一次，正常配置）；
4. 等 `/health` 200（`ROLLBACK_TIMEOUT=900s`），记录结果；
5. 写 `<ev>/rollback.md`（原因、时间、命令、health 终态）；
6. `ROLLBACK_DONE=1`，**不再重试**，非零退出。

> 若回滚后 V4 仍起不来：保持停机、人工按 `docs/MAINTENANCE-PLAN.md` §4 用最新备份恢复；脚本到此为止，不做第二次重启。

---

## 6. 成功判据（验收摘要 `acceptance.md` 逐条）

| # | 判据 | 阈值 / 证据 |
|---|---|---|
| S1 | V4 `/health` = 200 | 服务 active、容器 healthy；`systemctl-status-after.txt` |
| S2 | device 文件产生且非空 | `/data/cache/cann-ebpf/device-*.jsonl` ≥1 个非空；合并后 `device.jsonl` 记录数 >0 |
| S3 | host span 采到 | `attach` 退出 0 且 `spans.jsonl` span 数 >0（参考 V4 基线 1,765 spans） |
| S4 | host↔device 可 join | `diagnose.json` 给出可匹配率 / 慢 Sync 匹配率；**无证据时明确 unknown，不下精确结论** |
| S5 | profiler（仅启用时） | 产出 `kernel_details.csv`，且 `head -1` 含 `cube_fops`/`vector_fops`（`docs/OPS-FEASIBILITY.md` §8.3）；否则标 `skipped(patch_not_ready)` |
| S6 | ops.jsonl（仅转换器在时） | 每行 `duration_ns>0 && flops>0 && bytes>0`；缺项按规则跳过并统计 |
| S7 | 证据完整 | `<ev>/` 含 `manifest.txt`、`acceptance.md`、各阶段日志 |
| S8 | 无循环重启 | 日志中 V4 的 `systemctl stop` 计数=1、`start` 计数=1（成功路径） |

**不做假结论**：profiler 未就绪、Roofline ridge 未修、join 未验证时，对应判据写 `skip/unknown`，不写 `pass`。

---

## 7. 预计耗时

| 阶段 | 预计 | 说明 |
|---|---|---|
| Phase 0 腾卡（窗口外） | 5–15 min | 停 V4.1、排空流量、HBM 释放 |
| Phase 1 preflight | 3–5 min | 清单 + 人工复核 C11 |
| Phase 2 备份 | 2–3 min | 4 文件 + 3 快照 |
| Phase 3 注入配置 | 1–2 min | 仅写 env |
| Phase 4–5 stop+start | 1–3 min | 一次停一次启 |
| Phase 6 等待 ready | **10–45 min** | `health-start-period=45m`，模型加载未知（参考 `docs/MAINTENANCE-PLAN.md`） |
| Phase 7 采集 | 3–8 min | device merge + 30s attach + 5–10s profiler + diagnose |
| Phase 8 摘要 | 1–2 min | |
| **合计（成功）** | **约 25–80 min** | 主瓶颈是模型加载 |
| **失败回滚追加** | +10–45 min | 一次正常启动的等待 |
| **（若 `--decommission-on-success`）** | +10–45 min | 多一次正常重启（默认关闭，见 U1） |

---

## 8. 依赖「尚未完成补丁」的步骤（显式映射）

| 步骤 | 依赖 | 未完成时的行为 |
|---|---|---|
| **Phase 7 profiler 采集** | **G-P1**（`--profiler-config` + wrapper 透传/`ArithmeticUtilization` 补丁） | 自动 `skipped(patch_not_ready)`；不产出 `kernel_details.csv` |
| **Phase 7 ops.jsonl 转换** | **G-P1** + `ops_jsonl.py`（G-P6） | 补丁缺失或无转换器 → `ops=skipped(...)`；不产出 Roofline 真实点 |
| **Roofline `bound` 标签** | **G-P3**（`roofline.go:105` 单位修复） | 未修则不发布 `bound` 结论，只保留原始 `ops.jsonl` |
| **collector 进程收敛** | **G-P2**（父进程过滤） | 未实现则以 `MAX_BYTES=64MiB` 兜底；8 Worker 仍写、helper 仍空文件（可接受但不生产级） |
| **host↔device join 结论** | **G-P4**（本窗口验证） | 本窗口就是要产出该验证；失败则判据 S4 = unknown |
| **优雅停服务** | **G-P5**（AUTO_STOP/析构） | 停 V4 可能 exit 139；容忍，不回滚 |

---

## 9. 风险与未决项

| 编号 | 风险 / 未决 | 处理 |
|---|---|---|
| U1 | **成功后是否回到「正常 V4」** | **已解决**：`CANN_MSPTI_DURATION=60s` 到期自动停写，库 idle；默认不回滚、只 start 一次。物理卸载才需 `--decommission-on-success`（两次 start，需书面批准） |
| R2 | collector 进程扇出 | **已缓解**：`ONLY_PARENT_COMM=VLLM::EngineCor` 只命中 8 Worker；`MAX_BYTES=64MiB` 兜底；`DURATION=60s` 自动停 |
| R3 | profiler 运行时开销 | 5–10s、正常流量；前后查 `/health` |
| R4 | `cube_fops/vector_fops` 是 PMU 推算，非精确 FLOPs；W8A8 单 dtype 峰值冲突 | `ops.meta` 记 `flops_source=ascend_pmu`；按 dtype 多次报告（`docs/OPS-FEASIBILITY.md` G3/G4/G10） |
| R5 | stop V4 时 exit 139 | 数据已周期落盘；只要求服务停下，不因退出码回滚 |
| R6 | 等待期模型加载慢导致误判超时 | `TIMEOUT_HEALTH` 可调（默认 1800s）；超时只回滚一次，不循环 |

---

## 10. 脚本与证据目录

```text
scripts/v4-maintenance-window.sh      # 默认 dry-run；--apply 才操作
/var/log/cann-ebpf/runs/v4-window-<UTC>/
  preflight.txt              # §3 清单结果
  backup/                    # env/unit/collector.env/run_vllm 备份
  npu-smi-before.txt / -after.txt
  docker-ps-before.txt
  systemctl-status-before.txt / -after.txt
  stop.log / start.log / wait.log
  device-files.txt           # 启动后 device-*.jsonl 列表
  device-merged/device.jsonl # 合并结果
  run/                       # attach 输出（spans.jsonl 等）
  run/collector.json
  profiler/                  # （仅补丁就绪）kernel_details.csv 等
  ops.jsonl                  # （仅转换器在）
  diagnose.json
  rollback.md                # 仅失败时
  acceptance.md              # 验收摘要
  manifest.txt               # 证据清单 + SHA
```

**dry-run 示例（本文写作时在本地仿真树执行，见脚本文末 `--help`；真实主机上同一命令会显示 PASS/FAIL 实况）：**

```bash
# 只计划，不动主机（默认）
sh scripts/v4-maintenance-window.sh
# 真跑（窗口内、owner 批准后；需要 root）
sh scripts/v4-maintenance-window.sh --apply
```

> 部署到服务器后先 `chmod +x scripts/v4-maintenance-window.sh`。脚本用 POSIX `sh`，
> 只依赖 `systemctl`/`curl`/`sha256sum`/`find`/`python3`/`docker`（缺失的会 `SKIP`）。

### 10.1 实测 dry-run（本地仿真树，非生产）

为验证脚本逻辑（不是验证生产状态），在本地用 `FS_ROOT` 指向一个仿造的服务器目录树运行默认 dry-run。
**未连接生产、未执行 `--apply`。** 输出节选：

```text
$ FS_ROOT=<sim> sh scripts/v4-maintenance-window.sh --expect-collector-sha <sim-sha>
mode=dry-run service=vllm-ds4-8905.service fs_root='<sim>' profiler_want=auto
== preflight (fs_root='<sim>') ==
PASS C1   collector source sha                       <sim-sha>
PASS C2   staged collector matches source            /data/cache/cann-ebpf/libcannmspti.so
PASS C3   collector.env                              /etc/cann-ebpf/collector.env
PASS C4   msPTI library                              /usr/local/Ascend/cann-9.0.1/tools/mspti/lib64/libmspti.so
FAIL C5   cann-ebpf binary                           present but not executable: /opt/cann-ebpf/bin/cann-ebpf
PASS C6   device merge script                        /opt/cann-ebpf/b1_merge_device.py
PASS C7   V4 systemd unit                            /etc/systemd/system/vllm-ds4-8905.service
PASS C8   vllm-ascend profile                        /opt/cann-ebpf/profiles/vllm-ascend.yaml
SKIP C9   orinfer inactive                           systemctl unavailable / rehearsal
SKIP C10  V4 current state                           systemctl unavailable / rehearsal
SKIP C11  8-card exclusivity                         MANUAL: npu-smi info, no non-V4 inference procs
PASS C12  env has no collector block                 /etc/vllm-ds4.env
FAIL C13  run script has --profiler-config           not found (profiler patch NOT applied)
FAIL C14  wrapper patch marker                       missing: /opt/cann-ebpf/patches/vllm-ascend-profiler.applied (patch NOT applied)
SKIP C15  ops converter present                      missing: /tmp/ops_proto/ops_jsonl.py (ops.jsonl step will skip)
SKIP C16  profiler retrieve via docker               docker unavailable / rehearsal
-- profiler decision: skipped_patch_not_ready (want=auto patch_ready=0) --
== preflight blockers: 0 ==

================= PLAN (dry-run) =================
target service      : vllm-ds4-8905.service
exclusive service   : orinfer-v41.service (must be inactive)
env file            : /etc/vllm-ds4.env
collector (source)  : /opt/cann-ebpf/collectors/9.0.1/libcannmspti.so
collector (staged)  : /data/cache/cann-ebpf/libcannmspti.so
evidence dir (would): /var/log/cann-ebpf/runs/v4-window/v4-window-<UTC>
profiler            : skipped_patch_not_ready

1. backup /etc/vllm-ds4.env, unit, /etc/cann-ebpf/collector.env, /usr/local/libexec/run_vllm_ds4_8905.sh -> <ev>/backup/
2. stage collector block (idempotent) via deploy/b1/stage-collector.sh --apply
3. primary_stop  : systemctl stop vllm-ds4-8905.service        (exactly once)
4. primary_start : systemctl start vllm-ds4-8905.service       (exactly once)
5. wait          : /health=200 AND >=1 non-empty device-*.jsonl [AND profiler cmdline] (timeout 1800s, NO restart)
6. capture       : merge device -> attach(host span, 30s) [-> profiler 8s if ready] -> diagnose
7. success       : write acceptance.md, print WINDOW_OK
   failure       : rollback_once (restore env, one stop, one start of NORMAL V4), print WINDOW_ROLLED_BACK
==================================================
dry-run complete: no changes made.
```

要点：
- `C5` 在仿真树上是 `present but not executable`（Windows 文件系统无执行位），在真实主机应为 `PASS`；
  该 `FAIL` 在 dry-run 下不计阻断，`--apply` 下计阻断。
- `C13/C14` 反映**真实缺口**：profiler 补丁未应用 → `profiler=skipped_patch_not_ready`，
  **不是**脚本 bug，也不代表 profiler 已采到。
- `--apply` 在 `FS_ROOT` 下被明确拒绝（`ERROR: --apply cannot be used with FS_ROOT`）；
  `sh -n` 语法检查通过；全脚本无 `systemctl restart`，`stop`/`start` 各只出现在 primary 与 rollback 两处。

> 本文未执行任何 `--apply`、未停/启 V4 或 V4.1、未改生产配置、未 `git commit`。
