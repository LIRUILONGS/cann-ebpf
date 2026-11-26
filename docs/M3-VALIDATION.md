# M3 设备时间线上机验证记录

日期：2026-11-26  
主机：`bms-84865758`（`36.137.90.82`）  
容器：`deepseek-v4-flash-8905`（CANN 9.0.1）  
目标：验证 in-process msPTI collector、容器 pid/tid 映射、host span + device.jsonl join，以及慢 Sync 匹配率。

## 1. 结论

- `libcannmspti.so`、`cann-mspti-collector`、`cann-mspti-selftest`、`synthetic_acl` 在容器内编译通过。
- 合成 ACL 采集到 `API`、`KERNEL`、`MEMCPY`，`correlationId` 能把 API 与设备区间关联起来。
- A1 join 修复后，final7 原始证据复跑：`matchable_host_spans=305`、`matched_matchable_host_spans=305`、**可匹配设备调用匹配率 100%**；`slow_sync_spans=305`、`matched_slow_sync_spans=305`、**慢 Sync 匹配率 100%**；`api_name_mismatch=0`。
- A2 collector 加固后，8 线程并发 stop、未启动/重复 stop、非法配置自测通过；50ms flush 压力采集 32,809 条记录，JSONL 全部可解析，`records_skipped=0`、`callbacks_active=0`、`stop_complete=true`。
- `pidmap.json` 由宿主 `attach` 采集时生成，合成进程退出后仍能完成 27 条 pid/tid 映射并 join。
- 采集期间 V4 `/health` 为 200，`vllm-ds4-8905.service` 为 `active`；未重启、未注入运行中的 V4。

## 2. 运行环境

| 项 | 值 |
|---|---|
| 宿主 | `bms-84865758` |
| 容器 | `deepseek-v4-flash-8905` |
| CANN | `9.0.1` |
| 宿主 eBPF profile | `cann-generic` |
| 合成工作负载 | `synthetic_acl`，每轮 `aclnnAdd` + `aclrtSynchronizeStream` + D2H copy |
| collector 路径（容器） | `/tmp/cann-mspti-src-a2/build`（A2 验证）；final7 原路径为 `/tmp/cann-mspti-src/m3/mspti_collector/build` |
| 证据目录 | `/root/cann-ebpf/m3-test-20261126/synthetic-join-final7/ebpf` |

## 3. 关键结果

### 3.1 final7（A1 修复后，最新验收）

```text
host_spans                 11859
device_spans               148466
device_kinds               API=127862,KERNEL=4112,MEMCPY=16492
pid_map_applied            true
pid_map_entries            27
matchable_host_spans       305
unmatchable_host_spans     11554
matched_host_spans         305
matched_matchable_host_spans 305
match_rate                 100%
all_host_match_rate        2.57%
slow_sync_spans            305
matched_slow_sync_spans    305
slow_sync_match_rate       100%
adjacent_matches           16
clock_offset_ns            -18359
```

reason 分布：

```text
not_applicable  11554
  examples: aclrtGetDevice=11536, aclrtMalloc=18
ok              305
  examples: aclrtSynchronizeStream=305
```

设备汇总：

```text
device 0
spans       305
busy_ns     405,611,799
kernel_ns   395,802,549
memcpy_ns   0
hccl_ns     0
```

### 3.2 旧报告的 44.9% 和 5,320 条 `api_name_mismatch` 是 join 缺陷

旧 join 在同线程内按“最近 start 时间”选 API record，不校验函数名。final7 的 host span 中 11,536 条是
`aclrtGetDevice`（97.3%），被错误地配到 `DevMalloc`、`aclrtMemcpy`、`aclnnAdd` 等无关 API record，
产生 5,320 条假的 `api_name_mismatch`；`aclrtMalloc` 有 API record 但没有对应设备区间，又落入
`no_device_record`。因此旧的 `matched_host_spans=5,325` / `match_rate=44.9%` 不能作为 join 质量结论。

A1 修复后：

- API 名称先做别名归一，只接受与 host function 同名的 API record；
- `aclrtGetDevice` 属于 `handle` 类，`aclrtMalloc` 没有设备执行区间，均归为 `not_applicable`，不计入可匹配分母；
- `match_rate` 的分母改为“可匹配设备调用”，final7 为 305 条 `aclrtSynchronizeStream`，305/305=100%；
- `all_host_match_rate` 保留为 305/11,859=2.57%，只用于观察 host 调用构成，不作为验收口径；
- `api_name_mismatch` 现在只在同线程存在最近记录但同名候选缺失时出现，final7 为 0。

### 3.3 慢 Sync 修复前后

| 版本 | host_spans | device_spans | 慢 Sync | 匹配率 |
|---|---:|---:|---:|---:|
| final2（修复前） | 4,171 | 65,375 | 9/11 | 81.8% |
| final7（A1 修复后） | 11,859 | 148,466 | 305/305 | 100% |

final2 未匹配的两条是 correlationId 12524 / 12728：KERNEL 起始时间分别比 host span 结束晚 1,677ns 和 10,387ns。
现有 join 增加“相邻区间容差”：同一 correlationId 的 KERNEL/MEMCPY/HCCL 在 `TimeToleranceNS`（默认 100µs）内允许 `overlap=0` 的相邻匹配，
记录 `gap_ns` 和 `adjacent=true`，但不计入 busy 时间。这样既保留证据，又不把相邻区间伪造成重叠执行。

### 3.4 meta 运行中刷新

final3 运行中复制 `device.jsonl.meta.json`：

```text
约 7 秒:  records_written=66,527  output_bytes=22,296,435
约 15 秒: records_written=101,297 output_bytes=33,966,719
```

停止后 meta 写最终值：`records_written=147,174`、`output_bytes=49,378,361`（final5 样例）。

### 3.5 pid/tid 映射

final7 的 `pidmap.json`：

```text
container_id: 108b4917711c662c38a453acb408c8ae18d46df50fcf0f126bac5f67d9ef481f
pid_map:  {"42813": 3845025}
tid_map:  {"42813": 3845025, ..., "42840": 3845056}
```

`pid_map_entries=27`，diagnose 在 `synthetic_acl` 进程退出后仍完成 join。原因：collector 所在容器内
`/proc/self/status` 的 `NSpid` 只能看到容器内 pid，宿主真实 pid 由 `attach` 从宿主 `/proc/<pid>/status` 采集并写成
`pidmap.json`，不能由容器内 collector 自报。

### 3.6 A2 collector 加固验证

容器内重新构建（`BUILD_SELFTEST=ON`）后执行：

```bash
./cann-mspti-selftest
./cann-mspti-selftest   # 带 LD_PRELOAD=libmspti.so 再跑一次
```

结果：

```text
ok  is_active before start
ok  flush before start
ok  stop before start
ok  stop is idempotent
ok  invalid clock is rejected
ok  concurrent stop returns 0
ok  is_active after concurrent stop
ok  unwritable output is rejected
all selftests passed
```

50ms flush 压力采集（`synthetic_acl --duration 10`）：

```text
records_written            32809
records_skipped            0
callback_buffers_processed 188
callback_records_processed 32809
callbacks_active           0
clock_refresh_count        229
clock_drift_ns             -10
tid_map_entries            26
tid_map_refresh_count      228
unmapped_tid_records       0
stop_complete              true
warnings                   ""
```

本地逐行 `json.loads` 校验：32,809 行全部为合法 `cann.device.v1` JSON，0 行损坏；
`API=28,259`、`MEMCPY=3,640`、`KERNEL=910`，与 `records_written` 一致。

## 4. 验证步骤（可复现）

```bash
# 1) 容器内构建 + 生命周期自测
docker exec deepseek-v4-flash-8905 bash -lc '
  source /usr/local/Ascend/ascend-toolkit/set_env.sh
  cd /tmp/cann-mspti-src-a2 && rm -rf build && mkdir build && cd build
  cmake .. -DCMAKE_BUILD_TYPE=Release -DBUILD_SELFTEST=ON && make -j4
  ./cann-mspti-selftest
'

# 2) 启动合成 workload（A2 压力：50ms flush）
docker exec -d deepseek-v4-flash-8905 bash -lc '
  cd /tmp/cann-mspti-src-a2/build
  mkdir -p /tmp/cann-mspti-a2-stress
  export LD_PRELOAD=/usr/local/Ascend/cann-9.0.1/tools/mspti/lib64/libmspti.so
  export CANN_MSPTI_ACTIVITIES=api,acl_api,runtime_api,kernel,memcpy,hccl
  export CANN_MSPTI_FLUSH_INTERVAL_MS=50
  ./synthetic_acl --output /tmp/cann-mspti-a2-stress/device.jsonl \
    --run-id a2-stress-20261126 --duration 10
'

# 3) 宿主 attach（pid 用 ps 找 comm=synthetic_acl 的真实宿主 pid）
cann-ebpf attach --pid <host_pid> --profile cann-generic --profiles-dir profiles \
  --duration 30s --output <evidence_dir>/ebpf --blocking off --hardware off --no-context

# 4) 复制 device.jsonl / meta，运行 diagnose
cann-ebpf diagnose --input <evidence_dir>/ebpf --json --out <evidence_dir>/ebpf/diagnose.json
```

## 5. 边界与未验证项

- 本轮只验证合成 ACL；没有把 collector 注入正在运行的 V4，也没有真实 V4 设备时间线。
- A2 的并发/压力结论来自合成 ACL（10 秒、50ms flush）；真实 V4/Orinfer 的 callback 并发与长窗口开销仍需维护窗口验证。
- 没有真实 HCCL 工作负载，不能声称 HCCL correlationId 已验证；`msptiActivityHccl` 无 correlationId，只有 `Communication` 有。
- 合成 ACL 的 `device_bound` / `launch_starvation` 是 join 路径产物，不是生产性能结论。
- 切换到 Orinfer 后需要在授权窗口重新采真实 workload，重复本验证的慢 Sync 与设备 busy 统计。
