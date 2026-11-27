# M5 链路追踪与纯 eBPF CPython POC

日期：2026-11-26

## 1. 本轮交付范围

本轮在 M4 host 观测基础上增加两项能力：

1. W3C 形状的链路追踪上下文：batch span 作为父节点，CANN host span 作为子节点，输出到 JSONL、summary 和 `cann-ebpf trace` 命令。
2. 纯 eBPF CPython POC：不依赖 vLLM 插件，通过 `PyObject_GetAttr` 的 entry/return 直接读取 CPython 3.12 `scheduler_output.num_scheduled_tokens` dict 的 request ID 键。

这两项能力都不修改目标进程、不重启 V4、不读取 prompt/响应/token tensor。

## 2. 链路追踪

### 2.1 标识符语义

| 字段 | 位置 | 语义 |
|---|---|---|
| `trace.trace_id` | span | 32 位小写 hex，W3C Trace Context 形状 |
| `trace.span_id` | span | 16 位小写 hex，本 CANN span 的子 span ID，由 collector 派生 |
| `trace.parent_span_id` | span | 16 位小写 hex，batch span ID |
| `trace.batch_parent_span_id` | span | 可选的 batch 上游父 span ID；来自插件 `parent_span_id` |
| `trace.trace_state` | span | 可选 trace state，原样传递 |
| `trace.source` | span | trace 上下文来源，如 `vllm_general_plugin`、`protocol_smoke`、`collector_local_batch` |

现有 `span_id` 字段保持不变，仍是 JSONL 内部事件稳定 ID，不能与 W3C span ID 混用。新增 `trace` 对象专门承载链路追踪标识。

### 2.2 传播路径

```text
vLLM execute_model
  -> batch_begin: trace_id + batch span_id (+ 可选 parent_span_id)
  -> contextfeed 保存 batch window
  -> CANN host span 命中 batch window
  -> collector 派生 child span_id，parent_span_id = batch span_id，batch_parent_span_id = batch 的上游父 span
  -> spans.jsonl / summary.json / summary.md
  -> cann-ebpf trace 输出链路树
```

插件没有上游 HTTP `traceparent` 时，生成进程内本地 trace。设置 `CANN_EBPF_TRACEPARENT` 可以让 batch span 继承一个上游父 span：

```text
CANN_EBPF_TRACEPARENT=00-<32hex trace_id>-<16hex parent_span_id>-01
CANN_EBPF_TRACESTATE=vendor=value
```

这是进程级继承，不是逐 HTTP request 注入。要做完整的 `HTTP -> vLLM -> CANN` 分布式链路，还需要在 vLLM HTTP 层或反向代理中提取 `traceparent`，并把 request 级上下文传到 Worker；本轮没有声称已经打通这一层。插件提供的上游父 span 会保存在派生 CANN span 的 `trace.batch_parent_span_id` 中，即使 batch span 本身没有作为 CANN span 导出，也不会丢失上游关系。

### 2.3 collector 本地 trace

如果插件只发送 batch 上下文、没有发送 trace 字段，collector 仍会为该 batch 生成本地 trace：

- `trace.source=collector_local_batch`
- `trace_id` 与 batch `span_id` 由 collector 生成
- 每个命中的 CANN span 派生独立 child `span_id`
- 不伪造上游 parent；`parent_span_id` 只指向本地 batch span

这使旧插件也能获得可关联的 trace，但报告中必须明确 source 是 collector 本地生成，不能写成上游传播。

### 2.4 summary

`summary.json` 新增：

```json
{
  "trace_coverage": {
    "traced_spans": 17,
    "traced_span_pct": 8.994708994708995,
    "unique_traces": 1,
    "unique_batch_spans": 1,
    "missing_parent_spans": 0
  },
  "trace_sources": [
    {"source": "protocol_smoke", "count": 17, "span_pct": 100}
  ],
  "top_slow_traces": [
    {
      "trace_id": "55555555555555555555555555555555",
      "batch_span_ids": ["6666666666666666"],
      "count": 17,
      "total_ns": 731774117,
      "request_ids": ["req-smoke-1", "req-smoke-2"],
      "pids": [1452]
    }
  ]
}
```

`missing_parent_spans` 表示有 `trace_id` 但没有 `parent_span_id` 的 span。正常批处理路径应为 0；非 0 时必须先检查插件字段和 trace 传播，而不是补造 parent。

`top_slow_traces.total_ns` 是该 trace 内已观测 host span 的耗时之和，不是 trace 的端到端墙钟延迟；p50/p95/p99 也是这些 span 的分位数。报告不得把它写成 HTTP 或设备 kernel 的完整时延。

### 2.5 CLI

```text
cann-ebpf trace --input <spans.jsonl> [--trace-id <id>] [--limit N] [--json]
```

文本输出按 trace 聚合，再按 parent span 分组：

```text
trace report input=... traces=1 traced_spans=17 untraced=172
TRACE 55555555555555555555555555555555 spans=17 total_ns=731774117 requests=2 pids=1452
  sources=protocol_smoke
  upstream 7777777777777777 (not exported as a CANN span)
    batch 6666666666666666 (not exported as a CANN span)
      aclrtSynchronizeStream pid=1452 tid=1452 duration_ns=57863737 trace_span=181b2c38dadc2258 event_span=ae076c...
```

batch span 本身不是 CANN span，不会出现在 `spans.jsonl` 中；输出明确标注 `not exported as a CANN span`，不伪造一条 batch span 记录。上游 parent 也只在 `batch_parent_span_id` 和 trace 链路树中体现，不会被补造成一条实际 span。

### 2.6 最终 M5 smoke 证据

2026-11-26 使用最终 `cann-ebpf 0.5.0-m5` 二进制重跑：

```text
/opt/cann-ebpf/trace-cpy-20261126-m5/runs/context-test-m5-final-20261126T021000Z
```

结果：189 spans、17 batch spans、172 thread fallback、17 traced spans、1 个唯一 trace、
`missing_parent_spans=0`、`failures=[]`、HTTP health 前后均为 200、service 前后均为
active、uprobe 残留为 0。完整原始证据和 SHA-256 见交付包的 `EVIDENCE.md`。

## 3. 纯 eBPF CPython POC

### 3.1 目标

验证在没有 vLLM 插件、没有 Python USDT、没有 py-spy 的情况下，能否只靠 eBPF uprobe 读取 `scheduler_output.num_scheduled_tokens` 的 request ID 键。

POC 位于：

```text
poc/cpython/
  bpf/cpy_probe.bpf.c
  bpf/cpy_probe.bpf.o
  probe_linux.go
  probe_other.go
  synthetic_target.py
  README.md
cmd/cpy-poc/
  main_linux.go
  main_other.go
```

### 3.2 探针与读取路径

```text
uprobe   PyObject_GetAttr
  x0 = object
  x1 = attribute name PyUnicodeObject
  -> 只保留 name == "num_scheduled_tokens"
  -> inflight[pid_tgid] = {start_ns, object}

uretprobe PyObject_GetAttr
  x0 = 返回的 PyObject*
  -> 读取 PyDictObject
  -> 要求 ma_values == NULL，dk_kind == 1（combined unicode dict）
  -> 从 dk_indices 之后的 PyDictUnicodeEntry 数组读取 key
  -> 只接受 compact ASCII unicode key
  -> 通过 bpf_probe_read_user_str 读取以 NUL 结尾的 request ID
  -> ringbuf 输出最多 8 个 key/次
```

不读取 value（token 数）、prompt、响应或 tensor。

### 3.3 CPython 3.12.13 布局

服务器实际头文件确认：

- `PyASCIIObject` 在 3.12.13 中没有 `wchar_t *wstr` 成员，`sizeof == 40`；
- compact ASCII 字符串数据从对象起始地址 `+40` 开始；
- `PyDictObject` 的 `ma_keys` 在 `+32`，`ma_values` 在 `+40`；
- `PyDictKeysObject` 的 `dk_kind` 在 `+10`，`dk_nentries` 在 `+24`，`dk_indices` 在 `+32`；
- `PyDictUnicodeEntry` 每项 16 字节：key 指针 + value 指针。

这不是猜偏移；POC 的 BPF C 结构体按头文件字段顺序定义，Go 侧只解析固定 96 字节的 ringbuf 记录。

### 3.4 已验证结果

#### 合成 Python 3.12.13 进程

```text
pid=345742 duration=3s
entries=298 name_matches=298 returns=298 parsed=298 emitted=894
read_errors=0 skipped=0 split_dict=0 non_unicode=0 empty=0 ringbuf_lost=0
found=3
request_ids=synthetic-req-0001,synthetic-req-0002,synthetic-req-0003
```

#### 真实 V4 Worker PID 1452

```text
pid=1452 duration=6s max_events=500
entries=210995 name_matches=1116 returns=1116 parsed=1116 emitted=7450
read_errors=0 skipped=0 split_dict=0 non_unicode=0 empty=0 ringbuf_lost=0
dicts=3 dict_kind=1
found=8
request_ids:
  chatcmpl-8789bff262b9b8db-a1080497
  chatcmpl-9914419eefc6b60b-a3124eb2
  chatcmpl-9b803dc98149ac78-86b0cb3a
  chatcmpl-9d0d1324560addbc-8c7487f3
  chatcmpl-a578711205f10ecb-97f7d4f2
  chatcmpl-aa52e89f57b5ad8f-a2014ecb
  chatcmpl-b439645624afb10b-a03e68b5
  chatcmpl-bc8abd64b3d33387-9b49fef7
```

说明：`emitted=7450` 是 BPF 实际提交的 key 事件数；Go 侧为控制内存只保留前 500 条事件，所以 `found=8` 是保留事件中的唯一 request ID 数，不是 6 秒内全量请求数。`name_matches=1116` 是 `num_scheduled_tokens` 属性读取次数，不能直接等同于请求数。

### 3.5 能证明什么、不能证明什么

能证明：

- 在现有 V4 进程上，纯 eBPF 可以在不重启、不装插件的情况下读取 `num_scheduled_tokens` dict 的 request ID 键；
- CPython 3.12.13 的对象布局假设经过合成进程和真实 Worker 双重验证；
- 读取路径没有产生 read error、ringbuf lost 或 split/non-unicode dict 跳过。

不能证明：

- 不能把 POC 当成生产稳定的 request 归因接口；
- 不能从一次 dict 读取推导每个 request 的执行时长；
- 不能保证所有 vLLM 版本、所有 Python 构建、所有 dict 变体都适用；
- 不能把 `name_matches` 写成请求数；
- 不能把 POC 输出直接写进现有 `contextfeed` 作为 batch 窗口，因为 POC 只有属性读取时刻，没有真实的 batch_begin/batch_end 边界。

### 3.6 复现命令

构建：

```bash
clang --target=bpfel -mcpu=v1 -O2 -g -c   poc/cpython/bpf/cpy_probe.bpf.c -o poc/cpython/bpf/cpy_probe.bpf.o

CGO_ENABLED=0 GOOS=linux GOARCH=arm64   go build -buildvcs=false -trimpath -o dist/cpy-poc-linux-arm64 ./cmd/cpy-poc
```

运行：

```bash
./cpy-poc-linux-arm64 --pid 1452 --duration 6s --max-events 500   --output /tmp/cpy-v4-1452.json --json
```

合成目标：

```bash
nsenter -t 1452 -m -- /usr/local/python3.12.13/bin/python3.12   /tmp/synthetic_target.py
./cpy-poc-linux-arm64 --pid <synthetic-pid> --duration 3s --max-events 300 --json
```

## 4. 本轮验证边界

- V4 没有重启；POC 使用 uprobe 在现有 PID 上短时只读附加。
- 上下文 smoke 使用 `source=protocol_smoke`，不是真实 vLLM 插件流量。
- vLLM general plugin 仍未激活；本轮没有把 smoke 写成插件已验证。
- 没有接入 msPTI，没有设备 kernel 时间线，没有逐 request 执行时长。
- Orinfer 未运行，未做运行窗口验证。
- trace 的完整上游 HTTP 传播未实现；当前是本地 batch trace 或进程级 `CANN_EBPF_TRACEPARENT` 继承。
- `batch_parent_span_id` 只表示插件声明的 batch 上游父节点；没有该字段时，不能推断或补造上游链路。
