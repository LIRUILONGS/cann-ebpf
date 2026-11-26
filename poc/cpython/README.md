# 纯 eBPF CPython 3.12 POC

这不是生产采集器。它验证一个有限问题：

> 在不装 vLLM 插件、不重启进程、没有 Python USDT 的情况下，能否用 eBPF uprobe 读取 `scheduler_output.num_scheduled_tokens` 的 request ID 键？

## 结构

```text
bpf/cpy_probe.bpf.c   uprobe/uretprobe 程序
bpf/cpy_probe.bpf.o   预编译 BPF 对象（linux/arm64 目标）
probe_linux.go        Go loader、ringbuf 解码、库路径发现
probe_other.go        非 linux/arm64 占位
synthetic_target.py   合成 CPython 3.12 目标
```

## 构建

```bash
clang --target=bpfel -mcpu=v1 -O2 -g -c   poc/cpython/bpf/cpy_probe.bpf.c -o poc/cpython/bpf/cpy_probe.bpf.o

CGO_ENABLED=0 GOOS=linux GOARCH=arm64   go build -buildvcs=false -trimpath -o dist/cpy-poc-linux-arm64 ./cmd/cpy-poc
```

## 运行

```bash
./cpy-poc-linux-arm64 --pid <pid> --duration 6s --max-events 500   --output /tmp/cpy-poc.json --json
```

`--lib` 可以显式指定 `libpython3.12.so.1.0`；默认从 `/proc/<pid>/maps` 读取，并在容器场景下自动改写成 `/proc/<pid>/root/...`。

## 支持范围

- CPython 3.12.13，aarch64；
- combined unicode dict（`ma_values == NULL`，`dk_kind == 1`）；
- compact ASCII unicode key；
- 每次属性读取最多输出 8 个 key；
- 只读 request ID 键，不读 value、prompt、响应或 tensor。

## 已知限制

- 硬编码 CPython 3.12 对象布局，换 Python 版本必须重新核对头文件和偏移；
- 不处理 split dict、non-unicode dict、非 compact 字符串；
- 只过滤属性名，不能证明对象一定是 `SchedulerOutput`；
- `PyObject_GetAttr` 调用次数很高，POC 未做生产级开销优化；
- 没有 batch_begin/batch_end 边界，不能直接替代 `contextfeed`；
- 不能从 POC 结果推导逐 request 时长。

## 已验证

- 合成 CPython 3.12.13：3 个 request ID，`read_errors=0`、`ringbuf_lost=0`；
- 真实 V4 Worker PID 1452：6 秒、1116 次属性命中、8 个真实 `chatcmpl-*` request ID，`read_errors=0`、`ringbuf_lost=0`。

详细数据和边界见 `docs/M5-TRACE-AND-CPY-POC.md`。

## vLLM batch POC

在 M5 的 dict 读取基础上增加 batch 身份和类型确认：

```text
poc/cpython/bpf/batch_probe.bpf.c
poc/cpython/bpf/batch_probe.bpf.o
poc/cpython/batch_probe_linux.go
poc/cpython/batch_types.go
poc/cpython/offsets.go
poc/cpython/offsets/cpython-3.12.13-aarch64.json
poc/cpython/offsets/vllm-0.23.0rc1-aarch64.json
poc/cpython/synthetic_batch_target.py
cmd/cpy-batch-poc/main_linux.go
```

batch 身份是 `SchedulerOutput.num_scheduled_tokens` 的 dict 指针。新 dict 关闭旧 batch、打开新 batch；owner 类型名通过 `PyObject.ob_type -> PyTypeObject.tp_name` 读取并确认。这个边界不是 `execute_model` 函数级边界，`duration_ns` 也不是请求执行时长。

构建和运行：

```bash
clang --target=bpfel -mcpu=v1 -O2 -g -c   poc/cpython/bpf/batch_probe.bpf.c -o poc/cpython/bpf/batch_probe.bpf.o

CGO_ENABLED=0 GOOS=linux GOARCH=arm64   go build -buildvcs=false -trimpath -o dist/cpy-batch-poc-linux-arm64 ./cmd/cpy-batch-poc

./cpy-batch-poc-linux-arm64 --pid 1452 --duration 6s --max-events 20000   --output /tmp/cpy-batch-v4-1452.json --json
```

已验证：

- 合成 `SchedulerOutput`：120 个 batch、360 个 request ID、`read_errors=0`、`ringbuf_lost=0`；
- 真实 V4 Worker 1452：37 个 batch（36 完整、1 个捕获结束时仍打开）、6 个真实 `chatcmpl-*` request ID、`read_errors=0`、`ringbuf_lost=0`、`type_confirmed=true`。

详细说明见 `docs/VLLM-BATCH-POC.md`。
