# HBM 能力探测：V4（target-host，CANN 9.0.1 代际）

日期：2026-11-27
状态：只读探测完成（未 stop/restart 服务、未注入 collector、未发压测、未改任何生产配置）
对象：`target-host`（`target-ip`），8 × 910B2；宿主驱动 `25.2.1`、`npu-smi 25.2.1`、固件 `7.7.0.9.220`、产品串 `IT21HMDA_Bin5`

## 0. 结论（TL;DR）

| 需求字段 | 结论 | 采集路径 |
|---|---|---|
| HBM 带宽 | **只能拿“利用率 %”，拿不到绝对 GB/s**。`npu-smi info -t usages -i N` 已输出 `HBM Bandwidth Usage Rate(%)`（实测 6–15% 波动），DCMI 头文件同源字段 `dcmi_hbm_info.bandwith_util_rate`。绝对带宽需按机型硬编码峰值或自建 profiler。 | npu-smi（S）/ DCMI helper（M） |
| ECC / 错误 | **完整可用**：单/双 bit、累计、隔离页、下一代隔离页共 8 个计数器，外加 `ECC-Enable Status=True`。 | `npu-smi info -t ecc`、`-t ecc-enable`（S） |
| 频率 | **可用**：AICore 额定/当前频率（实测均 1800 MHz）、HBM 时钟（1600 MHz）。 | `-t common`、`-t memory`（S） |
| HCCS / PCIe 计数 | **HCCS 计数完整**（7 条 lane 的 tx/rx bytes、packets、retry、error、link speed）；**PCIe 只有错误计数**（TX/RX/LCRC/ECRC/Retry），没有 PCIe 吞吐。 | `-t hccs -i N -c 0`、`-t pcie-err -i N`（S–M） |
| 风扇 / 电压 | **电压可用**（0.81 V）；**风扇不可用**（本板卡无风扇，`pwm-mode`/`pwm-duty-ratio` 均 “does not support”）。 | `-t volt`（S）/ 无 |
| 降频原因 | **没有直接字段**。只能由 `curFreq < maxFreq` + 温度/阈值 + 功耗 + fault event 推断，归因置信度低。 | 推断（M–L），不建议先做 |

一句话：**除“HBM 绝对带宽”和“降频原因”外，其余都有官方只读接口，且 DCMI 头文件里都能找到对应 API；已实现的价值/成本比最高的是频率、ECC、电压、PCIe 错误计数。**

## 1. 探测方法与边界

- 只执行 `npu-smi info ...` 查询类子命令，以及本地 `grep`/`nm` 读取 `dcmi_interface_api.h` 与 `libdcmi.so`；**未执行**任何 `set`/`clear`/`reset`/`upgrade`/`restart`。
- `npu-smi info -t hccs-bw` 需要 `-i`/`-c` 和 **`-time <秒>` profiling 窗口**，属于主动测量，**本次没有执行**（见 §5.4）。
- 探测全程服务 `/health` 返回 200、systemd 状态 active；`npu-smi info` 8 卡 Health 均为 OK。
- 宿主为裸金属（`/usr/local/Ascend` 下只有 `driver` 和 `firmware`，没有 `ascend-toolkit`）；vLLM 跑在容器里。**容器内的 CANN 版本本次没有进容器核实**，但能力来自宿主驱动/DCMI，结论不受影响。

## 2. 实测记录（命令 → 输出摘要）

> 表中主机名/IP 一律用 `target-host` / `target-ip`；数值为探测当时快照。

| 命令（在 target-host 上） | 输出摘要 | 判定 |
|---|---|---|
| `npu-smi info -h` | 子命令类型清单包含 `memory, usages, sensors, temp, power, volt, common, health, ecc, ecc-enable, pcie-err, topo, hccs, hccs-bw, pwm-mode, pwm-duty-ratio ...` | 探测面确认 |
| `npu-smi info` | 8 × 910B2，Health OK，HBM-Usage 约 61380/65536 MB，AICore 13–32% | 生产在跑 |
| `npu-smi info -t board -i 0` | Software 25.2.1 / Firmware 7.7.0.9.220 / 产品串 IT21HMDA_Bin5 / Chip Count 1 | 版本基线 |
| `npu-smi info -t memory -i 0` | `HBM Capacity 65536 MB`、`HBM Clock Speed 1600 MHz`、`HBM Temperature 42 C`、`DDR Capacity 0` | 频率/HBM 温度可得 |
| `npu-smi info -t usages -i 0` | `HBM Usage Rate 93%`、`Aicore 21%→29%`、`DDR Bandwidth 0%`、**`HBM Bandwidth Usage Rate 15%→6%`**（间隔 4 s 两次采样） | **HBM 带宽利用率可得且实时变化** |
| `npu-smi info -t common -i 0` | `Aicore Freq 1800 MHZ`、`Aicore curFreq 1800 MHZ`、`Aicore Count 24`、`Temperature 42 C`、`Power 133.3 W` | 额定/当前频率可得 |
| `npu-smi info -t temp -i 0` | NPU 42 C、LM75A 47 C、LM75B 45 C、MCU 47 C | 温度覆盖面 |
| `npu-smi info -t sensors -i 0 -c 0` | Soc Max 43 C、SFP 39 C、NDIE 42 C、HBM 42 C | 分域温度可得（**必须带 `-c`**） |
| `npu-smi info -t power -i 0` | `NPU Real-time Power 239.4 W`（另一次 133.3 W） | 已有能力 |
| `npu-smi info -t volt -i 0` | `Voltage 0.81 V` | **芯片电压可得（单值）** |
| `npu-smi info -t ecc -i 0` | 单/双 bit 当前计数、累计计数、隔离页、下一隔离页共 8 项，均为 0 | **ECC 完整** |
| `npu-smi info -t ecc-enable -i 0` | `HBM ECC-Enable Status : True` | ECC 开关状态可得 |
| `npu-smi info -t pcie-err -i 0` | TX Error / RX Error / Lcrc / Ecrc / Retry Count 均为 0 | **PCIe 错误计数**（无吞吐） |
| `npu-smi info -t hccs -i 0 -c 0` | health OK；7 条 lane 的 `lane mode=4`、`link speed=224`、`tx bytes≈2.3e10–7.1e10`、`rx bytes≈4.6e10–8.4e10`、retry/error 全 0 | **HCCS 计数完整（需 `-c`）** |
| `npu-smi info -t topo` | 8 卡两两 `HCCS`，CPU affinity 144-167 / 0-23 / 96-119 / 48-71 | 互联拓扑确认 |
| `npu-smi info -t err-count -i 0` | `This device does not support querying err-count.` | 不支持 |
| `npu-smi info -t work-mode -i 0` | 不支持 | 不支持 |
| `npu-smi info -t product -i 0` | 不支持 | 不支持 |
| `npu-smi info -t hccs-credit -i 0` | 不支持 | 不支持 |
| `npu-smi info -t pwm-mode / -t pwm-duty-ratio -i 0 -c 0` | 均 `does not support` | **无风扇控制/转速** |
| `npu-smi info -t hccs-bw`（仅看 usage，未执行） | 提示必须输入 card id / chip id，Options 含 `-time %d Profiling Time` | 属主动测量，未执行 |
| `grep` / `nm -D libdcmi.so`（宿主，只读） | 见 §4：13 个相关 API 全部存在 | DCMI 可实现性确认 |

### 2.1 调用开销（实测）

- 单个 `-t <type> -i N` 约 **0.52–0.62 s**；`-t usages`、`-t ecc`、`-t volt` **不支持省略 `-i`**，必须逐卡调用。
- 因此“每族指标 × 8 卡”≈ **4.2–5.0 s wall**。在现有 `hardware.interval_s=10` 下再加 3–4 个指标族会把采集窗口占满，**建议 30 s 周期或按需开启**（与 `docs/A7-HARDWARE-INTERVAL.md` 的结论一致）。
- `npu-smi info` 一次性返回 8 卡约 1.1 s，是“广覆盖、低成本”的入口；新字段只能靠逐卡 `-t`。

## 3. 能力矩阵（主表）

工作量口径：**S ≈ 0.5–1 天**（纯文本解析 + schema 字段 + 单测）；**M ≈ 2–3 天**（含差分/速率/异常处理或 C helper）；**L ≥ 1 周**（需 profiling 窗口或跨版本适配）。
置信度：**高** = 本机命令或头文件直接证实；**中** = API 存在但未实测；**低** = 只能推断。

| 指标 | 是否暴露 | 来源命令或 API | 置信度 | 实现工作量 | 备注 |
|---|---|---|---|---|---|
| HBM 带宽（利用率 %） | 部分（仅 %） | `npu-smi info -t usages -i N` → `HBM Bandwidth Usage Rate(%)`；DCMI `dcmi_get_device_hbm_info().bandwith_util_rate`、`dcmi_get_device_utilization_rate(DCMI_UTILIZATION_RATE_HBM_BANDWIDTH=10)` | 高 | S（npu-smi）/ M（DCMI） | 整卡口径，不能归因到单 Worker/请求；字段名在头文件里拼写为 `bandwith`（少一个 d），照抄 |
| HBM 带宽（绝对 GB/s） | 否 | 无接口；只能 `利用率% × 理论峰值` | 低 | L | 理论峰值 = HBM 数据率 × 位宽 / 8；`-t memory` 只给 1600 MHz，**位宽不暴露**，需按 910B2 规格硬编码，跨机型会错 |
| ECC（HBM 单/双 bit、累计、隔离页） | 是 | `-t ecc`、`-t ecc-enable`；DCMI `dcmi_get_device_ecc_info(card,dev,DCMI_DEVICE_TYPE_HBM,...)`、`dcmi_get_ecc_info`、`dcmi_get_multi_ecc_record_info_v2` | 高 | S | 计数为**自上次清零以来累计**；重启/热复位会归零，0 是有效值；地址级记录要 `multi_ecc_record_info`（M） |
| 频率：AICore 当前/额定 | 是 | `-t common` → `Aicore Freq(MHZ)` / `Aicore curFreq(MHZ)`；DCMI `dcmi_get_device_frequency(DCMI_FREQ_AICORE_CURRENT_=7 / DCMI_FREQ_AICORE_MAX=9)`、`dcmi_get_aicore_info()` 的 `freq/curfreq` | 高 | S | 在产线负载下实测两者都 1800 MHz（未降频）；`curFreq` 是判断“是否降频”的唯一直接信号 |
| 频率：HBM 时钟 | 是 | `-t memory` → `HBM Clock Speed(MHz)`；DCMI `dcmi_get_device_frequency(DCMI_FREQ_HBM=6)` / `dcmi_get_device_hbm_info().freq` | 高 | S | 1600 MHz；DDR 时钟本板为 0（无 DDR） |
| 频率：VectorCore/CTRLCPU | 是（API） | DCMI `DCMI_FREQ_VECTORCORE_CURRENT=12`、`DCMI_FREQ_CTRLCPU=2` | 中 | S | npu-smi 无对应子命令，需 DCMI helper |
| HCCS 计数（bytes/packets/retry/error/link speed） | 是 | `-t hccs -i N -c 0`；DCMI `dcmi_get_hccs_link_bandwidth_info`（字段见 §4）+ `dcmi_get_hccs_avail_credit_info` | 高 | M | 输出是 7 元数组，解析要按 lane 建 label（`lane=0..6`）；`link speed=224` 单位未标注，**不能直接当 GB/s** |
| HCCS 带宽（被动差分推导） | 部分（可算） | 同上：对 `hccs tx/rx bytes` 做时间差分 | 中 | M | 推荐路径：完全被动、无注入；需处理计数器回绕/设备重启，首点只能丢 |
| HCCS 带宽（主动测量） | 未验证 | `-t hccs-bw -i N -c M -time T`；DCMI `dcmi_get_hccs_link_bandwidth_info()`（含 `profiling_time/total_txbw/total_rxbw`） | 低 | L | **本次未执行**：`-time` 意味着 profiling 窗口，存在干扰生产互联的风险，留维护窗口验证 |
| PCIe 错误计数 | 是 | `-t pcie-err -i N`；DCMI `dcmi_get_device_pcie_error_cnt()`、`dcmi_get_pcie_error_cnt()` | 高 | S | TX/RX/LCRC/ECRC/Retry 五项，全 0；可做错误率看板 |
| PCIe 带宽/吞吐 | 否（npu-smi 无） | DCMI `dcmi_get_pcie_link_bandwidth_info()`（`tx_p_bw/tx_np_bw/tx_cpl_bw/rx_*` 各 MIN/MAX/AVG，带 `profiling_time`） | 中 | M | 需 C helper；`profiling_time` 语义未实测，可能同样是窗口采样，需窗口验证 |
| 风扇转速 / PWM | 否 | `-t pwm-mode`、`-t pwm-duty-ratio` 均不支持；`-t sensors` 无风扇项；DCMI `dcmi_get_device_fan_count/fan_speed` 符号存在但板卡无风扇 | 高 | N/A | 本机型为被动散热（整机风扇由 BMC 管），NPU 侧没有可采数据；不要做“伪风扇指标” |
| 电压 | 是 | `-t volt -i N` → `Voltage(V)` 0.81；DCMI `dcmi_get_device_voltage()` | 高 | S | 单值芯片电压，无多路/上下限阈值；板卡级电压不可得 |
| 降频原因 | 否（无直接字段） | npu-smi 无 throttle 子命令；DCMI 25.2.1 头文件无 throttle-reason 字段（只有 `DCMI_AICORE_LIMIT_ID`、`DCMI_THERMAL_THRESHOLD_ID` 两个 sensor id 与 fault event） | 低 | M–L | 可做“疑似”推断：`curFreq < maxFreq` + 温度接近阈值 + 功耗；事件侧可 `dcmi_get_fault_event`/`dcmi_subscribe_fault_event`，但 event id 语义未文档化 |
| 温度（补充，已实现） | 是 | `-t temp`、`-t sensors -i N -c 0`、`-t memory`（HBM） | 高 | 已实现 | HBM/NDIE/SoC/SFP 分域可得，适合作为降频推断输入 |
| 功耗（补充，已实现） | 是 | `-t power`、DCMI `dcmi_get_device_power_info`（0.1 W 单位） | 高 | 已实现 | 芯片功耗 vs 板卡功耗测量点不同，见 `docs/A7-HARDWARE-INTERVAL.md` |

## 4. DCMI 映射（宿主 `dcmi_interface_api.h` + `libdcmi.so` 25.2.1 实测）

头文件：`/usr/local/Ascend/driver/include/dcmi_interface_api.h`（77 KB）；库：`/usr/local/Ascend/driver/lib64/driver/libdcmi.so`。
以下 13 个符号用 `nm -D --defined-only` 确认**全部 PRESENT**：

```
dcmi_get_device_frequency          dcmi_get_device_ecc_info
dcmi_get_ecc_info                  dcmi_get_hccs_link_bandwidth_info
dcmi_get_pcie_link_bandwidth_info  dcmi_get_device_sensor_info
dcmi_get_device_fan_speed          dcmi_get_device_voltage
dcmi_get_device_utilization_rate   dcmi_get_device_errorinfo
dcmi_get_device_pcie_error_cnt     dcmi_get_multi_ecc_record_info
dcmi_get_device_hbm_info
```

关键结构/常量（照抄头文件，注意拼写）：

```c
struct dcmi_hbm_info {
    unsigned long long memory_size;
    unsigned int freq;                 /* HBM MHz */
    unsigned long long memory_usage;
    int temp;
    unsigned int bandwith_util_rate;   /* HBM 带宽利用率 %，注意拼写 */
};

struct dcmi_ecc_info {                 /* DCMI_DEVICE_TYPE_HBM */
    int enable_flag;
    unsigned int single_bit_error_cnt, double_bit_error_cnt;
    unsigned int total_single_bit_error_cnt, total_double_bit_error_cnt;
    unsigned int single_bit_isolated_pages_cnt, double_bit_isolated_pages_cnt;
    unsigned int single_bit_next_isolated_pages_cnt, double_bit_next_isolated_pages_cnt;
};

enum dcmi_freq_type {
    DCMI_FREQ_DDR = 1, DCMI_FREQ_CTRLCPU = 2, DCMI_FREQ_HBM = 6,
    DCMI_FREQ_AICORE_CURRENT_ = 7, DCMI_FREQ_AICORE_MAX = 9,
    DCMI_FREQ_VECTORCORE_CURRENT = 12
};

#define DCMI_UTILIZATION_RATE_HBM_BANDWIDTH 10
#define DCMI_UTILIZATION_RATE_DDR_BANDWIDTH 5

struct dcmi_hccs_bandwidth_info {
    int profiling_time;
    double total_txbw, total_rxbw;
    double tx_bandwidth[DCMI_HCCS_MAX_PCS_NUM];
    double rx_bandwidth[DCMI_HCCS_MAX_PCS_NUM];
};

struct dcmi_pcie_link_bandwidth_info {   /* AGENTDRV_PROF_DATA_NUM = 3：MIN/MAX/AVG */
    int profiling_time;
    unsigned int tx_p_bw[3], tx_np_bw[3], tx_cpl_bw[3], tx_np_lantency[3];
    unsigned int rx_p_bw[3], rx_np_bw[3], rx_cpl_bw[3];
};
```

补充：`dcmi_get_device_fan_count/fan_speed`、`dcmi_get_device_voltage`、`dcmi_get_device_sensor_info(DCMI_THERMAL_THRESHOLD_ID / DCMI_AICORE_LIMIT_ID ...)` 都在；容器内部分 API 可能返回 `DCMI_ERR_CODE_NOT_SUPPORT_IN_CONTAINER`，**采集器应跑在宿主**。

## 5. 逐项说明

### 5.1 HBM 带宽：为什么只有百分比

- 驱动侧计数器给出的是 `bandwith_util_rate`（0–100 的整数），`npu-smi` 直接把它渲染成 `HBM Bandwidth Usage Rate(%)`；同一份数据在 `dcmi_get_device_hbm_info` 与 `dcmi_get_device_utilization_rate(DCMI_UTILIZATION_RATE_HBM_BANDWIDTH)` 两处都能取。
- 绝对带宽需要“理论峰值”常数，而接口只暴露 HBM 时钟 1600 MHz，**不暴露位宽/通道数**；`npu-smi` 也没有 `-t hbm-bw` 之类的绝对带宽命令。
- 因此：
  - **P0**：先落 `npu_hbm_bandwidth_util_ratio`（0–1 或 %），足以回答“HBM 是不是瓶颈”。
  - **P2**：若要 GB/s，按机型表硬编码峰值（并在报告里标注“估算，非设备直读”），或等维护窗口用 `hccs-bw` 式 profiling 标定。
- 口径提醒：利用率是**整卡 HBM 控制器**视角，vLLM 多 Worker 共享，不能归因到单请求；这与现有 `npu_aicore_utilization_ratio` 的边界一致。

### 5.2 ECC：8 个计数器足够做“是否在恶化”

- 当前全部为 0、`ECC-Enable=True`，说明板卡健康；采集器只需按周期抓 8 个累计值 + enable。
- 语义：`Single/Double Bit Error Count` 是“当前统计窗口/自清零”，`Aggregate Total` 是生命周期累计，`Isolated Pages` 是已隔离页数；**重启后归零**，跨 run 比较前要先判断设备是否重启（可用 `-t sys-time` 或 boot 时间对齐）。
- 不要做“ECC 错误率 = 计数 / 请求数”，分母不存在；只做趋势和阈值告警（例如非零即 Warning）。

### 5.3 频率：只有“当前值”，没有“为什么降”

- `-t common` 同时给 `Aicore Freq`（额定 1800）和 `Aicore curFreq`（当前 1800），这是判断降频最直接的信号；HBM 时钟在 `-t memory`。
- `dcmi_get_device_frequency` 还支持 `DCMI_FREQ_AICORE_MAX`、`DCMI_FREQ_VECTORCORE_CURRENT`、`DCMI_FREQ_CTRLCPU`；`dcmi_get_aicore_info()` 的 `dsmi_aicore_info_stru{freq, curfreq}` 与 npu-smi 同源。
- **降频原因没有字段**：25.2.1 头文件里没有 throttle reason / power-limit reason 枚举；只有 `DCMI_AICORE_LIMIT_ID`（限制值）和 `DCMI_THERMAL_THRESHOLD_ID`（温度阈值）两个 sensor id，以及 fault event。因此“为什么降”只能推断，不能直读。

### 5.4 HCCS / PCIe：计数可采，带宽要谨慎

- `-t hccs` 是**纯计数读取**（tx/rx bytes、packets、retry、error、link speed、lane mode），完全被动；对 bytes 做差分即可得每条 lane 的 GB/s，这是推荐实现。
- `-t hccs-bw` 带 `-time <秒>`，语义是“在 T 秒窗口内做带宽 profiling”。它是否注入流量、是否会短暂影响 HCCS 调度，厂商文档没有说明；**生产流量下不执行**，只作为后续维护窗口的验证项。
- PCIe 侧只有 `-t pcie-err` 的错误计数；`dcmi_get_pcie_link_bandwidth_info` 存在但同样带 `profiling_time`，且 npu-smi 没有对应子命令，需要自建 helper。
- `link speed=224` 的单位 npu-smi 未标注（HCCS 常见口径是 Gbps/链路），**在拿到厂商口径前不要写成 GB/s**。

### 5.5 风扇 / 电压

- 电压：`-t volt` 单值 0.81 V，DCMI `dcmi_get_device_voltage()` 同源；可以做“电压异常”粗告警，但没有额定/上下限字段。
- 风扇：`pwm-mode`、`pwm-duty-ratio` 均返回 does not support，`-t sensors` 也没有 fan 项；本机型是整机/BMC 散热，NPU 侧无风扇数据。DCMI 的 `fan_count/fan_speed` 符号存在只是通用接口，本板卡不可用。**结论：不要为风扇做采集。**

### 5.6 降频原因：只能“疑似归因”

可行的推断链（全部被动）：

1. `curFreq` 与 `freq`（额定）比较，判断**是否**降频；
2. 结合 `-t temp` / `-t sensors`（SoC/HBM/NDIE）与 `DCMI_THERMAL_THRESHOLD_ID`，判断是否**温度触发**；
3. 结合功耗（`-t power`）与机型 TDP，判断是否**功耗墙**；
4. `dcmi_get_fault_event` / `dcmi_subscribe_fault_event` 抓设备事件（event_name/additional_info），作为旁证。

风险：event id 语义未在头文件文档化，阈值 sensor 的实际返回值未在本机验证（需 C helper），所以**归因结论只能标“低置信度”**，报告里不能写成“已确认降频原因”。

## 6. 建议落地顺序

| 优先级 | 内容 | 路径 | 工作量 |
|---|---|---|---|
| P0 | 频率（AICore 额定/当前、HBM 时钟）、ECC 8 计数 + enable、电压、PCIe 错误 5 计数 | 扩展现有 `npu-smi -t` 文本解析（`internal/hardware`），`cann.hardware.v1` 加指针字段 | S × 4 |
| P1 | HBM 带宽利用率（%）、HCCS 计数与差分吞吐 | 同上；HCCS 需按 lane 打 label、处理计数器回绕 | S + M |
| P2 | DCMI helper 扩展上述字段（避免 `npu-smi` 文本/locale 依赖） | `m3/dcmi_probe` 增加 `dcmi_get_device_frequency/ecc_info/hbm_info/voltage/pcie_error_cnt` | M |
| P3 | PCIe 带宽、HCCS 绝对带宽、降频归因 | 需 profiling 窗口/厂商口径，先文档化再实现 | L |
| 不做 | 风扇转速、ECC 错误率分母、降频“原因”直读 | — | — |

字段命名建议（与现有 `npu_*` 前缀一致）：
`npu_hbm_bandwidth_util_ratio`、`npu_hbm_clock_mhz`、`npu_aicore_clock_mhz`、`npu_aicore_clock_max_mhz`、`npu_hbm_ecc_single_bit_total`、`npu_hbm_ecc_double_bit_total`、`npu_hbm_ecc_isolated_pages`、`npu_voltage_volts`、`npu_pcie_tx_errors_total`、`npu_pcie_retry_total`、`npu_hccs_tx_bytes_total{lane}`、`npu_hccs_rx_bytes_total{lane}`。

## 7. 风险与限制

1. **只读边界**：本次仅 `npu-smi info` 查询 + 头文件/符号检查；`-t hccs-bw` 未执行，未触碰任何 `set/clear/reset`。
2. **SSH 间歇性断连**：探测期间多次出现 `Connection closed`，均用重试完成；这会影响“逐卡 × 多子命令”的采集器设计，需要单命令超时和重试（现有 5 s 超时策略可复用）。
3. **开销**：新增 4 个指标族 × 8 卡 ≈ 17 s/轮，10 s 周期不可行；建议 30 s 或“诊断模式才开”。
4. **版本漂移**：`npu-smi` 输出是文本，跨驱动版本字段顺序/单位可能变；DCMI helper 更稳，但要处理 `NOT_SUPPORT_IN_CONTAINER` 与不同 SoC 的字段差异。
5. **容器**：vLLM 在容器内，DCMI 的 `bandwith_util_rate`、fan 等字段可能被容器策略屏蔽；采集器应部署在宿主。
6. **未验证项**：`-t hccs-bw` 行为、`dcmi_get_pcie_link_bandwidth_info` 的 `profiling_time` 语义、`DCMI_THERMAL_THRESHOLD_ID` 实际返回值、`link speed=224` 的单位。这些都需要维护窗口或厂商文档，不应写进生产报告。

## 8. 复现命令（脱敏）

```bash
# 版本/拓扑
npu-smi info -h
npu-smi info
npu-smi info -t board -i 0
npu-smi info -t topo

# HBM 带宽利用率 / 频率 / HBM 时钟
npu-smi info -t usages -i 0
npu-smi info -t common -i 0
npu-smi info -t memory -i 0

# ECC
npu-smi info -t ecc -i 0
npu-smi info -t ecc-enable -i 0

# 温度 / 电压 / 功耗
npu-smi info -t temp -i 0
npu-smi info -t sensors -i 0 -c 0      # 必须带 -c
npu-smi info -t volt -i 0
npu-smi info -t power -i 0

# HCCS / PCIe
npu-smi info -t hccs -i 0 -c 0         # 必须带 -c
npu-smi info -t pcie-err -i 0
# 不要在生产执行：npu-smi info -t hccs-bw -i 0 -c 0 -time 2

# DCMI 能力（宿主只读）
grep -n "dcmi_hbm_info\|dcmi_ecc_info\|dcmi_freq_type\|HBM_BANDWIDTH" \
  /usr/local/Ascend/driver/include/dcmi_interface_api.h
nm -D --defined-only /usr/local/Ascend/driver/lib64/driver/libdcmi.so \
  | grep -E "dcmi_get_(device_frequency|device_ecc_info|hccs_link_bandwidth_info|pcie_link_bandwidth_info|device_voltage|device_utilization_rate)"
```

## 9. 附：本次探测不改变的既有结论

- `cann.hardware.v1` 现有字段（aicore_pct / hbm_used_mb / hbm_total_mb / power_w / temp_c / health）不变；新增字段用指针 + `omitempty`，老数据仍可解析。
- HBM 仍是**整卡**口径，不能和 monotonic host span 逐调用 join（见 `docs/HARDWARE.md` §6）。
- msPTI/设备时间线不在本文范围；本文只回答“硬件层能不能采”。
