# M3 设备时间线上机验证记录

日期：2026-11-26  
主机：`bms-84865758`（`36.137.90.82`）  
容器：`deepseek-v4-flash-8905`（CANN 9.0.1）  
目标：验证 in-process msPTI collector、容器 pid/tid 映射、host span + device.jsonl join，以及慢 Sync 匹配率。

## 1. 结论

- `libcannmspti.so`、`cann-mspti-collector`、`synthetic_acl` 在容器内编译通过。
- 合成 ACL 采集到 `API`、`KERNEL`、`MEMCPY`，`correlationId` 能把 API 与设备区间关联起来。
- 最新一轮 final7：`host_spans=11859`、`device_spans=148466`、`slow_sync_spans=305`、`matched_slow_sync_spans=305`、**慢 Sync 匹配率 100%**。
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
| collector 路径（容器） | `/tmp/cann-mspti-src/m3/mspti_collector/build` |
| 证据目录 | `/root/cann-ebpf/m3-test-20261126/synthetic-join-final7/ebpf` |

## 3. 关键结果

### 3.1 final7（最新验收）

```text
host_spans                 11859
device_spans               148466
device_kinds               API=127862,KERNEL=4112,MEMCPY=16492
pid_map_applied            true
pid_map_entries            27
matched_host_spans         5325
match_rate                 44.9%
slow_sync_spans            305
matched_slow_sync_spans    305
slow_sync_match_rate       100%
adjacent_matches           2498
clock_offset_ns            761
```

reason 分布：

```text
no_device_record   6534
api_name_mismatch  5320
ok                 5
```

设备汇总：

```text
device 0
spans       5325
busy_ns     660,471,665
kernel_ns   401,183,715
memcpy_ns   8,990,760
hccl_ns     0
```

### 3.2 总匹配率为什么是 44.9%

本轮的 host span 包含大量 `aclrtGetDevice` 等没有对应 device 区间的调用，`no_device_record=6,534`。
总匹配率按全部 host span 做分母，因此不能拿 44.9% 判断 join 失效。验收指标是慢 Sync：305/305，100%。

`api_name_mismatch=5,320` 是 host 调用名与最近 msPTI API 记录名不一致的数量；这些 span 仍按时间/线程匹配到 API 记录，
但不能当作精确函数级归因，报告中保留 reason 供人工审查。

### 3.3 慢 Sync 修复前后

| 版本 | host_spans | device_spans | 慢 Sync | 匹配率 |
|---|---:|---:|---:|---:|
| final2（修复前） | 4,171 | 65,375 | 9/11 | 81.8% |
| final7（修复后） | 11,859 | 148,466 | 305/305 | 100% |

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

## 4. 验证步骤（可复现）

```bash
# 1) 容器内构建
docker exec deepseek-v4-flash-8905 bash -lc '
  source /usr/local/Ascend/ascend-toolkit/set_env.sh
  cd /tmp/cann-mspti-src/m3/mspti_collector && rm -rf build && mkdir build && cd build
  cmake .. -DCMAKE_BUILD_TYPE=Release && make -j4
'

# 2) 启动合成 workload
docker exec -d deepseek-v4-flash-8905 bash -lc '
  export LD_PRELOAD=/usr/local/Ascend/ascend-toolkit/latest/tools/mspti/lib64/libmspti.so
  export CANN_MSPTI_ACTIVITIES=api,acl_api,runtime_api,kernel,memcpy,hccl
  ./synthetic_acl --output /tmp/cann-mspti-final7/device.jsonl \
    --run-id join-final7-20261126T113000Z --duration 45
'

# 3) 宿主 attach（pid 用 ps 找 comm=synthetic_acl 的真实宿主 pid）
cann-ebpf attach --pid <host_pid> --profile cann-generic --profiles-dir profiles \
  --duration 30s --output <evidence_dir>/ebpf --blocking off --hardware off --no-context

# 4) 复制 device.jsonl / meta，运行 diagnose
cann-ebpf diagnose --input <evidence_dir>/ebpf --json --out <evidence_dir>/ebpf/diagnose.json
```

## 5. 边界与未验证项

- 本轮只验证合成 ACL；没有把 collector 注入正在运行的 V4，也没有真实 V4 设备时间线。
- 没有真实 HCCL 工作负载，不能声称 HCCL correlationId 已验证；`msptiActivityHccl` 无 correlationId，只有 `Communication` 有。
- 合成 ACL 的 `device_bound` / `launch_starvation` 是 join 路径产物，不是生产性能结论。
- 切换到 Orinfer 后需要在授权窗口重新采真实 workload，重复本验证的慢 Sync 与设备 busy 统计。
