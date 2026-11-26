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
`CANN_MSPTI_DURATION=0` 表示一直采到进程退出；维护窗口建议设短窗口（如 10–30 秒）。
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
