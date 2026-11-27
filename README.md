# cann-ebpf

CANN / vLLM-Ascend 的**主机侧只读观测工具**：用 eBPF uprobe 记录 host 侧 CANN 调用和阻塞，用 `sample` 采指标，用 `report` 生成离线 HTML。

> **当前只保证下面“生产可用”这一部分。**  
> device 时间线、critical path、Roofline、A/B、变化点没有达到生产可用，相关代码已移到 `experiments/`，不参与构建，也不要在生产上运行。

## 生产可用

| 能力 | 命令 | 说明 |
|---|---|---|
| 环境检查 | `cann-ebpf preflight` | 只读，检查内核/权限/探针依赖 |
| 进程/符号发现 | `cann-ebpf discover` | 只读，不 attach |
| host span | `cann-ebpf attach` | uprobe 记录 CANN host 调用，不重启目标进程 |
| off-CPU / futex | `cann-ebpf attach --blocking on` | 记录 host 阻塞 |
| 硬件基础指标 | `cann-ebpf sample --npu-smi` | HBM/AICore/功耗/温度 |
| HBM 扩展指标 | `cann-ebpf sample --npu-smi-ext` | 频率、ECC、电压、PCIe 错误、HBM 利用率（30s 周期） |
| 进程/cgroup/system 指标 | `cann-ebpf sample` | procfs、cgroup v2、主机系统 |
| Prometheus 指标 | `cann-ebpf sample --prometheus ...` | vLLM/Orinfer 等 endpoint |
| 离线报告 | `cann-ebpf report` | 只读，生成单文件 HTML |
| host-only 诊断 | `cann-ebpf diagnose` | 只读，JSON/Markdown |
| Prometheus exporter | `cann-ebpf metrics` | 导出已有 run 的 `cann_ebpf_*` 指标 |
| 压测客户端 | `scripts/run_sweep.sh` | 会发负载；不重启服务；有 `sweep.json` 时出 `load_latency` |

## 快速开始

### 1. 构建

Windows 上交叉编译：

```powershell
$env:CGO_ENABLED = "0"
$env:GOOS = "linux"
$env:GOARCH = "arm64"
go build -buildvcs=false -trimpath -o dist/cann-ebpf-linux-arm64 ./cmd/cann-ebpf
```

目标机需要 root 或对应 CAP/内核选项，具体见 [docs/USAGE.md](docs/USAGE.md)。

### 2. 环境检查

```bash
./cann-ebpf-linux-arm64 preflight
```

### 3. 发现目标

```bash
./cann-ebpf-linux-arm64 discover --profile vllm-ascend --profiles-dir profiles --dry-run
```

### 4. 采 host span + 指标

```bash
OUT="runs/ebpf-$(date -u +%Y%m%dT%H%M%SZ)"

./cann-ebpf-linux-arm64 attach \
  --pid auto \
  --profile vllm-ascend \
  --profiles-dir profiles \
  --duration 30s \
  --blocking on \
  --hardware on \
  --metrics auto \
  --output "$OUT"
```

### 5. 生成报告

```bash
./cann-ebpf-linux-arm64 report \
  --input "$OUT" \
  --metrics "$OUT/metrics.jsonl" \
  --out "$OUT/report.html"
```

报告是**只读**的：不会再 attach、不会再采样、不会发请求。

## 报告里有什么

生产报告只保留 4 类视图：

| 视图 | 状态 | 前提 |
|---|---|---|
| `load_latency` 负载-延迟 | 可用 | 需要 `sweep.json` |
| `off_cpu` 阻塞 | 可用 | host span |
| `operator_treemap` 算子占比 | 可用但 `inferred` | host span；不是设备算子级 |
| `service_topology` 服务拓扑 | 可用但 `partial` | host span；只到进程范围 |

另外有：

- host 时间线、延迟热图、函数排行、尾延迟 cohort
- metrics 小图：CPU/内存/NPU/HBM/ECC/PCIe、Prometheus 指标
- `metrics` 默认最多嵌入 256 个 series；超过会截断

**没有的图**：关键路径瀑布、Summed Delay、Slack CDF、Roofline、A/B、变化点、device busy/kernel/memcpy/HCCL 时间线。  
这些不是“差一点就有”，而是当前没有生产可用的数据链路。

## 输出文件

一次 `attach` 通常产生：

```text
runs/ebpf-.../
├── spans.jsonl        # host span
├── summary.json       # 汇总
├── summary.md
├── manifest.json      # run 元数据
├── metrics.jsonl      # 可选指标
└── report.html        # report 生成
```

## 压测（可选）

`scripts/run_sweep.sh` 是压测客户端，会占用目标服务容量，**不属于零影响观测**。

```bash
VLLM_API_KEY=... sh scripts/run_sweep.sh \
  --workdir runs/sweep-$(date -u +%Y%m%dT%H%M%SZ) \
  --tiers 1,2,4,8 \
  --duration 30 --max-tokens 64 \
  --base-url http://127.0.0.1:8905 --model deepseek-v4-flash \
  --bin ./cann-ebpf-linux-arm64 --yes
```

生产背景负载高时，`load_latency` 只能标 `partial`，不能计算容量 knee。

## 明确不支持

- 生产环境动态注入 device collector（msPTI）：
  - 需要 `LD_PRELOAD`、进程启动注入、重启；不是生产方案
  - 相关代码在 `experiments/device-collector/`
- `critical_path` / `summed_delay` / `slack_cdf`
- `roofline`（没有真实 `ops.jsonl` 生产者）
- `ab_diff`（没有同 workload 两次 run）
- `change_point`（没有 ≥5 分钟序列）
- HBM 绝对 GB/s（驱动只暴露利用率 %）
- 风扇、降频原因
- 任何“为了采数据反复重启生产”的操作

## 安全与权限

- 所有 attach/sample/report 都是只读；不会修改目标进程内存、不写目标配置
- 需要 root 或对应 CAP（BPF、perf_event 等）
- `report` 离线运行，不联网
- `sweep` 会发请求，必须在允许压测的环境执行
- 不要在未验证的版本上直接对生产做维护窗口

## 开发

```bash
CGO_ENABLED=0 go test ./...
CGO_ENABLED=0 go build ./...
gofmt -l ./cmd ./internal
```

## 实验代码

以下内容不参与默认构建，不属于生产交付：

```text
experiments/
├── device-collector/   # msPTI collector
├── profiler-patch/     # vLLM-Ascend profiler 补丁
├── report-views/       # critical path / slack / roofline / A-B / change point
├── deploy-b1/          # B1/B4 部署脚本
└── ...
```

## 文档

- [docs/USAGE.md](docs/USAGE.md)：完整操作手册
- [docs/PRODUCTION-SCOPE.md](docs/PRODUCTION-SCOPE.md)：生产可用/实验/不支持清单
- [docs/FAILURES.md](docs/FAILURES.md)：这次生产采集失败复盘
- [docs/DIAGNOSE.md](docs/DIAGNOSE.md)：诊断输出说明
- [docs/METRICS-CATALOG.md](docs/METRICS-CATALOG.md)：指标目录

## 版本状态

当前版本是**已收窄范围的生产工具**：host 观测、metrics、report 可用；device/profiler/roofline 不提供生产入口。  
不要把实验代码当作产品能力，也不要在生产上反复重启验证。
