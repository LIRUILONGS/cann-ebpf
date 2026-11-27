# ops.jsonl（Roofline per-op 证据）可行性评估

日期：2026-11-27  
范围：V4 生产 vLLM（`root@target-ip`，容器 `deepseek-v4-flash-8905`）能否产出 `ops.jsonl`，以及最小转换器原型。  
约束：不停止/重启生产 vLLM，不发压测，不注入 collector；只做只读检查、`--help`/版本查询和 `/tmp` 原型。

## 1. 结论

**条件可做（Conditional YES）**，但**当前生产进程直接产不出**。

- `ops.jsonl` 的 schema 和消费端是完整的：`internal/report/roofline.go` 定义七个字段，`build_views_v2.go` 负责加载，缺 `duration_ns`/`flops`/`bytes` 的记录会被跳过，不会用理论值补点。
- Ascend 侧存在可产出 per-op `duration`、`Input/Output Shapes`、`Input/Output Data Types`、以及**实测 FLOPs 计数器**（`cube_fops`/`vector_fops`）的 profiler 输出，落地文件是 `kernel_details.csv` 或 `op_summary_*.csv`。
- 但 V4 当前启动命令没有 `--profiler-config`，`/start_profile` 未启用；`msprof --pid` 动态 attach 在该 CANN 版本段错误；机器上没有历史 profile 导出。**不重启就采不到。**
- 即使重启并打开 vLLM-Ascend 自带 profiler，默认配置仍拿不到 FLOPs：`TorchNPUProfilerWrapper` 固定 `aic_metrics=PipeUtilization`、`record_op_args=False`，且不透传 `torch_profiler_record_shapes` / `torch_profiler_with_flops`。需要一处很小的 profiler 参数补丁（不属于本仓库的 tracked 代码）。
- 转换器原型已完成并在服务器 `/tmp` 与本地跑通：`kernel_details.csv` → `ops.jsonl` → `cann-ebpf report` 的 Roofline 卡片返回 `available=true, confidence=exact`，4 个合成点全部来自显式证据。

**建议**：把“产出第一批真实 `ops.jsonl`”定为 **M（3–5 天，含维护窗口）**；先修 Roofline 的 ridge 单位 bug（见 §6 G5），否则 `bound` 标签不可信。

## 2. 只读检查结果（2026-11-27）

### 2.1 版本与工具

| 项 | 实测值 |
|---|---|
| 主机 / 架构 | `target-host` / `aarch64` |
| 容器 | `deepseek-v4-flash-8905`，`Up 4 hours (healthy)`，镜像 `ds4/vllm-ascend:v0.23.0rc1-nonroot` |
| vLLM / vLLM-Ascend | `0.23.0+empty` / `0.23.0rc1`（源码在容器 `/vllm-workspace`） |
| Python | 容器 3.12.13；宿主机 3.9.9 |
| torch / torch_npu | `2.10.0+cpu` / `2.10.0.post2` |
| CANN | `9.0.1`，`/usr/local/Ascend/cann-9.0.1` |
| msPTI | `mspti 26.0.0`，`libmspti.so` 在 `tools/mspti/lib64` |
| msprof / msopprof | `/usr/local/Ascend/cann-9.0.1/bin/msprof`、`.../msopprof` |

只执行了 `import`、`inspect.signature`、`--help`、`ls`、`grep`、`ps` 和读取启动脚本；没有加载模型、没有执行任何 NPU 算子、没有调用 `/start_profile`。

### 2.2 当前生产进程没有 profiler

`/etc/systemd/system/vllm-ds4-8905.service` 的 `ExecStart` 是 `/usr/local/libexec/run_vllm_ds4_8905.sh`；脚本中的 `vllm serve /model` 参数包括 `--port 8905`、`--tensor-parallel-size 8`、`--quantization ascend`、MTP、`FULL_DECODE_ONLY` 等，但**没有 `--profiler-config`**。

vLLM 的 `ProfilerConfig.profiler` 默认 `None`，因此 `/start_profile` 没有可启动的 profiler。要采集必须重启服务并追加 `--profiler-config`，这属于维护窗口操作。

### 2.3 可用的 profiler 能力

`torch_npu.profiler.profile` 的签名（容器内实测）：

```text
(self, *, activities=None, schedule=None, on_trace_ready=None,
 record_shapes: bool = False, profile_memory: bool = False,
 with_stack: bool = False, with_flops: bool = False,
 with_modules: bool = False, experimental_config=None, use_cuda=None)
```

`_ExperimentalConfig` 支持：

```text
profiler_level=Level0|Level1|Level2
aic_metrics=AiCMetrics.PipeUtilization|ArithmeticUtilization|Memory|MemoryAccess|...
export_type=Text|Db
data_simplification=True|False
record_op_args=True|False
op_attr=True|False
```

关键事实：

- `Level1` 下 `ProfilerConfig.is_all_kernel_headers()` 返回 `True`，`kernel_details.csv` 会保留 `op_summary` 的全部列（`torch_npu/profiler/analysis/_profiler_config.py:206`）。
- `op_summary` 的 CANN 列定义包含 `Input Shapes`、`Input Data Types`、`Input Formats`、`Output Shapes`、`Output Data Types`、`Output Formats`，以及 `cube_fops`、`vector_fops`、`main_mem_read_bw(GB/s)`、`main_mem_write_bw(GB/s)`（`tools/profiler/profiler_tool/analysis/msconfig/prof_header_mapping.json`）。
- `cube_fops`/`vector_fops` 是**操作数计数**，不是速率；CANN 用 `mac_fp16_ratio * task_cyc * 16*16*16*2 + ...` 计算（`mscalculate/calculate_ai_core_data.py:119`），`fops_parser.py` 直接把 `cube_fops + vector_fops` 当作 `total_fops`。
- 这两个字段**只在 `ArithmeticUtilization` 指标组下生成**：`add_fops_header()` 明确判断 `sample_config.get(metric_key) == "ArithmeticUtilization"`（同文件 `:270` 附近）。
- vLLM-Ascend 的 `TorchNPUProfilerWrapper` 把 `aic_metrics` 固定为 `PipeUtilization`，且 `profile()` 没有传 `record_shapes`/`with_flops`（`vllm_ascend/profiler/torch_npu_profiler.py:55-75`）。因此默认 profile 只有 kernel 名/耗时，没有 FLOPs，`operator_details.csv` 的 `Input Shapes` 也可能为空。

### 2.4 各数据源对比

| 数据源 | 能给出 | 缺口 | 当前能否用于生产 |
|---|---|---|---|
| `torch_npu.profiler`（Level1 + `ArithmeticUtilization` + `record_shapes`） | kernel 名、device duration、输入/输出 shape 与 dtype、`cube_fops`/`vector_fops` | 需在进程内启用；`record_shapes`/`with_flops` 被 vLLM-Ascend 丢弃；需重启 | **否**（当前进程未启用） |
| vLLM-Ascend `--profiler-config` + `/start_profile` | `kernel_details.csv`、`operator_details.csv`、`op_statistic.csv`、`trace_view.json` | 默认无 FLOPs；启动参数已固定，需补丁 | **否**（未配置） |
| `msprof <app>` 应用模式 | `op_summary`，可用 `--aic-metrics=ArithmeticUtilization` | 必须由 msprof 启动目标进程；不能 attach 现有服务 | **否**（等于重启/新实例） |
| `msopprof` 单算子 | `--aic-metrics=Roofline`/`BasicInfo` 等 | 需要实际执行单个算子；不是服务级采集 | **否**（且禁止跑算子） |
| `msPTI` in-process | API/KERNEL/MEMCPY/HCCL 时间线；MEMCPY 带 bytes | 没有 FLOPs；需注入 collector 或重启 | **否**（禁止注入） |
| `msprof --pid` 动态 attach | 理论可采 | 该 CANN 9.0.1 组合下三种变体均段错误（`docs/M3-FEASIBILITY.md`） | **否**（已验证失败） |

结论：**FLOPs 的唯一现实来源是 Ascend profiler 的 `cube_fops`/`vector_fops` 计数器**；`bytes` 可由 shape+dtype 计算，或用 `main_mem_*_bw × duration` 的实测带宽回退。两者都要求一次受控的 profiler 采集。

## 3. `ops.jsonl` schema（消费端契约）

`internal/report/roofline.go:9`：

```go
type RooflineOp struct {
    RunID      string  `json:"run_id,omitempty"`
    Op         string  `json:"op"`
    Category   string  `json:"category,omitempty"`
    DurationNS int64   `json:"duration_ns"`
    Flops      float64 `json:"flops"`
    Bytes      float64 `json:"bytes"`
    DType      string  `json:"dtype,omitempty"`
}
```

示例（与 `docs/MULTI-AGENT-PLAN-VLLM-VIEWS.md` §3.2 一致）：

```json
{"run_id":"v4-20261127T1832","op":"MatMulV2","category":"gemm","duration_ns":3000000,"flops":2415919104,"bytes":7864320,"dtype":"bf16"}
```

消费规则（`internal/report/roofline.go:155`）：

- `op` 非空、`duration_ns > 0`、`flops > 0`、`bytes > 0` 才是一个完整点；否则整条跳过。
- `dtype` 可空；若报告显式给 `--roofline-dtype`，空 dtype 的记录仍可用，非空且不匹配的记录被跳过。
- 没有任何完整点时，视图必须是 `available=false`，不能画假点。
- `peak_tflops`、`peak_gbps` 必须由 CLI 显式提供且为正；峰值不参与测点。

## 4. 转换链路

### 4.1 采集前置（维护窗口，需授权）

在生产服务启动参数中增加：

```bash
--profiler-config '{"profiler":"torch","torch_profiler_dir":"/tmp/vllm_profile","torch_profiler_with_stack":false,"torch_profiler_record_shapes":true,"torch_profiler_with_flops":true}'
```

并在 vLLM-Ascend 的 `TorchNPUProfilerWrapper._create_profiler` 做两处透传/改指标组（该文件在容器 `/vllm-workspace/vllm-ascend`，不是本仓库）：

```python
experimental_config = torch_npu.profiler._ExperimentalConfig(
    export_type=torch_npu.profiler.ExportType.Text,
    profiler_level=torch_npu.profiler.ProfilerLevel.Level1,
    aic_metrics=torch_npu.profiler.AiCMetrics.ArithmeticUtilization,  # 原 PipeUtilization
    ...
)
return torch_npu.profiler.profile(
    ...,
    record_shapes=profiler_config.torch_profiler_record_shapes,       # 新增
    with_flops=profiler_config.torch_profiler_with_flops,             # 新增（可选）
    ...
)
```

说明：

- `ArithmeticUtilization` 是 `cube_fops`/`vector_fops` 的前置条件；不改为它，`kernel_details.csv` 不会有 FLOPs 列。
- `record_shapes` 用于 `operator_details.csv`/trace 的 shape 回退；CANN 的 `op_summary` 本身通常已带 shape，但需要首采验证。
- 不建议改 `record_op_args=True`；它会显著增大输出，且本方案不依赖它。

### 4.2 采集

服务稳定后，用正常业务流量（**不要压测**）采 5–10 秒：

```bash
curl -X POST http://127.0.0.1:8905/start_profile
# 正常请求若干条，或等待现有流量
curl -X POST http://127.0.0.1:8905/stop_profile
```

输出目录形如 `/tmp/vllm_profile/<host>_<timestamp>_ascend_pt/`。TP=8 时每个 worker 各写一份，需要合并。

### 4.3 解析

```bash
python3 -c "from torch_npu.profiler.profiler import analyse; analyse('/tmp/vllm_profile/<...>_ascend_pt')"
```

生成 `ASCEND_PROFILER_OUTPUT/`，其中：

- `kernel_details.csv`：Device 侧 kernel 行，Level1 保留 `op_summary` 全部列；
- `operator_details.csv`：torch op 级聚合（含 `Input Shapes`，但无 FLOPs）；
- `op_statistic.csv`、`trace_view.json`：辅助核对。

### 4.4 转换

```bash
python3 /tmp/ops_proto/ops_jsonl.py \
  --input /tmp/vllm_profile/<...>/ASCEND_PROFILER_OUTPUT \
  --out   /tmp/vllm_profile/ops.jsonl \
  --run-id v4-20261127T1832 \
  --dtype bf16
```

### 4.5 出报告

```bash
cann-ebpf report \
  --input <run-dir> \
  --ops /tmp/vllm_profile/ops.jsonl \
  --roofline-peak-tflops <910B2 bf16 峰值> \
  --roofline-peak-gbps  <910B2 HBM 峰值> \
  --roofline-dtype bf16 \
  --out report.html
```

峰值必须来自可核验的 910B2 规格或厂商数据，不能用模型参数量推算。

## 5. 最小原型（已跑通）

### 5.1 文件

- 服务器：`/tmp/ops_proto/ops_jsonl.py`、`/tmp/ops_proto/sample/kernel_details.csv`、`/tmp/ops_proto/ops.jsonl`
- 本地：`%TEMP%\ops_proto\ops_jsonl.py` 及同结构样例
- 本地验证报告：`.analysis/ops-proto/report.html`（`.analysis/` 已在 `.gitignore`）

原型只依赖 Python 标准库，兼容 3.9+。

### 5.2 解析规则

`kernel_details.csv`（Level1）的第一行形如：

```text
Device_id,Name,Type,Accelerator Core,Start Time(us),Duration(us),Wait Time(us),Block Num,Input Shapes,Input Data Types,Input Formats,Output Shapes,Output Data Types,Output Formats,cube_fops,vector_fops,main_mem_read_bw(GB/s),main_mem_write_bw(GB/s)
```

`op_summary_*.csv` 使用原始列名（`Op Name`、`OP Type`、`Task Duration(us)`），转换器同时接受两套名称。`Input Shapes` 的典型格式是 `1024,1024;1024,1024`（分号分隔多个张量），也兼容 `[[1024,1024],[1024,1024]]` 和 `1024x1024`。

1. 输入支持 `kernel_details.csv`、`op_summary_*.csv`，或包含它们的目录；列名按规范化别名匹配（`Name`/`Op Name`、`Type`/`OP Type`、`Duration(us)`/`Task Duration(us)` 等）。
2. `flops` 优先取 `cube_fops + vector_fops`；都为 0 时，只对 MatMul/GEMM/BatchMatMul 和 Conv 使用精确形状公式（`2*M*K*N`、`2*N*Hout*Wout*Cout*C*kh*kw`），其他算子不猜。
3. `bytes` 优先按 `Input Shapes + Output Shapes` 与 dtype 求张量字节数；shape 缺失时用 `(main_mem_read_bw + main_mem_write_bw) × duration` 回退；两者都没有则跳过。
4. `dtype` 优先取 `--dtype`，否则取输入/输出 dtype 的众数；`DT_BF16`/`BFLOAT16` 归一为 `bf16`。
5. 默认按 `(op, category, dtype)` 聚合，累加 duration/flops/bytes；`--per-launch` 保留逐 kernel 记录。聚合后的 duration 是 kernel task 时间之和，不是 wall-clock；只用于 per-op 吞吐，不与端到端延迟混用。
6. 无 duration/flops/bytes 的记录只计数、不输出，stderr 打印 `skipped(duration=..., flops=..., bytes=...)`。

### 5.3 合成样例

`kernel_details.csv` 包含 6 行：2 个 MatMul、1 个 FlashAttention、1 个 Add、1 个无 shape 的 Mul（走带宽回退）、1 个 Memcpy（无 FLOPs，应跳过）。

服务器运行：

```text
$ python3 /tmp/ops_proto/ops_jsonl.py --input /tmp/ops_proto/sample \
    --out /tmp/ops_proto/ops.jsonl --run-id proto-20261127 --dtype bf16
ops.jsonl: files=1 rows=6 emitted=4 skipped(duration=0, flops=1, bytes=0) mode=aggregated
```

输出：

```json
{"run_id":"proto-20261127","op":"FlashAttentionScore","category":"attention","duration_ns":8000000,"flops":115000000000.0,"bytes":134217728.0,"dtype":"bf16"}
{"run_id":"proto-20261127","op":"MatMulV2","category":"gemm","duration_ns":3000000,"flops":2415919104.0,"bytes":7864320.0,"dtype":"bf16"}
{"run_id":"proto-20261127","op":"Mul","category":"elementwise","duration_ns":100000,"flops":1000000000.0,"bytes":30000000.0,"dtype":"bf16"}
{"run_id":"proto-20261127","op":"Add","category":"elementwise","duration_ns":50000,"flops":16777216.0,"bytes":100663296.0,"dtype":"bf16"}
```

Memcpy 因为 `flops=0` 被跳过，符合 Roofline 只接受有 FLOPs 证据的点的规则。

### 5.4 端到端验证

```bash
dist/cann-ebpf.exe report \
  --metrics .analysis/metrics-v2-fixture/metrics.jsonl \
  --ops %TEMP%/ops_proto/ops.jsonl \
  --roofline-peak-tflops 400 --roofline-peak-gbps 1600 --roofline-dtype bf16 \
  --out .analysis/ops-proto/report.html
```

HTML 内 Roofline 状态：

```json
"roofline":{"status":{"available":true,"source":"ops.jsonl","confidence":"exact"},
"peak_tflops":400,"peak_gbps":1600,"dtype":"bf16",
"points":[{"op":"FlashAttentionScore",...,"bound":"compute"},...]}
```

`go test ./internal/report -run Roofline -count=1` 也通过（现有单测未修改）。

## 6. 缺口与风险

| 编号 | 缺口 | 影响 | 处理 |
|---|---|---|---|
| G1 | 生产进程未开启 profiler，且禁止重启 | 当前零产出 | 维护窗口重启；或等下一次计划重启 |
| G2 | vLLM-Ascend wrapper 固定 `PipeUtilization`、不透传 `record_shapes` | 默认 profile 没有 FLOPs，shape 可能为空 | 两行参数补丁（§4.1），首采验证列是否存在 |
| G3 | `cube_fops`/`vector_fops` 是 PMU 计数器推算，不是算子库上报的精确 FLOPs | 数值可能有采样误差；只覆盖 AICore，Memcpy/HCCL/AICPU 无 FLOPs | 在 `ops.jsonl` 旁记录 `flops_source=ascend_pmu`；只对 AICore 点画 Roofline |
| G4 | 生产是 W8A8 + MTP + `FULL_DECODE_ONLY` ACL graph | 同一 run 混合 int8/bf16，报告只接受一个 dtype/峰值对；融合算子公式不适用 | 按 dtype 分两次报告（int8 峰值另取）；融合算子依赖 `cube_fops`，不要用形状公式 |
| G5 | **`BuildRoofline` 的 ridge 单位错误**：`internal/report/roofline.go:105` 用 `peakTFLOPS / peakGBPS` | 400/1600 得到 0.25，而正确 ridge 是 250 FLOPs/byte；绝大多数访存受限算子会被误标为 `compute` | 改为 `peakTFLOPS * 1000 / peakGBPS`；修前不要对外发布 `bound` 结论 |
| G6 | TP=8，每个 rank 各写一份 profile | 直接拼接会重复/放大；`run_id` 语义不清 | 按 rank 合并后取每 op 的中位数或单 rank 代表；`run_id` 加 rank 后缀 |
| G7 | profiler 有运行时开销 | 不能长开，不能在生产长时间采集 | 5–10 秒窗口，正常流量；采前采后检查 `/health` 和延迟 |
| G8 | `ops.jsonl` 没有 provenance 字段 | 无法区分 `flops` 是实测还是形状公式 | 建议 schema 增补可选 `flops_source`/`bytes_source`/`rank`；或写 `ops.meta.json` 侧车。`encoding/json` 会忽略未知字段，增补不破坏消费端 |
| G9 | 还没有真实 profile 样本 | 列名/`data_simplification` 对 shape 的影响未实测 | 首次采集先 `head -1 kernel_details.csv` 核对列，再跑转换器；用一个已知 MatMul shape 手算 FLOPs 对账 |
| G10 | `BuildRoofline` 只支持单一 dtype 峰值 | W8A8 生产无法一张图看全 | 接受“按 dtype 出多张/多次报告”，或后续扩展视图契约（需升版本） |

## 7. 工作量评估

| 工作项 | 规模 | 说明 |
|---|---|---|
| 转换器 + 合成数据 + 文档（本轮） | **S**（0.5–1 天） | 已完成；无外部依赖 |
| 维护窗口：补丁 + 重启 + 5–10s 采集 + analyse | **M**（1–2 天，含窗口协调） | 风险主要在重启，不在转换 |
| 合并 8 rank、核对列、修 ridge、跑真实报告 | **M**（2–3 天） | 含一次已知 shape 对账 |
| W8A8/int8 峰值、融合算子覆盖、自动化采集 | **L**（1–2 周） | 需要产品化与回归 |

**结论：第一批可用 `ops.jsonl` 为 M（3–5 天，依赖维护窗口）；只做转换器为 S。**

## 8. 验证命令

### 8.1 本地（已执行）

```powershell
python $env:TEMP\ops_proto\ops_jsonl.py `
  --input $env:TEMP\ops_proto\sample `
  --out   $env:TEMP\ops_proto\ops.jsonl `
  --run-id proto-20261127 --dtype bf16

go test ./internal/report -run Roofline -count=1

dist\cann-ebpf.exe report `
  --metrics .analysis\metrics-v2-fixture\metrics.jsonl `
  --ops $env:TEMP\ops_proto\ops.jsonl `
  --roofline-peak-tflops 400 --roofline-peak-gbps 1600 --roofline-dtype bf16 `
  --out .analysis\ops-proto\report.html
```

检查：

```powershell
Select-String -Path .analysis\ops-proto\report.html -Pattern '"roofline":\{"status":\{"available":true'
```

### 8.2 服务器（已执行，只读/临时目录）

```bash
python3 /tmp/ops_proto/ops_jsonl.py \
  --input /tmp/ops_proto/sample \
  --out   /tmp/ops_proto/ops.jsonl \
  --run-id proto-20261127 --dtype bf16
wc -l /tmp/ops_proto/ops.jsonl   # 4
```

### 8.3 真实采集后（需授权）

```bash
# 1. 确认列
head -1 /tmp/vllm_profile/<...>_ascend_pt/ASCEND_PROFILER_OUTPUT/kernel_details.csv | tr ',' '\n' | grep -E 'Input Shapes|cube_fops|vector_fops'

# 2. 转换
python3 /tmp/ops_proto/ops_jsonl.py --input /tmp/vllm_profile/<...>_ascend_pt/ASCEND_PROFILER_OUTPUT \
  --out /tmp/vllm_profile/ops.jsonl --run-id v4-<timestamp> --dtype bf16

# 3. 报告
cann-ebpf report --input <run-dir> --ops /tmp/vllm_profile/ops.jsonl \
  --roofline-peak-tflops <peak> --roofline-peak-gbps <peak> --roofline-dtype bf16 \
  --out report.html
```

## 9. 证据位置

- 只读能力检查与系统信息：本文 §2；服务器 `/tmp/ops_proto/` 保留原型与样例。
- 动态 attach 失败：`docs/M3-FEASIBILITY.md`。
- Roofline 消费端：`internal/report/roofline.go`、`internal/report/build_views_v2.go`、`internal/report/views_v2.go`。
- 契约与示例：`docs/VIEWS-V2-CONTRACT.md`、`docs/MULTI-AGENT-PLAN-VLLM-VIEWS.md` §3.2、`testdata/views2/ops-complete.jsonl`。
- 本地端到端报告：`.analysis/ops-proto/report.html`（gitignored）。

本轮仅新增本文档和 `/tmp`、`.analysis` 下的原型文件；未修改任何 tracked 代码，未执行 `git commit`。
