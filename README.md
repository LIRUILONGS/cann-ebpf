# cann-ebpf

CANN / vLLM-Ascend 的**主机侧只读观测工具**：用 eBPF uprobe 记录 host 侧 CANN 调用和阻塞，用 `sample` 采指标，用 `report` 生成离线 HTML。

## 能做什么

| 能力 | 命令 | 说明 |
|---|---|---|
| 环境检查 | `cann-ebpf preflight` | 只读，检查内核/权限/探针依赖 |
| 进程/符号发现 | `cann-ebpf discover` | 只读，不 attach |
| host span | `cann-ebpf attach` | uprobe 记录 CANN host 调用，不重启目标进程 |
| off-CPU / futex | `cann-ebpf attach --blocking on` | 记录 host 阻塞 |
| 硬件基础指标 | `cann-ebpf sample --npu-smi` | HBM/AICore/功耗/温度 |
| HBM 扩展指标 | `cann-ebpf sample --npu-smi-ext` | 频率、ECC、电压、PCIe 错误、HBM 利用率（30s 周期） |
| **内核栈火焰图** | `cann-ebpf stacks` | 对目标 host 进程做 99Hz 内核栈采样，输出 `stacks.folded` |
| 进程/cgroup/system 指标 | `cann-ebpf sample` | procfs、cgroup v2、主机系统 |
| Prometheus 指标 | `cann-ebpf sample --prometheus ...` | vLLM/Orinfer 等 endpoint |
| 离线报告 | `cann-ebpf report` | 只读，生成单文件 HTML |
| host-only 诊断 | `cann-ebpf diagnose` | 只读，JSON/Markdown |
| Prometheus exporter | `cann-ebpf metrics` | 导出已有 run 的 `cann_ebpf_*` 指标 |
| 压测客户端 | `scripts/run_sweep.sh` | 会发负载；不重启服务；有 `sweep.json` 时出 `load_latency` |

典型用途：

- 看某个 CANN 调用在 host 侧花了多久、是否卡在锁/调度；
- 看 host 内核在 NPU 驱动、ioctl、DMA、SMMU、网络、调度路径上的 hot path；
- 看 HBM/AICore/功耗/温度/ECC/PCIe 等硬件状态；
- 把一次 run 的 span、指标、火焰图合成离线 HTML，事后分析。

## 运行环境

### 构建环境

- Go **1.24**（当前构建验证：go1.24.11）
- `CGO_ENABLED=0`
- 交叉编译目标：`GOOS=linux GOARCH=arm64`
- 纯 Go 二进制，目标机不需要 Python 才能运行 `preflight`/`discover`/`attach`/`sample`/`report`/`diagnose`
- 可选脚本 `scripts/run_sweep.sh` 使用 Python 3 跑压测客户端

### 目标机

- Linux **arm64**
- 已验证内核：**5.10.0-136...oe2203sp1.aarch64**（openEuler 22.03 系列）
- 8× **Ascend 910B2**
- 已验证 CANN：**9.0.1** 和 **9.2.0**
- 目标服务：vLLM-Ascend / Orinfer v4.1
- 硬件指标依赖：`npu-smi`（或 DCMI helper）
- 内核栈火焰图依赖：`perf_event` + `/proc/kallsyms` 可读

### 权限与内核能力

`attach` / `stacks` 需要：

- root，或 `CAP_BPF` + `CAP_PERFMON`（按内核版本）
- BPF、uprobe、tracefs、perf_event 可用
- `/proc/kallsyms` 可读（火焰图符号化）
- 能看到目标进程的 host PID/线程（容器内 PID 需先映射到 host PID）

`sample` 的 Prometheus 源需要能访问对应 `/metrics` endpoint；`report` 完全离线，不联网。

### 资源

- 10 分钟 host 采集的 `spans.jsonl` 约 **200MB**
- 同一份报告 HTML 约 **8–9MB**
- 火焰图 `stacks.folded` 约几百 KB
- 采样频率默认：host stack `99Hz`，metrics 基础间隔 `1s`，`--npu-smi-ext` 默认 `30s`

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

| 视图 | 状态 | 前提 |
|---|---|---|
| `load_latency` 负载-延迟 | 可用 | 需要 `sweep.json` |
| `off_cpu` 阻塞 | 可用 | host span |
| `operator_treemap` 算子占比 | 可用 | host span |
| `service_topology` 服务拓扑 | 可用 | host span |
| 内核栈火焰图 | 可用 | 需要 `cann-ebpf stacks` 的 `stacks.folded` |

另外有：

- host 时间线、延迟热图、函数排行、尾延迟 cohort
- metrics 小图：CPU/内存/NPU/HBM/ECC/PCIe、Prometheus 指标
- `metrics` 默认最多嵌入 256 个 series；超过会截断

真实 V4.1 十分钟报告（331,993 host spans、72 metrics、内核栈火焰图）：

![真实 V4.1 十分钟报告](docs/images/report-demo.png)

## 火焰图（host 内核栈）

`stacks` 采的是**目标 host 进程的内核栈**，回答“采样时刻 CPU 在内核里跑哪条调用链”。

它适合：

- 看 NPU 驱动 / ioctl / DMA / SMMU / 网络 / 锁 / 调度路径的占比；
- 判断 host 侧卡在内核哪里；
- 和 span、off-CPU 并列看：span 看 CANN 调用耗时，火焰图看内核 hot path。

它不适合：

- 不能代表 AI Core 的 operator 执行时间；
- 不采用户态 Python/PyTorch/vLLM 栈；
- 样本数是“采样命中次数”，不是请求数、不是精确耗时；
- 不能单独证明 NPU 算子慢。

采集：

```bash
cann-ebpf-linux-arm64 stacks \
  --pid auto \
  --profile vllm-ascend \
  --duration 30s \
  --freq 99 \
  --out runs/stacks-$(date -u +%Y%m%dT%H%M%SZ)
```

产物：

```text
runs/stacks-.../
├── stacks.folded      # root;...;leaf count
└── stacks.meta.json   # samples/unique/stack_fail/coverage
```

出报告（可单独，也可和 host span 一起）：

```bash
cann-ebpf-linux-arm64 report \
  --stacks runs/stacks-20261127T000000Z \
  --out runs/stacks-20261127T000000Z-report.html
```

```bash
cann-ebpf-linux-arm64 report \
  --input runs/ebpf-20261127T000000Z \
  --stacks runs/stacks-20261127T000000Z \
  --out runs/ebpf-20261127T000000Z/report.html
```

报告里的火焰图横轴是样本数，纵轴是栈深度；可点击缩放、搜索函数/模块。看不到栈时明确显示不可用，不画空图。

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

`scripts/run_sweep.sh` 是压测客户端，会占用目标服务容量。

```bash
VLLM_API_KEY=... sh scripts/run_sweep.sh \
  --workdir runs/sweep-$(date -u +%Y%m%dT%H%M%SZ) \
  --tiers 1,2,4,8 \
  --duration 30 --max-tokens 64 \
  --base-url http://127.0.0.1:8905 --model deepseek-v4-flash \
  --bin ./cann-ebpf-linux-arm64 --yes
```

生产背景负载高时，`load_latency` 会标 `partial`，不能计算容量 knee。

## 安全与权限

- 所有 attach/sample/report 都是只读；不会修改目标进程内存、不写目标配置
- 需要 root 或对应 CAP（BPF、perf_event 等）
- `report` 离线运行，不联网
- `sweep` 会发请求，必须在允许压测的环境执行

## 开发

```bash
CGO_ENABLED=0 go test ./...
CGO_ENABLED=0 go build ./...
gofmt -l ./cmd ./internal
```

## 文档

- [docs/USAGE.md](docs/USAGE.md)：完整操作手册
- [docs/STACKS-FLAMEGRAPH.md](docs/STACKS-FLAMEGRAPH.md)：内核栈火焰图采集与报告
- [docs/DIAGNOSE.md](docs/DIAGNOSE.md)：诊断输出说明
- [docs/METRICS-CATALOG.md](docs/METRICS-CATALOG.md)：指标目录

## 版本状态

覆盖 host span、off-CPU、硬件与系统指标、HBM 扩展、离线报告和压测客户端。
