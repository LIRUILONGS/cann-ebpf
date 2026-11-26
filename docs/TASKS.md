# cann-ebpf 开发任务

状态（2026-11-26，`0.6.0-m2`）：M0 完成；M1 host core 完成（`diagnose` 覆盖原 `top`/`why` 初版的只读定位）；
M4 的 vLLM-Ascend host profile 与上下文协议完成，vLLM 插件代码完成但未激活；M2 代码+单测完成、未上机；
M3、M6 未完成；Orinfer 未运行验证；`execute_model` 函数级耗时本轮不做。

## P0 低层定位命令（已完成）

- [x] `cann-ebpf diagnose`：只读读取 `spans.jsonl` + `manifest.json`
- [x] 时间窗（单调 ns / `--last`）、PID、TID、batch_id、request_id、trace_id 过滤
- [x] 输出命中 PID/TID/batch/request、top slow function/category、类别分布
- [x] host off-CPU/futex 迹象与 `host_lock_contention` 判定
- [x] 证据 span（span_id + start/end/duration + offcpu/futex）
- [x] 明确输出“无法判定”原因（缺 off-CPU、缺 msPTI、batch 非 request、插件未激活）
- [x] JSON（`cann.diagnose.report.v1`）与中文 Markdown
- [x] 单测：时间窗、PID/TID/batch/request/trace、空结果、判定分支、CLI 退出码
- [x] 对 M4 交付的真实 V4 evidence 只读运行（6,991 spans 与 164 spans batch run 均 status=ok）
- [ ] 在新的服务器窗口里对最新采集目录再跑一遍

## M0 骨架与发现

- [x] Go module / 工作区
- [x] 配置加载、`extends` 合并、JSON Schema 校验
- [x] `/proc` 进程发现：cmdline、maps、cgroup、root、namespace、listen port
- [x] profile 匹配：`any/all/exclude`、priority、`first_match/all_matching`
- [x] ELF dynsym 解析：vaddr→file offset、缺符号 fail-soft
- [x] `discover --dry-run`：JSON/human 输出、候选库、候选符号
- [x] 单元测试：配置合并、match 语义、stat/maps 解析、ELF offset、容器 root 回退
- [x] `linux/arm64` 交叉编译
- [x] 服务器只读验证：V4 vLLM（不 attach）
- [ ] 服务器只读验证：Orinfer（当前未运行，等待授权窗口）

## M1 host core（V4 采集已验证）

- [x] H1 `sync` 与 event/submit/HCCL 核心探针（memory 探针已由 M4 的 37 函数集合覆盖）
- [x] ringbuf / entry-exit 配对
- [x] JSONL + manifest
- [x] 30 秒试跑（V4，不重启服务）
- [x] V4 eBPF 历史基线：单 PID 2s 70 spans；8 worker 2s 487 spans；8 worker 30s 2088 spans（回归批次 2665 spans）；health 前后 200；uprobe 无残留
- [x] 只读定位由 `diagnose` 覆盖（原 `top`/`why` 初版不再单列）

M1 的 8 个核心函数和 2088 spans 是历史基线。当前 37 个 host 探针、10 秒 6,991 spans 的实测见 M4。

## M4 vLLM-Ascend host profile 与上下文协议

### 已完成

- [x] `profiles/vllm-ascend.yaml`：8 Worker_TP + frontend + EngineCore 的稳定发现规则
- [x] 37 个 CANN/ACL/HCCL host uprobe，74 个 entry/exit 程序
- [x] 真实 V4 service discovery：10 个目标；每目标 37/148 符号、4 个库
- [x] `--pid auto` 自动 attach；10 秒采集 6,991 spans；dropped 0
- [x] 8 个 Worker_TP 均有 span；frontend/EngineCore 在该窗口无 span，不能写成有调用证据
- [x] 采集前后 `/health` 200、service `active`、uprobe 残留 0
- [x] JSONL / manifest / summary 的 count、bytes、SHA-256 一致性检查
- [x] batch 上下文 Unix socket 协议；未知字段拒绝、PID 分桶、窗口匹配
- [x] 非 root UID/GID socket 权限：目标 `982:979`，socket owner `982:979`，`SO_PEERCRED` 校验
- [x] 上下文 smoke：164 spans、21 batch spans、143 thread spans、2 个 request ID、validation `ok=true`
- [x] vLLM general plugin 代码、entry point、后台有界队列、5 个 Python 单测通过
- [x] `docs/M4-VALIDATION.md`、`docs/M4-CONTEXT-PROTOCOL.md`

### 未完成

- [ ] 将插件安装到运行中的 V4 Python 环境
- [ ] 重启 V4 或启动新实例，激活 general plugin
- [ ] 用真实 vLLM request 验证 `attribution.level=batch`
- [ ] 容器/宿主 `CLOCK_MONOTONIC` 校准（激活前必须做）
- [ ] 逐 request 执行时长拆分；当前 batch 是窗口归因，不是 request span
- [ ] 真实插件路径下的事件丢失、延迟和回滚演练

### 可交付边界

- 插件未激活时，collector 仍能采 host span，并明确回退为 `attribution.level=thread`。
- `protocol_smoke` 只能证明协议、UID 权限和窗口标注链路；不能冒充 `vllm_general_plugin` 流量。
- 37 个函数已 attach 不等于同一个窗口内 37 个函数都触发；最终窗口只观察到 6 类函数。

## M2 off-CPU 与锁归因（代码完成，未上机）

- [x] `sched_switch` tracepoint 累计线程 off-CPU
- [x] `futex_wait` / `futex_wait_requeue_pi` kprobe 累计 futex 等待
- [x] `offcpu_ns` / `futex_ns` 写入标准 span；区分“未测量”与“测得 0”
- [x] 嵌套调用快照（8 层）与漏配对复位（64 层）
- [x] 主 uprobe collection 与 blocking collection 分离加载，blocking 失败不影响 host span
- [x] `attach --blocking auto|on|off`；按 profile `probes.sched/futex` 默认值
- [x] `diagnose` 输出 `host_lock_contention` / `host_blocked_non_futex` / `host_cpu_running`
- [x] 单测：48 字节事件解码、测量 flag、模式解析、BPF 程序类型
- [ ] 真实内核 verifier/attach（预期 `offcpu=true futex=true failed=0`）
- [ ] 合成 pthread mutex 竞争对照
- [ ] V4 10 秒短窗口 + `/health`/service/残留检查
- [ ] 10 秒窗口开销基线（sched_switch 为全系统 tracepoint）
- [ ] 暂不实现 `pthread_mutex_lock` uprobe（由 futex 等待覆盖主要锁场景）

验证步骤见 `docs/M2-OFFCPU.md`；在完成上机验证前，不得把 M2 数字写成已验证事实。

## 纯 eBPF vLLM batch 标准采集

不依赖 vLLM 插件、不修改目标进程；`attach --python-batch` 可选启用。

- [x] 固定 CPython 3.12.13 / vLLM 0.23.0rc1 的硬编码对象布局
- [x] `SchedulerOutput.num_scheduled_tokens` dict 身份作为 batch key
- [x] owner 类型名确认（`SchedulerOutput`）
- [x] 合成目标和真实 V4 Worker 1452 实测；`read_errors=0`、`ringbuf_lost=0`
- [x] batch span 并入标准 `spans.jsonl`，`category=batch`、`attribution.source=ebpf_scheduler_output_dict`
- [x] 逐 Worker 生成 `python-batch-<pid>.json`，包含 stats、类型确认和 warnings
- [x] split unicode dict 可读；单次最多 64 个 request ID；超出标记 `truncated`
- [x] 真实 V4 标准输出验证：10 个 batch、6 个 request ID、10 条 batch span
- [ ] 8 Worker 同时开启的持续开销基线和自动降级策略
- [ ] 不是函数级 `execute_model` 边界；不能输出逐 request 执行时长

## M3 msPTI join

状态：未实现；M4 也没有初始化 msPTI。

- [ ] msPTI collector：API/KERNEL/MEMCPY/HCCL
- [ ] 时钟校准、`(device_id, correlationId)` join
- [ ] 对齐率报告、未匹配原因

## M5 V4/Orinfer A/B（需要人工窗口）

状态：等待 Orinfer 运行窗口；当前只有 V4 基线。

- [ ] 先采 V4 稳态
- [ ] 授权窗口停 V4、起 Orinfer
- [ ] 同 workload、同并发、同时长
- [ ] `compare.md`

## M6 生产化

- [ ] Prometheus/OTLP、Grafana
- [ ] systemd、权限收敛、自动 detach
- [ ] runbook、升级/回滚

## 交付门禁

- [ ] 代码、脚本、profile、adapter、文档全部通过 `gofmt` / `go test` / `go vet` / Python 单测
- [ ] 交付包只包含已验证二进制、配置、脚本、adapter、文档和证据
- [ ] `SHA256SUMS.txt` 覆盖二进制、证据 JSONL/manifest/summary 和关键文档
- [ ] Git 提交完成，提交信息明确区分 host 采集已完成与插件未激活
