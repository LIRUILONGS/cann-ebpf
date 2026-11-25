# M1 profiles 与只读验证

日期：2026-11-26

## 1. 范围

本交付包含 M1 profile、smoke 脚本和已实测的 host 采集路径；不修改目标服务、容器、CANN 库或驱动，不重启 V4/Orinfer。默认 smoke 只执行 `validate` 和 `discover --dry-run`；只有显式传 `--attach` 才允许短时 attach。

M1 的 host 采集重点是 H1 同步阻塞和 M 内存/句柄生命周期。H2 event/submit 和 C HCCL 的函数、采样参数保留在 profile 中用于后续阶段，但 M1 不把 host 入队时长解释成设备执行时长。

## 2. 服务器事实与本轮目标

服务器 preflight 已确认：

| 项 | 值 |
|---|---|
| 系统 | openEuler 5.10 aarch64 |
| BTF | 存在 |
| `perf_event_paranoid` | 2 |
| `unprivileged_bpf_disabled` | 0 |
| root caps | 完整 |
| tracefs/debugfs | 已挂载，`uprobe_events` 可写 |
| V4 health | 200 |
| frontend | PID 4188787 |
| EngineCore | PID 4192024 |
| `VLLM::Worker_TP` | 1452 / 3962 / 6568 / 8890 / 11340 / 13505 / 15629 / 17758 |

这些 PID 只用于本轮对照，不能写进 profile。服务重启后 PID 会变化，profile 必须按 `comm/cmdline/maps` 发现目标。

真实采集基线已通过（V4）：tracefs 单 PID 3 秒 166 spans；eBPF 单 PID 2 秒 70 spans、8 worker 2 秒 487 spans；最终交付件 8 worker 30 秒 2088 spans（回归批次 2665 spans）。采集前后 `/health` 均为 HTTP 200，uprobe 清理后无残留，服务保持 active。对应可执行文件为仓库内 `dist/cann-ebpf-linux-arm64`（version=0.1.0-m1），smoke 默认使用该路径。

## 3. 目标选择

### 3.1 vLLM-Ascend

`profiles/vllm-ascend.yaml`：

- `selector: all_matching`：当前已存在的 frontend、EngineCore、8 个 Worker_TP 和 8 个 Python spawn 子进程都需要覆盖，不能只选第一个进程。
- `process_tree: true` + `include_children: true`：覆盖 attach 后新 fork 的进程；当前 M0 `discover` 仍逐个枚举 `/proc`，真正按进程树展开由后续 attach 实现。
- 稳定匹配条件：
  - `comm: "VLLM::Worker_TP"`；
  - `comm: "VLLM::EngineCor"`（kernel comm 最长 15 字符，不能写 `EngineCore`）；
  - `cmdline_contains: vllm` 覆盖 frontend 的 `/vllm serve`；
  - `maps_contains: libtorch_npu.so` 覆盖 worker 的 Python spawn 子进程；
  - `all: maps_contains: libascendcl.so` 排除只加载 PyTorch 的普通 Python；
  - `exclude` 明确排除 Orinfer 的 `comm/cmdline/container_name`。
- `max_processes: 128` 足够覆盖 18 个目标，同时超过上限时会拒绝启动，不静默截断。

M0 已实测得到 18 个目标：8 个 Worker_TP、8 个 Python 子进程、1 个 frontend、1 个 EngineCore。每个目标发现 4 个库；按 18 个进程累计是 666 个可用符号、1998 个未命中符号，单个目标为 37/148。

### 3.2 Orinfer

`profiles/orinfer.yaml`：

- `selector: first_match`：Orinfer 的主进程是唯一入口，attach 由 `process_tree/include_children` 覆盖其子进程；不使用 `all_matching`，避免误选同机其他 8000 端口服务。
- 必须同时满足：
  - `any`: `comm=orinfer`、cmdline 含 `orinfer`、`start-orinfer.sh` 或容器名 `orinfer-cann920`；
  - `all`: 映射了 `libascendcl.so`。
- 镜像名不能作为判据：历史环境里 Orinfer 的镜像也可能是 `vllm-ascend`。
- Orinfer 没运行时，匹配结果必须为空，不能命中 V4 的 `Worker_TP`/`EngineCor`；脚本在只读模式下输出 `SKIP`，显式 `--attach` 时返回 3。
- 不写死 PID 1691816；该历史 PID 已不存在。

## 4. H1/H2/M/C 函数边界

| 层 | 函数 | profile probe | M1 语义 |
|---|---|---|---|
| H1 host 阻塞 | `aclrtSynchronizeStream`、`aclrtSynchronizeStreamWithTimeout`、`aclrtSynchronizeEvent` | `sync`, full | 真正可能阻塞宿主线程；全量 entry/exit 配对 |
| H2 event 依赖 | `aclrtRecordEvent`、`aclrtStreamWaitEvent`、`aclrtQueryEventStatus`、`aclrtQueryEventWaitStatus`、`aclrtQueryEvent` | `event`, aggregate | 只测 event 入队/轮询/依赖，不代表设备执行完成 |
| H2 异步提交 | `aclrtMemcpyAsync`、`aclrtLaunchKernel`、`rtKernelLaunch`、`rtKernelLaunchWithFlagV2`、`rtMemcpyAsync` | `submit`, sample 1/64 | 只测 host 入队延迟；慢/错误调用强制保留 |
| M 内存 | `aclrtMalloc/Free`、`aclrtMallocHost/FreeHost`、`aclrtHostRegister/Unregister`、`aclrtGetMemInfo`、`rtMemGetInfoEx` | `memory`, full | 分配、锁页、HBM 余量；慢调用全量保留 |
| 关联键 | `aclrtSetDevice/GetDevice`、`aclrtSetCurrentContext`、stream/event create/destroy/reset | `handle`, full | 用于把 span 归到 device/stream/event；不代表阻塞 |
| C HCCL | `HcclAllReduce/AlltoAll/AllGather/ReduceScatter/Broadcast/Send/Recv` | `hccl`, sample 1/16 | 仅 host 入队；设备侧 HCCL timeline 属于 M3 msPTI |

`aclrtQueryEvent*` 的返回值需要结合 `ACL_EVENT_NOT_READY` 等状态解释，不能把轮询本身当成“设备忙”。

## 5. msPTI 在 M1 的处理

为保持 M0 配置兼容，profile 保留 vLLM `mspti.mode=shared`、Orinfer `mspti.mode=exclusive` 和 Orinfer `cann.version=9.2.0`。这不代表 M1 已启用设备侧采集：

- M1 的 tracefs 路径使用 `attach-tracefs --only sync,memory,handle`，只挂 H1 和 M/关联键 host 探针；eBPF 路径同样只覆盖 host 探针，不初始化 msPTI；
- M3 完成独立验证后才能启用 msPTI；
- Orinfer 的 `libmspti.so` 绝对路径在容器内可能不存在，M3 启用前必须按 `/proc/<pid>/root` 重新解析，不能直接相信宿主绝对路径。

## 6. smoke 脚本

脚本：`scripts/m1-smoke.sh`

默认只读：

```bash
bash scripts/m1-smoke.sh
```

等价于 `vllm-ascend` + `validate` + `discover --dry-run`，并检查 8 个 Worker_TP、frontend、EngineCore 是否齐全。

指定 profile/PID/时长：

```bash
bash scripts/m1-smoke.sh --profile vllm-ascend
bash scripts/m1-smoke.sh --profile vllm-ascend --pid 4188787
bash scripts/m1-smoke.sh --profile orinfer
bash scripts/m1-smoke.sh --profile vllm-ascend --out-dir runs/m1-vllm-drill
```

显式短时 attach：

```bash
bash scripts/m1-smoke.sh --profile vllm-ascend --pid 1452 --attach --duration 3s
bash scripts/m1-smoke.sh --profile vllm-ascend --pid 4188787 --attach --duration 30s
bash scripts/m1-smoke.sh --profile orinfer --attach --duration 30s
bash scripts/m1-smoke.sh --profile vllm-ascend --pid 1452 --attach --only sync,memory
```

对应底层命令（用于复核路径和参数）：

```bash
./dist/cann-ebpf-linux-arm64 attach-tracefs \
  --pid 1452 \
  --profile vllm-ascend \
  --profiles-dir profiles \
  --duration 3s \
  --output runs/m1-vllm-pid1452 \
  --only sync,memory,handle
```

参数：

| 参数 | 说明 |
|---|---|
| `--profile` | `vllm-ascend`（默认）或 `orinfer`，也支持 YAML 路径 |
| `--pid` | 只发现/attach 一个 PID；vLLM 全量检查必须省略，vLLM attach 必须提供 |
| `--duration` | 1–300 秒，默认 30s |
| `--out-dir` | 验证日志和 attach 输出目录 |
| `--profiles-dir` | profile 目录，默认仓库内 `profiles/` |
| `--binary` | 默认 `dist/cann-ebpf-linux-arm64` |
| `--expect-workers` | vLLM 无 `--pid` 时期望的 Worker_TP 数，默认 8；设为 0 可关闭检查 |
| `--only` | attach 的函数类别，默认 `sync,memory,handle`；`all` 表示不过滤 |
| `--attach` | 唯一允许 attach 的开关；M1 tracefs 后端一次只 attach 一个 PID |

脚本不会执行 `systemctl restart`、`docker restart`，也不会停止/重启 Orinfer 或 V4。attach 只按 duration 自动结束。`attach-tracefs` 是单 PID 后端：vLLM 全量只用于发现，attach 必须显式 `--pid`；Orinfer 无 `--pid` 时选第一个已发现目标。

`--attach` 调用 `attach-tracefs`，需要包含该子命令的 M1 二进制。脚本会保留 attach 的原始退出码，不把失败伪装成成功。

## 7. 预期输出

只读模式：

```text
[1/3] validate
OK kind=Profile profile=vllm-ascend ...
[2/3] discover --dry-run
  targets=18 workers=8 frontend=1 enginecore=1
  libraries=4 symbols=37/148 (per target; see discover.txt for every PID)
  target processes:
    pid=1452 comm=VLLM::Worker_TP
    ...
    pid=4188787 comm=vllm
    pid=4192024 comm=VLLM::EngineCor
[3/3] attach skipped (default read-only mode)
```

文件：

```text
<out>/validate.txt
<out>/discover.txt
<out>/attach.txt          # 仅 --attach
<out>/attach/             # 仅 --attach
```

Orinfer 未运行：

```text
discover exit=3
SKIP: Orinfer is not running; no process matched and no false-positive target was selected.
```

## 8. 失败诊断

| 现象 | 先检查 | 处理 |
|---|---|---|
| 容器内库找不到 | `/proc/<pid>/root` 是否可读、`/proc/<pid>/maps` 中的库路径是否存在、是否带 `(deleted)` | 用 `/proc/<pid>/root` 解析，不假设宿主 CANN 路径；`deleted` 不伪造 offset，重新发现 |
| `containers` 字段为空但进程在容器里 | cgroup、`/proc/<pid>/root`、`/proc/<pid>/ns/*` | 容器元数据缺失不影响匹配；匹配不能只依赖 container_name/image |
| no BTF | `/sys/kernel/btf/vmlinux` | preflight 直接失败，禁止静默降级到 CO-RE 或伪造 BTF |
| `perf_event_paranoid=2` | 是否 root、caps、`ulimit -n`、`perf_event_mlock_kb` | 当前 root 可继续；不要在生产机擅自改 sysctl，按审批窗口处理 |
| `binfmt`/`Exec format error` | `uname -m`、`file dist/cann-ebpf-linux-arm64`、挂载是否 `noexec` | 这是原生 aarch64 ELF；错误架构或 noexec 不能用 qemu binfmt 兜底 |
| `uprobe_events` 不可写 | tracefs/debugfs 挂载、root/CAP_SYS_ADMIN | 恢复可写后再 attach；不要改目标进程或库 |
| Orinfer 无目标 | 服务是否运行、`comm/cmdline` 是否变化 | 只读模式 SKIP；显式 attach 返回 3 |
| V4 worker 数不足 8 | PID 命名空间、container root、TP 启动日志 | 脚本返回 3；不要用假 PID 或旧 discover 结果补数 |
| SSH 断连/`Connection closed` | 是否短时间反复重连、端口 22 限流 | 合并为单次会话；等待 30–60 秒再试，不要循环重连 |
| 符号 MISS | 是否是 `libhccl.so` 的 ACL 导入符号、CANN 版本 | `allow_missing_symbols=true`，只对真实定义的符号 attach；记录 CANN 版本 |
| msPTI 库不存在 | `/proc/<pid>/root` 下 glob | M1 只挂 host 类别，不初始化 msPTI；M3 再按容器 root 解析，禁止使用宿主绝对路径 |

## 9. 当前验证状态与剩余风险

已完成（本机静态验证）：

- 两份 YAML 通过 `go run ./cmd/cann-ebpf validate --config ...`；
- profile 的 vLLM/Orinfer 互斥条件、H1/H2/M/C 分组已落盘，M0 的 `mspti.mode` 和 `cann.version` 断言保持兼容；
- smoke 脚本默认路径不执行 attach。

已完成（V4 服务器实测）：

- `attach-tracefs` 在 V4 `PID 1452` 跑通：3 秒、166 spans；作为 tracefs 后备路径基线保留。
- 本轮可复核的 tracefs 证据为 `--only sync` 10 秒、199 spans；3 秒 166 spans 仅作为历史短时记录。
- eBPF 后端在 V4 实测：单 PID 2 秒 70 spans；8 worker 2 秒 487 spans；最终交付件 8 worker 30 秒 2088 spans；前一构建回归批次 30 秒 2665 spans。
- 每次采集前后 `/health` 均为 HTTP 200，V4 服务保持 active；采集结束后检查 `uprobe` 无残留。
- 当前交付二进制 `dist/cann-ebpf-linux-arm64` 已由当前源码重建，version=0.1.0-m1。

未完成 / 未验证：

- tracefs 后端仍是单 PID；eBPF 已完成 8 worker 的 2 秒和 30 秒运行，但 tracefs 多 PID 并发覆盖未验证。
- Worker_TP 的 per-TID 线程级归属、ringbuf 丢事件和长时间运行可靠性仍需进一步验证。
- 设备侧 msPTI/内核时间线未实现，M1 host span 不能解释为设备执行时间。
- Orinfer 当前未运行；其真实 `comm/cmdline`、first_match 最终匹配和 attach 仍未验证，不能用 V4 进程替代。
