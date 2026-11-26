# M1 输出与证据层

状态：`internal/runlog` 已实现；CLI 已接入 `attach` 与 `attach-tracefs`。本文件记录的
`cann.span.v1` / `cann.manifest.v1` / `cann.summary.v1` 已扩展到 M4 的 batch 上下文和 PID 汇总；
历史 M1 的 8 worker 30s 2088 spans 仍是有效基线，当前 M4 的 37 探针、6,991 spans 实测见
`docs/M4-VALIDATION.md`。

本层只负责把已观测 span 稳定落盘、维护运行级证据索引、生成派生摘要。它不采集事件、不 attach、不补造缺失值，也不修改原始 JSONL。

## 1. Span JSONL 契约

每行一个 UTF-8 JSON 对象，schema 固定为 `cann.span.v1`。

| 字段 | 类型 | 必填 | 语义 |
|---|---|---|---|
| `schema` | string | 是 | 固定 `cann.span.v1` |
| `run_id` | string | 是 | 运行 ID，必须与 manifest 一致 |
| `profile` | string | 是 | `orinfer` / `vllm-ascend` 等，必须与 manifest 一致 |
| `ts_ns` | int64 | 是 | 采集侧观测时间戳，单位 ns；M1 为采集进程单调时钟读数 |
| `start_ns` | int64 | 否 | 观测到的调用起始时间，单位 ns；与 `ts_ns` 同一时钟域 |
| `end_ns` | int64 | 否 | 观测到的调用结束时间，单位 ns；不得早于 `start_ns` |
| `pid` | int | 是 | 进程 ID，即 Linux thread-group ID |
| `tid` | int | 是 | 线程 ID |
| `tgid` | int | 是 | 进程 ID 显式副本，必须等于 `pid` |
| `comm` | string | 否 | 内核 comm |
| `thread_name` | string | 否 | 采集侧线程名 |
| `function` | string | 是 | CANN/ACL/HCCL 函数名，原样记录 |
| `api` | string | 否 | 函数/API 名；用于保留 profile 中的调用标识，不替代 `function` |
| `library` | string | 是（可为空串） | 共享库路径；无法解析时保留空串，不编造 |
| `category` | string | 是 | `sync` / `event` / `submit` / `memory` / `hccl` / `sched` 等 |
| `duration_ns` | int64 | 是 | 观测耗时，单位 ns，必须 `>= 0` |
| `ret` | int64 | 否 | 原始返回值；缺失则省略，真实 `0` 保留 |
| `phase` | string | 否 | 如 `enter` / `exit`，不由输出层补造 |
| `dev_id` | int | 否 | 设备 ID；未知时省略，不能填占位值 |
| `batch_id` | string | 否 | 上下文协议匹配到的 batch 标识；没有匹配时省略 |
| `request_ids` | array | 否 | 与 batch 关联的 request 标识数组；只是关联键，不是业务内容 |
| `attribution` | object | 否 | 派生归因：`level`、`source`、`confidence`、`verdict`；不修改原始观测值 |
| `span_id` | string | 是 | 稳定派生 ID；为空时 writer 用身份字段和 `seq` 生成 SHA-256 |
| `seq` | uint64 | 是 | 运行内严格递增；0 由 writer 按接收顺序分配 |
| `unknown_fields` | array | 否 | 显式保留的前向兼容字段 |

`attribution.level` 只有三种稳定结果：`batch`（span 时间窗与上下文 batch 窗口重叠）、
`thread`（没有 batch 窗口，回退到线程级）、以及缺失时的 `unattributed`。`batch` 不是
“逐 request 时长”；`request_ids` 只说明该 batch 包含哪些请求，不能把同一 batch 的耗时
平摊或复制给每个 request。

示例：

```json
{
  "schema": "cann.span.v1",
  "run_id": "20261126-vllm-01",
  "profile": "vllm-ascend",
  "ts_ns": 1234567890,
  "start_ns": 1234567890,
  "end_ns": 1244567890,
  "pid": 4321,
  "tid": 5000,
  "tgid": 4321,
  "comm": "VLLM::EngineCore",
  "thread_name": "worker-0",
  "function": "aclrtSynchronizeStream",
  "api": "aclrtSynchronizeStream",
  "library": "/usr/local/Ascend/cann-9.2.0/aarch64-linux/lib64/libascendcl.so",
  "category": "sync",
  "duration_ns": 10000000,
  "ret": 0,
  "phase": "exit",
  "batch_id": "b-1",
  "request_ids": ["req-1", "req-2"],
  "attribution": {
    "level": "batch",
    "source": "vllm_general_plugin",
    "confidence": 1,
    "verdict": "batch_window"
  },
  "span_id": "f03f...",
  "seq": 1
}
```

### 未知字段

未知顶层字段不会被静默丢弃，`ReadSpans` 会放入：

```json
"unknown_fields": [
  {"name": "legacy_field", "value": {"nested": true}}
]
```

- `unknown_fields` 是显式标记，不表示字段已验证。
- 未知字段名不能与稳定字段重名。
- 名称和 JSON 值原样保留，不能借它覆盖 `pid`、`function`、`duration_ns` 等字段。

## 2. JSONL Writer

```go
writer, err := runlog.NewWriter(runlog.WriterOptions{
    Dir:         "/var/lib/cann-ebpf/runs/20261126-vllm-01",
    RunID:       "20261126-vllm-01",
    Profile:     "vllm-ascend",
    ToolVersion: "cann-ebpf/0.4.0-m4",
    TargetPIDs:  []int{4321, 4322},
    Libraries: []runlog.LibraryRef{{
        Path:   "/usr/local/Ascend/cann-9.2.0/aarch64-linux/lib64/libascendcl.so",
        Device: 8,
        Inode:  424242,
    }},
})
if err != nil {
    return err
}
defer writer.CloseWithError(runErr)
if err := writer.WriteSpan(span); err != nil {
    return err
}
if err := writer.Flush(); err != nil {
    return err
}
```

- 自动创建目录（创建模式 `0700`），JSONL/manifest 文件创建模式 `0600`。
- JSONL 以 append 写入；默认要求文件不存在，防止新运行混入旧证据。确需续写时显式设置 `AppendExisting: true`。
- `WriteSpan` 并发安全；writer 串行分配序号并写完整一行。
- 显式 `seq` 必须严格递增；`run_id` / `profile` 与 writer 不一致时拒绝写入，不静默改写。
- `Flush` 先 flush 再 `fsync`，成功后才把 durable event count 写入 manifest。
- `AddDropped(n)` 与 `AddWarning(s)` 立即更新 manifest。
- `CloseWithError(err)` 完成 flush/fsync、JSONL SHA-256 和最终 manifest。采集失败原因写入 `error`，不作为关闭 I/O 错误返回。
- writer 的 I/O 或 manifest 更新失败会进入 failed 状态，后续写入被拒绝。

M1 未实现 JSONL 轮转；单文件 SHA-256 在关闭后写入 manifest。

## 3. Manifest 契约

`manifest.json` 与 `spans.jsonl` 同目录，schema 为 `cann.manifest.v1`。启动即写 `status=running`；正常关闭改 `complete`；异常关闭改 `error`。

| 字段 | 语义 |
|---|---|
| `schema` | 固定 `cann.manifest.v1` |
| `run_id` / `tool_version` / `profile` | 运行身份 |
| `status` | `running` / `complete` / `error` |
| `host` / `kernel` / `cann_version` | 可选环境身份 |
| `target_pids` | 目标 PID 数组，不允许为空 |
| `libraries[]` | 目标库 `path`、`device`、`inode`、可选 `size` / `mtime` |
| `started_at` / `ended_at` | RFC3339 时间；结束态必须有 `ended_at` |
| `event_count` | 已成功 flush+fsync 的当前运行 span 数 |
| `dropped_events` | 上游明确报告的丢失事件数 |
| `warnings[]` | 采集/发现 warning，按发生顺序保留 |
| `operator` / `command` | 操作者与命令 |
| `segments[]` | JSONL 分片 `path`、`bytes`、`sha256` |
| `error` | `status=error` 时的错误文本 |

manifest 不覆盖已有文件；每次更新使用同目录临时文件、`fsync` 后原子 rename，写入失败时内存状态不前进。

## 4. Summary

已有 span 对象时：

```go
summary, err := runlog.BuildSummary(spans)
```

只有 JSONL 时：

```go
summary, err := runlog.BuildSummaryFromReader(jsonlReader)
```

已有 JSONL 文件路径时：

```go
summary, err := runlog.BuildSummaryFromFile("/path/spans.jsonl")
```

输出：

```go
err := summary.WriteJSON("/path/summary.json")
err = summary.WriteMarkdown("/path/summary.md")
```

统计口径：

- 按 `function` 聚合：`count`、`total_ns`、`p50_ns`、`p95_ns`、`p99_ns`、`max_ns`。
- 按 `category` 聚合：同样统计，并增加 `span_pct`。
- 按 `pid` 聚合：`count`、`batch_spans`、`thread_spans`、`total_ns`；用于确认多 Worker 覆盖。
- 按 `attribution.level` 聚合：`count`、`span_pct`；`batch` / `thread` / `unattributed` 分别计数。
- `context_coverage`：batch/thread/unattributed span 数、`batch_span_pct`、唯一 batch 和 request ID 数。
- `top_slow_batches`：按 batch 的 `total_ns` 降序，最多 10 条；附带 request IDs、PIDs、run IDs 和示例 span IDs。没有 `batch_id` 时明确为空，不伪造排名。
- 分位数使用 nearest-rank：升序排序后 `rank = ceil(p * n)`，取 `values[rank-1]`。
- 空 `function`、空 `category`、负 `duration_ns` 返回错误，不做静默修正。
- 空输入输出 `status=empty`、`span_count=0`、空数组；不生成 p50/p95/p99/max/total 行。JSON 使用 `[]`，Markdown 明确写“未生成统计”。
- summary 是派生文件，不替代、不改写 `spans.jsonl` 和 `manifest.json`。

Summary JSON schema：

```json
{
  "schema": "cann.summary.v1",
  "status": "ok",
  "span_count": 2,
  "functions": [
    {"function": "aclrtSynchronizeStream", "count": 2, "total_ns": 30, "p50_ns": 10, "p95_ns": 20, "p99_ns": 20, "max_ns": 20}
  ],
  "categories": [
    {"category": "sync", "count": 2, "span_pct": 100, "total_ns": 30, "p50_ns": 10, "p95_ns": 20, "p99_ns": 20, "max_ns": 20}
  ],
  "pids": [
    {"pid": 4321, "count": 2, "batch_spans": 2, "thread_spans": 0, "total_ns": 30}
  ],
  "attribution_levels": [
    {"level": "batch", "count": 2, "span_pct": 100}
  ],
  "context_coverage": {
    "batch_spans": 2,
    "thread_spans": 0,
    "unattributed_spans": 0,
    "batch_span_pct": 100,
    "unique_batches": 1,
    "unique_request_ids": 2
  },
  "top_slow_batches": [
    {"batch_id": "b-1", "count": 2, "total_ns": 30, "p50_ns": 10, "p95_ns": 20, "p99_ns": 20, "max_ns": 20, "request_ids": ["req-1", "req-2"], "pids": [4321], "run_ids": ["20261126-vllm-01"], "example_span_ids": ["f03f..."]}
  ]
}
```

## 5. 对外接口

```go
func NewWriter(options WriterOptions) (*Writer, error)
func (w *Writer) WriteSpan(span Span) error
func (w *Writer) Flush() error
func (w *Writer) AddDropped(count uint64) error
func (w *Writer) AddWarning(warning string) error
func (w *Writer) Close() error
func (w *Writer) CloseWithError(runErr error) error
func (w *Writer) JSONLPath() string
func (w *Writer) SnapshotManifest() Manifest

func NewManifestStore(path string, manifest Manifest) (*ManifestStore, error)
func (s *ManifestStore) Snapshot() Manifest
func (s *ManifestStore) Update(fn func(*Manifest)) error
func (s *ManifestStore) Finish(endedAt time.Time, runErr error) error

func ReadSpans(r io.Reader) ([]Span, error)
func BuildSummary(spans []Span) (Summary, error)
func BuildSummaryFromReader(r io.Reader) (Summary, error)
func BuildSummaryFromFile(path string) (Summary, error)
func (s Summary) Markdown() string
func (s Summary) WriteJSON(path string) error
func (s Summary) WriteMarkdown(path string) error
```

## 6. 验证

已执行：

```text
go test ./internal/runlog
ok  cann-ebpf/internal/runlog
```

测试覆盖：

- 未知字段显式保留、保留字段冲突拒绝、进程/线程归属校验。
- 并发写入 320 条 span、序号唯一、JSONL 可逐行解码。
- manifest 启动即写、状态迁移、空数组、禁止覆盖、原子更新、库 inode。
- flush 后可见、最终关闭计算字节数和 SHA-256、丢事件与 warnings。
- nearest-rank 分位数、function/category 聚合、空数据不伪造统计、JSON/Markdown 双格式输出。

## 7. M1 边界

本层只负责输出/证据：CLI 接入、ringbuf 消费、entry/exit 配对和探针 attach 已由 M1/M4 其他组件实现；
本层仍未实现 JSONL 轮转、`journal.jsonl`、summary 模板变量替换。接入这些部分时，应直接使用本层
API，不改变上述字段语义。

版本边界：

- M1 历史基线是 8 个 host 探针、8 worker 30 秒 2,088 spans；M4 已扩展到 37 个 host 探针，
  V4 10 秒实测 6,991 spans。两者不能混写成一个探针集合。
- `batch_id` / `request_ids` / `attribution` 是上下文协议的派生字段；没有插件或 smoke 时省略或标
  `thread`，不能补造。
- 当前没有 msPTI 设备侧字段，也没有逐 request 的 `duration_ns` 拆分。summary 的 batch 聚合是
  “时间窗内 host span 的合计”，不是设备 kernel 时长。
