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
