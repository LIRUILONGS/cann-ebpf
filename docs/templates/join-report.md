# cann-ebpf msPTI join 报告

## 1. 接入方式

| 项 | 值 |
|---|---|
| msPTI mode | `{{mode}}` |
| 库路径 | `{{library_path}}` |
| activity | `{{activities}}` |
| 是否需要重启 | `{{restart_required}}` |
| 采集窗口 | `{{start}}`–`{{end}}` |

## 2. 对齐率

| API | host span | msPTI API record | 匹配 | 匹配率 | 备注 |
|---|---:|---:|---:|---:|---|
| `{{api}}` | `{{host_n}}` | `{{mspti_n}}` | `{{matched}}` | `{{pct}}` | `{{note}}` |

总体：`{{matched}} / {{total}} = {{pct}}`

## 3. 未匹配原因

| 原因 | 数量 | 说明 |
|---|---:|---|
| `no_record` | `{{n}}` | msPTI 没记录 |
| `time_skew` | `{{n}}` | 超出时间窗 |
| `api_name_mismatch` | `{{n}}` | 名称不一致 |
| `duplicate` | `{{n}}` | 同一 record 被多次使用 |
| `mspti_off` | `{{n}}` | msPTI 未开启 |

## 4. 时钟校准

| 项 | 值 |
|---|---|
| host offset | `{{host_offset_ns}}` ns |
| device offset | `{{device_offset_ns}}` ns |
| 中位数偏差 | `{{median_skew_ns}}` ns |
| 告警阈值 | `{{threshold_ns}}` ns |
| 是否超阈值 | `{{over_threshold}}` |

## 5. 设备时间线

| device | kernel ns | memcpy ns | HCCL ns | busy % | 备注 |
|---|---:|---:|---:|---:|---|
| `{{dev}}` | `{{kernel_ns}}` | `{{memcpy_ns}}` | `{{hccl_ns}}` | `{{busy_pct}}` | `{{note}}` |

## 6. 结论

- 可用性：`{{join_verdict}}`
- 置信度：`{{confidence}}`
- 不能写成精确 join 的情况：`{{caveats}}`
