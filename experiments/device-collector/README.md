# msPTI in-process collector（M3 设备时间线生产端）

输出 `cann.device.v1` JSONL，供 `cann-ebpf diagnose` 与 host span 做 join。

## 为什么是预加载库

msPTI 在同一进程只允许一个 subscriber，并且必须在 ACL/torch_npu 初始化前订阅。
因此不能用外部进程“attach”正在运行的 V4/Orinfer；生产路径是在服务启动时通过
`LD_PRELOAD` 把 `libmspti.so` 和 `libcannmspti.so` 一起加载。该库不修改目标进程内存、
不安装 Python 包、不依赖 vLLM 插件。

## 构建（容器内）

```bash
source /usr/local/Ascend/ascend-toolkit/set_env.sh
cd m3/mspti_collector
mkdir -p build && cd build
cmake .. -DCMAKE_BUILD_TYPE=Release
make -j
```

产物：

```text
libcannmspti.so
cann-mspti-collector
cann-mspti-selftest
synthetic_acl
```

## 生产启用（V4 / Orinfer 维护窗口）

```bash
export MSPTI=/usr/local/Ascend/ascend-toolkit/latest/tools/mspti/lib64/libmspti.so
export CANN_MSPTI=/opt/cann-ebpf/libcannmspti.so
export LD_PRELOAD="$MSPTI:$CANN_MSPTI"
export CANN_MSPTI_OUTPUT=/var/log/cann-ebpf/device.jsonl
export CANN_MSPTI_RUN_ID=orinfer-20261126T120000Z
export CANN_MSPTI_ACTIVITIES=api,acl_api,runtime_api,kernel,memcpy,hccl
export CANN_MSPTI_DURATION=30
export CANN_MSPTI_MAX_BYTES=536870912
export CANN_MSPTI_CLOCK=auto
```

`libmspti.so` 必须排在 `libcannmspti.so` 前面，保证 mspTI 自身先完成初始化。
`CANN_MSPTI_DURATION=0` 表示一直采到进程退出；维护窗口建议设短窗口
（B1 一次采集窗口推荐 60s，可用 `CANN_MSPTI_DURATION=60s`）。
运行期间每 1 秒 flush 时同步刷新 `device.jsonl.meta.json`，记录 pid、container_id、run_id、
活动类型、当前记录数、字节数、丢弃/截断计数和 warning；停止时写最终值。

A2 加固后，meta 还会输出 `clock_offset_start_ns` / `clock_offset_last_ns` / `clock_drift_ns` /
`clock_refresh_count`、`callback_buffers_processed` / `callback_records_processed` /
`callbacks_active`、`tid_map_entries` / `tid_map_refresh_count` / `unmapped_tid_records` 和
`stop_complete`。`stop_complete=true` 表示 stop 流程已完成，最终 sidecar 可安全归档。

`CANN_MSPTI_CLOCK=auto|realtime|monotonic` 控制时间戳归一化。CANN 9.0.1 的 msPTI 使用
`CLOCK_REALTIME` 纳秒，而 eBPF 的 `bpf_ktime_get_ns` 使用 `CLOCK_MONOTONIC`；collector
默认 `auto` 将其转换为 monotonic，并保留 `raw_start_ns`/`raw_end_ns` 原始值。
offset 不是常量：collector 启动时记录一次，之后每个 flush 周期重估，NTP 跳变和容器 time
namespace 调整会体现在 `clock_drift_ns`。

## 进程门禁（多进程 LD_PRELOAD）

`LD_PRELOAD` 会被容器里的所有子进程继承。`CannMsptiAutoStart()` 按下面的顺序做门禁，
任一条件不通过就完全不启动（不会创建输出文件或 meta）：

| 变量 | 读取位置 | 语义 |
|---|---|---|
| `CANN_MSPTI_ONLY_COMM` | `/proc/self/comm` | 逗号分隔子串匹配；加载期的 python 进程 comm 通常是 `python3`，不是 Worker 标题 |
| `CANN_MSPTI_ONLY_PARENT_COMM` | `/proc/<getppid()>/comm` | 逗号分隔子串匹配；父进程 comm 读不到时不启动。V4 的 8 个 Worker 父进程均为 `VLLM::EngineCor`（comm 内核截断到 15 字节，正好等于该值），可精确只命中 Worker |
| `CANN_MSPTI_ONLY_CMDLINE` | `/proc/self/cmdline` | 只反映 exec 时的 argv；vLLM 在 Worker 内 `set_process_title()` 之后改写的标题不可见，因此不能用于 Worker 过滤 |

vLLM 的 Worker 由 EngineCore 以 `multiprocessing spawn` 启动，加载期 cmdline 是
`python3 -c "from multiprocessing.spawn import spawn_main; ..."`。所以
`CANN_MSPTI_ONLY_CMDLINE=VLLM::Worker...` 会命中 0 个进程并静默无数据；B1 推荐用
`CANN_MSPTI_ONLY_PARENT_COMM=VLLM::EngineCor` 把采集限制到 8 个 Worker。

## 一次性采集窗口（CANN_MSPTI_DURATION）

`CANN_MSPTI_DURATION` 接受纯秒数（`60`、`1.5`）或 Go duration（`60s`、`1m30s`、`500ms`）。
启动后由后台线程在到期时调用 `cann_mspti_stop()`：输出文件和 meta 正常收尾
（`stop_complete=true`），库仍留在进程里但不再写文件，因此不必为了卸载 collector 再
重启一次服务。`0` 或不设置表示一直采到进程退出；B1 一次窗口建议 `60s`。
`CANN_MSPTI_AUTO_STOP=0` 仍然表示“进程退出时不做 stop”，与 `CANN_MSPTI_DURATION`
到期停止相互独立。

## 独立启动器

```bash
./cann-mspti-collector \
  --output /tmp/device.jsonl --run-id orinfer-smoke --duration 30 \
  --activities api,acl_api,runtime_api,kernel,memcpy,hccl \
  --exec python -m vllm.entrypoints.openai.api_server ...
```

它会 `fork`/`exec` 子进程并注入 `LD_PRELOAD` 与环境变量；`--mspti-lib` 可覆盖
`libmspti.so` 路径，`--collector-lib` 可覆盖 `libcannmspti.so` 路径。

## 合成 ACL 验证

`synthetic_acl` 先在 ACL 初始化前启动 collector，然后循环执行小规模 `aclnnAdd` 和
同步 copy，适合验证 API→KERNEL/MEMCPY 的 correlationId：

```bash
export LD_PRELOAD=/usr/local/Ascend/ascend-toolkit/latest/tools/mspti/lib64/libmspti.so
mkdir -p /tmp/cann-mspti-synthetic
./synthetic_acl --output /tmp/cann-mspti-synthetic/device.jsonl \
  --run-id synthetic-$(date -u +%Y%m%dT%H%M%SZ) --duration 10
```

注意：不要对正在运行的 V4 容器直接 `LD_PRELOAD` 注入（需要重启服务）；`synthetic_acl`
是独立进程，不碰 V4 的 stream/内存。

## 生命周期自测

```bash
cd build
./cann-mspti-selftest
```

覆盖未启动 flush/stop、stop 幂等、8 线程并发 stop、非法 `CANN_MSPTI_CLOCK` 和不可写 output。
并发 stop 的约束：一个线程执行 `FlushAll` + `unsubscribe` + 等待 callback 排空，其余线程等待同一个
停止流程；不会重复关闭 `FILE*`。callback 内的 `msptiActivity*` 只在 callback 期间有效，collector
只把字段复制到 `std::string` 后写文件。

## 宿主侧 pidmap

容器内 `/proc/self/status` 的 `NSpid` 只能看到容器内 pid。宿主侧的 `cann-ebpf attach` 会在采集时
读取 `/proc/<hostpid>/status` 与各线程 status，写出 `pidmap.json`（`cann.pidmap.v1`）。
`diagnose` 优先使用这个快照，因此目标进程退出后仍能 join；不要用 collector meta 里的容器内 pid
直接当宿主 pid。

## 活动类型

| 参数 | 内部 kind | 说明 |
|---|---|---|
| `api` | `MSPTI_ACTIVITY_KIND_API` | 默认；ACL/aclnn host API |
| `acl_api` | `MSPTI_ACTIVITY_KIND_ACL_API` | 某些 CANN 版本单独上报 ACL API 时使用 |
| `runtime_api` | `MSPTI_ACTIVITY_KIND_RUNTIME_API` | 某些版本单独上报 runtime API 时使用 |
| `kernel` | `MSPTI_ACTIVITY_KIND_KERNEL` | device kernel，带 correlationId |
| `memcpy` | `MSPTI_ACTIVITY_KIND_MEMCPY` | device copy，name 为 `MEMCPY_HTOD` 等 |
| `hccl` | `MSPTI_ACTIVITY_KIND_HCCL` + `COMMUNICATION` | HCCL 区间；`Communication` 记录带 correlationId |
| `memory` / `memset` | 对应 kind | 当前 schema 不支持，会记入 ignored |

`msptiActivityHccl` 结构没有 correlationId，collector 会把它写成 `correlation_id: 0`，
`source=mspti_hccl_no_correlation`；此时 join 只能按时间窗回退，不能声称精确关联。
`msptiActivityCommunication` 有 correlationId，优先用于精确 join。

## 边界

- 只采当前进程；msPTI 不能从外部 attach 已经在跑的进程。
- 必须重启目标服务或在容器入口注入；不属于“无侵入动态 attach”。
- 设备时间线不等于逐 request 延迟；batch 仍是窗口归因。
- API 的 `correlationId` 只在同一 device 内唯一；跨卡不能直接合并。
- `msptiActivityHccl` 无 correlationId，时间窗回退必须在报告中标注。
- stop 等待 callback 超过 5 秒时只写 warning，不强行 `fclose`；此时 `callbacks_active` 会大于 0，
  证据目录不能当作完整归档。

## 多进程输出模板（B1）

`CANN_MSPTI_OUTPUT` 支持 `{pid}` 占位符：

```text
CANN_MSPTI_OUTPUT=/cache/cann-ebpf/device-{pid}.jsonl
```

每个继承 `LD_PRELOAD` 的 Worker 会写自己的文件，避免多进程竞争同一路径；
验证后用 `scripts/b1_merge_device.py` 合并成单个 `device.jsonl` + meta。
