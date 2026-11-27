# cann-ebpf 项目交接文档

文档日期：2026-11-27
适用版本：`0.6.0-m2` + M3 + metrics/visual（`diagnose`、M2 off-CPU/futex、in-process msPTI collector；metrics 与可视化增强为只读派生功能）
代码状态：`main` @ 本次交接提交（`git log -1` 见提交号）
文档性质：工程交接。本文只写已验证事实；代码完成但未上机验证的内容会单独标注。

## 1. 项目地址

| 项 | 地址/值 |
|---|---|
| 本地工作区 | `C:\Users\liruilong\pentagi\cann-ebpf` |
| Git 分支 | `main` |
| 最新提交 | 见 `git log -1`（M3 A1/A2、diagnose/metrics、A3-A8 收尾） |
| Git remote | 未配置；交接前建议先配置内部 Git 远程并推送 |
| Linux/arm64 二进制（M3 + A1/A2） | `dist/cann-ebpf-linux-arm64-m3` |
| 二进制大小 | `13,407,163` bytes |
| 二进制 SHA-256 | `3fe700bb4d2345e7cee459c2c9449a1478e0b720da8db5ade29fc6f4569769c3` |
| Linux/arm64 二进制（metrics/visual + system/stats） | `dist/cann-ebpf-linux-arm64-m1m2-stats` |
| 二进制大小 | `14,730,758` bytes |
| 二进制 SHA-256 | `1150b2dbb955a936a681f1dd7fff24b9b8677f6faef506afe4a2b7580fbe882f` |
| collector A2 产物 | `dist/cann-mspti-a2-20261126/`（`libcannmspti.so`、`cann-mspti-selftest`、`synthetic_acl`、`SHA256SUMS.txt`） |
| collector A2 服务器副本 | `/root/cann-ebpf/cann-mspti-a2-20261126/` |
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

当前阶段已经做到“host 侧调用链 + batch/request 关联 + 原始证据链 + host 阻塞归因 + 合成 ACL 的设备时间线验证”；
真实 V4/Orinfer 设备时间线需要维护窗口注入 in-process collector。

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
| P1 `report` 性能仪表盘 | 完成 | 只读生成单文件离线 HTML；时间轴/散点/热力图/TopN/CDF/设备四象限，无外部依赖 |
| 独立 metrics 采集 | 完成 | `cann-ebpf sample`；DCMI/npu-smi、进程、cgroup、主机 system、Prometheus 五类源；不修改 trace 原始证据 |
| M1/M2 可视化增强 | 完成 | 延迟热图、次秒偏移热图、函数分位带、尾 cohort、多指标小倍数；有明确空态/降级原因 |
| 指标统计表/主机指标 | 完成 | metrics series 的 current/mean/p50/p95/p99/max、counter rate；`sample --system` 采集主机 CPU/load/memory/PSI/disk/net/thermal/frequency |
| M2 off-CPU/futex 归因 | 完成并上机验证 | sched_switch + syscalls futex tracepoint、`offcpu_ns`/`futex_ns`；V4 10 秒与合成锁竞争均通过 |
| E 层 npu-smi 硬件指标 | 完成并上机验证 | `hardware.jsonl`、manifest artifact、`hbm_pressure`；8 设备/HBM/AICore/功耗/温度 |
| `preflight` 环境检查 | 完成并上机验证 | 15 项检查；V4 pass=15 warn=0 fail=0 |
| A3 uprobe attach 规模优化 | 完成并上机验证 | 系统级 attach + BPF `target_pids` 过滤；37 函数/74 links；V4 墙钟 ~110s→~18s，退出 ~4.5s，残留 0 |
| A4 固定负载开销脚本 | 脚本 + dry-run 完成 | `scripts/overhead_fixed_load.py`；真实负载门禁待维护窗口 |
| A5 M6 交付骨架 | 脚本 + dry-run 完成 | systemd timer、install/uninstall/upgrade、make-release；均支持 dry-run |
| A6 Prometheus exporter | 完成并上机验证 | `cann-ebpf metrics` text/serve；V4 证据 69 个指标，标签基数受控 |
| A7 硬件短间隔/DCMI | 完成并上机验证 | 2s 间隔 5 采样×8 设备、health 正常；DCMI helper + `--hardware-backend dcmi` V4 验证通过 |
| A8 M5 采集/对比脚本 | 完成（smoke） | `m5_capture.sh` + `m5_compare.py`；真实 A/B 待 Orinfer 窗口 |
| M2 开销基线 | 部分完成 | agent +0.28pp 单核 CPU、+1.3MB RSS、0 丢；业务吞吐因生产负载不可控未下 ≤3% 结论 |
| M3 device join 管线 | 完成 | `cann.device.v1` schema、join、device verdict、diagnose 集成；`attach` 生成 `pidmap.json` |
| M3.0 msprof 动态 attach | 已验证不可用 | `msprof --pid` 三变体段错误；改为容器内 in-process collector |
| vLLM general plugin | 代码和单测完成，未激活 | 未安装到运行中的 V4，也未重启验证；本阶段不做 |
| Orinfer | 仅 profile validate | 服务未运行，没有真实采集窗口 |
| M3 msPTI 设备时间线 | 代码 + 合成验证 + A1/A2 加固完成 | `m3/mspti_collector`；final7 可匹配 305/305、慢 Sync 305/305；collector 并发 stop + 50ms flush 压力通过；生产注入待维护窗口 |
| M6 生产化 | 未实现 | Prometheus/OTLP、Grafana、systemd 等未做 |

注意：版本号 `0.6.0-m2` 是功能基线；M3 in-process collector 已在合成 ACL 上验证，但还没有注入
运行中的 V4/Orinfer。M2 已在 V4 和合成锁竞争上验证（见 `docs/M2-VALIDATION.md`），硬件层见
`docs/HARDWARE.md`，join 管线和 collector 见 `docs/M3-DEVICE-JOIN.md`、`docs/M3-COLLECTOR.md`、
`docs/M3-VALIDATION.md`。它们都不回答单 request 延迟；M5 的 V4/Orinfer A/B 对比也还没有做。

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

### 3.9 E 层 npu-smi 硬件指标（已完成并上机验证）

- `internal/hardware` 周期采样 `npu-smi info`，写 `hardware.jsonl`，并作为 manifest artifact 封存；
- 字段：AICore%、HBM used/total/free、功耗、温度、健康；缺失不补造；
- 解析器只接受合法 Health 行，忽略 `npu-smi` 末尾的进程表（曾误解析成第 9 个设备，已修复）；
- `attach --hardware auto|on|off`；`diagnose` 输出每设备硬件表，并在 HBM free<5% 且存在慢 memory
  调用时给出 `hbm_pressure`；
- 硬件是 wall-clock 采样，不能与 monotonic host span 逐调用 join。详见 `docs/HARDWARE.md`。

### 3.10 M3 device 时间线 join 与 in-process collector（代码 + 合成验证完成）

- `internal/device` 定义 `cann.device.v1`（API/KERNEL/MEMCPY/HCCL）与 join：
  `(pid,tid,start±100µs)` 匹配 API record → `correlationId` → 设备区间；
- 支持时钟偏移估计、reason 分类（ok/not_applicable/no_api_record/api_name_mismatch/duplicate/no_device_record/time_window）、
  相邻区间容差（gap ≤100µs，字段 `gap_ns`/`adjacent`）、时间窗退化、每设备 busy 并集、四类 device verdict；
- `diagnose` 自动读取 run 目录 `device.jsonl`，有设备证据才输出 `device_bound` 等结论；
- final7 合成 ACL：11,859 host spans + 148,466 device spans，慢 Sync 305/305（100%），进程退出后仍可 join；
- A1 join 质量：API 名称别名归一后只接受同名 record，`match_rate` 改为可匹配设备调用分母；final7 复跑
  可匹配 305/305（100%）、慢 Sync 305/305、`api_name_mismatch=0`；旧的 5,320 条 mismatch / 44.9% 已确认是
  最近时间选错 API record 的假失配；
- A2 collector 加固：callback buffer RAII 生命周期、stop 状态机与 8 线程并发 stop、时钟偏移周期重估、
  `/proc/self/task` tid 热刷新、JSON 转义审计；容器内 selftest 与 50ms flush 压力通过
  （32,809 条记录 0 损坏、`records_skipped=0`、`callbacks_active=0`、`stop_complete=true`、tid 映射 26 条/0 未映射）；
- `m3/mspti_collector` 的 `libcannmspti.so` / `cann-mspti-collector` / `synthetic_acl` 已在容器内编译并验证；
- M3.0 `msprof --pid` 动态 attach 三变体均段错误，判定不可用；生产端只能通过重启或容器入口 wrapper 注入。
  详见 `docs/M3-DEVICE-JOIN.md`、`docs/M3-COLLECTOR.md`、`docs/M3-VALIDATION.md`、`docs/M3-FEASIBILITY.md`。

### 3.11 `preflight` 与采集时序

- `cann-ebpf preflight [--json]`：只读检查 root、BTF、tracefs、uprobe_events、四个 tracepoint、
  perf_event_paranoid、lockdown、memlock、nofile、npu-smi、cgroup；有 fail 退出码 4。
  V4 实测 15 pass / 0 warn / 0 fail。详见 `docs/PREFLIGHT.md`。
- `attach --preflight enforce|warn|off`（默认 enforce）在启动前自动跑同一套检查，fatal 时退出 4；
  非 Linux 主机跳过（attach 本身不可用）。
- attach 现在输出 `capture_start` / `capture_stop` / `capture_end` 时间戳，以及
  `timings setup/capture/teardown/close_links/batch/consume/writer/summary`，便于区分采集窗口和清理。
- V4 上 740 个 uprobe 的内核串行 detach 约 40 秒；现在 `closeLinks` 有界等待 1 秒，
  剩余 fd 由进程退出清理（残留最终为 0），`capture_stop` 之后不再计入采集窗口。
- 硬件采样在 `capture_stop` 时停止，避免把清理阶段算进硬件窗口。

### 3.12 M3 host 侧证据接入

- `attach --device-jsonl <host-path>`：把容器内 msPTI collector 的 `device.jsonl` 与
  `device.jsonl.meta.json` 复制进 run 目录，并在目标存活时抓 `pidmap.json`；
- 三个文件分别封存为 manifest artifact：`device_jsonl`、`device_meta`、`pidmap`；
- 已存在同名文件不覆盖；源缺失只告警，不影响 host span；
- `diagnose` 自动读取这三者做 join；`pidmap.json` 让目标进程退出后仍可复现 join。

### 3.13 A3/A4/A5（本任务）

- **A3 attach 规模优化**：对库去重后每个 `(library,function)` 只挂一对 system-wide uprobe，
  BPF `target_pids` 过滤目标 tgid；V4 上 37 函数 = 74 links，10 秒采集墙钟 ~110s→~18s，
  退出 ~4.5s，uprobe/kprobe 残留 0，8 个 Worker 覆盖不变。详见 `docs/A3-ATTACH-SCALING.md`。
- **A4 固定负载开销**：`scripts/overhead_fixed_load.py`（dry-run 已验证），固定 prompt/并发/
  max_tokens，on/off 多轮，读 `/metrics`+agent CPU/RSS；真实负载门禁待维护窗口。
- **A5 交付骨架**：`deploy/` systemd service/timer、capture-once、install/uninstall/upgrade；
  `scripts/make-release.sh` 生成发布包与 SHA256SUMS；全部默认 dry-run，已在服务器验证计划输出。
  用法见 `docs/OPERATIONS.md`。
- **A6 Prometheus exporter**：`cann-ebpf metrics --input <run|runs> [--listen addr]`，输出
  `cann_ebpf_*`（run/spans/function/category/blocking/hardware/device/verdict）；V4 证据 69 个指标，
  HTTP /metrics 200。详见 `docs/A6-METRICS.md`。
- **A7 短间隔/DCMI**：`--hardware-interval 2s` 10 秒得到 5 采样×8 设备，health 正常；
  DCMI 直连已实现：`m3/dcmi_probe` + `--hardware-backend dcmi`，V4 8 卡只读验证通过。
  详见 `docs/A7-HARDWARE-INTERVAL.md`。
- **A8 M5 脚本**：`scripts/m5_capture.sh`（带 health/service 检查、拒绝覆盖）与
  `scripts/m5_compare.py`（两边 diagnose 后生成 compare.md）；V4 双 run smoke 通过。
  详见 `docs/A8-M5.md`。

### 3.14 `report` 性能仪表盘（已完成）

- **命令**：`cann-ebpf report --input <run_dir> --out <report.html>`；只读，不 attach、不联网、
  不修改原始证据。
- **输入**：`spans.jsonl`（必需），`device.jsonl` / `hardware.jsonl`（可选）；有 pidmap 时复用
  `device.CapturePIDMaps`/`ReadPIDMap` 做容器 PID 映射，无快照时可用 `--device-container-id`。
  `--metrics <path|dir>` 可叠加独立采集的 `metrics.jsonl`；run 目录中也有时会自动发现。
- **输出**：单文件 HTML（`//go:embed template.html`），原生 Canvas/CSS/JS，无 CDN；数据内嵌为
  JSON，`</` 转义为 `<\/`，避免脚本提前闭合。
- **图表**：线程时间轴（TID 泳道 + device stream 轨道，滚轮缩放/拖拽/密度条）、延迟散点
  （时间 × 对数耗时，慢阈值线，点击聚焦）、时间热力图（p95 或慢调用数）、TopN 函数
  （总耗时/p95/max/慢调用数）、延迟 CDF（p50/p95/p99）、设备四象限（host 等待 vs
  device busy%，区分 `device_bound`/`launch_starvation`）、设备忙率（busy/kernel/memcpy/HCCL），
  以及延迟热图、次秒偏移热图、函数分位带、尾 cohort、多指标小倍数。
- **判定口径**：设备区间只按 `device.Join` 的匹配结果统计；busy 是区间并集，kernel/memcpy/HCCL
  是区间时长之和，可能重叠；无 device 证据时明确显示“设备证据不可用”，不补造。
- **边界**：没有调用栈采样，不是按栈聚合的火焰图；TID 泳道只能到线程/函数级，不能回答单
  request 延迟，也不能替代 msPTI kernel 级时间线。metrics 是周期采样，不是逐 request 关键路径；
  只有 metrics、没有 spans 时生成 metrics-only 降级报告，函数归因和设备四象限明确不可用。
- **验证**：`go test ./internal/report ./cmd/cann-ebpf`、`node --check` 生成页面 JS、
  Chrome headless 截图检查；样本见 `.analysis/report-samples/report-m4.html`（6,991 spans，
  18 TID，无 device）与 `report-final7-m1m2.html`（11,859 spans，305 device intervals）。
  独立 QA 记录见 `.analysis/report-samples/QA-REPORT.md` 与 `.analysis/report-samples/QA-REPORT-STATS.md`；
  metrics-only + 指标统计样本见 `.analysis/report-samples/report-metrics-only-stats-final.html`
  （42 points、7 series、无 span 证据）；system 合成样本见 `.analysis/report-samples/report-system-stats.html`。

### 3.15 独立 metrics 与 M1/M2 可视化增强

- **采集命令**：`cann-ebpf sample --out <dir> [--dcmi|--npu-smi|--system] [--pid <pid>] [--cgroup <path>]
  [--prometheus name=url] [--interval 2s] [--duration 30s]`。
- **输出契约**：`metrics.jsonl` 每行一个 `cann.metrics.v1` 点，支持 gauge/counter/histogram；
  旁路 `metrics.meta.json` 记录 schema、interval、sources、status、warnings 和 artifact SHA-256。
- **隔离性**：metrics 采集与 trace 采集完全独立；已有 `metrics.jsonl` 使用 `O_EXCL` 拒绝覆盖，
  失败不会修改 `spans.jsonl`、`device.jsonl` 或 `hardware.jsonl`。
- **报告叠加**：`report --metrics` 只对 run 窗口内的点做派生；早于起点或晚于终点的点丢弃并写 warning，
  metrics-only 的 `HardwareAvailable` 仅在存在 `npu_*`/DCMI/npu-smi series 时为真。
  主机系统指标见 `docs/METRICS-SCHEMA-SYSTEM.md`，只表示宿主机整体，不代表进程/cgroup/NPU。
- **指标统计**：报告新增 `指标统计` 表，展示每个 series 的当前值、均值、p50/p95/p99、最大值和 counter 速率；
  统计基于完整 metrics 样本，span 窗口裁剪只影响绘图序列，不重新计算统计值。
- **主机指标**：`sample --system` 只表示宿主机整体，不等同于进程、cgroup 或 NPU 指标；缺失的 procfs/sysfs 文件不补零。
- **空态**：每个新视图都有 `available/reason`；缺 span 或样本不足时显示原因，不插值、不补造数据。

详见 `docs/METRICS-SCHEMA.md`、`docs/M1-M2-DATA-CONTRACT.md` 与 `docs/VISUALIZATION-RESEARCH.md`。

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
- 有 `device.jsonl` + `pidmap.json` 且 join 成功的 run，可以给出设备 busy/kernel/memcpy 的观测值；
  没有设备证据时不能输出这些结论，不能凭 host span 推断。
- 真实 V4/Orinfer 尚无设备时间线：collector 未注入生产进程；合成 ACL 结果不能当生产性能。
- 真实 HCCL 的 `Communication` correlationId 尚未验证；`msptiActivityHccl` 本身无 correlationId。
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
| `cmd/cann-ebpf/report.go` | `report` 只读性能仪表盘 CLI |
| `cmd/cann-ebpf/preflight.go` | `preflight` 环境检查 CLI |
| `cmd/cann-ebpf/metrics.go` | `metrics` Prometheus 导出 CLI |
| `internal/export/` | 诊断报告到 `cann_ebpf_*` 指标的渲染 |
| `internal/report/` | 只读派生报告数据与单文件 HTML 渲染（`template.html`） |
| `internal/probes/functions.go` | 37 个 CANN/ACL/HCCL 函数声明 |
| `internal/probes/bpf/cann.bpf.c` | BPF C 源码（37 组 uprobe + sched_switch/futex kprobe） |
| `internal/probes/bpf/cann.bpf.o` | 已编译并嵌入的 BPF 对象，目标机不需要现编 |
| `internal/probes/blocking_common.go` | blocking 模式、测量 flag、span 赋值 |
| `internal/probes/blocking_linux.go` | sched_switch/futex attach、blocking map 配置 |
| `internal/probes/device_evidence.go` | `--device-jsonl` 导入、pidmap 抓取、artifact 封存 |
| `internal/probes/loader.go` | 主 collection 与 blocking collection 分离加载 |
| `internal/diagnose/` | 已读证据的过滤、聚合、判定、JSON/Markdown |
| `internal/hardware/` | E 层 npu-smi 采样、解析、JSONL |
| `internal/device/` | `cann.device.v1` schema 与 host/device join |
| `internal/preflight/` | 环境预检的纯判定表与 Linux 采集 |
| `deploy/` | systemd service/timer、capture-once、install/uninstall/upgrade |
| `scripts/overhead_fixed_load.py` | 固定负载开销门禁脚本（A4） |
| `scripts/m5_capture.sh` / `m5_compare.py` | M5 采集与 A/B 对比（A8） |
| `scripts/make-release.sh` | 发布包与 SHA256SUMS（A5） |
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
| `docs/HARDWARE.md` | E 层 npu-smi 采样、schema、验证与边界 |
| `docs/M3-DEVICE-JOIN.md` | device 时间线 schema、join 算法、诊断接线与合成验证 |
| `docs/M3-FEASIBILITY.md` | msprof/msPTI 动态采集可行性结论 |
| `docs/PREFLIGHT.md` | preflight 检查项、退出码与 V4 结果 |
| `docs/M2-OVERHEAD.md` | M2 agent 开销基线与吞吐不可测说明 |
| `docs/WORK-SPLIT.md` | 两个并行任务的文件所有权、接口约定与评审备注 |
| `docs/NEXT-WORK.md` | M3 合成验证之后的下一阶段工作与分工 |
| `docs/A3-ATTACH-SCALING.md` | 系统级 uprobe + BPF PID 过滤的规模/退出优化 |
| `docs/OPERATIONS.md` | 安装/卸载/升级/systemd/固定负载/发布包运维说明 |
| `docs/A6-METRICS.md` | Prometheus exporter 指标、用法与验证 |
| `docs/A7-HARDWARE-INTERVAL.md` | 硬件短间隔开销与 DCMI 评估 |
| `docs/A8-M5.md` | M5 采集/对比脚本与边界 |
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
| E 层硬件验证 | 10 秒 7,794 spans + 2 采样×8 设备；HBM free 6.3%、AICore 峰值 46%；manifest artifact 4202 bytes |
| M3 设备时间线验证（合成 ACL） | final7（A1 复跑）：11,859 host spans / 148,466 device spans，可匹配 305/305（100%），慢 Sync 305/305（100%），`api_name_mismatch=0`，pidmap 27 条；A2 50ms flush：32,809 条 0 损坏、`stop_complete=true` |
| M3.0 msprof 动态 attach | 三变体均 exit 139（Segmentation fault），V4 health/service/worker 全程正常 |
| preflight | V4 pass=15 warn=0 fail=0；tracepoint id、lockdown、memlock、npu-smi 均正常 |
| M2 开销（60s×2+2） | agent CPU off 1.365% → on 1.64%（+0.28pp 单核），RSS +1.3MB，dropped 0 |
| 采集时序 | setup ~1.2s，capture 60s，teardown ~1.5s；进程退出关闭剩余 perf fd 约 40s |
| A3 系统级 attach | 37 函数/74 links；10 秒采集墙钟 ~18s（旧 ~110s），退出 ~4.5s，残留 0，8 Worker 覆盖不变 |
| A4/A5 dry-run | 固定负载脚本、install/uninstall/upgrade、make-release 均通过服务器 dry-run |

## 7. 下一步工作方向

详细的分阶段方案、文件所有权与下一阶段分工见 `docs/NEXT-WORK.md` 和 `docs/WORK-SPLIT.md`。
当前结论：M3 collector 合成验证、A1 join 质量、A2 collector 加固已完成；无窗口阶段继续 teardown/规模优化、
固定负载脚本和 M6 交付骨架；真实 V4/Orinfer 设备时间线与 M5 A/B 需要维护窗口。
插件/`execute_model` 函数级耗时不做，见 P2 说明。

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

### P1：M3 设备时间线与 join（代码 + 合成验证完成，生产待窗口）

已完成：

- `internal/device`：`cann.device.v1` schema、API→correlationId→KERNEL/MEMCPY/HCCL join、
  时钟偏移、时间窗退化、reason 分类、四类 device verdict；
- `diagnose` 自动读取 `device.jsonl`，有证据才输出设备结论；
- 合成 ACL 上验证：慢 Sync 305/305（100%）、API/KERNEL/MEMCPY correlationId、clock offset 761ns；
- M3.0 可行性结论：`msprof --pid` 三变体段错误，动态 attach 路线不可用；
- host 侧接入：`attach --device-jsonl` 导入 device/meta、抓 `pidmap.json` 并封存 artifact；
- 容器内 in-process collector（`m3/mspti_collector`）已完成并验证。

待做（需要维护窗口）：

- 把 collector 注入 V4 或 Orinfer，采集 10–30 秒真实设备时间线；
- 用真实流量复核慢 Sync 匹配率 ≥95% 与设备 busy/kernel/memcpy；
- 真实 HCCL 工作负载验证 `Communication` correlationId；
- join 失败时降级为时间窗，不伪造精确关联。

详见 `docs/M3-DEVICE-JOIN.md`、`docs/M3-COLLECTOR.md`、`docs/M3-VALIDATION.md`、`docs/M3-FEASIBILITY.md`。

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
- 合成 `device.jsonl` 只用于验证 join 代码路径，不能写成真实设备执行时间；没有真实 device 数据时
  不输出 `device_bound` 等结论。
- `msprof --pid` 已确认会段错误，不要在生产容器反复重试；M3 生产端走维护窗口。
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
./cann-ebpf-linux-arm64-m2 preflight
./cann-ebpf-linux-arm64-m2 preflight --json

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

只读性能仪表盘（单文件 HTML，可直接离线打开）：

```bash
./cann-ebpf report --input "$OUT" --out "$OUT/report.html"
./cann-ebpf report --input "$OUT" --out "$OUT/report.html" --slow-threshold 5ms --min-duration 100us

# 独立 metrics（短窗口示例）；再叠加到报告
./cann-ebpf sample --out "$OUT/metrics" --dcmi --interval 2s --duration 10s
./cann-ebpf sample --out "$OUT/system-metrics" --system --interval 1s --duration 10s
./cann-ebpf report --input "$OUT" --metrics "$OUT/metrics" --out "$OUT/report-metrics.html"

# metrics-only 降级报告（无 spans，函数归因会明确不可用）
./cann-ebpf report --metrics "$OUT/metrics" --out "$OUT/report-metrics-only.html"
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
- [ ] 跑 `go test -count=1 ./...`、`go vet ./...`，并确认 `sample` 与 `report --metrics` 至少各有一个成功样本。
- [ ] 用 `report --metrics-only` 检查空态文字、无 JS 异常、无造数；用 headless 浏览器确认 5 个新面板存在。
- [ ] 确认服务器 SSH/密钥可用；最近一次曾出现 `Permission denied (publickey,password)`，不要假设一定可连。
- [ ] 确认 V4 服务当前状态和 `/health`，不要在未授权窗口 attach 或重启。
- [ ] 确认 `profiles/vllm-ascend.yaml` 的目标发现规则仍匹配当前容器。
- [ ] 确认 `runs/` 和旧证据目录不被覆盖。
- [ ] 先跑 `go test ./...` 和 `go vet ./...`。
- [x] 先跑 `preflight`，确认 tracefs/uprobe/tracepoint/lockdown/npu-smi 全 pass。
- [x] 用已有 `runs/` 跑一次 `diagnose`，确认只读、JSON/Markdown 都能出。
- [x] 上机验证 M2：`--blocking on` 的 10 秒窗口，确认 `offcpu=true futex=true failed=0`、无残留、
      `/health` 和服务状态不变，并用合成锁竞争对照一次（见 `docs/M2-VALIDATION.md`）。
- [ ] 按 M3 → 生产化的顺序推进，不要先做网关指标重复功能。
- [ ] 插件/`execute_model` 函数级耗时未经明确维护窗口不要动。

---

维护说明：本文与 `docs/TASKS.md`、`docs/PLAN.md`、`docs/M4-VALIDATION.md`、`docs/DIAGNOSE.md`、
`docs/M2-OFFCPU.md` 有交叉引用；如果实现状态变化，先更新本文件的“已完成/未完成”和“下一步”，再同步 TASKS。




