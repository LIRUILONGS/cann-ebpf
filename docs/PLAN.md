# 昇腾 CANN eBPF 可观测工具完整方案

## 0. 文档信息

| 项 | 内容 |
|---|---|
| 工具名 | `cann-ebpf` |
| 版本 | v1.1（审查修订） |
| 目标平台 | 昇腾 910B2 / CANN 9.x / openEuler |
| 首个验证对象 | Orinfer（PID 1691816，容器 `orinfer-cann920`） |
| 第二验证对象 | vLLM-Ascend（`vllm serve`，按 profile 适配） |
| 执行方式 | 阶段化自动推进，中间不找用户确认 |
| 唯一人工门禁 | 切换 V4/vLLM 流量、停止/重启 Orinfer、安装会改动宿主 CANN 的组件 |

配套的未完成项、优先级和验收标准见 `docs/GAPS.md`；本文件是方案，GAPS 是缺口登记，两者不要混为一谈。

本方案的核心不是重造一个 profiler，而是把三件事接起来：

1. **宿主侧**：哪个线程、哪个 CANN API、等了多久、为什么等。
2. **设备侧**：msPTI 给出的 kernel / memcpy / HCCL 时间线。
3. **业务侧**：Orinfer 或 vLLM-Ascend 的请求、batch、阶段上下文。

## 1. 结论

架构冻结为：

> **一个 `cann-ebpf` 核心 + 多个 YAML profile + 可选的框架上下文适配器。**

不做 `cann-ebpf-orinfer` 和 `cann-ebpf-vllm` 两个分支。两个框架共用 80% 以上的探针、msPTI 关联、判定规则和输出 schema；差异全部收进配置文件和可选插件。

## 2. 目标与非目标

### 2.1 目标

- 在不重启、不修改 Orinfer 的前提下，采集 CANN API 的宿主侧耗时、返回值、off-CPU、futex 等待。
- 通过 msPTI 的 `correlationId` 把宿主 span 关联到 device kernel / memcpy / HCCL。
- 回答“哪个线程/请求/batch 卡住、卡在 host 还是 device、证据是什么”。
- 同一套二进制支持 Orinfer 与 vLLM-Ascend，差异通过 profile 解决。
- 默认开销可控：吞吐下降目标 ≤ 3%，ringbuf 丢事件 < 0.1%。
- 输出原始 JSONL、中文 Markdown 报告、CLI 视图，后续接 Grafana/OTLP。

### 2.2 非目标

- 不解析 AI Core 指令、不读 device memory、不做算子级 PC sampling。
- 不替代 msPTI / ascend-profiler；卡内细节以 msPTI 为准。
- 不在 Phase A 做 request-level 精确归因；没有请求上下文时明确标记 `thread/process` 级。
- 不修改原始事件数值，不做脱敏、补造或“修正”证据。
- 不在未授权时切换 V4/vLLM、停止 Orinfer 或重启 CANN 容器。

### 2.3 当前限制

- Orinfer Phase A 没有稳定 request hook，只能到 `thread`；回答“哪个请求”需要反向代理/USDT/访问日志，见 8.3。
- msPTI 的 `correlationId`、`shared`/`exclusive` 行为尚未实测；不成立时只能退化为时间窗 join，置信度下降。
- vLLM-Ascend（V4，`vllm-ds4-8905.service`）已运行；M4 已完成 profile、10 个目标的自动 discovery、
  37 个 host uprobe 和 10 秒实采。vLLM general plugin 代码完成但未装入/重启激活；逐 request 时长、
  msPTI 设备侧关联仍未实现。
- 不开 msPTI 时，eBPF 只能看 host 入队/阻塞，看不到 kernel/memcpy/HCCL 的设备执行时间。
- `aclrtRecordEvent` 的设备侧完成时间 `t_d` 不在 eBPF 中；只能用 msPTI 或 event 查询补。
- DCMI 字段、HCCL activity 是否可用，需在目标机实测；不可用时按 7.1 的降级路径处理。
- 当前每卡 HBM 余量约 4.6GB，不能为验证 vLLM 硬起一个抢 HBM 的实例。

## 3. 设计原则

1. **配置驱动，固定模板**：profile 可以选择探针、符号、采样率、阈值和上下文方式，但不允许 YAML 现场生成任意 BPF 代码；核心只实现有限的安全模板。
2. **一层为准**：ACL 和 Runtime 可能同时命中；默认只选一层，避免双计。
3. **同步与异步分开**：`SynchronizeStream` 测阻塞，`MemcpyAsync`/`LaunchKernel` 只测入队延迟。
4. **host 与 device 分开**：eBPF 给 host span，msPTI 给 device span，用户态按 `correlationId`/时间窗 join。
5. **先证据后结论**：没有 msPTI 或设备利用率证据时，不轻易判定“算力不足”。
6. **可回退**：所有探针可单独关闭；超过开销阈值自动降采样或 detach。

## 4. 总体架构

```text
业务请求 / HTTP / gRPC
        │
        ▼
Orinfer / vLLM-Ascend
        │
        ├── CANN ACL / Runtime / HCCL
        │       │
        │       ├── eBPF uprobe（宿主侧）
        │       │     ├─ API 进入/返回、ret、参数
        │       │     ├─ off-CPU / futex
        │       │     └─ 线程/设备/stream/event handle
        │       │
        │       └── msPTI（设备侧）
        │             ├─ API record
        │             ├─ kernel / memcpy / HCCL
        │             └─ correlationId
        │
        ▼
cann-ebpf agent
  ├─ ELF/符号解析
  ├─ ringbuf 消费
  ├─ msPTI join
  ├─ 阻塞分类
  └─ JSONL / Markdown / Prometheus / OTLP
        │
        ▼
CLI / Grafana / 报告
```

## 5. 运行流程

1. **Discover**：读取 `/proc/<pid>/cmdline`、`maps`、`cgroup`、容器信息，识别 profile。
2. **Resolve**：用 `/proc/<pid>/root` 找到 CANN 库，解析 ELF dynsym，计算 uprobe offset。
3. **Probe discovery**：对候选 launch 符号做 5 秒计数，决定 ACL 层还是 Runtime 层。
4. **Attach**：挂 H1/M 全量探针和 H2 采样探针；可选 sched/futex/HCCL。
5. **Collect**：ringbuf 批量消费，配对 entry/exit，生成 host span。
6. **Join**：按 `pid + tid + 时间窗` 对齐 msPTI API record，再用 `correlationId` 关联 kernel/memcpy/HCCL。
7. **Classify**：根据 off-CPU、futex、device overlap、HBM、ret 生成 verdict。
8. **Export**：JSONL 原始数据、Markdown 报告、Prometheus/OTLP。

## 6. 适配层与配置模型

### 6.1 配置文件合并顺序

```text
configs/base.yaml
  → profiles/<framework>.yaml
    → profiles/<framework>-<env>.yaml（可选）
      → CLI flags（最高优先级）
```

### 6.2 Profile 负责什么

- 进程匹配：`comm`、`cmdline`、`maps`、`cgroup`、容器名、监听端口。
- CANN 版本与库路径。
- launch 层选择：`auto` / `acl` / `runtime` / `off`。
- 探针开关、采样率、耗时阈值。
- 硬件采样源：`auto` / `dcmi` / `npu-exporter` / `npu-smi`，默认 10 秒。
- 请求上下文来源：`tid` / `usdt` / `plugin` / `otel`。
- msPTI 模式：`off` / `exclusive` / `shared` / `auto`。
- 分类阈值和输出路径。

### 6.3 不把“任意 BPF”暴露给配置

profile 只能选择以下固定模板：

| 模板 | 用途 |
|---|---|
| `host_blocking` | 真正阻塞 host 的 API |
| `event_dependency` | Record/Wait/Query 事件操作 |
| `handle_lifecycle` | device/stream/event handle 创建与销毁 |
| `memory_alloc` | HBM/主机内存分配与释放 |
| `submit` | Memcpy/Launch 等异步入队 |
| `hccl` | 通信 API 进入/返回 |
| `sched` | sched_switch off-CPU |
| `futex` | futex 等待 |
| `ioctl` | davinci 驱动交互耗时 |

这是“配置灵活”和“运行安全”的边界。

### 6.4 两个业务 profile + 一个兜底

- `profiles/orinfer.yaml`：当前 Orinfer，默认 `context.mode=tid`，msPTI `exclusive`。
- `profiles/vllm-ascend.yaml`：vLLM-Ascend，默认 `context.mode=plugin`，`batch=true`，msPTI `shared`。
- `profiles/cann-generic.yaml`：未知 CANN 进程的兜底 profile，只做 host span，不强求请求上下文。

### 6.5 配置 schema 与校验规则

`apiVersion` 固定为 `cann-ebpf/v1`，`kind` 为 `Config` 或 `Profile`。profile 的 `extends` 必须是相对路径，最多 4 层，禁止循环。

合并规则：

- map 递归合并。
- list 默认替换，不拼接，避免符号列表出现隐式重复。
- 标量以子 profile 为准。
- 支持 `${ENV_VAR}` 展开，但只允许在路径、socket、端口字段，不允许在模板名里。
- 未知字段直接报错，不静默忽略。
- YAML 1.1 的 `on/off/yes/no` 会被解析成布尔值；枚举值必须加引号，例如 `mode: "off"`。schema 校验前先做类型检查，不能只看文本。

`match` 语义：

- `any`：命中任意一条即可。
- 可选 `all`：必须全部命中。
- `exclude`：任意一条命中就排除，优先级高于 `any/all`。
- `priority`：多个 profile 同时命中时取最高；同分时报错，要求人工指定 `--profile`。
- 单个 `matchExpr` 内写多个字段时，字段之间是 AND；数组元素之间按 `any/all` 的语义组合。

组合规则固定为：`(any 任一命中) AND (all 全部命中) AND NOT exclude`。只写 `any` 时等价于 `all=[]`；只写 `all` 时等价于 `any=[]`。两者不能同时为空。

`match` 只用于选进程，不用于证明框架类型。`discover` 必须输出 `matched_rules[]` 和 `confidence`，让人能复核“为什么选了这个 profile”，不能只给一个结论。

这条是必要修正：Orinfer 的容器镜像名是 `vllm-ascend`，如果只看镜像，会被误判成 vLLM-Ascend。`vllm-ascend.yaml` 已加 `exclude: comm=orinfer / start-orinfer.sh / container_name=orinfer-cann920`。

`target.selector`：

- `first_match`：只 attach 第一个命中进程，适合 Orinfer。
- `all_matching`：attach 所有 worker，适合 vLLM 多进程。
- `explicit`：必须提供 `--pid`。

`preflight`、`self_metrics`、`output.manifest` 的字段见 `configs/base.yaml`；启动时先校验，再 attach。

### 6.6 配置到 BPF 的映射边界

配置只能选择模板和参数，不能改变 BPF 程序的控制流。模板到程序的映射在编译期固定，运行时通过只读 `config` map 下发采样率、阈值和开关。这样 profile 可以适配 Orinfer/vLLM，但不会因为一份坏 YAML 生成不可验证的 BPF。

模板名与 probe 名称的对应关系在核心代码里固定（`sync→host_blocking`、`event→event_dependency` 等），schema 只校验枚举，不能替代这层语义校验。

## 7. Probe 模板与语义

### 7.1 六层采集协议（定稿）

| 层 | 性质 | 采集策略 | 符号 |
|---|---|---|---|
| **H1 Host 阻塞层** | host 真的被卡住 | 全量、逐条配对 | `aclrtSynchronizeStream`、`WithTimeout`、`aclrtSynchronizeEvent` |
| **H2 Stream 入队/依赖层** | 只测入队，不测设备执行 | 采样 + 聚合 | `aclrtRecordEvent`、`aclrtStreamWaitEvent`、`aclrtQueryEventStatus`、`aclrtQueryEventWaitStatus`、`aclrtMemcpyAsync`、`aclrtLaunchKernel` / `rtKernelLaunch*` |
| **M 内存配额层** | 分配、锁页、余量 | 全量或低频 | `aclrtMalloc/Free`、`MallocHost/FreeHost`、`HostRegister/Unregister`、`GetMemInfo`、`rtMemGetInfoEx` |
| **C 通信层** | HCCL 入队 + 设备通信 | host 采样 + msPTI HCCL | `HcclAllReduce`、`HcclAlltoAll`、`HcclAllGather`、`HcclReduceScatter`、`HcclBroadcast`、`HcclSend/Recv` |
| **D 归因层** | 为什么 host 阻塞 | 条件聚合 | `sched_switch`、`futex`、`pthread_mutex_lock`、`do_sys_ioctl` |
| **E 硬件层** | 卡内利用率 | 5–15 秒周期 | DCMI / npu-exporter，最后才 `npu-smi` |

这张表是采集范围的定稿；下面 7.2 只是把它落到具体符号和默认阈值。

handle 不是独立的观测层，而是 H1/H2 的关联键；`handle_lifecycle` 只维护 device/stream/event 的生命周期，不单独产出“handle 性能”结论。

E 层由 `hardware` 配置控制：优先 DCMI，其次 npu-exporter，最后 npu-smi；默认 10 秒，不跟高频 probe 混在一起。

### 7.2 符号与默认策略

| 层 | 符号 | 测什么 | 默认策略 |
|---|---|---|---|
| H1 Host 阻塞 | `aclrtSynchronizeStream`、`WithTimeout`、`aclrtSynchronizeEvent` | host 真正等待的时间 | 全量，>1ms 输出 |
| H2 事件/依赖 | `aclrtRecordEvent`、`aclrtStreamWaitEvent`、`aclrtQueryEventStatus`、`aclrtQueryEventWaitStatus` | 入队/轮询，不是 host 阻塞 | 聚合 + 慢事件输出 |
| H2 提交 | `aclrtMemcpyAsync`、`aclrtLaunchKernel`、`rtKernelLaunch*`、`rtMemcpyAsync` | 入队延迟、背压 | 1/64 采样，>500µs 输出 |
| M 内存 | `aclrtMalloc/Free`、`MallocHost/FreeHost`、`HostRegister/Unregister`、`GetMemInfo`、`rtMemGetInfoEx` | 配额、锁页、HBM 余量 | 全量，低频 |
| H Handle | `aclrtSetDevice`、`GetDevice`、`CreateStream/Event`、`Reset/Destroy` | 关联键生命周期 | 全量，低频 |
| C 通信 | `HcclAllReduce`、`HcclAlltoAll`、`HcclAllGather`、`HcclReduceScatter`、`HcclBroadcast`、`HcclSend/Recv` | 通信入队；设备时长看 msPTI | 采样 + msPTI HCCL |
| D 归因 | `sched_switch`、`futex`、`pthread_mutex_lock`（可选）、`do_sys_ioctl` | off-CPU、锁、驱动等待 | 条件聚合 |
| E 硬件 | DCMI / npu-exporter / 低频 npu-smi | HBM、AICore、温度、功耗 | 5–15 秒 |

重要语义：

- `aclrtMemcpyAsync` 的 host 耗时是**入队延迟**，不是拷贝耗时；拷贝耗时看 msPTI `MEMCPY`。
- `aclrtLaunchKernel` / `rtKernelLaunch*` 的 host 耗时是**提交延迟**，不是 kernel 执行时间。
- `aclrtStreamWaitEvent` 阻塞的是 stream，不是 host；不要把它归到 H1。
- `aclrtRecordEvent` 也是入队；设备侧完成时间 `t_d` 不在 eBPF 中，需 msPTI 或 event 查询。
- `aclrtLaunchKernel` 在当前 CANN 9.2 中存在并导出，但 vLLM/Orinfer 不一定使用；由 discovery 决定。
- HCCL API 通常是 stream 异步：host 进出只测入队，通信时长看 msPTI `HCCL`。
- `event.mode=aggregate` 仍保留 entry，慢事件和错误照常输出；`aggregate` 不等于“只统计 count”。

### 7.3 API 参数捕获表

aarch64 上 uprobe 入参在 `pt_regs->regs[0..7]`，返回值在 `x0`；以下只记录元数据，不读 tensor/prompt/payload。

| API | 入参 | 退出时补充 |
|---|---|---|
| `aclrtSynchronizeStream` | `arg0=stream` | `ret` |
| `aclrtSynchronizeStreamWithTimeout` | `arg0=stream`、`arg1=int32 timeout` | `ret` |
| `aclrtSynchronizeEvent` | `arg0=event` | `ret` |
| `aclrtRecordEvent` | `arg0=event`、`arg1=stream` | `ret` |
| `aclrtStreamWaitEvent` | `arg0=stream`、`arg1=event` | `ret` |
| `aclrtQueryEventStatus` | `arg0=event`、`arg1=status*` | `ret`；仅 ret=0 时读 status |
| `aclrtMalloc` | `arg1=size`、`arg2=policy` | `arg0` 指向的返回指针 |
| `aclrtFree` | `arg0=devPtr` | `ret` |
| `aclrtMallocHost` | `arg1=size` | `arg0` 指向的 host 指针 |
| `aclrtHostRegister` | `arg1=size`、`arg2=type` | `arg3` 指向的 devPtr |
| `aclrtGetMemInfo` | `arg0=attr` | `arg1/arg2` 指向的 free/total |
| `aclrtMemcpyAsync` | `arg1=destMax`、`arg3=count`、`arg4=kind`、`arg5=stream` | `ret` |
| `aclrtLaunchKernel` | `arg0=funcHandle`、`arg1=numBlocks`、`arg3=argsSize`、`arg4=stream` | `ret` |
| `Hccl*` | `count`、`dataType`、`op`、`comm`、`stream` | `ret`；不记录 buffer 内容 |
| `aclrtSetDevice` | `arg0=deviceId` | `ret` |
| `aclrtGetDevice` | `arg0=deviceId*` | `ret`；exit 读 deviceId |
| `aclrtSetCurrentContext` | `arg0=context` | `ret` |
| `aclrtCreateStream/Event` | 输出 handle 指针 | `ret`；exit 时读 handle |
| `rtMemGetInfoEx` | `arg0=memInfoType` | `arg1/arg2` 指向的 free/total |

Runtime 符号（`rtKernelLaunch`、`rtKernelLaunchWithFlagV2`、`rtMemcpyAsync`、`rtMemGetInfoEx`）的参数位以 `runtime/rt.h` 为准，不能套用 ACL 的位置；M0 从 CANN 头文件生成签名表。

HCCL 各 API 的参数顺序不同，表中只列语义字段，不承诺固定寄存器位置。

约束：

- 用户指针一律用 `bpf_probe_read_user`，先判空，失败只记 `read_error=1`。
- `argsData`、tensor buffer、prompt、图片内容一律不读。
- `stream/event/handle` 只作为关联键，不作为业务语义。
- 返回值错误码原样保留，不做“翻译后覆盖”。

### 7.4 BPF 运行时结构

| map | 类型 | 用途 |
|---|---|---|
| `config_map` | array | 只读采样率、阈值、开关 |
| `target_pids` | hash | 目标进程过滤，避免全系统噪声 |
| `inflight` | hash / per-tid | 保存 entry 时间、参数、嵌套深度 |
| `handle_map` | hash | stream/event → device/pid 关联 |
| `agg` | per-CPU hash | count/sum/max/log2 histogram |
| `events` | ringbuf | 慢事件、错误事件、配对完成的 span |
| `lost` | per-CPU array | ringbuf/配对/map full 计数 |

entry/exit 规则：

- entry 写 `start_ns + args`；exit 计算 `dur = now - start`。
- 默认只在 exit 输出 span；entry 只写 inflight，不单独发事件。H1 的 Sync 额外保留 entry 超时心跳，用于发现长时间不返回的卡死请求。
- 每个 tid 允许 8 层嵌套；超过则 `depth_overflow++`，不生成错误配对。
- 只选一层 launch/submit，避免 ACL 与 Runtime 双计。
- H2 的 entry 始终记录 `start_ns + args`；`sampled` 只决定正常 span 是否输出，错误和超过阈值的调用在 exit 强制输出。否则被采样掉的慢调用永远看不到。
- 采样哈希只在 entry 计算一次并写入 `inflight`；exit 读取同一记录，不能重新哈希，否则 entry/exit 会错配。
- 如果实测 entry 开销超过预算，降级为“entry 采样 + 错误退出采样”；此时 slow 覆盖率会下降，必须在报告里写明。
- 嵌套调用（例如 `aclrtLaunchKernel` 内部调用 `rtKernelLaunch`）只把最外层记为 top-level；内层标记 `nested=true`，不计入 top-level 聚合，避免 ACL/Runtime 双计。
- 对 H1 的 Sync 调用，`inflight` 超过 `stuck_threshold_ms`（默认 30s）时先发一条 `stuck=1` 心跳，不等待 exit；exit 到达后补一条 `stuck=0` 的完成 span，两者用同一个 `span_id` 关联。
- 每个 span 生成 `span_id = hash(pid, tid, api, start_ns, seq)`，用于 JSONL 去重和 msPTI join 对账。

attach 规则：

- 5.10 的 `perf_event_open` uprobe 以线程为单位 attach：先枚举 `/proc/<pid>/task/*`，再对每个 tid 挂事件；`inherit=1` 只覆盖之后 fork 的子任务，不能覆盖已存在的线程。
- 多进程 vLLM 用 `all_matching` 时，对每个命中进程重复上述步骤；命中进程数超过 `target.max_processes` 时拒绝启动并提示收窄 selector，不静默截断。
- 记录每个目标库的 `(dev, inode, mtime)`；容器重启或库升级后 inode 变化，旧 uprobe 会静默失效。watchdog 每 10s 校验，变化时重新 resolve + attach，并在 manifest 记录 `library_changed`。
- 每 10s 重扫 `/proc/<pid>/maps`，发现新映射的 CANN 库（`dlopen`、延迟加载的 `libhccl.so`）也要 attach，并记录 `library_added`；只在启动时扫一次会漏掉 Python/vLLM 的动态加载。
- 进程身份用 `pid + /proc/<pid>/stat` 的 starttime + boot_id，不能只用 pid；PID 复用会导致证据串线。
- 进程/线程异常退出时，未配对的 inflight 标记 `unmatched_exit`，不伪造 `end_ns`；run 结束把数量写进 manifest。
- 解析 `.dynsym` 时保留 symbol version；同名多版本优先默认版本，记录 `symbol_version`，不能只按名字取第一个。
- 采样降低的是 ringbuf/用户态处理开销，不是 entry uprobe 本身；entry 开销高时只能减少 H2 符号或改 exit-only，不能假装“采样率降了，entry 开销就没了”。

ringbuf/背压：

- 8MB ringbuf，用户态 128 条一批消费。
- 生产者失败时 `lost++`，不阻塞业务。
- 用户态消费延迟超过 1s 时自动降低 H2 采样率。
- JSONL 每 256MB 轮转，保留 7 天；`manifest.json` 单独保留。

uprobe 落地：

- 5.10 用经典 `perf_event_open` uprobe，不依赖 `uprobe_multi`。
- ELF 解析 dynsym，并把 vaddr 转成 file offset；不能直接把 `st_value` 当 offset。
- 符号缺失时 fail-soft；记录 `symbol_missing`，不阻断 H1/M。
- 挂载前校验 `/proc/sys/kernel/perf_event_paranoid` 与 BPF 权限。

## 8. 关联模型

### 8.1 三层关联

```text
host span (pid, tid, api, start, end, ret)
  → msPTI API record (pid, tid, start/end)
    → correlationId
      → msPTI kernel / memcpy / HCCL
```

### 8.2 Handle 图

必须维护：

- `pid/tid → device_id`：`aclrtSetDevice`、`aclrtGetDevice`、`aclrtSetCurrentContext`。
- `stream_handle → device_id`：`aclrtCreateStream` / `DestroyStream`。
- `event_handle → device_id`：`aclrtCreateEventWithFlag` / `ResetEvent` / `DestroyEvent`。
- handle 复用：至少用创建/销毁时间窗隔离；无法隔离时标记低置信度。
- stream 可能被多个线程共享；`aclrtSynchronizeStream` 的等待可能由其他线程提交的任务造成。默认把 span 归到调用线程，同时记录 `stream_owner_tids[]`，报告里必须区分“调用线程”和“任务提交线程”。
- 如果 handle 创建发生在 attach 之前，`dev_id` 填 `-1` 并降低置信度；不要用当前线程的 device 猜测。

### 8.3 请求/批次上下文

| 级别 | 来源 | 可信度 |
|---|---|---|
| `request` | USDT/插件拿到单请求 ID | 最高 |
| `batch` | vLLM scheduler / Orinfer batch 事件 | 高 |
| `batch_window` | 按 batch_begin/end 时间窗推断 | 中 |
| `thread` | 只用 tid | 中低 |
| `process` | 只有 pid | 最低 |

没有上下文就明确标记，不把 thread 级数据伪装成 request 级。

vLLM-Ascend 注意：continuous batching 下，一次 ACL 调用可能服务多条请求，因此 schema 必须支持 `batch_id + request_ids[] + phase`。

Orinfer 当前没有稳定的 request hook，Phase A 只能到 `thread`。升级路径必须先选型，不能默认“tid 就是请求”：

| 方案 | 做法 | 可达归因级别 | 代价 |
|---|---|---|---|
| 反向代理 | 在 Orinfer 前加一层代理，注入 `X-Request-ID` 并记录起止时间 | `batch_window` | 不改 Orinfer，但入口流量必须经过代理 |
| Python USDT | 在 Orinfer 的 HTTP 入口加 USDT probe | `request` | 需要改代码或重启 |
| 访问日志 join | 读取 Orinfer/uvicorn 访问日志，按时间窗匹配 | `batch_window` | 日志精度通常 1ms，误差较大 |
| 仅 tid | 不引入外部上下文 | `thread` | 零改动，但回答不了“哪个请求” |

Phase A 默认选“仅 tid”，报告明确写 `attribution.level=thread`；任何情况下都不把 tid 结果写成 request 级。

### 8.4 时钟

- eBPF 用 `bpf_ktime_get_ns()`。
- 用户态用 `clock_gettime(CLOCK_MONOTONIC)`。
- 启动时校准 offset，默认容差 100µs；实测后再收紧。
- 容器可能启用 time namespace，`CLOCK_MONOTONIC` 与宿主不一致；校准必须按 pid/time namespace 分别做，不能全机一个 offset。
- msPTI 如果使用设备时间戳，需要单独校准 `device_clock_offset`，不能复用 host offset。

### 8.5 上下文协议

框架插件不直接写 BPF map，只把上下文事件发给 agent；agent 维护 `pid/batch/request` 映射。协议是 Unix socket 上的 JSON Lines，一行一个事件：

```json
{"v":1,"type":"batch_begin","ts_ns":123,"pid":4242,"batch_id":"b-001","phase":"prefill","request_ids":["r-1","r-2"],"model":"deepseek-v4.1"}
{"v":1,"type":"batch_end","ts_ns":456,"pid":4242,"batch_id":"b-001","phase":"prefill"}
{"v":1,"type":"request_begin","ts_ns":123,"pid":4242,"request_id":"r-1","batch_id":"b-001"}
{"v":1,"type":"request_end","ts_ns":456,"pid":4242,"request_id":"r-1","batch_id":"b-001"}
```

规则：

- `batch_begin/end` 必须成对；缺失时标记 `context_incomplete`。
- 一个 batch 可以有多个 `request_ids`，一个 ACL span 可以对应整个 batch。
- 并发 batch 重叠时，按 `pid + tid + 时间窗` 再匹配；仍不唯一时保留 `attribution.level=batch`，
  并在 `attribution.verdict` 标记 `batch_window`，不能把多个 batch 的 request 列表合并。
- 插件事件可能晚于 span 到达；agent 保留最近 30s 的 span 做回填，超过窗口标记 `context_late`，不能把晚到的 batch 硬贴到旧 span 上。
- 事件缓冲上限 10 万条；超出丢最旧并计数。
- 插件崩溃不影响 eBPF 采集；agent 只失去上下文，不丢 host span。
- socket 目录 0755、socket 默认 0600；目标为非 root 时 chown 到目标 UID/GID 并设为 0660。
  agent 用 `SO_PEERCRED` 校验对端 uid，只接受 collector UID 和已发现目标 UID 的连接。
- 事件必须做 schema 校验；未知 `type` 丢弃并计数，不让插件把任意字段写进 agent。
- `request_id`/`batch_id` 只作为关联键，不写入 prompt、响应内容或图片。

USDT 路径使用同一语义：provider `cann_obs`，probes 为 `batch_begin/batch_end/request_begin/request_end`，参数顺序与 JSON 字段一致。

### 8.6 msPTI 接入

msPTI 可能要求在目标进程内初始化，不能假设“外部 attach 一定可用”。M3 按以下顺序验证：

1. **existing trace**：如果 vLLM/Orinfer 已经启用 msPTI/torch_npu profiler，优先读取现有 trace。
2. **preload/helper**：在目标容器内加载 collector；不改业务代码，但可能需要重启或维护窗口。
3. **external**：确认 msPTI 支持外部订阅后再用；不支持就放弃该模式，不硬做。

采集内容：

- `MSPTI_ACTIVITY_KIND_API`：`start/end`、`pt.processId/threadId`、`correlationId`、`name`。
- `MSPTI_ACTIVITY_KIND_KERNEL`：`start/end`、`ds.deviceId/streamId`、`correlationId`、`type/name`。
- `MSPTI_ACTIVITY_KIND_MEMCPY`：`start/end`、`correlationId`。
- `MSPTI_ACTIVITY_KIND_HCCL`：`start/end`、通信时长/带宽。

Join 算法：

1. 以 `(pid, tid, api_name)` 建索引。
2. 对每个 host span，在 `start ± 100µs` 找最近的 msPTI API record。
3. 一对一匹配；同一 record 只能被一个 span 使用。
4. 用 `(device_id, correlationId)` 找 kernel/memcpy/HCCL；`correlationId` 只在同一 device 内唯一，不能只用它做全局键。
5. 未匹配原因分类：`no_record` / `time_skew` / `api_name_mismatch` / `duplicate` / `mspti_off`。

补充约束：

- msPTI 库路径按 `/proc/<pid>/root` 解析；profile 里的绝对路径不存在时用 `library_globs`，不假设宿主路径就是容器路径。
- HCCL record 没有 `deviceId` 时标记 `device_unknown`，不要猜一个 device 填进去。
- `correlationId` 缺失时只能退化为时间窗匹配，报告里把置信度降级，不能伪装成精确 join。

时钟校准：用同一批 API record 的 `start/end` 与 eBPF span 做中位数偏差估计；偏差 > 1ms 时告警，不静默使用。

设备忙碌：把同一 `deviceId` 上的 kernel 区间做并集，再与 host span 求交；`busy_pct` 是交集占 span 的比例，不是瞬时 AICore。

### 8.7 多进程与多 worker

- vLLM-Ascend 常见 frontend/engine/worker 多进程；`target.process_tree=true`，必要时 `selector=all_matching`。
- 请求 ID 可能只在 frontend 存在，ACL 调用在 worker；插件按 `batch_id` 广播上下文，agent 按 `pid/batch` 关联。
- 跨进程上下文无法关联时，降级到 `batch_window` 或 `thread`，不伪造 `request`。
- Orinfer 若为单进程多线程，按 tid 归属；若有子进程，同样走进程树。

## 9. 判定规则

| 观察 | verdict | 证据 |
|---|---|---|
| Sync 长，`offcpu/wall > 50%`，`futex/wall > 30%` | `host_lock_contention` | futex 地址、线程栈（按需） |
| Sync 长，device overlap > 80% | `device_bound` | msPTI kernel、AICore |
| Sync 长，device overlap < 20%，设备空闲 | `launch_starvation` | 上一个 kernel 结束时间、队列 |
| Malloc 长，HBM free < 5% | `hbm_pressure` | GetMemInfo / npu-smi |
| MemcpyAsync 入队长，拷贝活动重叠高 | `copy_bound` | msPTI memcpy |
| Launch 返回队列满 | `queue_backpressure` | ret、队列深度 |
| HCCL 入队长，通信活动重叠高 | `communication_bound` | msPTI HCCL |
| ret=timeout | `timeout` | timeout 参数、设备时间线 |

两个口径必须写死：

- `queue_backpressure` 的“队列深度”是 agent 按 `stream` 统计的 inflight submit 数（进入 API 未返回），不是设备硬件队列；设备侧排队看 msPTI。两者不能混用。
- `host_lock_contention` 只在 futex/off-CPU 证据属于同一调用线程时成立；如果 stream 被其他线程共享，必须同时看 `stream_owner_tids[]`，否则会把“等别的线程提交的任务”误判为锁竞争。

## 10. 数据契约

JSONL 每行一个 span：

```json
{
  "schema": "cann.span.v1",
  "run_id": "20261125-orinfer-01",
  "profile": "orinfer",
  "host": "bms-84865758",
  "pid": 1691816,
  "tid": 12345,
  "dev_id": 0,
  "api": "aclrtSynchronizeStream",
  "phase": "exit",
  "start_ns": 1234567890,
  "end_ns": 1244567890,
  "dur_ns": 10000000,
  "ret": 0,
  "offcpu_ns": 9800000,
  "futex_ns": 9700000,
  "args": {"stream": "0x7f..."},
  "device": {
    "overlap_ns": 200000,
    "busy_pct": 3,
    "correlation_id": 998877,
    "kernel": "FFN"
  },
  "attribution": {
    "level": "thread",
    "batch_id": "",
    "request_ids": [],
    "source": "tid"
  },
  "verdict": "host_lock_contention",
  "confidence": 0.91
}
```

字段原则：

- `schema`、`run_id`、`profile` 必填。
- `span_id` 必填；`nested`、`stuck` 默认 `false`；`dev_id=-1` 表示未建立 handle 关联。
- `ret` 保留原始错误码；`ret_name` 只是派生字段，不能覆盖 `ret`。
- `request_ids` 永远是数组，允许为空。
- `attribution.level` 必须如实反映数据来源。
- `device.correlation_id` 只有 msPTI join 成功才填。
- 原始数值不修改；派生字段单独命名。

### 10.1 Run manifest 与证据链

每次运行必须生成 `manifest.json`，与 `spans.jsonl` 同目录：

```json
{
  "run_id": "20261125-orinfer-01",
  "tool_version": "cann-ebpf/0.1.0",
  "config_sha256": "...",
  "profile": "orinfer",
  "host": "bms-84865758",
  "kernel": "5.10.0-136...",
  "cann_version": "9.2.0",
  "target_pids": [1691816],
  "started_at": "2026-11-25T12:00:00+08:00",
  "ended_at": "2026-11-25T12:00:30+08:00",
  "event_count": 12345,
  "lost_events": 0,
  "jsonl_sha256": "...",
  "operator": "codex",
  "command": "cann-ebpf attach --pid 1691816 --profile orinfer --duration 30s"
}
```

规则：

- 启动即写 `manifest.json`，`status=running`；正常结束改 `complete`，异常退出保留 `running` 并写 `error`。
- 每 5s 写 `journal.jsonl`（run_id、last_seq、last_ts、lost），崩溃后可从 journal 判断数据边界。
- `jsonl_sha256` 在文件关闭后计算；轮转文件分别记录。
- `config_sha256` 是合并后配置的哈希，不是某一个 YAML 的哈希。
- `manifest.json` 不轮转、不覆盖；JSONL 可轮转但 manifest 记录所有分片。
- 报告里的典型 case 必须引用 `run_id + JSONL 行号 + manifest hash`。
- 不记录 prompt、图片、tensor、完整请求体；只记录 request_id 等元数据。
- JSONL 按 `rotate_mb` flush/fsync；磁盘余量低于 `output.min_free_gb` 时停止写入并 detach，不能阻塞业务。
- 运行目录 0700，文件 0600；manifest 和 journal 只允许运行用户读取。

## 11. 展示

### 11.1 CLI

```text
cann-ebpf discover --pid 1691816
cann-ebpf attach --profile orinfer --duration 30s
cann-ebpf top --profile orinfer
cann-ebpf why --profile orinfer --since 5m
cann-ebpf trace --pid 1691816 --tid 12345 --last 30s
cann-ebpf report --run runs/20261125-orinfer-01
cann-ebpf detach --all
cann-ebpf validate --config /etc/cann-ebpf/config.yaml
```

退出码是自动化契约，不能只用 0/1：

| 退出码 | 含义 |
|---|---|
| 0 | 成功 |
| 2 | 配置错误（schema/合并/未知字段） |
| 3 | 目标进程或符号未找到 |
| 4 | 预检/权限失败 |
| 5 | attach 失败 |
| 6 | 运行时自保护 detach |
| 7 | 数据完整性失败（ringbuf loss 超限、JSONL 校验失败） |

### 11.2 Markdown 报告

每次运行生成：

- `summary.md`：环境、基线、开销、top 慢 span、verdict 分布、典型 case、结论。
- `spans.jsonl`：原始事件。
- `overhead.md`：探针前后吞吐/p95/CPU 对比。
- `join-report.md`：msPTI 对齐率、未匹配原因。

模板文件在 `docs/templates/`；生成器只填充占位符，不改原始数值，空字段显式写 `unknown`。

`summary.md` 固定结构：

1. 运行信息：`run_id`、profile、pid、时长、合并配置 hash、operator。
2. 环境与基线：CANN/驱动/NPU/容器/负载。
3. 开销：吞吐、p50/p95/p99、agent CPU/RSS、ringbuf loss。
4. Top 慢 span：按 API 和 verdict 分组。
5. Verdict 分布：每类给出样本数和占比。
6. 典型 case：`run_id + JSONL 行号 + manifest hash`，不贴 prompt/tensor。
7. 结论与限制：明确 `attribution.level`，没有证据的结论不写。

### 11.3 Grafana

M6 接 Prometheus/OTLP，面板包括：

- Host API p50/p95/p99
- Sync wait vs device busy overlap
- off-CPU / futex ratio
- HBM free / AICore
- top slow spans
- verdict 分布
- Orinfer vs vLLM-Ascend 对比

### 11.4 语言

- 给你看的报告、结论、面板标题：中文。
- 代码、JSON 字段、指标名、CLI 参数：英文。
- CLI 表头英文，说明文字中文。

## 12. 目录结构

```text
cann-ebpf/
  docs/
    PLAN.md
    GAPS.md
    templates/
      summary.md
      overhead.md
      join-report.md
      compare.md
  schema/
    config.schema.json
  configs/
    base.yaml
  profiles/
    cann-generic.yaml
    orinfer.yaml
    vllm-ascend.yaml
  bpf/
    cann_trace.bpf.c
    sched.bpf.c
    futex.bpf.c
  cmd/cann-ebpf/
    main.go
  internal/
    config/
    discovery/
    probes/
    join/
    classify/
    export/
    report/
  adapters/
    vllm_ascend/
    orinfer/
  deploy/
    cann-ebpf.service
  scripts/
    install.sh
    uninstall.sh
    upgrade.sh
  testdata/
    fake-libascendcl/
  runs/
```

## 13. 实施阶段

### M0：骨架与发现（0.5–1 天）

- 配置加载、profile 合并、YAML 校验。
- `/proc` 发现、容器 root、ELF dynsym 解析。
- `discover --dry-run` 输出候选符号和 offset。
- 不 attach，不影响服务。

### M1：Orinfer host core（1–2 天）

- 挂 H1 + M 探针。
- ringbuf、entry/exit 配对、JSONL。
- `top` / `why` 初版。
- 30 秒试跑，确认不重启 Orinfer。

### M2：off-CPU 与锁归因（2–3 天）

- `sched_switch`、`futex`、可选 `pthread_mutex_lock`。
- 计算 `offcpu_ns`、`futex_ns`。
- 输出 `host_lock_contention` / `launch_starvation` 初版。

### M3：msPTI join（3–5 天）

- msPTI collector，API/kernel/memcpy/HCCL。
- 时钟校准、时间窗匹配、correlationId join。
- 对齐率报告；未匹配原因分类。

### M4：vLLM-Ascend host profile（host 部分已完成）

- [x] 新增 `vllm-ascend.yaml`，覆盖 8 个 Worker_TP、frontend、EngineCore。
- [x] launch 层 auto discovery；最终 V4 discovery 为 10 个目标，每目标 37/148 符号。
- [x] 37 个 host uprobe、74 个 entry/exit 程序；10 秒实采 6,991 spans，dropped 0。
- [x] batch 上下文协议与 vLLM general plugin 代码；5 个 Python 单测通过。
- [x] 非 root UID/GID socket 权限与 `protocol_smoke` 验证。
- [ ] 插件安装、重启激活和真实 vLLM request 路径验证；需要单独维护窗口。
- [ ] 逐 request 时长拆分；当前只有 batch 窗口归因。

设备侧 join 仍属于 M3；不能用 M4 的 host span 代替 kernel/memcpy/HCCL 执行时间。

### M5：V4/vLLM A/B（等你授权，1–2 天）

- 按授权切换 V4/vLLM 目标。
- 同一 workload、同一时长采集。
- 对比 Orinfer vs vLLM：host span、device timeline、verdict。
- 输出 `compare.md`。

### M6：生产化（2–3 天）

- Prometheus/OTLP、Grafana dashboard。
- systemd、权限收敛、自动 detach。
- 文档、runbook、交接。

### 阶段推进规则

- 中间不找用户确认；按 M0→M1→…顺序自动推进。
- 只有以下情况暂停：
  1. 需要停止/重启 Orinfer；
  2. 需要切换 V4/vLLM 流量；
  3. 探针开销 > 3% 或存在 OOM 风险；
  4. 需要修改宿主 CANN/驱动。

## 14. 验收标准

| 阶段 | 标准 |
|---|---|
| M0 | 能发现 Orinfer、CANN 库、4 个以上候选符号；不 attach |
| M1 | 30 秒采集成功；Orinfer 不重启；吞吐下降 ≤ 3% |
| M2 | 能区分 host_lock / device_bound / queue_backpressure 三类 |
| M3 | 95% 慢 Sync 能匹配 msPTI API record；时间偏差 < 100µs |
| M4 | 同一二进制能加载 `vllm-ascend` profile；不依赖 Orinfer 代码 |
| M5 | Orinfer 与 V4/vLLM 的同 workload 对比报告完成 |
| M6 | JSONL/OTLP/Grafana 可用；ringbuf 丢事件 < 0.1% |

### 14.1 测试矩阵与基线方法

| 测试 | 内容 | 通过标准 |
|---|---|---|
| 单元测试 | YAML 合并、match 优先级、ELF vaddr→offset、JSONL schema | 全部通过 |
| 假库集成 | 自建导出 CANN 符号的 `.so`，验证 attach/entry/exit | 不依赖真实 NPU |
| Orinfer 集成 | PID 1691816，30 秒，H1+M | 不重启，事件非空 |
| Orinfer 归因 | 5 分钟真实流量，H1+M+D | 至少三类 verdict |
| msPTI join | 同一窗口 API/kernel/memcpy | 95% 匹配，偏差 <100µs |
| vLLM profile | 空容器 + 真实 `vllm serve`（资源允许时） | 同一二进制加载 profile |
| 故障注入 | 缺符号、无权限、ringbuf 满、msPTI 冲突 | fail-soft，不崩溃 |

基线方法：

1. 先跑 60 秒 warmup，丢弃。
2. 记录 5 分钟 idle 基线。
3. 记录 5 分钟代表性负载基线。
4. 开探针，同样 workload 再跑 5 分钟。
5. 重复 3 次，取中位数。
6. 对比 throughput、p50/p95/p99、CPU、RSS、ringbuf loss。

负载必须固定：同一组 prompt、同一并发、同一 max_tokens、同一 warmup；否则开销数字没有意义。Orinfer 与 vLLM 的 A/B 也必须用同一组负载。

判定：

- 吞吐下降 ≤3%，p95 回归 ≤5%，才算通过。
- 任一指标超过 5%，自动降低 H2 采样率或关闭 H2。
- 超过 7%，立即 detach，保留现场并生成 `overhead-abort.md`。

## 15. 风险与回退

| 风险 | 处理 |
|---|---|
| CANN 版本/符号变化 | 启动时校验 dynsym，失败降级到 generic |
| uprobe 热路径开销 | H2 采样、阈值输出、自动降采样 |
| ringbuf 丢事件 | 8MB ringbuf、批量消费、丢事件计数 |
| msPTI 订阅冲突 | `shared` 复用已有 trace；`exclusive` 独占 |
| 容器 mount namespace | 统一用 `/proc/<pid>/root` 解析库 |
| 时间戳偏差 | 启动校准 + 100µs 容差 |
| 请求级归因不成立 | 标记 `thread/process`，不伪造 request_id |
| 切换 V4 影响服务 | 只在授权窗口执行，保留回滚脚本 |

### 15.1 自监控、清理与故障处理

工具自身必须可观测：

- `attach_success`、`probe_hits`、`symbol_missing`。
- `ringbuf_lost`、`map_full`、`consumer_lag`、`dropped_spans`。
- `active_pids`、`uptime`、`overhead_pct`。
- BPF verifier 日志和 attach 失败原因。

`overhead_pct` 的定义必须先固定，否则“≤3%”没有意义：

- 首选：目标框架自身 Prometheus 的请求吞吐（req/s）相对基线回归。
- 次选：agent 旁路统计的完成请求数（需要 request hook；Orinfer Phase A 没有）。
- 兜底：agent 进程 CPU 占单核百分比，单独命名 `agent_cpu_pct`，不冒充业务开销。
- 没有基线时，watchdog 只做资源保护（agent RSS/CPU/磁盘/ringbuf），不自动判定“业务开销 3%”。

watchdog：

- 每 5 秒计算一次开销；>3% 连续 30 秒，H2 采样率减半。
- >5% 连续 30 秒，关闭 H2；>7%，全部 detach。
- 目标进程退出、容器停止、SIGINT/SIGTERM 时，关闭 ringbuf、detach 所有 uprobe、flush JSONL、写 manifest。
- 启动失败时清理已 attach 的探针，不留下半挂状态。
- 资源保护：agent RSS 超过 `safety.max_rss_mb`、单核 CPU 超过 `safety.max_cpu_pct`、磁盘余量低于 `output.min_free_gb` 时，按“降采样 → 关 H2 → 全 detach”逐级自保。
- kill switch：`SIGUSR1` 立即 detach，`SIGUSR2` 降采样，`SIGTERM` 优雅退出；外部可用 `cann-ebpf detach --all`。
- 任何自保护动作都写 `manifest.json` 的 `safety_actions[]`，并保留最后一次成功 flush 的序号。

日志：

- 结构化 JSON，字段名英文；人类可读摘要中文。
- 不输出 prompt/tensor/buffer 内容。
- 错误码原样记录，不只写“失败”。

## 16. 当前环境基线（2026-11-26）

| 项 | 值 |
|---|---|
| 主机 | `bms-84865758`，openEuler 5.10，aarch64 |
| NPU | 8 × 910B2，每卡 64GB HBM |
| Orinfer | 当前未运行；历史 PID `1691816` 已不存在；等待单独授权窗口 |
| V4 / vLLM-Ascend | `vllm-ds4-8905.service` active；8 Worker_TP + frontend + EngineCore |
| CANN（V4 目标进程） | `9.0.1`，目标 root 下 `libascendcl.so`、`libruntime.so`、`libhccl.so`、`libtorch_npu.so` |
| CANN（宿主/msPTI 规划） | `9.2.0`，包含 `libmspti.so`；M3 尚未初始化 |
| msPTI 头 | `/usr/local/Ascend/cann-9.2.0/tools/mspti/include` |
| 当前 HBM | 约 60.9/65.5 GB 每卡，余量约 4.6GB |
| CPU 推理 | 5 个 llama.cpp systemd 服务（embedding/reranker/4B/8B） |

当前 V4 已运行并完成 10 秒 host 采集；本轮没有停止、重启或修改 V4。vLLM 插件仍未激活，
激活需要单独维护窗口。Orinfer 当前未运行，不能用 V4 进程替代 Orinfer 验证。

### 16.1 兼容性矩阵

| 项 | 已验证/目标 | 说明 |
|---|---|---|
| 内核 | 5.10.0-136 openEuler aarch64 | ringbuf/BPF/UPROBE 均可用 |
| CANN | 9.2.0（宿主）/ 9.0.1（V4 目标进程） | `aclrtLaunchKernel`、`aclrtQueryEventStatus` 存在；两个版本不能混写 |
| CANN 8.x | 目标兼容 | 可能无 `aclrtLaunchKernel`，自动降级到 Runtime/msPTI |
| msPTI | CANN 9.2 `tools/mspti/include` | API/kernel/memcpy/HCCL activity |
| 驱动/固件 | 以 `npu-smi info` 为准 | 兼容性矩阵必须记录 driver/firmware，不只记 CANN |
| Orinfer | 0.3.14 | 历史版本；当前未运行，等待授权窗口 |
| vLLM-Ascend | `v0.23.0rc1-nonroot` | V4 已运行并完成 host 采集；插件代码完成但未激活 |
| Python 插件 | 3.10/3.11 | 仅 vLLM 插件需要 |
| 架构 | aarch64 | 参数从 `pt_regs` 取，返回从 x0 取 |
| DCMI | 待验证 | 需要 `libdcmi.so`/`/dev/davinci_manager`；不可用时降级 npu-exporter |
| msPTI HCCL | 待验证 | 若 CANN 9.2 不提供 HCCL activity，HCCL 只保留 host 入队 |

### 16.2 前置条件与构建

内核/系统检查：

- `CONFIG_BPF_SYSCALL=y`
- `CONFIG_UPROBE_EVENTS=y`
- `CONFIG_DEBUG_INFO_BTF=y`
- `CONFIG_PERF_EVENTS=y`
- `/sys/kernel/btf/vmlinux` 存在
- `perf_event_paranoid` 允许 perf event
- `unprivileged_bpf_disabled` 不阻止目标方式
- `/sys/kernel/security/lockdown` 不是 `confidentiality`/`integrity`；lockdown 会阻止 BPF
- `ulimit -n` ≥ 4096；每个线程一个 perf event，fd 不够会漏挂
- `perf_event_mlock_kb` 与 `RLIMIT_MEMLOCK` 足够容纳 perf event + BPF map
- cgroup v1/v2 都能识别；容器检测不能只按 cgroup v2
- SELinux/AppArmor 不阻止 `perf_event_open`、`bpf`、`/proc/<pid>/root`
- `/proc/<pid>/root` 可读；hidepid 或不同 mount namespace 要单独处理

构建工具：

- `clang`/`llvm`（BPF target，`-target bpfel`）
- `bpftool`（BTF/头文件）
- `libbpf` headers 或 vendored libbpf
- Go 1.22+（静态 agent/CLI）
- `gcc`/`make`/`pkg-config`

构建策略：

- 优先在一次性构建容器里编译，避免往宿主装包。
- 产物是静态 Go 二进制 + `.bpf.o`，运行时不需要 Go/Python。
- 构建时固定 CANN 头文件版本；运行时不依赖宿主 CANN 头，只依赖目标库符号。
- `cann.library_globs` 里的 `**` 不能直接用 Go `filepath.Glob`（不支持递归通配）；用 `doublestar` 或显式目录遍历，并在 M0 单测覆盖。
- CANN 头文件是 ABI 的唯一真相：M0 从 `acl_rt.h` 生成/校验符号签名表，签名不匹配时构建失败，不能手抄。
- 没有 BTF 时不要静默降级到 CO-RE；要么用匹配的内核头编译非 CO-RE 版本，要么在 preflight 直接报错。

权限：

- 初期用 root 运行；后续收敛到 `CAP_BPF` + `CAP_PERFMON` + `CAP_SYS_ADMIN`。
- 容器场景需要 host PID namespace、`/proc`、`/sys/kernel/btf`；库路径用 `/proc/<pid>/root`。
- 不允许为了 attach 修改 CANN 库或重启业务；msPTI 若必须进程内初始化，走维护窗口。
- `CAP_BPF`/`CAP_PERFMON` 在 5.10 可用；如果内核未启用，回退 root + `CAP_SYS_ADMIN`，但要在报告里记录实际权限模型。

### 16.3 部署方式

宿主机 systemd 形态：

```ini
[Unit]
Description=cann-ebpf observability agent
After=network.target docker.service

[Service]
Type=simple
ExecStart=/opt/cann-ebpf/cann-ebpf daemon --config /etc/cann-ebpf/config.yaml
Restart=on-failure
LimitMEMLOCK=infinity
AmbientCapabilities=CAP_BPF CAP_PERFMON CAP_SYS_ADMIN
CapabilityBoundingSet=CAP_BPF CAP_PERFMON CAP_SYS_ADMIN
NoNewPrivileges=true

[Install]
WantedBy=multi-user.target
```

容器 sidecar 形态：

- `hostPID: true`
- 挂载 `/proc`、`/sys/kernel/btf`、`/sys/fs/bpf`（如需要）
- 加 `CAP_BPF`/`CAP_PERFMON`/`CAP_SYS_ADMIN`
- 不默认 `privileged: true`；只有 caps 不足时才评估

部署后先跑 `discover --dry-run`，再 `attach --duration 30s`；不要直接上生产长跑。

## 17. 运行手册（初版）

```bash
# 只发现，不 attach
./cann-ebpf discover --pid 1691816 --profile orinfer --dry-run

# 30 秒采集
./cann-ebpf attach --pid 1691816 --profile orinfer --duration 30s

# 实时 top
./cann-ebpf top --pid 1691816 --profile orinfer

# 5 分钟归因
./cann-ebpf why --pid 1691816 --profile orinfer --since 5m

# 生成报告
./cann-ebpf report --run runs/20261125-orinfer-01
```

## 18. 变更记录

| 日期 | 版本 | 变更 |
|---|---|---|
| 2026-11-25 | v1.0 | 方案定稿：一核心 + 三 profile；Orinfer 先行，V4/vLLM 后置授权 |
| 2026-11-25 | v1.1 | 补采样配对、嵌套去重、卡死心跳、PID/库 inode、共享 stream、Orinfer 归因边界、socket 安全、manifest、退出码、overhead 定义、预检、schema |
