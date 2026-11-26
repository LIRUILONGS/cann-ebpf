# M3：device 时间线与 host/device join

日期：2026-11-26
状态：**schema、join 算法、诊断输出、单测、in-process msPTI collector 与合成 ACL 上机验证完成**。
最新 final7：11,859 host spans + 148,466 device spans，慢 Sync 305/305（100%）。真实 V4/Orinfer
生产注入仍需维护窗口；`msprof --pid` 动态采集在本环境不可用（见 `docs/M3-FEASIBILITY.md`）。

## 1. 设计

```text
host span (pid, tid, api, start, end)
  → device API record (pid, tid, api, start, end, correlationId)   # 时间窗匹配
    → KERNEL / MEMCPY / HCCL (device_id, stream_id, start, end, correlationId)
```

- 第一跳用 `(pid, tid) + start±tolerance` 一对一匹配 API record；
- 第二跳用 `(correlationId)` 找同一次调用的设备区间；`correlationId` 只在同一 device 内唯一，
  所以报告按 device 分别统计；
- 没有 API record 时退化为“时间窗重叠”，报告 `time_window_fallback=true`，置信度下降；
- 时钟偏移：用同一批 host span 与 API record 的 start 差值中位数估计，写入 `clock_offset_ns`；
  设备区间的起止先减去该偏移再与 host 求交。
- 相邻区间容差：同一 correlationId 的 KERNEL/MEMCPY/HCCL 与 host span 不重叠、但 gap ≤ `TimeToleranceNS`
  （默认 100µs）时按相邻匹配计入；记录 `gap_ns` / `adjacent=true`，`overlap_ns=0`，不贡献 busy 时间。
- 容器 pid/tid 映射：`attach` 在宿主采集 `pidmap.json`（`cann.pidmap.v1`），`diagnose` 优先使用该快照；
  没有快照时才按 `container_id` 现场扫描 `/proc`。

## 2. device.jsonl schema

Schema `cann.device.v1`，一行一个对象：

```json
{"schema":"cann.device.v1","kind":"API","name":"aclrtSynchronizeStream","pid":1452,"tid":1452,
 "start_ns":10189043659397624,"end_ns":10189043700000000,"duration_ns":40602376,
 "correlation_id":1001,"source":"mspti"}
{"schema":"cann.device.v1","kind":"KERNEL","name":"ffn","device_id":0,"stream_id":1,
 "start_ns":10189043670000000,"end_ns":10189043695000000,"duration_ns":25000000,
 "correlation_id":1001,"source":"mspti"}
```

`kind` 只允许 `API` / `KERNEL` / `MEMCPY` / `HCCL`。缺失字段不补造；`duration_ns` 为 0 时用
`end_ns - start_ns` 归一化。

## 3. join 报告

Schema `cann.device.join.v1`：

| 字段 | 内容 |
|---|---|
| `host_spans` / `device_spans` / `device_kinds` | 输入规模与类型计数 |
| `matched_host_spans` / `match_rate` | 匹配数量与比例 |
| `time_window_fallback` | 是否退化为时间窗 |
| `clock_offset_ns` | 估计的 host/device 时钟偏移 |
| `adjacent_matches` | 相邻区间容差匹配数量（不贡献 busy） |
| `reasons[]` | `ok` / `no_api_record` / `api_name_mismatch` / `duplicate` / `no_device_record` / `time_window` |
| `devices[]` | 每设备 busy 并集、kernel/memcpy/HCCL 重叠时长 |
| `verdicts[]` | `device_bound` / `launch_starvation` / `copy_bound` / `communication_bound` 的计数 |
| `hosts[]` | 每个匹配 host span 的 busy%、三类设备时长、correlationId；`matches[]` 可带 `gap_ns` / `adjacent` |

阈值（可由 diagnose 覆盖）：

- busy% ≥ 0.8 → `device_bound`；
- busy% ≤ 0.2 → `launch_starvation`；
- memcpy 重叠 / duration ≥ 0.5 → `copy_bound`；
- HCCL 重叠 / duration ≥ 0.5 → `communication_bound`；
- 只对 duration ≥ 慢阈值（默认 1ms）的 span 计数。

busy 用区间并集，避免同一 host 调用内多个重叠 kernel 被重复累加。

## 4. diagnose 接线

`diagnose` 会自动读取 run 目录下的 `device.jsonl`：

```json
"device": { "matched_host_spans": 7794, "match_rate": 1.0, "clock_offset_ns": 0,
            "time_window_fallback": false, "verdicts": [{"verdict":"device_bound","count":5721}] }
```

- 有设备证据时才输出 device 类 verdict，并把 host span_id 作为 evidence；
- 匹配率 < 90% 写入 `unknowns`：未匹配 host span 不能用于设备侧结论；
- fallback 模式写入 `unknowns`：时间窗不是精确 correlationId 关联；
- 没有 `device.jsonl` 时继续输出“无法回答设备侧执行时间”，不伪造。
- Markdown 增加“设备侧时间线（M3 join）”章节：匹配率、reason 分布、每设备 busy、top host。

CLI 新增 `--device-tolerance`（默认 100µs）。

## 5. host 侧证据接入

外部 collector 在容器内写 `device.jsonl` / `device.jsonl.meta.json` 后，用 attach 导入：

```bash
./cann-ebpf-linux-arm64-m3 attach --pid auto --profile vllm-ascend --profiles-dir profiles \
  --duration 30s --device-jsonl /host/path/to/device.jsonl --output runs/ebpf-m3
```

行为：

- 把 `device.jsonl` 复制到 run 目录，同名 `.meta.json` 复制为 `device.jsonl.meta.json`；
- 在目标进程仍存活时抓 `pidmap.json`（容器 local → host pid/tid），用于 diagnose 翻译；
- 三个文件分别封存为 manifest artifact：`device_jsonl`、`device_meta`、`pidmap`；
- 已存在同名文件时不覆盖，只告警；源文件缺失不阻断 host span 采集；
- 之后 `diagnose --input runs/ebpf-m3` 会自动读取三者并做 join；无 `pidmap.json` 时可用
  `diagnose --device-container-id <id>` 在线解析。

注意：`--device-jsonl` 必须是 host 可见路径（容器内路径请先 `docker cp`）；msPTI collector 需要
重启服务或入口注入，不能对正在运行的 V4 动态 attach。

## 5.1 已验证内容（2026-11-26）

| 测试 | 结果 |
|---|---|
| 单测：API+correlationId、名称不匹配、duplicate、no_device_record、时钟偏移、时间窗 fallback | 通过 |
| 服务器：in-process msPTI collector + 合成 ACL（final7） | 11,859 host spans / 148,466 device spans；慢 Sync 305/305（100%）；`pid_map_entries=27`，进程退出后仍可 join |
| 服务器：修复前 final2 | 4,171 host spans / 65,375 device spans；慢 Sync 9/11（81.8%），两个 kernel 分别晚 1,677ns / 10,387ns |
| 服务器：只给 KERNEL 不给 API，验证 fallback | 200 条 kernel 命中 215 host span，`time_window_fallback=true`，匹配率 2.8% 并写入 unknown |
| 单测 + 服务器 | Markdown/JSON 均输出设备章节；没有 device.jsonl 时明确 unknown |

**注意**：final7 是合成 ACL 的真实 msPTI 设备时间线（不是 V4 生产流量）。它证明 collector、correlationId、
时钟归一化、pid/tid 映射和 join 路径可用，但不能当作 V4/Orinfer 的性能结论。

## 6. 生产端（collector 已实现，待维护窗口注入）

容器内 in-process collector 已完成，产物和启用方式见 `docs/M3-COLLECTOR.md`：

- `libcannmspti.so` 在目标进程内订阅 API/KERNEL/MEMCPY/HCCL，输出 `cann.device.v1`；
- `attach` 采集 `pidmap.json`，`diagnose` 直接读取 `device.jsonl` + `pidmap.json`；
- 需要重启 V4/Orinfer 或容器入口 wrapper，只能在维护窗口执行；
- 与 vLLM 插件分开：它只做设备时间线，不做 request/batch 上下文。

备选：若维护窗口不可得，只能保留硬件层（`docs/HARDWARE.md`）的 run 级判断，并明确写
“没有设备时间线，不能下 device_bound 结论”。

## 7. 边界

- `correlationId` 只在同一 device 内唯一；跨卡不能直接合并。
- 一个 API record 对多个 kernel 时，busy% 用并集，kernel 时长求和仍可能大于 busy。
- 设备时间线不能拆成单 request；batch 仍是窗口归因。
- 没有设备数据时，`diagnose` 不会输出 device verdict；这是硬约束，不是缺省值。
