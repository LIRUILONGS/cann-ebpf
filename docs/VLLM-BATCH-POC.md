# 纯 eBPF vLLM Batch POC

日期：2026-11-26

## 1. 范围和结论

本轮只回答一个有限问题：

> 针对当前这一台 V4 服务器上的固定运行时，纯 eBPF 能否在不重启、不装插件、不修改目标进程的前提下，
> 从 `SchedulerOutput.num_scheduled_tokens` 直接读出 batch 的 request ID，并给出可关联的 batch 窗口？

答案是可以，但边界必须说清楚：

- 这是 **固定版本 POC**，不是通用 Python/vLLM 支持；
- batch 身份是 `num_scheduled_tokens` 这个 dict 的指针，不是 `execute_model` 函数入口/返回；
- `begin_ns` / `end_ns` 是两次相邻 SchedulerOutput dict 身份之间的观测窗口；
- 它不能推导 request 延迟、模型执行时间或设备 kernel 时间；
- 它不读取 prompt、响应、token tensor 或业务文本。

固定运行时：

```text
OS            openEuler 5.10.0-136.108.0.188.31.oe2203sp1.bclinux.aarch64
Arch          aarch64
Python        3.12.13
vLLM          0.23.0+empty
vLLM-Ascend   0.23.0rc1
Container     deepseek-v4-flash-8905
Worker PID    1452 (VLLM::Worker_TP0_EP0)
EngineCore    4192024
libpython     /usr/local/python3.12.13/lib/libpython3.12.so.1.0
vLLM source   /vllm-workspace/vllm (editable)
Ascend source /vllm-workspace/vllm-ascend (editable)
```

## 2. 与 M5 POC 的区别

M5 POC 只做了：

```text
PyObject_GetAttr("num_scheduled_tokens")
  -> 读取返回 dict 的 key
  -> 输出去重后的 request ID
```

它没有 batch 边界，不能把 request ID 和一段 CANN host span 关联起来。batch POC 增加：

1. 读取 owner object 的类型名，确认不是随便一个带同名属性的对象；
2. 以返回 dict 指针作为 batch 身份；
3. 输出 `batch_begin` / `batch_end` 事件；
4. 把 request ID 事件绑定到 batch；
5. 在 Go 侧按 batch 聚合、去重、排序，并读取 BPF `current` map 给捕获结束时仍打开的 batch 补 `last_seen_ns`；
6. 把 offset 配置从代码中抽成版本化 JSON，并在报告中回显。

## 3. 硬编码对象布局

配置文件：

```text
poc/cpython/offsets/cpython-3.12.13-aarch64.json
poc/cpython/offsets/vllm-0.23.0rc1-aarch64.json
```

CPython 3.12.13 aarch64：

| 字段 | 偏移/大小 | 用途 |
|---|---:|---|
| `PyASCIIObject` sizeof | 40 | compact ASCII 字符串数据起点 |
| `PyDictObject.ma_keys` | +32 | dict keys 指针 |
| `PyDictObject.ma_values` | +40 | combined dict 要求为 NULL |
| `PyDictKeysObject.dk_kind` | +10 | 必须为 1（unicode keys） |
| `PyDictKeysObject.dk_nentries` | +24 | entries 数量 |
| `PyDictKeysObject.dk_indices` | +32 | indices 起点；entries 紧随其后 |
| `PyDictUnicodeEntry` sizeof | 16 | key 指针 + value 指针 |
| `PyTypeObject.tp_name` | +24 | owner 类型名 |

验证方式：

- 服务器 CPython 3.12.13 头文件；
- 运行中的 Python 进程用 `ctypes` 读取 `id(int)`、`id({})` 的对象内存；
- 合成 `SchedulerOutput` 目标实测；
- 真实 V4 Worker 1452 实测。

`SchedulerOutput` 本身是 Python dataclass，**没有可硬编码的 C struct offset**。因此 vLLM 侧配置只固定：

```text
target_attr      = num_scheduled_tokens
target_attr_len  = 20
boundary         = scheduler_output_dict_identity
```

## 4. 探针和 batch 语义

BPF 只挂两个点：

```text
uprobe   PyObject_GetAttr
uretprobe PyObject_GetAttr
```

入口：

```text
x0 = owner object
x1 = attribute name PyUnicodeObject
只保留 name == "num_scheduled_tokens"
inflight[pid_tgid] = {start_ns, owner_object}
```

返回：

```text
x0 = returned dict
读取 PyDictObject / PyDictKeysObject
接受 combined unicode dict（ma_values == NULL, dk_kind == 1）
读取前 8 个 compact ASCII unicode key
```

批次判定：

```text
新 dict 指针 != 当前 dict 指针
  -> 关闭旧 batch（batch_end，ts=now）
  -> 打开新 batch（batch_begin，ts=入口 start_ns）
  -> 读取 owner 类型名
  -> 解析 request ID

同一 dict 指针
  -> 只更新 last_seen_ns
  -> 仍解析 key，但 Go 侧按 (batch, request_id) 去重
```

为什么用 dict 指针，而不是函数名：

- `execute_model` 是 Python 函数，uprobe 不能直接挂 Python 字节码；
- `PyObject_GetAttr` 是稳定的 C API 符号；
- 每个调度步都会构造新的 `SchedulerOutput`，其 `num_scheduled_tokens` dict 是新对象；
- dict 指针身份足以把同一个调度步内的多次属性访问归到同一 batch；
- 这个边界是可解释的，但**不是函数级边界**。

## 5. 事件和报告 schema

BPF 固定事件 184 字节：

```text
ts_ns, dict, object, batch_begin_ns
pid, tid, event_type, dict_used
index, total, nentries, kind, truncated, id_len
id[48], type_name[64]
```

`event_type`：

```text
1 = request ID
2 = batch_begin
3 = batch_end
```

报告 schema：

```text
cann.cpython.vllm_batch_poc.v1
```

关键字段：

```json
{
  "status": "ok",
  "boundary": "scheduler_output_dict_identity",
  "window_semantics": "observed interval between SchedulerOutput.num_scheduled_tokens dict identities; not request latency, model execution time, or device kernel time",
  "batch_count": 37,
  "complete_batches": 36,
  "open_batch_count": 1,
  "request_count": 6,
  "type_names": ["SchedulerOutput"],
  "type_confirmed": true,
  "batches": [
    {
      "batch_id": "batch-0001",
      "dict": 70366648542208,
      "object": 70366647865840,
      "type_name": "SchedulerOutput",
      "begin_ns": 10173312796205402,
      "end_ns": 10173312822683000,
      "duration_ns": 26478000,
      "open": false,
      "request_ids": ["chatcmpl-..."],
      "request_count": 6,
      "observed_id_events": 12,
      "dict_used": 6,
      "truncated": false
    }
  ]
}
```

`duration_ns` 的语义是观测窗口，不是请求耗时。它从第一次看到该 dict 的 `num_scheduled_tokens` 属性读取开始，到下一次看到不同 dict 指针为止；其中可能包含调度等待和其他 Python/CANN 工作。

## 6. 构建和运行

构建 BPF 对象：

```bash
clang --target=bpfel -mcpu=v1 -O2 -g -c \
  poc/cpython/bpf/batch_probe.bpf.c \
  -o poc/cpython/bpf/batch_probe.bpf.o
```

构建 linux/arm64 二进制：

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
  go build -buildvcs=false -trimpath \
  -o dist/cpy-batch-poc-linux-arm64 ./cmd/cpy-batch-poc
```

合成目标：

```bash
nsenter -t <enginecore-pid> -m -- \
  /usr/local/python3.12.13/bin/python3 /tmp/synthetic_batch_target.py

./cpy-batch-poc-linux-arm64 \
  --pid <synthetic-pid> --duration 3s --max-events 20000 \
  --output /tmp/cpy-batch-synthetic.json --json
```

真实 V4 Worker：

```bash
./cpy-batch-poc-linux-arm64 \
  --pid 1452 --duration 6s --max-events 20000 \
  --output /tmp/cpy-batch-v4-1452-clean.json --json
```

参数：

| 参数 | 说明 |
|---|---|
| `--pid` | 目标进程；必须是已存在的 Worker |
| `--duration` | 短时只读采集窗口；POC 建议 3–10 秒 |
| `--lib` | 可选；默认从 `/proc/<pid>/maps` 找 `libpython3.12.so` |
| `--max-events` | Go 侧保留事件上限；真实 V4 6 秒建议 >= 20000 |
| `--offsets` | 可选；覆盖内置 offset bundle |
| `--require-type` | 默认 `SchedulerOutput`；类型不匹配时 status 为 `ok_type_mismatch` |
| `--output` | 写完整 JSON 报告 |
| `--json` | 标准输出 JSON；否则打印摘要和每个 batch |

退出码：

```text
0  ok
3  找到数据但类型未确认/不匹配，或 matched_but_unparsed
4  no_match
5  附加、读取或参数错误
```

## 7. 实测证据

### 7.1 合成 CPython 3.12.13

文件：

```text
dist/cpy-batch-poc-20261126/evidence/cpy-batch-synthetic-20261126.json
SHA256 388816c4fc8e5b1a7abe2a4f9b44c179ee7b76420e37f51c2e50fe7c057274fd
```

结果：

```text
status=ok
pid=874221
batch_count=120
complete_batches=119
open_batch_count=1
request_count=360
type_names=["SchedulerOutput"]
type_confirmed=true
entries=240 name_matches=240 returns=240 parsed=240 emitted=720
read_errors=0 split_dict=0 non_unicode=0 empty=0 ringbuf_lost=0
batches_started=120 batches_closed=119 type_read_errors=0
```

合成目标每 50 ms 创建两个新的 `SchedulerOutput`，每个对象被 `getattr` 两次；报告得到 120 个 batch、每个 batch 3 个 request ID，验证了 dict 身份切换、重复访问去重和类型名读取。

### 7.2 真实 V4 Worker 1452

文件：

```text
dist/cpy-batch-poc-20261126/evidence/cpy-batch-v4-1452-20261126.json
SHA256 afa83ca23e5bd19dcc5023753810adaf5ebe2fbeb446414af3a61183e1b41c03
```

结果：

```text
status=ok
pid=1452
batch_count=37
complete_batches=36
open_batch_count=1
request_count=6
type_names=["SchedulerOutput"]
type_confirmed=true
entries=513059 name_matches=538 returns=538 parsed=538 emitted=1993
read_errors=0 split_dict=0 non_unicode=0 empty=0 ringbuf_lost=0
batches_started=37 batches_closed=36 type_read_errors=0
```

真实 request ID 示例（未脱敏，来自实际证据）：

```text
chatcmpl-8ba2861befc43b50-a71cf0eb
chatcmpl-968fea4404cb972d-9c87ae51
chatcmpl-9b85d4d89ab0154b-8d6e8ddf
chatcmpl-a8707ea6124ddf2c-8ff0cdf2
chatcmpl-accbe898e3fd857a-b52b1212
chatcmpl-b2761e3f4923751d-8b8c85f4
```

36 个完整 batch 的观测窗口：最小约 57.2 ms，中位数约 70.9 ms，p95 约 690.0 ms，最大约 834.9 ms。长尾说明观测窗口会包含调度等待或卡顿，不能把它解释为模型执行时间或单请求执行时长。

## 8. 能证明什么

- 当前 V4 进程的 `SchedulerOutput.num_scheduled_tokens` dict 可以被纯 eBPF 读取；
- 读取到的 owner 类型名是 `SchedulerOutput`；
- 真实 `chatcmpl-*` request ID 可以被读出；
- 同一 dict 的多次属性访问可以被归到同一 batch；
- 不同调度步的 dict 切换可以被识别为 batch 边界；
- 整个路径没有 `read_errors`、`ringbuf_lost`、split dict 或 non-unicode dict 跳过；
- 不需要重启 V4、不需要安装 Python/vLLM 插件、不需要修改目标进程。

## 9. 不能证明什么

- 不能证明这是 `execute_model` 的函数级开始/结束；
- 不能把 `duration_ns` 当成 request 延迟或模型执行时间；
- 不能推导设备 kernel 时间线；这仍需要 msPTI/CANN profiler；
- 不能覆盖所有 dict 变体：当前只接受 combined unicode dict、compact ASCII key；
- 单次属性读取最多 8 个 key；超过 8 个 request 的 batch 会标记 `truncated=true`；
- `inflight` 以 `pid_tgid` 为 key；同一线程内若发生目标属性的重入读取，开始时间可能被覆盖；
- 不能保证 Python 版本、vLLM 版本或容器镜像变化后仍然可用；
- 不是生产稳定接口，不能直接替代 `contextfeed`；
- `ringbuf_lost>0` 或 `--max-events` 截断可能造成 batch 合并/缺失；必须同时检查 `warnings` 和 `ringbuf_lost`；
- 6 秒内观察到 513,059 次 `PyObject_GetAttr`，开销不低，不能长时间挂载。

## 10. 复现和证据边界

- 本次 V4 没有重启；POC 只对现有 PID 1452 做短时只读 uprobe；
- 没有修改 V4 容器、模型文件、CANN 库或 Python 运行时；
- 没有读取 prompt、响应、token tensor 或业务文本；
- 证据文件保留原始 request ID，未脱敏；
- `dist/` 被 `.gitignore` 忽略，证据是本地交付产物，不进入代码提交；
- 代码提交只包含 POC 源码、offset 配置、测试和文档。
