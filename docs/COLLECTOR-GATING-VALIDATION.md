# Collector 限制项验证：`CANN_MSPTI_ONLY_CMDLINE` / `CANN_MSPTI_MAX_BYTES` / `CANN_MSPTI_AUTO_STOP`

日期：2026-11-27 18:24–18:47（CST）
验证人：DSH 子代理
范围：只验证 collector 自身限制逻辑与 V4 只读进程信息；**未停 V4、未向 V4 注入 collector、未发压测、未修改 tracked 代码、未 commit**。

---

## 0. 结论（TL;DR）

1. **`CANN_MSPTI_MAX_BYTES` 生效且值得依赖**：合成验证 65,536 字节上限时最终文件 65,408 字节（292 条记录，7,708 条被丢弃），meta 中 `truncated=true`、`stop_complete=true`。真实代码路径（`WriteRecord` 的 `g_bytes_written` 判断）被直接覆盖。
2. **`CANN_MSPTI_ONLY_CMDLINE` 的过滤逻辑本身正确**：不匹配时完全不启动（0 个文件、退出码 0）；匹配时正常启动。
3. **但它无法只命中 V4 的 Worker**：V4 Worker 在 `exec` 之后由 vLLM 调用 `set_process_title()` 改写 argv，collector 在 `LD_PRELOAD` 构造函数阶段读到的 `/proc/self/cmdline` 仍是 `python3 -c "from multiprocessing.spawn import spawn_main; ..." --multiprocessing-fork`。
   - 设 `CANN_MSPTI_ONLY_CMDLINE=VLLM::Worker_TP` → **匹配 0 个进程，采不到任何 device 数据（静默失效）**；
   - 设 `--worker` → 同样匹配 0；
   - `--multiprocessing-fork` 会命中 17 个进程（EngineCore + 8 Worker + 8 个 Worker 的 spawn helper），并非只 Worker。
4. **因此当前这三项限制只解决“单文件无上限/总输出过大”，不解决“28 个进程/多文件”**：
   - 8 个非空 Worker × 64 MiB = 最多约 512 MiB（原始 3.2 GB → 约 0.5 GB，约 6 倍收敛）；
   - 但进程/文件数量不变：启动瞬间仍是 8 个非空文件 + 约 20 个空文件；当前稳态容器内共有 **313 个后代进程**（8 Worker + 17 spawn_main + 147 forkserver + 6 resource_tracker + 其他）。
5. **要“只命中 Worker”需要在 collector 里新增父进程过滤**（建议 `CANN_MSPTI_ONLY_PARENT_COMM=VLLM::EngineCor`，比较 `/proc/<ppid>/comm`），或按 Worker PID 显式启动。只靠现有 `CANN_MSPTI_ONLY_CMDLINE` 做不到。
6. **`CANN_MSPTI_AUTO_STOP` 必须保持默认 0**：把 `AUTO_STOP=1` 后静态析构里调用 `cann_mspti_stop()` 复现了 SIGSEGV（exit 139）；默认 0 时 bash/python 均 exit 0。

> 一句话：**大小问题基本解决（每文件 64 MiB 硬顶），进程扇出问题没有解决；不要把 `ONLY_CMDLINE` 设成 `VLLM::Worker_TP`，否则会变成“看起来配了过滤、实际一行数据都没有”。**

---

## 1. 被验证的代码

### 1.1 `m3/mspti_collector/collector.cpp`

- `CannMsptiAutoStart()`（约 L1088–L1151）在 `LD_PRELOAD` 静态构造阶段执行，顺序为：
  1. `CANN_MSPTI_OUTPUT` 为空 → 直接 return；
  2. `CANN_MSPTI_ONLY_COMM`：读 `/proc/self/comm`，按逗号分词，**子串**匹配（L1096–L1114）；
  3. `CANN_MSPTI_ONLY_CMDLINE`：读 `/proc/self/cmdline`（最多 8192 字节），把 `\0` 换成空格，**子串**匹配，不匹配直接 return（L1115–L1135）；
  4. 替换 `{pid}`，读 `CANN_MSPTI_MAX_BYTES`（0 表示默认 512 MiB）并调用 `cann_mspti_start()`。
- `WriteRecord()`（L396–L419）：`g_max_bytes > 0 && g_bytes_written + line.size() > g_max_bytes` 时置 `g_truncated=true`、`g_records_skipped++`，只打印一次 stderr。
- `cann_mspti_start()`：`WriteMetaFile(true)` 与 `fopen(output,"w")` 发生在 `msptiSubscribe()` **之前**（L872–L890）。因此“门禁是否通过”可以从**是否产生 `.meta.json` / 输出文件**判定，即使 `msptiSubscribe` 因无 NPU 失败也成立。
- `CannMsptiAutoStop()`（L1153–L1167）：仅当 `CANN_MSPTI_AUTO_STOP=1` 时在静态析构里调用 `cann_mspti_stop()`；默认 0 表示让进程退出时关闭文件。

### 1.2 `deploy/b1/stage-collector.sh`

当前生成的 env 块包含：

```
CANN_MSPTI_ACTIVITIES=api,acl_api,runtime_api,kernel,memcpy,hccl
CANN_MSPTI_ONLY_COMM=vllm,python
CANN_MSPTI_OUTPUT=/cache/cann-ebpf/device-{pid}.jsonl
CANN_MSPTI_RUN_ID=vllm-collector
CANN_MSPTI_MAX_BYTES=67108864
CANN_MSPTI_AUTO_STOP=0
CANN_MSPTI_FLUSH_INTERVAL_MS=1000
```

注意：**脚本没有设置 `CANN_MSPTI_ONLY_CMDLINE`**（默认关闭）。如果后续有人按文档示例把它设成 `VLLM::Worker`，将得到 0 个文件。

---

## 2. 验证环境与方法

- 宿主机：`root@target-ip`（aarch64，V4 生产机，只做只读观察）。
- 镜像：`ds4/vllm-ascend:v0.23.0rc1-nonroot`（V4 正在使用的镜像；内含 **CANN 9.0.1**：`/usr/local/Ascend/cann-9.0.1/tools/mspti/{lib64,include}`，g++ 11.4）。
- 临时容器：`docker run --rm --network none --memory 4g --cpus 4 --entrypoint bash -v /tmp/cann-gating:/work <image> /work/run_tests.sh`
  - **不挂载任何 NPU 设备**、不映射端口、不联网；容器退出即删除。
- 构建：
  - 真实库版：`g++ -std=c++11 -fPIC -shared -I<cann>/tools/mspti/include collector.cpp -L<cann>/tools/mspti/lib64 -lmspti -Wl,-rpath,... -pthread -o libcannmspti_real.so`（219,584 B）
  - 合成版：`collector.cpp + stub_mspti.cpp`（stub 只实现 collector 用到的 9 个 `mspti*` 符号，用同步方式伪造 API 记录；**`WriteRecord`/`g_max_bytes` 是真实代码**）
- 判定方法：每次运行前清空输出目录；运行后统计文件、读取 `.meta.json`、记录退出码。

> 环境限制（必须说明）：临时容器没有 CANN driver（`libascend_hal.so` 不存在），真实 `libmspti.so` 在 `msptiSubscribe` 内部会 `abort()`（exit 134）。这是**容器缺 NPU 驱动**导致，不是 collector 回归；因此“真实 mspti 下 bash/python exit 0”在纯 CPU 容器内不可测，B1 已在真实 V4 上验证过 CANN 9.2 的 exit 0。门禁决定发生在 `msptiSubscribe` 之前，所以不影响 `ONLY_COMM`/`ONLY_CMDLINE` 的判定结论。

---

## 3. 验证矩阵

### 3.1 真实 CANN 9.0.1 `libmspti.so`（门禁行为）

| 用例 | 过滤条件 | 退出码 | 输出文件 | 结论 |
|---|---|---:|---:|---|
| A | bash，无过滤 | 134 | 2（meta + 0 B jsonl） | 门禁放行；随后因缺 `libascend_hal.so` abort（环境限制） |
| B | python3，无过滤 | 134 | 2 | 门禁放行；同上 |
| C | bash + `ONLY_COMM=python` | **0** | **0** | comm 不匹配，完全不启动 ✅ |
| D | python3 + `ONLY_COMM=python` | 134 | 2 | 门禁放行（comm=python3）✅ |
| E | python3 + `ONLY_CMDLINE=VLLM::Worker_TP` | **0** | **0** | 普通 python 命令不含该串，不启动 ✅ |
| F | `exec -a VLLM::Worker_TP0_EP0 python3 ...` + `ONLY_CMDLINE=VLLM::Worker_TP` | 134 | 2 | 命中，放行 ✅ |
| G | 同上但 `ONLY_CMDLINE=VLLM::NoSuchWorker` | **0** | **0** | 不命中，不启动 ✅ |
| H | python3 + `ONLY_COMM=vllm` | **0** | **0** | `/proc/self/comm` 是 `python3`，不匹配 ✅ |
| I | 命中 + `AUTO_STOP=1` | 134 | 2 | 环境 abort 掩盖析构行为，见 3.2 |
| J | 命中 + `AUTO_STOP=0` | 134 | 2 | 同上 |

> F 说明：`exec -a` 在 **exec 之前**把 argv[0] 设成 `VLLM::Worker_TP0_EP0`，所以 collector 构造函数能看到；这正是 V4 Worker 做不到的前提（见第 5 节）。

### 3.2 合成 msPTI（排除 NPU 依赖后的生命周期 + 门禁）

| 用例 | 条件 | 退出码 | 输出 | 结论 |
|---|---|---:|---|---|
| K | bash，无过滤，`AUTO_STOP` 默认 | **0** | meta 874 B + 0 B jsonl | 构造函数/析构不再崩溃 ✅ |
| L | python3，无过滤，flush=50 ms，sleep 0.3 s | **0** | **3,125,728 B** | 正常写记录 ✅ |
| M | python3 + `ONLY_CMDLINE=VLLM::Worker_TP` | **0** | 0 文件 | 不匹配不启动 ✅ |
| N | `exec -a VLLM::Worker_TP0_EP0` + 同过滤 | **0** | **3,146,292 B** | 命中即启动并写入 ✅ |
| O | python3 + `ONLY_COMM=vllm` | **0** | 0 文件 | comm 不匹配 ✅ |
| P | 命中 + `AUTO_STOP=1` | **139** | 3,880,000 B | **静态析构 SIGSEGV**（数据已落盘）；证明默认 0 必要 ⚠️ |

### 3.3 `CANN_MSPTI_MAX_BYTES` 截断

命令：`maxbytes_probe /work/max.jsonl 65536`（合成 msPTI 持续灌入 API 记录）

| 指标 | 值 |
|---|---|
| 进程退出码 | **0** |
| 最终文件大小 | **65,408 B**（≤ 65,536） |
| 记录行数 | 292 |
| `records_written` | 292 |
| `records_skipped` | 7,708 |
| `max_bytes` | 65,536 |
| `truncated` | **true** |
| `stop_complete` | true |
| stderr（仅一次） | `cann-mspti: max_bytes 65536 reached; further records are dropped` |

→ 上限是**硬上限**：`g_bytes_written + line.size() > max` 时丢弃整条，文件不会越过阈值。
（本次验证用 64 KiB 便于快速触发；“64 MiB 实际值”按同一线性逻辑成立，未单独跑 64 MiB 全量。）

### 3.4 加载期 cmdline 可见性（关键）

用 20 行 `LD_PRELOAD` logger 在构造函数里记录 `/proc/self/cmdline`：

| 进程 | comm | 构造函数看到的 cmdline |
|---|---|---|
| python 多进程 spawn 子进程 | `python3` | `/usr/local/python3.12.13/bin/python3 -c from multiprocessing.spawn import spawn_main; spawn_main(tracker_fd=4, pipe_handle=6) --multiprocessing-fork` |
| `exec -a VLLM::Worker_TP0_EP0 python3 ...` | `python3` | `VLLM::Worker_TP0_EP0 -c import time;time.sleep(0.2)` |

→ 结论：**只有 exec 前设置的 argv 才对 `CANN_MSPTI_ONLY_CMDLINE` 可见**；进程运行中 `set_process_title()` 改写的标题不可见。

---

## 4. V4 只读侦察（`ps -eo pid,args` / `/proc`，未启动 collector）

采集时间：2026-11-27 18:3x（V4 已运行约 3.5 小时，healthy）。

- 容器：`deepseek-v4-flash-8905`，镜像 `ds4/vllm-ascend:v0.23.0rc1-nonroot`，CANN **9.0.1**。
- 主进程：`PID 3076197`，`/usr/local/python3.12.13/bin/vllm serve /model ... --tensor-parallel-size 8 --enable-expert-parallel ...`（**命令里没有 `--worker`**）。
- 进程树（节选）：

```
3076197 vllm            /usr/local/python3.12.13/bin/vllm serve /model ...
 3079374 python3        -c from multiprocessing.resource_tracker import main;main(35)
 3079391 VLLM::EngineCor VLLM::EngineCore
  3081988 VLLM::Worker_TP VLLM::Worker_TP0_EP0
  3084049 VLLM::Worker_TP VLLM::Worker_TP1_EP1
  ... （共 8 个 Worker）
   3117453 python3      -c from multiprocessing.spawn import spawn_main; ... --multiprocessing-fork
   3118537 python3      -c from multiprocessing.forkserver import main; ...
```

- 8 个 Worker 的 `PPID` 全部是 `3079391`，父进程 `comm = VLLM::EngineCor`。
- Worker 当前 `cmdline` 已是运行期标题：`VLLM::Worker_TP0_EP0` … `VLLM::Worker_TP7_EP7`（由 vLLM 在 Python 内改写）。
- 全容器后代进程：**313**；其中 `spawn_main` 17 个、`forkserver` 147 个、`resource_tracker` 6 个。
- `/etc/vllm-ds4.env` 当前只有 `LD_PRELOAD=/usr/lib/aarch64-linux-gnu/libjemalloc.so.2`，**没有任何 `CANN_MSPTI_*`**，collector 未启用（与“已回滚”一致）。
- `/data/cache/cann-ebpf/libcannmspti.so`（74,296 B，16:10）`strings` 已含 `CANN_MSPTI_ONLY_CMDLINE`、`CANN_MSPTI_MAX_BYTES`、`CANN_MSPTI_AUTO_STOP`。

### vLLM 源码证据（镜像内 `/vllm-workspace/vllm/vllm`）

- `v1/executor/multiproc_executor.py`：
  - `set_process_title(name="Worker")`（L1002）与按 TP/EP 拼接的 `set_process_title(name=process_name)`（L1030）位于 `WorkerProc.setup_proc_title_and_log_prefix()`，**在 Worker 子进程内部执行**，发生在 `LD_PRELOAD` 构造函数之后。
  - `set_multiprocessing_worker_envs()` 中调用 `_maybe_force_spawn()`，即 Worker 强制走 `spawn`。
- 其它标题：`VLLM::EngineCore`（`v1/engine/core.py:1135`）、`APIServer`（`v1/utils.py:490`）、`DPCoordinator`（`v1/engine/coordinator.py:152`）。

这与第 3.4 节的实验一致：Worker 的加载期 cmdline 是 `multiprocessing.spawn ... --multiprocessing-fork`，**不含** `VLLM::Worker`。

---

## 5. `CANN_MSPTI_ONLY_CMDLINE` 应该匹配什么？

### 5.1 直接结论

- 对 V4 **没有**任何 cmdline 字符串可以“只命中 Worker”。
  - `VLLM::Worker` / `VLLM::Worker_TP` → 0 命中（静默无数据）；
  - `--worker` → 0 命中（V4 启动命令无此参数）；
  - `--multiprocessing-fork` → 17 命中（EngineCore + 8 Worker + 8 个 Worker 的 spawn helper），不是 Worker-only；
  - `VLLM::EngineCore` → 0 命中（Worker 是 spawn，不是 fork 自已改名的 EngineCore）。
- 因此 **不要**把 `deploy/b1/stage-collector.sh` 的 `CANN_MSPTI_ONLY_CMDLINE` 设为 Worker 标题。

### 5.2 推荐做法

**推荐（需一处小改动）**：新增按父进程 comm 过滤，例如

```
CANN_MSPTI_ONLY_PARENT_COMM=VLLM::EngineCor
```

实现：在 `CannMsptiAutoStart()` 里读 `/proc/<getppid()>/comm`（注意 comm 最长 15 字节，`VLLM::EngineCor` 正好是 15 字节），做子串匹配后再继续。只读侦察已证明 8 个 Worker 的父进程 comm 全部是 `VLLM::EngineCor`，因此该过滤会精确命中 8 个 Worker，且排除 EngineCore/frontend/forkserver/helpers。

**次选**：按 PID 显式启动（`CANN_MSPTI_PID` 或 CLI `cann-mspti-collector --pid`），对 8 个 Worker 逐个启动；不需要改 collector 的自动启动逻辑，但编排成本更高。

**如果现在必须给出一个 `CANN_MSPTI_ONLY_CMDLINE` 字符串**：留空（不设置），仅靠 `CANN_MSPTI_ONLY_COMM=vllm,python` + `CANN_MSPTI_MAX_BYTES`；这是当前代码下唯一“不会误伤、也不会静默丢数据”的配置。

---

## 6. 推荐 env（当前代码可直接用）

```sh
LD_PRELOAD=<原有>:<cann>/tools/mspti/lib64/libmspti.so:/cache/cann-ebpf/libcannmspti.so
CANN_MSPTI_ACTIVITIES=api,acl_api,runtime_api,kernel,memcpy,hccl
CANN_MSPTI_ONLY_COMM=vllm,python
# CANN_MSPTI_ONLY_CMDLINE 不设置（当前无法只命中 Worker；设 VLLM::Worker 会 0 输出）
CANN_MSPTI_OUTPUT=/cache/cann-ebpf/device-{pid}.jsonl
CANN_MSPTI_RUN_ID=vllm-collector
CANN_MSPTI_MAX_BYTES=67108864      # 每文件 64 MiB 硬顶
CANN_MSPTI_AUTO_STOP=0             # 必须 0；=1 会静态析构 SIGSEGV
CANN_MSPTI_FLUSH_INTERVAL_MS=1000
```

若采纳 5.2 的改动，再加：

```sh
CANN_MSPTI_ONLY_PARENT_COMM=VLLM::EngineCor
```

---

## 7. 残留风险

1. **进程数没有下降**：`MAX_BYTES` 只限大小；`ONLY_COMM=vllm,python` 仍让全部 Python helper 启动 collector。启动期仍是 8 个非空 + 约 20 个空文件；稳态容器 313 个后代进程。要达到“只有 8 个 Worker”，必须用父进程过滤或按 PID 启动。
2. **总量仍可能到约 512 MiB/窗口**：8 个 Worker × 64 MiB；没有总量上限、没有轮转/保留策略。长窗口需配合 `CANN_MSPTI_DURATION` 或外部清理。
3. **截断是“静默丢尾”**：到 64 MiB 后只打印一次 stderr，meta 里 `truncated=true`、`records_skipped` 增大；下游 host↔device join 会看到**不完整时间线**，必须读取 meta 判断。
4. **`{pid}` 命名**：同一进程重启后 PID 可能复用；旧文件会被 `fopen("w")` 覆盖，需要 run 目录隔离。
5. **`ONLY_CMDLINE` 语义**：只用前 8192 字节、大小写敏感、纯子串、空串=关闭；短字符串可能误命中无关参数。不要把长路径当匹配串。
6. **`ONLY_COMM` 语义**：只读 `/proc/self/comm`（内核截断到 15 字节）。加载期 python 进程 comm 是 `python3`，不是 `VLLM::Worker_TP`，所以不能拿它替代 Worker 过滤。
7. **`AUTO_STOP=0` 的代价**：进程退出不写最终 meta（`stop_complete=false`、`stop_unix_ns=0`），计数器是最后一次周期快照；异常退出可能残留未 flush 的尾部。本次已实测 `AUTO_STOP=1` 静态析构 SIGSEGV（exit 139），所以不能改回 1。
8. **msPTI 单订阅者**：helper/子进程里若已有其它 msPTI 订阅者，`msptiSubscribe` 返回 2，collector 直接放弃；覆盖率可能不一致。
9. **真实 msPTI 端到端未在临时容器验证**：缺 `libascend_hal.so` 导致 abort（exit 134）；真实 NPU 上的 `msptiSubscribe`/记录内容需按 B1 方法在真实进程验证（B1 已有 CANN 9.2 真实 V4 device 证据）。
10. **`MAX_BYTES` 只管 JSONL**，不含 meta；实测 65,408 ≤ 65,536，符合预期，但别把 meta 体积算进预算。
11. **helper 进程的固定开销**：即使 0 记录，每个进程仍会 `msptiSubscribe` + `EnableActivities` + 建 1 MiB stdio 缓冲，28+ 进程的常驻开销不会被 `MAX_BYTES` 消除。

---

## 8. 复现命令

```sh
# 1) 构建（在临时容器内，不挂 NPU）
docker run --rm --network none --memory 4g --cpus 4 --entrypoint bash \
  -v /tmp/cann-gating:/work ds4/vllm-ascend:v0.23.0rc1-nonroot /work/run_tests.sh

# 2) 单独跑大小上限（合成 msPTI，真实 WriteRecord）
/work/maxbytes_probe /work/max.jsonl 65536
stat -c %s /work/max.jsonl
grep -E '"(truncated|records_written|records_skipped|max_bytes)"' /work/max.jsonl.meta.json

# 3) V4 只读进程信息（不启动 collector）
ps -eo pid,ppid,comm,args | grep -E 'vllm|VLLM::'
for p in $(ps -eo pid=,comm= | awk '$2 ~ /^VLLM::Worker/ {print $1}'); do
  echo "$p parent=$(awk '{print $4}' /proc/$p/stat) $(tr '\0' ' ' < /proc/$p/cmdline | cut -c1-80)";
done
```

> 本次所有容器均为 `--rm`，验证结束后已清理服务器 `/tmp/cann-gating*`；V4 未重启、未 exec、未注入、未压测。

---

## 9. 变更说明

- 新增本文档：`docs/COLLECTOR-GATING-VALIDATION.md`。
- 未修改任何 tracked 代码（`collector.cpp`、`stage-collector.sh` 等均保持原样），未 commit。
- 验证用临时源码/脚本只存在于服务器 `/tmp/cann-gating`（已删除）与本地临时目录，不属于仓库交付物。
