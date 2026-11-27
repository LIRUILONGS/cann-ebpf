# B4 真实 HCCL 采集 harness（离线准备，未执行）

状态：**只准备，未在任何 NPU 上执行**。本文与 `scripts/b4_hccl_probe.sh` 一起，把
`docs/B4-HCCL-PLAN.md` 里的两条路线固化成可复用的 build/run/验收/回滚流程。

> **红线：共享卡禁止执行。** V4 占用 8 卡、HBM 余量仅约 6.3%，在 V4/Orinfer 正在推理的卡上跑
> HCCL 集合通信会争 HBM 与通信带宽，可能直接让线上请求 OOM 或超时。只允许在**独占卡 / 独立
> 测试节点 / 维护窗口内已确认 V4 停止的节点**上执行，且执行前必须完成下文的 G1–G5 门禁。
> 本 harness 不会停/启 V4、不会操作任何服务器，`--apply` 还要求显式 `--confirm-exclusive`。

相关文档：`docs/B4-HCCL-PLAN.md`（方案与命令模板）、`m3/mspti_collector/README.md`
（collector 环境变量与 `{pid}` 模板）、`docs/B1-PREP.md`（`b1_merge_device.py` 的合并流程）、
`docs/M3-DEVICE-JOIN.md`（device 记录 schema 与 join 语义）。

## 1. 目标与验收口径

B4 只回答两个问题：

1. **真实 HCCL 集合通信能否被 msPTI 记为设备区间**：`device-<pid>.jsonl` 中出现 `kind=HCCL`
   记录（`msptiActivityHccl` 与 `msptiActivityCommunication` 两种来源）。
2. **`correlationId` 能否用于精确关联**：
   - `msptiActivityHccl` 结构没有 correlationId，collector 写成 `correlation_id: 0`、
     `source=mspti_hccl_no_correlation`（见 `m3/mspti_collector/collector.cpp:466-479`）；
   - `msptiActivityCommunication` 有 correlationId，写成 `source=mspti_communication`
     （`collector.cpp:481-493`）。
   - 因此验收只允许两种结论之一：**存在 `mspti_communication` 记录 → 可用精确 join**；
     **只有 `mspti_hccl_no_correlation` → 只能按时间窗/通信域降级**，诊断不得输出精确函数级结论。

不追求“HCCL 时长=逐请求延迟”：batch 仍是窗口归因，设备时间线不等于 request 级延迟。

## 2. 前置条件（门禁，逐条确认后才可 `--apply`）

| 门禁 | 要求 | 确认方式 |
|---|---|---|
| G1 | 目标卡**独占**：无 V4/Orinfer/其他推理作业 | `npu-smi info` 看不到他人进程；`docker ps` 无 vllm/orinfer/mindie/raylet |
| G2 | 每卡 HBM 空闲 ≥16GB | `npu-smi info` 的 HBM-Usage 列；把输出存成 `npu-smi-before.txt` 作证据 |
| G3 | 无活跃推理进程 | harness `--apply` 会自动扫（`docker ps` + `ps -eo args`），扫到即退出码 3 拒绝 |
| G4 | collector + msPTI 库就位 | `libcannmspti.so`（m3 构建产物）与 `libmspti.so`（CANN toolkit）路径存在 |
| G5 | CANN 环境已 source，`torch_npu` 可用 | `source /usr/local/Ascend/ascend-toolkit/set_env.sh`；`python3 -c "import torch, torch_npu; print(torch.__version__, torch_npu.__version__)"` |

补充事实收集（写进证据目录）：`cat /usr/local/Ascend/ascend-toolkit/latest/version.cfg`、
`sha256sum libcannmspti.so`、测试卡列表、开始/结束时间。

## 3. Build

### 3.1 collector（宿主机/容器内均可）

```bash
source /usr/local/Ascend/ascend-toolkit/set_env.sh
cd m3/mspti_collector
mkdir -p build && cd build
cmake .. -DCMAKE_BUILD_TYPE=Release
make -j
ls libcannmspti.so cann-mspti-collector cann-mspti-selftest synthetic_acl
```

先跑一次生命周期自测，确认库本身没问题（不碰 NPU 通信）：

```bash
cd m3/mspti_collector/build && ./cann-mspti-selftest
```

> 路径注意：probe 默认 `--collector-lib /opt/cann-ebpf/libcannmspti.so`。若 probe 在容器里跑，
> 用容器内可见路径（例如 B1 的 `/cache/cann-ebpf/libcannmspti.so`），显式 `--collector-lib` 指定；
> `LD_PRELOAD` 的路径必须是**探针进程命名空间内可见**的路径，否则库不会加载、也不会报错得很明显。

### 3.2 路线 A：torch_npu + torchrun（首选）

无需构建，只确认版本：

```bash
python3 -c "import torch, torch_npu, torch.distributed as d; print(torch.__version__, torch_npu.__version__)"
command -v torchrun
```

### 3.3 路线 B：CANN `tools/hccl_test`

```bash
cd /usr/local/Ascend/ascend-toolkit/latest/tools/hccl_test
make -j4      # 依赖 MPI 等；产物位置按该目录 README（可能在 opbase_test/ 或当前目录）
```

harness 会在 `--hccl-src` 下按 `hccl_test`、`bin/hccl_test`、`opbase_test/hccl_test` 查找可执行文件，
找不到才执行 `make -j4`（日志写 `build.log`）。`hccl_test` 的参数（`-p/-b/-e/-f`，以及多 rank 时
是否要 `mpirun` 或 `-d` 设备列表）**随 CANN 版本不同**，必须以目录内 README 为准，必要时用
`--hccl-args` 追加或 `--hccl-cmd` 整体覆盖。

## 4. Run

### 4.1 先看计划（任何环境都可跑，不碰 NPU/容器）

```bash
sh scripts/b4_hccl_probe.sh --dry-run
sh scripts/b4_hccl_probe.sh --dry-run --route hccl-test --ranks 8 --devices 0,1,2,3,4,5,6,7
```

`--dry-run`（默认模式）只打印：路线、rank/卡、环境变量块、门禁清单、将执行的命令、合并命令，
**不**执行 `npu-smi`、不建目录、不跑 torchrun/make。

### 4.2 真正执行（独占卡，G1–G5 已确认）

```bash
# 2 rank all_reduce，卡 0,1
sh scripts/b4_hccl_probe.sh --apply --confirm-exclusive --ranks 2 --devices 0,1

# 8 rank all_reduce，全卡
sh scripts/b4_hccl_probe.sh --apply --confirm-exclusive --ranks 8 --devices 0,1,2,3,4,5,6,7

# 走 hccl_test（需要先 build）
sh scripts/b4_hccl_probe.sh --apply --confirm-exclusive --route hccl-test --ranks 2 --devices 0,1
```

`--apply` 的固定动作顺序：`--confirm-exclusive` 检查 → `uname` 必须是 Linux → 库文件存在检查 →
busy 扫描 → `mkdir -p $OUT` → `npu-smi info` 存证 → 注入环境 → 跑路线 → 存 `run.log`/`exit-code.txt`
→ 再存一次 `npu-smi info` → 列 `device-*.jsonl` 与各自 meta 记录数 → 打印合并/统计命令。

主要参数（`--help` 有全量）：

| 参数 | 默认 | 说明 |
|---|---|---|
| `--route` | `auto` | `auto` 有 `torchrun` 走 torch，否则 hccl-test |
| `--ranks` | `2` | 2..8 |
| `--devices` | `0..ranks-1` | 逻辑卡号，写入 `ASCEND_RT_VISIBLE_DEVICES` |
| `--out` | `/tmp/b4` | 产物目录；`device-{pid}.jsonl` 写在这里 |
| `--iterations` / `--elems` | `20` / `4096` | all_reduce 次数与 fp32 元素数（4096=16KiB/rank） |
| `--duration` | `0` | `CANN_MSPTI_DURATION`，0=采到进程退出（探针是短进程，用 0） |
| `--max-bytes` | `536870912` | `CANN_MSPTI_MAX_BYTES`，防日志写爆 |
| `--collector-lib` / `--mspti-lib` | 见 help | 覆盖库路径（容器内路径不同时必须改） |
| `--skip-busy-check` | off | 跳过 docker/ps 启发式，**只在人工已确认独占时**用 |
| `--hccl-args` / `--hccl-cmd` | 空 | 覆盖 hccl_test 参数/整条命令 |
| `B4_TIMEOUT` | `900` | 单次 probe 的 `timeout` 秒数，防 HCCL 卡死 |

注入的环境变量（与 `m3/mspti_collector/README.md` 一致）：

```text
LD_PRELOAD=<libmspti.so>:<libcannmspti.so>   # libmspti.so 必须在前
CANN_MSPTI_ACTIVITIES=api,acl_api,runtime_api,kernel,memcpy,hccl
CANN_MSPTI_OUTPUT=/tmp/b4/device-{pid}.jsonl  # {pid} 由 collector 替换，多 rank 各写各的
CANN_MSPTI_RUN_ID=b4-hccl-<UTC stamp>
CANN_MSPTI_DURATION=0
CANN_MSPTI_MAX_BYTES=536870912
CANN_MSPTI_FLUSH_INTERVAL_MS=1000
CANN_MSPTI_CLOCK=auto
ASCEND_RT_VISIBLE_DEVICES=0,1
HCCL_CONNECT_TIMEOUT=120
```

## 5. 产物与合并

每个 rank（以及 torchrun launcher 自身）会写自己的 `device-<pid>.jsonl` + `.meta.json`：

```text
/tmp/b4/
├── hccl_allreduce.py        # torch 路线的探针脚本（脚本内生成）
├── run.log                  # 探针 stdout（含每 rank 一行 JSON 结果）
├── exit-code.txt
├── started_at.txt / ended_at.txt
├── npu-smi-before.txt / npu-smi-after.txt
└── device-<pid>.jsonl (+ .meta.json)   # 每 rank 一份
```

合并成单文件（`docs/B1-PREP.md` 同一套工具，先 `--dry-run` 看输入列表）：

```bash
python3 scripts/b1_merge_device.py --input-dir /tmp/b4 --out /tmp/b4/merged --run-id b4-hccl-2rank --dry-run
python3 scripts/b1_merge_device.py --input-dir /tmp/b4 --out /tmp/b4/merged --run-id b4-hccl-2rank
```

> torchrun 的 launcher 进程也会继承 `LD_PRELOAD`，可能留下一个**空**的 `device-<pid>.jsonl`
> （它不初始化 ACL）。合并脚本会跳过 0 记录/非 `cann.device.v1` 的行，属预期噪声，不要当成失败。

可选：导入成 eBPF run 目录做 host↔device join（**要在探针进程存活期间** attach，且 profile 用
`cann-generic`；探针是短进程，需要并行启动 attach 或让探针末尾 `sleep`）：

```bash
./cann-ebpf attach --pid <probe_host_pid> --profile cann-generic --profiles-dir profiles \
  --duration 15s --device-jsonl /tmp/b4/merged/device.jsonl --output runs/b4-hccl
./cann-ebpf diagnose --input runs/b4-hccl --json --out runs/b4-hccl/diagnose.json
```

## 6. 验收

按顺序打勾，全部通过才算 B4 有结论（对应 `docs/B4-HCCL-PLAN.md` §5）：

| # | 验收项 | 判定标准 | 命令/证据 |
|---|---|---|---|
| A1 | collector 真的加载了 | 至少一个非空 `device-<pid>.jsonl` 且 `.meta.json` 里 `records_written>0`，`stop_complete=true` | `ls -l /tmp/b4/device-*.jsonl`；`grep records_written /tmp/b4/device-*.jsonl.meta.json` |
| A2 | 出现 HCCL 记录 | 合并后 `kind=HCCL` 计数 > 0 | 见下方统计脚本 |
| A3 | 区分两种来源 | 统计 `source`：`mspti_communication` 与/或 `mspti_hccl_no_correlation` 各多少条 | 同上 |
| A4 | correlationId 结论 | 若 `mspti_communication` 存在 → 记录其 `correlation_id` 非 0 条数；若只有 `mspti_hccl_no_correlation` → 结论必须写“时间窗降级” | 同上，输出 `hccl_with_nonzero_correlation_id` |
| A5 | 诊断不越界 | 设备-only/降级场景下，`diagnose` 只输出 `unknowns`，不得输出 `communication_bound` 等精确结论 | `docs/DIAGNOSE.md` §“没有 device.jsonl 或 join 未匹配时” |
| A6 | 证据完整 | 记录卡数、消息大小、迭代次数、耗时、HBM 峰值、CANN 版本、库 sha256 | `run.log` + `npu-smi-*.txt` |

统计脚本（无 jq 依赖，harness 也会打印同一段）：

```bash
python3 - /tmp/b4/merged/device.jsonl <<'PY'
import collections, json, sys
kinds, src, nonzero = collections.Counter(), collections.Counter(), 0
for line in open(sys.argv[1], encoding="utf-8-sig", errors="replace"):
    line = line.strip()
    if not line:
        continue
    try:
        r = json.loads(line)
    except Exception:
        continue
    if r.get("schema") != "cann.device.v1":
        continue
    kinds[r.get("kind", "?")] += 1
    if r.get("kind") == "HCCL":
        src[r.get("source", "?")] += 1
        nonzero += 1 if r.get("correlation_id") else 0
print("kinds:", dict(kinds))
print("hccl_source:", dict(src))
print("hccl_with_nonzero_correlation_id:", nonzero)
PY
```

jq 等价写法（人肉看更方便）：

```bash
jq -r 'select(.kind=="HCCL")|.source' /tmp/b4/merged/device.jsonl | sort | uniq -c
jq -r 'select(.kind=="HCCL" and (.correlation_id|tonumber)>0)|.name' /tmp/b4/merged/device.jsonl | wc -l
```

## 7. 回滚 / 清理

B4 harness **不改任何持久配置**：不写 `/etc/vllm-ds4.env`、不装 systemd unit、不碰 CANN 库/驱动，
只写 `/tmp/b4` 和（torch 路线）该目录下的探针脚本。因此回滚就是清理临时产物：

```bash
# 1) 确认 V4 没被影响（本 harness 不会动它，这里只是核对）
systemctl is-active vllm-ds4-8905.service
curl -s -o /dev/null -w '%{http_code}\n' -m 5 http://127.0.0.1:8905/health

# 2) 归档证据后清理临时目录（先打包再删）
tar czf /tmp/b4-evidence-$(date -u +%Y%m%dT%H%M%SZ).tar.gz -C /tmp b4
rm -rf /tmp/b4

# 3) 若用了容器跑：删掉探针容器
docker rm -f <b4-probe-container> 2>/dev/null || true
```

环境变量只影响执行它的 shell 子进程；关掉该 shell 即消失，无需全局 unset。若曾 `--stage` 过 collector
（B1 流程），按 `docs/B1-PREP.md` §4 回滚，与 B4 无关。

## 8. 风险与边界

- **共享卡是硬风险**：HCCL 会占 HBM 与 HCCL 链路，和 V4 抢资源；harness 只能挡住“有推理进程”的
  明显情况，HBM 是否够必须人工看 `npu-smi`。
- **msPTI 单 subscriber**：同一进程只能订阅一次，且必须在 ACL/torch_npu 初始化前 `LD_PRELOAD`；
  探针是新进程，满足条件；对已运行的 V4 不能动态注入。
- **correlationId 只在同 device 内唯一**（`m3/mspti_collector/README.md` 边界），跨卡不能直接合并。
- **`msptiActivityHccl` 无 correlationId**：有这类记录也不能声称精确关联。
- **HCCL 可能卡死**：`HCCL_CONNECT_TIMEOUT=120` + `B4_TIMEOUT=900` 两层兜底；超时后保留
  `run.log` 与已有 `device-*.jsonl`，不要重跑前先看日志。
- **8 rank 的 `hccl_test` 参数未知**：`-p 8` 是否等价于 8 进程/8 卡，取决于该版本工具，必须人工确认。
- **launcher 空文件**：torchrun 主进程可能产生 0 记录文件，属噪声。
- **`--device-jsonl` 必须是宿主可见路径**（`docs/M3-DEVICE-JOIN.md`），容器内路径先 `docker cp`。

## 9. 仍需人工确认的点

1. G1/G2：目标卡是否真的独占、每卡 HBM 是否 ≥16GB（本机当前 V4 占 8 卡，**不具备条件**）。
2. 执行环境：宿主机直跑，还是 CANN 容器内跑？决定 `--collector-lib` / `--mspti-lib` 的实际路径。
3. `hccl_test` 的构建依赖（MPI）是否装好，以及 `-p/-b/-e/-f` 在该 CANN 版本下的确切语义。
4. 是否需要 host↔device join（决定要不要在探针存活期间并行跑 `attach --profile cann-generic`）。
5. 8 rank 是否必须整机独占（若只能拿到 2 卡，先按 2 rank 出结论，8 rank 留待整机窗口）。
6. 证据归档位置（`/tmp/b4` 之外的持久目录）与保留期。
