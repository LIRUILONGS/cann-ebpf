# E 层：DCMI / npu-smi 硬件指标

日期：2026-11-28
状态：代码 + 单测完成；npu-smi 链路已在 target-host 验证，DCMI/HCCS 已在 910B2 测试服务器完成只读实测（详见 docs/DCMI-HCCS-METRICS.md）。

## 1. 定位

硬件层是**粗粒度**的补充，不替代 msPTI：

- 能回答：HBM 余量、AICore 利用率、功耗、温度、设备健康；
- 不能回答：某个 kernel/memcpy/HCCL 的执行时间、correlationId、单 request 延迟。

`hardware.jsonl` 的时间戳是 wall clock，host span 是 monotonic；两者**不做逐调用 join**，
只做 run 级统计。设备侧逐调用时间线见 `docs/M3-DEVICE-JOIN.md`。

## 2. 数据源与 schema

当前实现两条后端：`npu-smi info`（host 与容器内都可用）和独立 C helper `dcmi_probe`（host 上直读 DCMI；基础模式默认，`--extended` 打开频率/温度/ECC/电压/PCIe/health error 扩展）。DCMI/HCCS 的指标、单位、unknown 规则与实测证据见 [DCMI-HCCS-METRICS.md](DCMI-HCCS-METRICS.md)。

```json
{"schema":"cann.hardware.v1","ts_ns":1795681823289280259,"device_id":0,"name":"910B2",
 "health":"OK","aicore_pct":42,"memory_used_mb":0,"memory_total_mb":0,
 "hbm_used_mb":61404,"hbm_total_mb":65536,"hbm_free_mb":4132,"power_w":216,"temp_c":43,
 "source":"npu-smi"}
```

规则：

- 缺失指标不写字段（指针语义），`0` 是有效测量值；
- 解析器只接受合法 `Health`（OK/Warning/Alarm/Critical/Unknown）的 NPU 行，并且拒绝
  `npu-smi info` 末尾“进程表”里的行（曾把进程表误解析成第 9 个设备，已修复并加测试夹具）；
- 解析失败/命令失败只产生 warning，不影响 host span 采集。

## 3. 接线

| 位置 | 变化 |
|---|---|
| `internal/hardware/types.go` | schema、指针字段、HBM free 计算 |
| `internal/hardware/npusmi.go` | `npu-smi info` 两行一设备的解析 |
| `internal/hardware/npusmi_hccs.go` | `npu-smi info -t hccs` 按实际 lane 数量的解析 |
| `internal/metrics/npusmi_hccs.go` | HCCS 逐卡被动采样（30s 默认，失败仅 warning） |
| `m3/dcmi_probe/dcmi_probe.c` | DCMI 直读 helper（基础模式默认，`--extended` 扩展标量） |
| `internal/hardware/collector.go` | 周期采样、JSONL 写入、失败降级 |
| `internal/probes/hardware.go` | `--hardware auto\|on\|off` 与 profile 合并 |
| `internal/probes/run.go` | 在 uprobe attach 前启动、采集结束停止、manifest artifact |
| `cmd/cann-ebpf/attach.go` | `--hardware` 参数与输出 |
| `internal/diagnose` | 硬件汇总与 `hbm_pressure` 判定 |

采样间隔取 `profile.hardware.interval_s`（默认 10 秒），CLI `--hardware-interval` 可覆盖。
单次 `npu-smi info` 在本机约 **1.14 秒**，`usages -i N`/`memory -i N` 单设备也要 0.5–0.65 秒，
所以不建议低于 2 秒；低于 2 秒会写 warning。每次采样有 5 秒超时，避免 npu-smi 卡住采集协程。

10 秒窗口通常得到 2 个采样点；硬件采样在 `capture_stop` 时停止，不会把清理阶段算进窗口。

`--hardware`：

- `auto`（默认）：按 profile `hardware.enabled`（`base.yaml` 为 `auto` → 开启）；
- `on`：强制开启；
- `off`：不启动、不产出 `hardware.jsonl`。

## 4. diagnose 输出

JSON 新增：

```json
"hardware": {
  "available": true, "source": "npu-smi", "samples": 16,
  "devices": [{"device_id":0,"name":"910B2","samples":2,"aicore_avg_pct":34,
               "aicore_max_pct":42,"hbm_free_min_mb":4132,"hbm_free_min_pct":6.3,
               "power_avg_w":172.9,"temp_max_c":43}],
  "hbm_free_min_pct": 6.3, "aicore_max_pct": 46
}
```

Markdown 增加“硬件层（DCMI/npu-smi，粗粒度）”表格，并明确写“不能和 monotonic host span 逐调用 join”。

判定：`hbm_pressure` 只在

- 最低 HBM free% < `--hbm-free-pct`（默认 5），且
- 存在 `category=memory` 且超过慢阈值的 span

时输出，confidence 0.6，并附证据 span。没有设备时间线时仍不会输出 `device_bound` 等结论。

## 5. 上机验证（2026-11-26）

| 测试 | 结果 |
|---|---|
| `attach --blocking on --hardware on --duration 10s` | 7,794 spans；hardware 2 采样 × 8 设备；manifest `artifacts[hardware_samples]` bytes 4202 |
| `attach --blocking off --hardware on --duration 5s` | 7,001 spans；hardware 正常，blocking 关闭 |
| `attach --hardware off` | 无 `hardware.jsonl`，`hardware.available=false` |
| health/service | 200→200 / active→active（全部窗口） |
| diagnose | 显示 8 设备表：HBM free 6.3%、AICore 峰值 46%、功耗/温度；无 `hbm_pressure`（6.3% > 5%） |

证据目录：`/opt/cann-ebpf/m2-test-20261126T151851Z/runs/m3-hw-*/`。

## 6. 边界

- 采样是周期性快照，短窗口只有 2 个点，不能代表峰值；需要更细粒度时把 `interval_s` 降到 1–2 秒，
  但要先确认 npu-smi 调用开销。
- `npu-smi` 的 AICore% 是整卡利用率，不是某个 stream/kernel。
- HBM free% 以整卡为准；多进程共享时不能归因到单个 Worker。
- DCMI 后端通过独立 C helper 实现（`m3/dcmi_probe/dcmi_probe.c`），Go 侧用 `--metrics-dcmi-ext` 接入；`sample` 侧对应 `--dcmi-ext`。
- HCCS 通过 `sample --hccs` / `attach --metrics-hccs` 接入，只读 `npu-smi info -t hccs -i N -c 0`；不执行 hccs-bw。
