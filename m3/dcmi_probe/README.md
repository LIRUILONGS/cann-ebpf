# DCMI probe（A7）

只读的 DCMI 直连探针，输出一行一个 `cann.hardware.v1` JSON，与 `npu-smi` 后端字段一致。

## 构建（目标机，需 DCMI 头文件/库）

```bash
gcc -O2 -o dcmi_probe dcmi_probe.c \
  -I/usr/local/Ascend/driver/include \
  -L/usr/local/Ascend/driver/lib64/driver -ldcmi \
  -Wl,-rpath,/usr/local/Ascend/driver/lib64/driver
```

## 运行

```bash
./dcmi_probe
# {"schema":"cann.hardware.v1","ts_ns":...,"device_id":0,...,"source":"dcmi"}
```

## 字段

| 字段 | DCMI API |
|---|---|
| `aicore_pct` | `dcmi_get_device_utilization_rate(..., DCMI_UTILIZATION_RATE_AICORE, ...)` |
| `hbm_used_mb` / `hbm_total_mb` | `dcmi_get_device_hbm_info`（按 MB 归一） |
| `power_w` | `dcmi_get_device_power_info` |
| `temp_c` | `dcmi_get_device_temperature` |

## 边界

- 只读：不初始化设备、不分配 HBM、不影响运行中的推理服务。
- 不提供 kernel/memcpy/HCCL 执行时间；仍是整卡 wall-clock 快照。
- 若驱动版本字段单位不同，probe 用 `>10,000,000` 判定 bytes→MB；需要时按实际驱动校准。
