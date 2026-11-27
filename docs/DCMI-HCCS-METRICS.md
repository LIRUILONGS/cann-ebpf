# DCMI 标量扩展与 HCCS 被动计数

日期：2026-11-28
状态：代码、单测完成；已在 910B2 / 驱动 25.2.1 / npu-smi 25.2.1 服务器上完成只读实测。
范围：DCMI 直连标量扩展 + npu-smi HCCS 被动计数器。不做 hccs-bw / pcie-bw 主动 profiling，不碰 msPTI，不重启或停止 vLLM/Orinfer。

## 1. 结论

- DCMI helper 默认基础模式；加 --extended（或 DCMI_PROBE_EXTENDED=1）后采集扩展标量。扩展项失败只写 stderr warning，基础字段继续输出。
- DCMI 的 AICore 读取失败不再写 0，health 不再写死 OK：失败时省略对应字段；health 来自 dcmi_get_device_health。
- DCMI 可直出频率、HBM 时钟/带宽利用率、8 个 ECC 计数 + enable、电压、5 个 PCIe 错误计数；字段映射与单位已实测校准。
- HCCS 通过 npu-smi info -t hccs -i N -c 0 被动读取，按实际数组长度解析 lane（实测 7 lane，不硬编码）。
- bytes/packets/retry/error 是 counter；link_speed/lane_mode 保留驱动原始值。报告侧只对 counter 做差分，不生成速率指标。
- 所有指标都是设备级快照，不能归因到单个请求、Worker、kernel 或 stream。

## 2. 指标清单

### 2.1 DCMI 扩展标量（source=dcmi）

| 指标名 | 类型 | 单位 | 用途 |
|---|---|---|---|
| npu_aivector_utilization_ratio | gauge | ratio | DCMI_UTILIZATION_RATE_VECTORCORE / 100；整卡 AIVector 利用率 |
| npu_aicpu_utilization_ratio | gauge | ratio | DCMI_UTILIZATION_RATE_AICPU / 100 |
| npu_ctrlcpu_utilization_ratio | gauge | ratio | DCMI_UTILIZATION_RATE_CTRLCPU / 100 |
| npu_utilization_ratio | gauge | ratio | DCMI_UTILIZATION_RATE_NPU / 100；接口定义的 NPU 综合利用率，不等同 AICore |
| npu_vectorcore_clock_mhz | gauge | mhz | DCMI_FREQ_VECTORCORE_CURRENT；910B2/25.2.1 返回 -8255，当前省略 |
| npu_ctrlcpu_clock_mhz | gauge | mhz | DCMI_FREQ_CTRLCPU；实测 2000 |
| npu_aicore_clock_ratio | gauge | ratio | 派生 AICore 当前/最大频率；仅表示“疑似降频”，不是降频原因 |
| npu_hbm_temperature_celsius | gauge | celsius | HBM 温度 |
| npu_soc_temperature_celsius | gauge | celsius | SoC 温度（传感器 union iint） |
| npu_ndie_temperature_celsius | gauge | celsius | N_DIE 温度 |
| npu_sfp_temperature_celsius | gauge | celsius | SFP 温度（DCMI_FP_TEMP_ID） |
| npu_health_error_count | gauge | count | dcmi_get_device_errorcode_v2 返回的错误码数量；0 是有效值 |

DCMI 直出、沿用既有名字的字段：

| 指标名 | 类型 | 单位 | 来源 |
|---|---|---|---|
| npu_aicore_clock_mhz / npu_aicore_clock_max_mhz | gauge | mhz | DCMI_FREQ_AICORE_CURRENT_ / DCMI_FREQ_AICORE_MAX |
| npu_hbm_clock_mhz | gauge | mhz | DCMI_FREQ_HBM |
| npu_hbm_bandwidth_util_ratio | gauge | ratio | DCMI_UTILIZATION_RATE_HBM_BANDWIDTH / 100；整卡 HBM 控制器视角 |
| npu_voltage_volts | gauge | volts | dcmi_get_device_voltage，原始值按 10 mV 解释 |
| npu_hbm_ecc_single_bit_errors / double_bit_errors | gauge | count | 当前窗口 ECC 计数 |
| npu_hbm_ecc_single_bit_total / double_bit_total | counter | count | 累计 ECC 计数 |
| npu_hbm_ecc_single_bit_isolated_pages / double_bit_isolated_pages | gauge | count | 已隔离页数 |
| npu_hbm_ecc_single_bit_next_isolated_pages / double_bit_next_isolated_pages | gauge | count | 下一代隔离页数 |
| npu_hbm_ecc_enabled | gauge | count | 1/0，DCMI ecc.enable_flag |
| npu_pcie_tx_errors_total / rx_errors_total / lcrc_errors_total / ecrc_errors_total / retry_total | counter | count | dcmi_pcie_link_error_info 五字段 |

### 2.2 HCCS 被动计数器（source=npu-smi）

每张卡每轮执行一次 npu-smi info -t hccs -i N -c 0；输出数组长度按驱动实际值解析。每个 lane 一条序列，label lane=0..N-1。

| 指标名 | 类型 | 单位 | 来源字段 |
|---|---|---|---|
| npu_hccs_tx_bytes_total | counter | bytes | hccs tx bytes |
| npu_hccs_rx_bytes_total | counter | bytes | hccs rx bytes |
| npu_hccs_tx_packets_total | counter | count | hccs tx packets |
| npu_hccs_rx_packets_total | counter | count | hccs rx packets |
| npu_hccs_retry_total | counter | count | hccs retry count |
| npu_hccs_error_total | counter | count | hccs error count |
| npu_hccs_link_speed | gauge | raw | hccs link speed；实测 224，单位未在 npu-smi 标注 |
| npu_hccs_lane_mode | gauge | raw | hccs lane mode；实测 4 |
| npu_hccs_healthy | gauge | count | hccs health status；1=OK，0=其他；带 status label |

## 3. DCMI 实测校准（2026-11-28，8 x 910B2）

原始证据：docs/evidence/20261128-dcmi-extended-8cards.jsonl

| 项目 | 原始读法 | 输出 | 结论 |
|---|---|---|---|
| AICore 当前/最大频率 | dcmi_get_device_frequency(DCMI_FREQ_AICORE_CURRENT_=7 / DCMI_FREQ_AICORE_MAX=9) | 1800 / 1800 | 单位 MHz，与 npu-smi -t common 一致 |
| HBM 时钟 | DCMI_FREQ_HBM=6 | 1600 | 单位 MHz，与 npu-smi -t memory 一致 |
| CtrlCPU 时钟 | DCMI_FREQ_CTRLCPU=2 | 2000 | 单位 MHz |
| VectorCore 时钟 | DCMI_FREQ_VECTORCORE_CURRENT=12 | 返回 -8255 | 不支持；省略并 warning |
| HBM 容量/使用 | dcmi_hbm_info.memory_size / memory_usage | 65536 / 约 61186 | 25.2.1 上按 MB 解释；probe 对疑似 bytes 的大值做 /1024^2 兼容 |
| HBM 温度 | dcmi_hbm_info.temp | 38-42 | 摄氏度，与 npu-smi -t sensors 同源 |
| HBM 带宽利用率 | dcmi_get_device_utilization_rate(DCMI_UTILIZATION_RATE_HBM_BANDWIDTH) | 0-9 | 百分比整数；接口只给利用率，不给绝对 GB/s |
| 电压 | dcmi_get_device_voltage | 81 / 83 | 除以 100 得 0.81 / 0.83 V，与 npu-smi -t volt 一致 |
| SOC/SFP/N_DIE 温度 | dcmi_get_device_sensor_info(union.iint) | SOC 41-43、SFP 39-43、N_DIE 41-43 | iint 分支正确，与 npu-smi -t sensors 一致 |
| ECC | dcmi_get_device_ecc_info(DCMI_DEVICE_TYPE_HBM) | 8 计数全 0，enable=true | 字段逐一对应，0 是有效读数 |
| PCIe 错误 | dcmi_get_device_info(PCIE / PCIE_ERROR_INFO) | 5 计数全 0 | 字段对应 tx/rx/lcrc/ecrc/retry |
| 健康状态 | dcmi_get_device_health | raw 0 -> health=OK | 0=OK 已实测；硬件 JSONL 同时保留 health_code 原始值；非 0 枚举语义未在头文件标注，按 Warning/Alarm/Critical 映射并保留限制 |
| 健康错误码数量 | dcmi_get_device_errorcode_v2 | 0 | 0 是有效值，不是“未知” |

## 4. HCCS 实测与参数矩阵

原始证据：docs/evidence/20261128-hccs-8cards.txt

实测 8 张卡均返回 7 个 lane，字段与数组长度完全一致；health 全部 OK，retry/error 全 0，link speed 224，lane mode 4。

参数矩阵（本机型实测）：

| 子命令 | 是否带 -c | 说明 |
|---|---|---|
| npu-smi info -t hccs -i N -c 0 | 必须带 | 不带或 -i 缺失会报 Invalid card id |
| npu-smi info -t common -i N | 不带 | 实测可用 |
| npu-smi info -t ecc-enable -i N | 不带 | 实测可用 |
| npu-smi info -t ecc -i N | 不带 | 实测可用 |
| npu-smi info -t usages -i N | 不带 | 实测可用 |
| npu-smi info -t sensors -i N -c 0 | 必须带 | 实测可用 |
| npu-smi info -t hccs-bw ... -time T | 不执行 | 主动 profiling，禁止在生产使用 |

说明：HCCS 的 lane 数量来自数组长度，不假定 7；当前 910B2 为 7，其他驱动/机型可能不同，解析器按实际长度生成 lane label。

## 5. unknown / unsupported 规则

1. 缺失即省略：Go 字段全部为指针 + omitempty；C 端只有读取成功才写 JSON key。未知绝不写成 0。
2. extended 失败只 warning：DCMI helper 的每个扩展 API 独立读取；失败写 stderr warning，不阻止基础 JSON 行输出。
3. 明确不支持的字段：例如 VectorCore 频率在 910B2/25.2.1 返回 -8255；probe 省略该 key 并打印 warning，不用 0 或 2000 冒充。
4. HCCS 数组中的 NA 项解析为 nil，不生成该 lane 的对应指标。
5. health：dcmi_get_device_health 失败时省略 health；成功时按 raw code 映射为 OK/Warning/Alarm/Critical，非 0 枚举语义在 docs 中保留为待厂商确认项。
6. HBM 绝对带宽、HCCS GB/s、降频原因均没有设备直读字段；不做估算，不用其他字段代替。

## 6. 采样频率与开销

| 来源 | 默认 | 最小 | 说明 |
|---|---|---|---|
| DCMI 基础 | 1s（沿用 --interval） | 1s | 单次约 2 秒/8 卡，实际按调度排队 |
| DCMI 扩展 --dcmi-ext | 10s | 5s | 单次约 3.6 秒/8 卡；低于 5s 自动回落到 10s |
| HCCS --hccs | 30s | 10s | 每卡一次子进程，8 卡约数秒；10s 仅显式诊断使用 |
| npu-smi 基础 | 5s | 5s | 既有行为不变 |

所有来源都有独立超时；单个来源失败只进 metrics.meta.json 的 warnings，不影响 host span、device.jsonl 或其他来源。

## 7. 边界

- 设备级，不是请求级：AIVector/AICPU/CtrlCPU/NPU/HBM 利用率与温度都是整卡视角，无法归因到某个 vLLM Worker 或请求。
- 不是 kernel/memcpy/HCCL 执行时间，不替代 msPTI。
- 不提供 FLOPs、算术强度、Roofline、AI Core pipeline 利用率、occupancy、stall reason、指令数、cycle 数。
- HBM 带宽只有利用率百分比；绝对 GB/s 不可得。
- HCCS 不提供绝对带宽，只提供累计 counter；报告端差分必须处理重启/回绕，首个点只能丢弃。
- link_speed 原样保留；在厂商给出单位前不能写成 GB/s。
- 降频只能由 cur/max 比值提示“疑似”，没有 throttle reason 直读字段。

## 8. 复现命令

    # 编译 DCMI helper（在目标机，只读）
    gcc -O2 -o dcmi_probe dcmi_probe.c       -I/usr/local/Ascend/driver/include       -L/usr/local/Ascend/driver/lib64/driver -ldcmi       -Wl,-rpath,/usr/local/Ascend/driver/lib64/driver

    # 基础模式：8 行，一行一个设备
    ./dcmi_probe

    # 扩展模式：新增频率/温度/ECC/电压/PCIe/health error
    ./dcmi_probe --extended

    # HCCS 被动读取（每卡一次）
    npu-smi info -t hccs -i 0 -c 0

    # 集成采样
    cann-ebpf sample --out runs/hw-ext --duration 1m --dcmi-ext --hccs

## 9. 验证记录

- C helper 在目标机 gcc 10.3.1 编译通过；dcmi_probe 基础 8 行、extended 8 行执行成功（exit=0）。
- 扩展 stderr 证据：docs/evidence/20261128-dcmi-extended-8cards.stderr.txt（8 卡均报告 VectorCore frequency -8255，字段按 unsupported 省略）。
- Go 单测覆盖 DCMI JSONL 扩展字段、未知不补 0、HCCS 解析动态 lane 数、NA 保持 unknown、counter 类型、lane 标签、HCCS source 命令参数与间隔策略。
- 未执行：hccs-bw、pcie-bw、任何 set/clear/reset、任何服务重启。
- 未验证：非 0 health code 的厂商枚举语义；VectorCore frequency 在其他机型是否可用；HCCS link speed 的官方单位。
