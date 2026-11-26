# A7：硬件采样间隔与 DCMI 评估

日期：2026-11-26
状态：`npu-smi` 短间隔评估完成；DCMI 后端未接入（当前 npu-smi 已通过 DCMI 驱动读取）。

## 1. 结论

- `npu-smi info` 单次约 **1.14s**（8 卡一次性返回）；`-t usages -i N` 0.64s、`-t memory -i N` 0.52s，
  按设备逐个采样更慢。
- `--hardware-interval 2s`、10 秒窗口：**5 个采样点 × 8 设备 = 40 行**，V4 `/health` 200、service active；
  但 npu-smi 子进程累计执行约 5×1.14s≈5.7s，占窗口约 57% 的单核 wall duty，开销偏高。
- 默认保持 `hardware.interval_s=10`；短窗口建议 ≥5s，不建议 1s（会变成连续执行 npu-smi）。
- 短间隔不会丢服务：采集期间 health/service 正常，agent 主进程只负责拉起子进程。

## 2. 实测

```text
attach --duration 10s --blocking off --hardware on --hardware-interval 2s
hardware samples=5 devices=8 hardware_lines=40
wall 17.7s（setup 2.3s + capture 10s + teardown 1.1s + exit ~4s）
health 200→200
```

数值与 `npu-smi info` 同源：首采样 device0 `aicore=23%`、`hbm_used=61408/65536`，
实时 `npu-smi` 显示 AICore 46%（不同时刻，属正常波动）。

## 3. 建议

| 场景 | interval |
|---|---|
| 默认/长跑 | 10s（profile 默认） |
| 短窗口（10–30s）分析 HBM/AICore 趋势 | 5s |
| 1–2s | 仅用于短时诊断，需接受 npu-smi 高占空比；不建议生产长跑 |

## 4. DCMI

- 宿主有 `/usr/local/dcmi/libdcmi.so`，但当前 Go 二进制不引入 CGO；`npu-smi` 本身通过 DCMI 驱动读取。
- 若后续需要更细字段（HBM 带宽、ECC、每 chip 功耗），再评估独立 CGO helper 或 `npu-smi -t` 子命令；
  不在本阶段实现，避免引入 CGO 交叉编译复杂度。
- 文档明确：A7 的验证对象是 **npu-smi 后端**，不是 DCMI API 直连。

## 5. 边界

- 硬件采样是 wall-clock 快照，不能和 monotonic host span 逐调用 join。
- AICore/HBM 是整卡值，不能归因到单 Worker 或单 request。
- 2s 采样的 CPU 影响只测了 wall duty，未做长跑 CPU/温度稳态测试；生产前应复测。
