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

| 视图 | 状态 | 前提 |
|---|---|---|
| `load_latency` 负载-延迟 | 可用 | 需要 `sweep.json` |
| `off_cpu` 阻塞 | 可用 | host span |
| `operator_treemap` 算子占比 | 可用 | host span |
| `service_topology` 服务拓扑 | 可用 | host span |

另外有：

- host 时间线、延迟热图、函数排行、尾延迟 cohort
- metrics 小图：CPU/内存/NPU/HBM/ECC/PCIe、Prometheus 指标
- `metrics` 默认最多嵌入 256 个 series；超过会截断

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
- [docs/DIAGNOSE.md](docs/DIAGNOSE.md)：诊断输出说明
- [docs/METRICS-CATALOG.md](docs/METRICS-CATALOG.md)：指标目录

## 版本状态

覆盖 host span、off-CPU、硬件与系统指标、HBM 扩展、离线报告和压测客户端。
