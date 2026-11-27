# M3 设备时间线采集器（msPTI in-process）

日期：2026-11-26  
状态：**A2 加固完成：容器内编译、生命周期自测、并发 stop、50ms flush 压力采集均通过；合成 ACL 上机验证通过。V4/Orinfer 生产进程尚未注入，需要维护窗口。**

## 1. 解决什么问题

host eBPF span 只能说明“哪个线程在哪个 CANN API 上等了多久”，不能说明“设备侧 kernel/memcpy 实际执行了多久”。
`m3/mspti_collector` 在目标进程内部订阅 msPTI，输出统一的 `cann.device.v1` JSONL，供 `cann-ebpf diagnose`
把 host span 与 device 区间按 `correlationId` 关联。

它不是无侵入动态 attach：msPTI 在同一进程只允许一个 subscriber，且必须在 ACL/torch_npu 初始化前订阅。
生产启用只能通过重启服务、容器入口 wrapper 或 `LD_PRELOAD` 启动时注入。

## 2. 组成

| 文件/产物 | 作用 |
|---|---|
| `collector.h` / `collector.cpp` | `libcannmspti.so`，msPTI 订阅、记录归一化、JSONL 与 meta sidecar |
| `cli.cpp` | `cann-mspti-collector` 独立启动器：fork/exec 目标命令并注入环境 |
| `selftest.cpp` | `cann-mspti-selftest` 生命周期自测：stop 幂等、并发 stop、非法配置 |
| `synthetic_acl.cpp` | `synthetic_acl` 独立 ACL 工作负载，用于验证 API/KERNEL/MEMCPY 和 correlationId |
| `CMakeLists.txt` | 容器内构建，输出上述产物（默认构建 selftest） |

## 3. 输出的证据

### 3.1 `device.jsonl`

一行一个 `cann.device.v1` 对象：

```json
{"schema":"cann.device.v1","run_id":"...","kind":"API","name":"aclrtSynchronizeStream","pid":1452,"tid":1452,"start_ns":...,"end_ns":...,"duration_ns":...,"correlation_id":12524,"source":"mspti"}
{"schema":"cann.device.v1","run_id":"...","kind":"KERNEL","name":"Add_...","device_id":0,"stream_id":...,"start_ns":...,"end_ns":...,"duration_ns":...,"correlation_id":12524,"source":"mspti"}
```

- `kind`：`API` / `KERNEL` / `MEMCPY` / `HCCL`；
- CANN 9.0.1 的 msPTI 时间戳是 `CLOCK_REALTIME` 纳秒；collector 默认 `CANN_MSPTI_CLOCK=auto` 转为 monotonic，并保留 `raw_start_ns` / `raw_end_ns`；
- `correlation_id` 只在同一 device 内唯一，跨卡不能直接合并。

### 3.2 `device.jsonl.meta.json`

记录 `schema`、版本、容器内 `pid`、`container_id`、`run_id`、活动类型、时钟模式、起止时间、
`records_written`、`records_ignored`、`records_skipped`、`output_bytes`、`max_bytes`、`truncated`、`warnings`。

A2 新增的可观测字段：

| 字段 | 含义 |
|---|---|
| `clock_offset_start_ns` / `clock_offset_last_ns` | 启动时与最近一次重估的 `CLOCK_REALTIME - CLOCK_MONOTONIC` 偏移 |
| `clock_drift_ns` | 最近偏移减启动偏移；超过 ±100ms 时停止阶段写入 warning |
| `clock_refresh_count` / `clock_last_refresh_unix_ns` | 时钟重估次数与最近重估时间 |
| `callback_buffers_processed` / `callback_records_processed` | 已处理的 msPTI 缓冲与记录数 |
| `callbacks_active` | 停止写入 meta 时仍在执行的 callback 数；正常应为 0 |
| `tid_map_entries` / `tid_map_refresh_count` | 容器 local tid → 宿主 tid 映射条目数及刷新次数 |
| `unmapped_tid_records` | 查不到 `NSpid` 的记录数；正常应为 0，非 0 说明 tid 映射不完整 |
| `stop_complete` | 最终 sidecar 是否已完成 stop；最终写入应为 `true` |

运行期间每 1 秒 flush 时同步刷新 meta；复制运行中的 sidecar 可以看到当前记录数，而不是只有启动时的 0。停止时写最终值。

### 3.2.1 生命周期与并发约束（A2）

- **回调 buffer 生命周期**：`UserBufferComplete` 只在 callback 内解析 `msptiActivity*`；每条记录先复制到
  `std::string` 再写文件，不跨 callback 保存 msPTI 指针。buffer 由 RAII guard 释放一次。
- **stop 状态机**：`stop_in_progress` / `stop_complete` 两个状态配合条件变量；重复调用 stop 会等待同一个
  停止流程结束，不会重复 `msptiUnsubscribe` 或二次 `fclose`。
- **callback 排空**：stop 先 `FlushAll` + `unsubscribe`，再等待 `callbacks_active == 0`（最多 5 秒）。
  超时只写 warning，不强行关闭 `FILE*`，避免 use-after-free。
- **时钟重估**：启动时记录一次 offset，flush 线程每次 tick 重估；记录文件使用最新 offset，并在 meta 中
  保留 drift。NTP 跳变或容器 time namespace 调整不会被当作固定常量。
- **tid 热刷新**：启动时扫描一次 `/proc/self/task/*/status`，之后每个 flush 周期刷新；新线程不需要重启
  collector 就能补映射。查不到映射时不缓存 fallback，下一条记录会重试。
- **JSON 转义**：`run_id`、`kind`、`name`、`source`、`container_id`、`activities`、`clock`、`warnings`
  全部走同一套转义；控制字符输出为 `\u00xx`，不写业务 payload。

### 3.2.2 自测

`cann-mspti-selftest` 覆盖未启动 flush/stop、stop 幂等、8 线程并发 stop、非法 clock、不可写 output。
构建时默认生成（`-DBUILD_SELFTEST=OFF` 可关闭）：

```bash
cd build
./cann-mspti-selftest
```

容器内实测：无 `LD_PRELOAD` 与带 `libmspti.so` 两种场景均通过；8 个并发 stop 全部返回 0，停止后
`is_active=0`。

本次 A2 构建产物已封存为 `dist/cann-mspti-a2-20261126/`，服务器副本在
`/opt/cann-ebpf/cann-mspti-a2-20261126/`；`SHA256SUMS.txt` 覆盖 `libcannmspti.so`、
`cann-mspti-selftest` 和 `synthetic_acl`。

### 3.3 `pidmap.json`（宿主侧）

`attach` 在采集时从宿主 `/proc/<hostpid>/status` 的 `NSpid` 读取容器内 pid/tid → 宿主 pid/tid 映射：

```json
{
  "schema": "cann.pidmap.v1",
  "container_id": "108b...",
  "host_pids": [3758592],
  "pid_map": {"41769": 3758592},
  "tid_map": {"41769": 3758592, "41770": 3758593}
}
```

`diagnose` 优先使用该快照，其次才在目标进程仍存活时按 `container_id` 扫描 `/proc`。快照是让证据在进程退出后仍可复现 join 的关键。

## 4. 环境变量

| 变量 | 默认 | 说明 |
|---|---|---|
| `CANN_MSPTI_OUTPUT` | 无，必填 | `device.jsonl` 路径 |
| `CANN_MSPTI_RUN_ID` | 空 | 写入每条记录和 meta 的 run id |
| `CANN_MSPTI_ACTIVITIES` | `api,acl_api,runtime_api,kernel,memcpy,hccl` | 活动类型，逗号分隔 |
| `CANN_MSPTI_DURATION` | 0 | 秒；0 表示直到进程退出 |
| `CANN_MSPTI_MAX_BYTES` | 512 MiB | 超过后丢弃新记录并置 `truncated=true` |
| `CANN_MSPTI_CLOCK` | `auto` | `auto` / `realtime` / `monotonic` |
| `CANN_MSPTI_FLUSH_INTERVAL_MS` | 1000 | 周期 flush 与 meta 刷新间隔 |

## 5. 生产启用（维护窗口）

```bash
export MSPTI=/usr/local/Ascend/ascend-toolkit/latest/tools/mspti/lib64/libmspti.so
export CANN_MSPTI=/opt/cann-ebpf/libcannmspti.so
export LD_PRELOAD="$MSPTI:$CANN_MSPTI"
export CANN_MSPTI_OUTPUT=/var/log/cann-ebpf/device.jsonl
export CANN_MSPTI_RUN_ID=orinfer-20261126T120000Z
export CANN_MSPTI_ACTIVITIES=api,acl_api,runtime_api,kernel,memcpy,hccl
export CANN_MSPTI_DURATION=30
```

`libmspti.so` 必须排在 `libcannmspti.so` 前面。不要在运行中的 V4 容器里直接 `LD_PRELOAD`；需要重启目标服务或由容器入口 wrapper 注入。

独立启动器示例：

```bash
./cann-mspti-collector \
  --output /tmp/device.jsonl --run-id orinfer-smoke --duration 30 \
  --activities api,acl_api,runtime_api,kernel,memcpy,hccl \
  --exec python -m vllm.entrypoints.openai.api_server ...
```

## 6. 约束

- 只采当前进程；不能从外部 attach 已在运行的进程。
- `msptiActivityHccl` 没有 `correlationId`，只能写 `correlation_id: 0`；`msptiActivityCommunication` 有 `correlationId`，但本轮没有真实 HCCL 工作负载验证。
- 当前 schema 不接受 `memory` / `memset` 记录；开启会在 collector 内记入 `records_ignored`，不会伪造进 device.jsonl。
- 设备时间线不是逐 request 延迟；batch 仍只能做窗口归因。
- 采集窗口越长、活动越多，磁盘和内存缓冲开销越高；生产建议 10–30 秒窗口并设置 `CANN_MSPTI_MAX_BYTES`。
