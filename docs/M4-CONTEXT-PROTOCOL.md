# M4 batch 上下文协议与 vLLM 插件

日期：2026-11-26

## 1. 目的

`cann-ebpf` 的 eBPF span 天然只能看到 host 线程和 CANN 调用。为了把 host span 关联到
vLLM 的 batch，本版本增加一个可选、只传标识符的 Unix socket 协议。它有三种明确结果：

1. 命中 batch 窗口：`attribution.level=batch`；
2. 没有窗口：`attribution.level=thread`，置信度 `0.3`；
3. 协议或 socket 不可用：采集继续，只失去 batch 归因，不丢 host span。

该协议不修改 eBPF 观测值，不读取 prompt、响应、token tensor 或业务文本。

## 2. 传输

- Unix domain socket，newline-delimited UTF-8 JSON；
- 单行最大 64 KiB；
- 版本字段 `v=1`；
- 连接可长驻，允许多个连接；
- collector 关闭时主动关闭 listener 和所有已建立连接，不会卡在 `wg.Wait()`。

默认路径为 profile 中的：

```text
/run/cann-ebpf/vllm-ascend.sock
```

## 3. 事件

### 3.1 字段

| 字段 | 必填 | 说明 |
|---|---|---|
| `v` | 否 | 缺省或 `1`；其他版本拒绝 |
| `type` | 是 | `batch_begin` / `batch_end` / `request_begin` / `request_end` / `heartbeat` |
| `ts_ns` | 除 heartbeat 外必填 | 事件时间戳，ns，必须大于 0 |
| `pid` | 是 | batch 所属目标进程 PID，必须匹配 eBPF span 的 PID |
| `batch_id` | 除 heartbeat 外必填 | batch 标识符 |
| `phase` | 否 | 如 `prefill` / `decode` |
| `request_ids` | 否 | request 标识符数组，最多 4096 个 |
| `model` | 否 | 模型标识，不包含提示词或输出 |
| `source` | 否 | 来源；缺省 `vllm_general_plugin` |
| `trace_id` | 否 | 32 位小写 hex；与 `span_id` 成对出现 |
| `span_id` | 否 | 16 位小写 hex；本 batch span ID |
| `parent_span_id` | 否 | 16 位小写 hex；batch 的上游父 span，可选 |
| `trace_state` | 否 | trace state 字符串，原样传递 |
| `trace_source` | 否 | 如 `vllm_general_plugin`、`protocol_smoke`；缺省取 `source` |

未知字段被 `DisallowUnknownFields` 拒绝。未知事件类型、空 `batch_id`、空 request ID、
非正时间戳、超过上限的 request 数组都会被拒绝并计数。

`trace_id`、`span_id` 和 `parent_span_id` 使用与 W3C Trace Context 相同的字符长度和
校验规则，但当前协议不是完整的 W3C HTTP header 传播实现。缺少 trace 字段时，
`batch_begin` 会由 collector 生成 `collector_local_batch` 本地 trace；如果只提供
`trace_id` 而不提供 `span_id`，则拒绝该事件，不能用无父节点的 trace 伪造 batch。

### 3.2 示例

```json
{"v":1,"type":"batch_begin","ts_ns":10161864490000000,"pid":1452,"batch_id":"b-1","phase":"decode","request_ids":["req-1","req-2"],"source":"vllm_general_plugin","trace_id":"55555555555555555555555555555555","span_id":"6666666666666666","parent_span_id":"7777777777777777","trace_state":"vendor=value","trace_source":"vllm_general_plugin"}
{"v":1,"type":"batch_end","ts_ns":10161864520000000,"pid":1452,"batch_id":"b-1","source":"vllm_general_plugin","trace_id":"55555555555555555555555555555555","span_id":"6666666666666666"}
```

### 3.3 匹配规则

- 按 PID 分桶，保留最近 2 分钟的窗口；
- 每个 PID 最多保留 512 个 batch；
- `batch_end` 必须能找到对应 `batch_begin`；
- span `[start_ns,end_ns]` 与 batch `[begin,end]` 有交叠即匹配；
- 多个窗口重叠时选开始时间更晚、接收时间更新的窗口；
- `batch_end` 携带 trace 时必须与 `batch_begin` 的 `trace_id` 和 `span_id` 一致；
  不携带 trace 时沿用 `batch_begin` 的值，不会用结束事件覆盖成另一条 trace；
- 未关闭的窗口以 span 结束时间为临时上界，不制造结束时间；
- request ID 只复制到 span，不改变每 request 的时长；
- 命中的 CANN span 会派生 child `span_id`，`parent_span_id` 指向 batch span；
  batch 的上游父 span 保存在 `batch_parent_span_id`，batch span 本身不写入 JSONL；
- `request_begin/request_end` 目前保留为协议证据，不生成 request span。

## 4. 权限模型

collector 通常以 root 运行，vLLM 容器实际以 UID `982`、GID `979` 运行。直接用
`0600 root:root` 会让容器插件无法连接。实现如下：

1. 从每个目标 `/proc/<pid>/status` 读取 `Uid:` 和 `Gid:` 的 real UID/GID；
2. 优先选一个非 root 目标作为 socket owner；
3. socket 目录 `0755`；
4. socket 默认 `0600`；有非 root 目标时 chown 到目标 UID/GID 并设为 `0660`；
5. 允许集合始终包含 collector 自身 UID，再加入所有目标 UID；
6. 使用 `SO_PEERCRED` 校验连接方 UID，不在允许集合内立即拒绝并记录 warning。

最终 V4 目标的 UID 均为 `982`，因此 socket owner 为 `982:979`。`context-smoke.sh`
会读取目标 UID/GID，在 Python 客户端中 `setgroups([])`、`setgid`、`setuid` 后再连接，
证明验证的是容器 UID 路径，而不是 root 自测。

## 5. vLLM general plugin

代码位于：

```text
adapters/vllm_ascend/
  cann_ebpf_vllm/plugin.py
  tests/test_plugin.py
  pyproject.toml
```

entry point：

```toml
[project.entry-points."vllm.general_plugins"]
cann_ebpf = "cann_ebpf_vllm.plugin:register"
```

插件 monkey patch 优先级：

1. `vllm_ascend.worker.model_runner_v1.NPUModelRunner.execute_model`
2. `vllm.v1.worker.gpu_model_runner.GPUModelRunner.execute_model`

从 `scheduler_output.num_scheduled_tokens.keys()` 读取 request ID，发送
`batch_begin` 和 `batch_end`。后台线程加有界队列，socket 不可用时只增加错误计数，
不阻塞模型执行；`finally` 保证发送 `batch_end`。

环境变量：

| 变量 | 默认 | 说明 |
|---|---|---|
| `CANN_EBPF_VLLM_SOCKET` | `/run/cann-ebpf/vllm-ascend.sock` | socket 路径 |
| `CANN_EBPF_VLLM_SOURCE` | `vllm_general_plugin` | span 中的 source |
| `CANN_EBPF_VLLM_DISABLE` | 空 | `1/true/yes/on` 时禁用 |
| `CANN_EBPF_TRACEPARENT` | 空 | 可选 W3C 形状 `00-<32hex>-<16hex>-01`，作为 batch 上游父 span |
| `CANN_EBPF_TRACESTATE` | 空 | 可选 trace state，随 batch 原样传递 |

`CANN_EBPF_TRACEPARENT` 是进程级配置：同一个 vLLM Worker 启动后，所有 batch 共享
该上游 `trace_id`，每个 batch span 仍由插件生成新的 `span_id`。它不是逐 HTTP request
注入；要打通 `HTTP -> vLLM -> CANN`，还需要在 vLLM HTTP 层或反向代理中提取请求头，
并把请求级 trace 上下文传给 Worker。当前文档不把进程级配置写成完整的分布式链路。

## 6. 激活条件与步骤

当前 V4 **没有**安装或激活该插件。vLLM 只在启动时加载 general plugin，复制文件到
运行中的容器不会生效。激活必须获得明确的维护窗口，并按以下顺序执行：

1. 备份当前 V4 启动配置和镜像/容器信息；
2. 将 `adapters/vllm_ascend` 安装到实际 vLLM Python 环境：

   ```bash
   python3 -m pip install --no-deps /path/to/adapters/vllm_ascend
   ```

3. 确认插件能被该 Python 环境导入，且 `importlib.metadata.entry_points()` 能看到
   `vllm.general_plugins` 注册项；
4. 将 host 的 `/run/cann-ebpf` 绑定到容器同一路径，或把
   `CANN_EBPF_VLLM_SOCKET` 指向双方共享的路径；
5. 确认目标 UID 对 socket 目录可 traverse、对 socket 可读写；
6. 重启 vLLM 或启动新实例；
7. 启动 collector，观察 `context socket owner` warning 和 `context batches > 0`；
8. 运行真实请求，验证 span 出现 `attribution.level=batch` 和 `trace`，并保留 JSONL/manifest；
9. 用 `cann-ebpf trace --input <spans.jsonl>` 验证 trace 到 batch 的链路树；
10. 失败时回滚：移除插件安装、恢复启动配置，collector 仍可退化为 thread。

在不重启的目标上，可以做的替代验证是 `context-smoke.sh`：它使用同一协议、同一 UID
和同一 socket 权限，但 source 为 `protocol_smoke`，不能冒充真实插件流量。

## 7. 时钟与 namespace

协议假定插件与 eBPF 使用同一 `CLOCK_MONOTONIC` 时间基准。Docker/容器通常不启用独立
time namespace，但本版本没有在所有运行环境中证明这一点。真实插件激活前必须：

- 读取容器内与宿主 `time.monotonic_ns()`，计算 offset；
- 或从插件事件中携带可校准的 host 侧锚点；
- 若 offset 非零，先实现校准，不能直接把容器时间戳与 host `bpf_ktime_get_ns()`
  匹配。

当前 smoke 在同一 host 上运行，因此不受该风险影响。

## 8. smoke 证据

最终证据目录：

```text
/opt/cann-ebpf/m4-20261126/runs/context-test-final-20261126
```

结果：

| 项 | 值 |
|---|---|
| target | PID 1452，UID 982，GID 979 |
| spans | 164 |
| batch spans | 21 |
| thread fallback spans | 143 |
| batch / request | `ctx-smoke-1` / 2 个 request ID |
| attribution | `batch` 21，`thread` 143 |
| validation failures | 0 |
| collector exit | 0 |
| health before/after | 200 / 200 |
| service | active / active |
| uprobe residual | 0 |
| manifest event_count | 164，与 JSONL 行数一致 |
| dropped events | 0 |

## 9. 边界

- 插件未在运行中的 V4 加载；真实 request 路径尚未验证。
- 只做 batch 窗口归因，不做 request 级耗时拆分。
- 不读取 prompt/response/token 内容。
- 未实现 msPTI 设备侧关联。
- 不把 `protocol_smoke` 结果写成 `vllm_general_plugin` 结果。
