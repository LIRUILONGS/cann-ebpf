# cann-ebpf 项目交接文档

文档日期：2026-11-26  
适用版本：`0.6.0-m2`（`diagnose` 命令 + M2 host off-CPU/futex，均已上机验证）  
代码状态：`main` @ 本次交接提交（`git log -1` 见提交号）  
文档性质：工程交接。本文只写已验证事实；代码完成但未上机验证的内容会单独标注。

## 1. 项目地址

| 项 | 地址/值 |
|---|---|
| 本地工作区 | `C:\Users\liruilong\pentagi\cann-ebpf` |
| Git 分支 | `main` |
| 最新提交 | 见 `git log -1`（`diagnose`、M2 off-CPU/futex 及上机验证记录） |
| Git remote | 未配置；交接前建议先配置内部 Git 远程并推送 |
| Linux/arm64 二进制（M2） | `dist/cann-ebpf-linux-arm64-m2` |
| 二进制大小 | `9,389,783` bytes |
| 二进制 SHA-256 | `8A515B96116EC19A28CBC73F7F629B99FA9F564D57FDCB3075380F37F7CC428E` |
| 历史已验证二进制 | `dist/cann-ebpf-linux-arm64-batch`（`0.5.0-m5`，上表 SHA 已替换为 M2 构建，旧包仍保留在 dist/） |
| 服务器验证副本（历史） | `/root/cann-ebpf`，V4 服务器 `bms-84865758` |
| 服务器 profiles | `/root/cann-ebpf/profiles` |

说明：服务器上的 `/root/cann-ebpf` 是历史验证副本，不一定是最终源码仓库；以本地 Git 仓库和对应提交为准。

## 2. 项目定位

`cann-ebpf` 是昇腾 CANN 主机侧低开销观测工具，定位是：

> 网关负责发现异常和给出线索；本工具负责低层定位。

它不重复实现网关已经有的 HTTP 延迟、TTFT、QPS、请求统计等功能，而是回答：

- 哪个进程、哪个线程、哪个 CANN/ACL/HCCL 调用；
- 调用等了多久、返回值是什么、命中哪个库和函数；
- 属于哪个 vLLM batch、batch 里有哪些 request ID；
- 后续需要回答 host 阻塞、off-CPU、锁竞争、设备执行时间时，证据在哪里。

当前阶段已经做到“host 侧调用链 + batch/request 关联 + 原始证据链 + host 阻塞归因”；还不能完整回答
“设备侧到底执行了多久”，这项属于下一步的 M3。

## 3. 已经完成的内容

### 3.1 总览

| 模块 | 状态 | 说明 |
|---|---|---|
| M0 配置、发现、profile 匹配、ELF 符号解析 | 完成 | 已在 V4 服务器只读验证 |
| M1 host core | 完成 | ringbuf、entry/exit 配对、JSONL/manifest/summary |
| M4 vLLM-Ascend host profile | 完成 | 37 个函数、74 个 entry/exit 程序、10 个目标自动发现、10 秒实采 |
| M5 trace context + CPython POC | 完成 | trace 链路树、纯 eBPF CPython batch POC |
| 纯 eBPF vLLM batch 标准采集 | 完成 | 已并入 `spans.jsonl`/summary，真实 V4 验证通过 |
| P0 `diagnose` 命令 | 完成 | 只读读取已有证据，JSON/Markdown，单测覆盖；不需要 NPU |
| M2 off-CPU/futex 归因 | 完成并上机验证 | sched_switch + syscalls futex tracepoint、`offcpu_ns`/`futex_ns`；V4 10 秒与合成锁竞争均通过 |
| vLLM general plugin | 代码和单测完成，未激活 | 未安装到运行中的 V4，也未重启验证；本阶段不做 |
| Orinfer | 仅 profile validate | 服务未运行，没有真实采集窗口 |
| M3 msPTI 设备时间线 | 未实现 | 下一阶段重点 |
| M6 生产化 | 未实现 | Prometheus/OTLP、Grafana、systemd 等未做 |

注意：版本号 `0.6.0-m2` 表示本次交付 `diagnose` 与 M2 host 阻塞归因。M2 已在 V4 生产服务和
合成锁竞争目标上验证（见 `docs/M2-VALIDATION.md`），但它仍不回答设备执行时间和单 request 延迟；
M5 的 V4/Orinfer A/B 对比也还没有做。

### 3.2 M0：发现与符号解析

- 支持 YAML profile 加载、`extends` 合并和 JSON Schema 校验。
- 只读扫描 `/proc`：cmdline、maps、cgroup、容器 root、namespace、监听端口。
- 支持 `any/all/exclude` 匹配、优先级、`first_match/all_matching`。
- 解析 ELF dynsym，完成 vaddr 到 file offset 转换；缺符号时 fail-soft，不创建假 attach。
- `discover --dry-run` 支持 JSON/human 输出，可用于交接前检查目标环境。

### 3.3 M1/M4：host 侧采集

- 37 个 CANN/ACL/HCCL 函数，共 74 个 entry/exit uprobe 程序。
- 覆盖类别：
  - `sync`：`aclrtSynchronizeStream`、`aclrtSynchronizeStreamWithTimeout`、`aclrtSynchronizeEvent`
  - `event`：`aclrtRecordEvent`、`aclrtStreamWaitEvent`、`aclrtQueryEventStatus`、`aclrtQueryEventWaitStatus`、`aclrtQueryEvent`
  - `handle`：设备、context、stream、event 的创建/销毁/查询
  - `memory`：`aclrtMalloc/Free`、`MallocHost/FreeHost`、`HostRegister/Unregister`、`GetMemInfo`、`rtMemGetInfoEx`
  - `submit`：`aclrtMemcpyAsync`、`aclrtLaunchKernel`、`rtKernelLaunch*`、`rtMemcpyAsync`
  - `hccl`：AllReduce、AlltoAll、AllGather、ReduceScatter、Broadcast、Send/Recv
- `vllm-ascend` profile 自动发现 8 个 `VLLM::Worker_TP` + frontend + `VLLM::EngineCor`，共 10 个目标。
- 真实 V4 10 秒采集：6,991 条 span，8 个 Worker 均有数据，dropped 0，`/health` 前后均为 200，服务前后均为 `active`，uprobe 残留 0。
- 注意：37 个函数“已 attach”不等于同一窗口内全部触发；该 10 秒窗口实际只观测到 6 类函数调用。

### 3.4 纯 eBPF vLLM batch 采集

- 不安装 Python 包、不注入目标进程、不重启 V4、不修改目标内存。
- 通过 uprobe/uretprobe `PyObject_GetAttr`，只识别属性 `num_scheduled_tokens`，以返回 dict 指针作为 batch 身份，读取 dict key 作为 request ID。
- 固定运行时：CPython 3.12.13 + vLLM 0.23.0 + vLLM-Ascend 0.23.0rc1；不是通用 Python/vLLM 支持。
- 标准输出增加 `category=batch` span，`attribution.source=ebpf_scheduler_output_dict`。
- 每个 Worker 生成 `python-batch-<pid>.json`，包含统计、类型确认和 warning。
- 单 Worker 1452 实测：10 个 batch、6 个真实 `chatcmpl-*` request ID、`read_errors=0`、`ringbuf_lost=0`。
- 双 Worker 1452/3962 实测：每个 Worker 4 个 batch，共 8 条 batch span。
- `--no-context` 2 秒实测：4 个 batch、4 个 request ID、4 条 batch span，manifest artifact 哈希校验通过，采集后 `PyObject_GetAttr` uprobe 残留为 0。

边界：batch 身份是 `SchedulerOutput.num_scheduled_tokens` 的观测窗口，不是 `execute_model` 函数级入口/返回；`duration_ns` 不能当成 request 延迟或 NPU 执行时间。

### 3.5 证据链与输出

| 输出 | 内容 |
|---|---|
| `spans.jsonl` | 原始 host span，一行一个对象 |
| `manifest.json` | 运行环境、目标 PID、库 inode/size/mtime、统计、warnings、SHA-256 |
| `summary.json` | 派生 function/category/PID/context/batch 汇总 |
| `summary.md` | 人类可读摘要 |
| `python-batch-<pid>.json` | 纯 eBPF batch 逐 Worker 统计、类型确认和 warnings |

Span 关键字段：

```text
schema, run_id, profile, ts_ns, start_ns, end_ns,
pid, tid, tgid, comm, thread_name,
function, api, library, category, duration_ns, ret, phase,
dev_id, batch_id, request_ids,
attribution{level, source, confidence, verdict},
trace{trace_id, span_id, parent_span_id, batch_parent_span_id, trace_state, source},
span_id, seq
```

证据原则：

- 原始值以 `spans.jsonl` / `manifest.json` 为准，不修改、不补造。
- `summary` 是派生数据，不覆盖原始事件。
- manifest 记录 segments 的 bytes 和 SHA-256；warnings 保留 ringbuf 丢失、读取错误等数据损失。
- 不覆盖已有证据目录。

### 3.6 链路追踪

- 支持 `trace_id`、`span_id`、`parent_span_id`、`batch_parent_span_id`。
- `cann-ebpf trace --input <spans.jsonl>` 按 trace_id 聚合，输出链路树。
- batch span 可以作为 host span 的父节点/关联节点，按 batch 查看请求与调用链。

### 3.7 `diagnose` 低层定位命令（P0，已完成）

- 只读读取已有 run 的 `spans.jsonl` / `manifest.json`，不 attach、不重启、不改原始证据。
- 支持时间窗（单调时钟 ns、`--last`）、PID、TID、batch_id、request_id、trace_id 过滤。
- 输出 JSON（`cann.diagnose.report.v1`）与中文 Markdown：命中 PID/TID/batch/request、top slow
  function/category、host off-CPU/futex 比例、证据 span、判定与 confidence、无法判定原因。
- 判定只在有 off-CPU/futex 采样时给出；没有 msPTI 时永远不输出 `device_bound` 之类的结论。
- 单测覆盖所有过滤维度、时间窗、空结果、判定分支和 CLI 退出码。
- 详见 `docs/DIAGNOSE.md`。

### 3.8 M2 host off-CPU / futex 归因（已上机验证）

- `sched/sched_switch` 累计线程在 CANN 调用期间被切出 CPU 的时间；
  `syscalls/sys_enter_futex` + `sys_exit_futex` 累计 futex 等待（按 op 过滤 WAIT 系列）。
- 只测最外层 CANN span；嵌套 span 不写 blocking 字段，避免父子重复累计。
- span 新增可选字段 `offcpu_ns` / `futex_ns`；字段缺失=未测量，字段为 0=测得 0。
- `sched_process_exit` 清理 tid 状态，避免 tid 复用继承死线程的累计值。
- 主 uprobe collection 与 blocking collection 分离加载；blocking attach 失败只降级为无
  off-CPU 数据，不会拖垮 host span 采集。
- `attach --blocking auto|on|off`；默认按 profile（`vllm-ascend`/`orinfer` 当前为 true）。
- 已验证：V4 生产服务 10 秒（7,561 spans、`attached=4 failed=0`、无效样本 0、health/service/残留正常）；
  合成 pthread mutex 竞争（off-CPU 99.7%、futex 99.9%、`host_lock_contention` 1.00）；
  `--blocking off` 不产生任何 blocking 字段。详见 `docs/M2-VALIDATION.md`。
- 仍未做：设备侧 join、生产流量下的真实锁竞争样本、长跑开销基线。

## 4. 当前能回答和不能回答的问题

### 能回答

- 哪个 PID/TID 调用了哪个 CANN/ACL/HCCL 函数。
- 调用从进入到返回持续了多久、返回值是什么、命中哪个库。
- 某次采集窗口内有哪些 batch、每个 batch 有哪些 request ID（限固定运行时和纯 eBPF 路径）。
- 哪些 span 属于同一个 `trace_id`，链路上下游是什么。
- 按函数、类别、PID、batch 的分布统计，以及原始证据的哈希和完整性。
- 把已有证据按时间窗/PID/batch/request 转成低层定位报告（`diagnose`）。
- 在 M2 探针启用时，给出最外层 sync span 的 host off-CPU/futex 采样，以及
  `host_lock_contention` / `host_blocked_non_futex` / `host_cpu_running` 判定（阈值可调）。

### 不能回答

- 不能回答“为什么卡住”的完整因果：M2 只覆盖 host 阻塞，设备侧仍缺。
- 不能可靠区分 `device_bound`、`launch_starvation`、`copy_bound`、`communication_bound`。
- 不能回答 NPU kernel / memcpy / HCCL 的设备执行时间：msPTI 未接入。
- 不能回答单 request 延迟；batch 窗口不能拆成逐 request 时长。
- 不能回答 `execute_model` 的 Python 函数级耗时；插件未激活，纯 eBPF 的 `PyObject_GetAttr` 不是函数边界。
- 没有 DCMI/npu-exporter 硬件层，不能直接给出 NPU 利用率、HBM 余量、温度、功耗。

## 5. 代码地图

| 路径 | 作用 |
|---|---|
| `cmd/cann-ebpf/main.go` | CLI 入口、`validate/discover/attach/attach-tracefs/trace/diagnose/version` |
| `cmd/cann-ebpf/attach.go` | eBPF attach 参数、自动发现、输出、`--blocking` |
| `cmd/cann-ebpf/trace.go` | `trace` 报告和链路树 |
| `cmd/cann-ebpf/diagnose.go` | `diagnose` 只读定位报告 CLI |
| `internal/probes/functions.go` | 37 个 CANN/ACL/HCCL 函数声明 |
| `internal/probes/bpf/cann.bpf.c` | BPF C 源码（37 组 uprobe + sched_switch/futex kprobe） |
| `internal/probes/bpf/cann.bpf.o` | 已编译并嵌入的 BPF 对象，目标机不需要现编 |
| `internal/probes/blocking_common.go` | blocking 模式、测量 flag、span 赋值 |
| `internal/probes/blocking_linux.go` | sched_switch/futex attach、blocking map 配置 |
| `internal/probes/loader.go` | 主 collection 与 blocking collection 分离加载 |
| `internal/diagnose/` | 已读证据的过滤、聚合、判定、JSON/Markdown |
| `internal/probes/pythonbatch_linux_arm64.go` | 纯 eBPF CPython/vLLM batch 采集 |
| `internal/runlog/` | JSONL、manifest、summary、span 读取 |
| `profiles/vllm-ascend.yaml` | V4/vLLM-Ascend profile |
| `profiles/orinfer.yaml` | Orinfer profile（仅配置校验通过） |
| `poc/cpython/` | CPython offset、batch span、POC 程序 |
| `adapters/vllm_ascend/` | vLLM general plugin 适配器（未激活） |
| `docs/PLAN.md` | 总体方案和判定规则 |
| `docs/TASKS.md` | 任务清单和未完成项 |
| `docs/DIAGNOSE.md` | `diagnose` 命令、时间窗语义、判定规则与边界 |
| `docs/M2-OFFCPU.md` | M2 设计、判定口径与边界 |
| `docs/M2-VALIDATION.md` | M2 上机验证数据、修复记录和证据位置 |
| `docs/NEXT-PLAN.md` | M2 之后的分阶段方案（M3 可行性、E 层、开销基线、生产化） |
| `docs/M4-VALIDATION.md` | V4 实测记录 |
| `docs/VLLM-BATCH-POC.md` | 纯 eBPF batch POC 记录 |

## 6. 已实测环境与结果

| 项 | 值 |
|---|---|
| 主机 | `bms-84865758` |
| 架构 | `aarch64` |
| 内核 | `5.10.0-136.108.0.188.31.oe2203sp1.bclinux.aarch64` |
| 目标服务 | `vllm-ds4-8905.service` |
| 容器镜像 | `ds4/vllm-ascend:v0.23.0rc1-nonroot` |
| 目标进程内 CANN | `cann-9.0.1` |
| Python | `3.12.13` |
| vLLM | `0.23.0+empty` |
| vLLM-Ascend | `0.23.0rc1` |
| 目标 UID/GID | `982:979` |
| discovery | 10 个目标，每目标 37/148 符号、4 个库 |
| 10 秒 host 采集 | 6,991 spans，dropped 0，8 Worker 均有 span |
| 采集影响 | `/health` 200/200，service active/active，uprobe 残留 0 |
| 纯 eBPF batch | 10 个 batch、6 个 request ID（单 Worker）；双 Worker 各 4 个 batch |
| 构建验证 | `go test ./...`、`go vet ./...`、Linux/arm64 交叉编译通过（含 `diagnose` 与 M2 代码） |
| `diagnose` 验证 | Windows 单测 + 对 M4 交付里的 V4 真实 evidence 只读运行：6,991 spans 的 10 秒 run、164 spans 的 batch run 均正常出报告 |
| M2 验证（V4） | 7,561 spans，`attached=4 failed=0`，无效样本 0，off-CPU 1.1%、futex 0.0%，verdict `host_cpu_running`；health/service/残留正常 |
| M2 验证（合成） | 791 spans，off-CPU 99.7%、futex 99.9%，verdict `host_lock_contention`（confidence 1.00） |
| `--blocking off` | 7,145 spans，无任何 offcpu/futex 字段，服务状态不变 |

## 7. 下一步工作方向

详细的分阶段方案、依赖、验收和命令见 `docs/NEXT-PLAN.md`。结论：先用 1 天做 msPTI 动态 attach
可行性（容器内 `msprof --pid`）+ E 层 npu-smi 指标 + M2 开销基线，再决定 M3 走动态解析还是
容器内 in-process collector。插件/`execute_model` 函数级耗时本轮明确不做，见 P2 说明。

### P0：`diagnose` 命令（已完成）

已实现 `cann-ebpf diagnose`，验收项全部满足：

- 只读，不 attach，不修改原始 evidence；
- JSON（`cann.diagnose.report.v1`）和 Markdown 两种输出；
- 空数据/缺字段明确报 `unknown`，不补造；
- 单测覆盖时间窗、PID、TID、batch_id、request_id、trace_id 过滤；
- 输出命中 PID/TID/batch/request、top slow function/category、类别分布、off-CPU/futex 迹象、
  证据 span、判定与无法判定原因。

用法见 `docs/DIAGNOSE.md`；已对 V4 真实 evidence 只读验证，不需要授权窗口。

### P1：M2 off-CPU 与锁归因（已完成并上机验证）

已实现：

- `sched_switch` tracepoint 统计线程 off-CPU 时间；
- `sys_enter_futex` / `sys_exit_futex` tracepoint 统计 futex 等待（`futex_wait` kprobe 覆盖不足，
  已弃用）；
- `offcpu_ns` / `futex_ns` 写入标准 span，区分“未测量”与“测得 0”；
- `diagnose` 输出 `host_lock_contention` / `host_blocked_non_futex` / `host_cpu_running` 判定，
  并排除 `>duration` 的不可能样本；
- 主 uprobe collection 与 blocking collection 分离加载，blocking 失败不影响 host span；
- `sched_process_exit` 清理 tid 状态，entry 清陈旧 since，只测最外层 span。

验收结果（2026-11-26，`bms-84865758`）：

- 合成锁竞争：off-CPU 99.7%、futex 99.9%，`host_lock_contention` confidence 1.00；
- V4 10 秒：`attached=4 failed=0`、7,561 spans、无效样本 0、`/health` 200/200、service active/active、
  探针残留 0；该窗口 off-CPU 1.1%、futex 0.0%，判定 `host_cpu_running`；
- `--blocking off`：无 blocking 字段，服务状态不变。

完整证据和修复记录见 `docs/M2-VALIDATION.md`。剩余限制：嵌套 span 不采样、没有生产流量下的
真实锁竞争样本、长跑开销基线未做、仍不能下设备侧结论。

### P1：M3 msPTI / CANN profiler join

目标：把 host span 关联到 device kernel / memcpy / HCCL 时间线。

实现：

- 接入 msPTI API / KERNEL / MEMCPY / HCCL record；
- 校准 host 与容器/设备时钟；
- 按 `(device_id, correlationId)` join；
- 生成 join report：对齐率、未匹配数量、未匹配原因。

验收：

- 在一个已知 kernel 上验证 correlationId 真能关联；
- `device_bound`、`launch_starvation`、`copy_bound`、`communication_bound` 只有在有设备侧证据时才输出；
- join 失败时明确降级为时间窗，不伪造精确关联。

### P2：`execute_model` 边界（本轮明确不做）

纯 eBPF 无法直接挂 Python 方法。若要精确拿到 `execute_model` 的 Python 入口/返回：

- 使用仓库中的 vLLM general plugin 包装 `NPUModelRunner.execute_model`；
- 需要安装 Python 包并重启 V4，必须走维护窗口；
- 默认不启用，仍然以纯 eBPF 为主；
- 插件也只能解决 Python batch 边界，不能解决单 request 和设备时间。

本轮按交接要求**不安装、不重启、不激活插件**，因此执行 `execute_model` 函数级耗时仍然不可得；
纯 eBPF 的 `PyObject_GetAttr` batch 窗口不能当作函数边界。插件代码保留，等明确维护窗口再动。

### P2：生产化

- Prometheus/OTLP exporter、Grafana dashboard；
- systemd 单元、安装/升级/回滚脚本；
- preflight 检查、权限收敛、资源 watchdog、自动 detach；
- 固定负载生成器和开销基线，验证吞吐下降 ≤ 3%、ringbuf 丢事件 < 0.1%。

### 等待窗口

- Orinfer 运行后进行真实 discovery/attach 验证；
- V4/Orinfer 同 workload、同并发、同时长 A/B 对比；
- 插件真实请求路径验证和容器/宿主 monotonic clock 校准。

## 8. 交接硬约束

以下约束来自项目要求，接手时不要突破：

- 没有明确说明，永远不要脱敏；证据必须是实际原始数据，不能修改、补造、替换。
- 不读取 prompt、响应、token tensor 或业务文本；request ID 只是关联键。
- 不重启 V4/Orinfer、不停止服务、不修改目标进程内存；纯 eBPF POC 只允许短时只读 attach。
- 不覆盖旧证据目录；采集前后必须检查 health、service 状态和 uprobe 残留。
- 不把 batch 窗口说成 `execute_model` 函数级耗时，不把 `duration_ns` 说成 request 延迟或 NPU 执行时间。
- 不把 37 个已 attach 函数说成同一窗口全部触发；必须区分“可解析/可 attach”和“实际观测到”。
- M2 只覆盖最外层 span 的 host 阻塞，不能写成 request 延迟或 NPU 执行时间；嵌套 span 缺 blocking
  字段是设计行为，不是数据丢失。
- 插件默认不启用；插件路径和纯 eBPF 路径必须分开描述。
- `dist/`、`runs/`、`__pycache__` 不提交；代码修改需要 Git 提交。

## 9. 快速验证命令

Windows 本机构建：

```powershell
$env:CGO_ENABLED = "0"
go test ./...
go vet ./...
$env:GOOS = "linux"
$env:GOARCH = "arm64"
go build -buildvcs=false -trimpath -o dist/cann-ebpf-linux-arm64-m2 ./cmd/cann-ebpf
Remove-Item Env:GOOS, Env:GOARCH, Env:CGO_ENABLED
```

修改 `internal/probes/bpf/cann.bpf.c` 后必须重新生成嵌入对象（Windows 本机即可，不依赖目标机）：

```powershell
clang --target=bpfel -mcpu=v1 -O2 -g -c internal/probes/bpf/cann.bpf.c -o internal/probes/bpf/cann.bpf.o
```

服务器只读发现：

```bash
./cann-ebpf-linux-arm64 discover \
  --profile vllm-ascend \
  --profiles-dir profiles \
  --dry-run --json > runs/discover.json
```

标准 host 采集：

```bash
OUT="runs/ebpf-$(date -u +%Y%m%dT%H%M%SZ)"
./cann-ebpf-linux-arm64 attach \
  --pid auto \
  --profile vllm-ascend \
  --profiles-dir profiles \
  --duration 10s \
  --output "$OUT"
```

纯 eBPF batch 采集（不装插件）：

```bash
OUT="runs/ebpf-batch-$(date -u +%Y%m%dT%H%M%SZ)"
./cann-ebpf-linux-arm64 attach \
  --pid auto \
  --profile vllm-ascend \
  --profiles-dir profiles \
  --duration 10s \
  --python-batch \
  --no-context \
  --output "$OUT"
```

查看链路：

```bash
./cann-ebpf-linux-arm64 trace --input "$OUT/spans.jsonl" --limit 20
```

只读定位报告（不需要 attach）：

```bash
./cann-ebpf-linux-arm64-m2 diagnose --input "$OUT"
./cann-ebpf-linux-arm64-m2 diagnose --input "$OUT" --pid 1452 --last 2s --json --out "$OUT/diagnose.json"
```

M2 off-CPU/futex 采集（先短窗口，显式开探针）：

```bash
OUT="runs/ebpf-m2-$(date -u +%Y%m%dT%H%M%SZ)"
./cann-ebpf-linux-arm64-m2 attach \
  --pid auto \
  --profile vllm-ascend \
  --profiles-dir profiles \
  --duration 10s \
  --blocking on \
  --no-context \
  --output "$OUT"
```

关闭 blocking（长跑或怀疑开销时）：

```bash
./cann-ebpf-linux-arm64-m2 attach ... --blocking off ...
```

## 10. 交接检查清单

- [ ] 确认本地 Git 仓库提交和 `dist/cann-ebpf-linux-arm64-m2` SHA-256。
- [ ] 确认服务器 SSH/密钥可用；最近一次曾出现 `Permission denied (publickey,password)`，不要假设一定可连。
- [ ] 确认 V4 服务当前状态和 `/health`，不要在未授权窗口 attach 或重启。
- [ ] 确认 `profiles/vllm-ascend.yaml` 的目标发现规则仍匹配当前容器。
- [ ] 确认 `runs/` 和旧证据目录不被覆盖。
- [ ] 先跑 `go test ./...` 和 `go vet ./...`。
- [x] 用已有 `runs/` 跑一次 `diagnose`，确认只读、JSON/Markdown 都能出。
- [x] 上机验证 M2：`--blocking on` 的 10 秒窗口，确认 `offcpu=true futex=true failed=0`、无残留、
      `/health` 和服务状态不变，并用合成锁竞争对照一次（见 `docs/M2-VALIDATION.md`）。
- [ ] 按 M3 → 生产化的顺序推进，不要先做网关指标重复功能。
- [ ] 插件/`execute_model` 函数级耗时未经明确维护窗口不要动。

---

维护说明：本文与 `docs/TASKS.md`、`docs/PLAN.md`、`docs/M4-VALIDATION.md`、`docs/DIAGNOSE.md`、
`docs/M2-OFFCPU.md` 有交叉引用；如果实现状态变化，先更新本文件的“已完成/未完成”和“下一步”，再同步 TASKS。




