# cann-ebpf 使用手册

本手册从零开始说明 `cann-ebpf` 的安装/构建、`preflight`、`attach`、`sample`、`report`、
`sweep`，以及可选能力（device / ops / HBM / stacks 等）的前提、输出文件、常见问题和
权限/安全注意事项。

> 约定
>
> - 文档中的 `target-host`、`target-ip` 是占位符，代表真实目标机的主机名/IP；请自行替换，
>   不要把真实主机名或 IP 写回仓库。
> - `cann-ebpf` 是**主机侧、按需短窗口**的诊断工具，不是常驻监控 agent。它观测 CANN/ACL/HCCL
>   主机调用、线程阻塞和设备关联，**不替代**网关指标系统或 msPTI 设备 profiler。
> - **不是所有图表都会出现**。每一类视图都有证据门槛，缺证据时显示 `unavailable` / `partial` /
>   `inferred`，不插值、不补零、不伪造成功。可用性以报告输出为准，不要假设“所有图都有”。

---

## 1. 开箱即用 vs 需要维护窗口/额外数据

先看清能力边界，再决定怎么采：

| 类别 | 代表能力 | 前提 / 代价 |
|---|---|---|
| **开箱即用**（只读、短窗口、不改目标） | `preflight`、`validate`、`discover`；`attach` host span；`--blocking`（off-CPU/futex）；`--hardware`（npu-smi 的 HBM/AICore/功耗/温度）；`--metrics`（DCMI/npu-smi/进程/cgroup/system/Prometheus）；`sample` 独立指标；`report` 离线 HTML；`metrics` Prometheus exporter；`diagnose` 只读定位 | 目标机 Linux/arm64 + root（或 CAP_BPF/CAP_PERFMON/CAP_SYS_ADMIN）+ 内核选项；`npu-smi` 可选。**不停服务、不改目标进程/库/驱动/sysctl** |
| **需要维护窗口**（要重启或注入） | 真实 **device 时间线**（msPTI in-process collector → `device.jsonl`）；vLLM general plugin 的 batch/request 上下文；真实设备侧判定（`device_bound` / `launch_starvation` 等） | 需在 ACL/torch_npu 初始化前 `LD_PRELOAD` 注入或重启服务；当前只在**合成 ACL** 上验证，V4/Orinfer 生产注入未做 |
| **需要额外数据/独立窗口** | Roofline（`--ops ops.jsonl` + 显式峰值）；变化点（`--events events.jsonl` + ≥5 分钟、≥30 个有效点）；跨服务拓扑（`--otel otel.jsonl`）；负载-延迟拐点（`sweep` + ≥4 档并发 + 压测窗口）；A/B（两次同 workload 完整 run）；kernel 栈火焰图（`stacks`） | 外部证据文件或独立压测窗口；缺失时对应视图不可用。`stacks` 目前是契约/未上机验证实现，见 `docs/STACKS-FLAMEGRAPH.md` |

一句话：**短窗口、只读、不重启**能拿到 host span / 阻塞 / 硬件快照 / metrics / 离线报告；
**设备侧结论和插件上下文**要维护窗口；**负载扫描、Roofline、拓扑、A/B、变化点**要额外数据或独立压测。

---

## 2. 安装与构建

### 2.1 目标机要求

- Linux / arm64（昇腾主机），**零运行时依赖**：纯 Go 静态编译的单一二进制，不需要 libc、Python、
  容器运行时或 CANN 头文件；Prometheus exporter 也编译在同一二进制内。
- 需要 **root**，或具备 `CAP_BPF` / `CAP_PERFMON` / `CAP_SYS_ADMIN`。
- 内核需满足：`CONFIG_BPF_SYSCALL`、`CONFIG_UPROBE_EVENTS`、`CONFIG_DEBUG_INFO_BTF`、
  `CONFIG_PERF_EVENTS`（openEuler 5.10 默认满足）。用 `preflight` 确认，见第 3 节。
- 可选：`npu-smi`（`/usr/local/sbin/npu-smi`）用于硬件层；DCMI helper 用于 `--hardware-backend dcmi`。

### 2.2 构建机（开发）交叉编译

构建机只需 **Go 1.24**；只有修改 `internal/probes/bpf/cann.bpf.c` 时才需要 clang（LLVM BPF target）。
仓库已包含 `internal/probes/bpf/cann.bpf.o`，不要依赖目标机现编。

Windows PowerShell：

```powershell
$env:CGO_ENABLED = "0"
go test ./...
go vet ./...

$env:GOOS = "linux"
$env:GOARCH = "arm64"
go build -buildvcs=false -trimpath -o dist/cann-ebpf-linux-arm64 ./cmd/cann-ebpf
Remove-Item Env:GOOS, Env:GOARCH, Env:CGO_ENABLED
```

Linux/macOS 等价：

```bash
CGO_ENABLED=0 go test ./... && go vet ./...
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -buildvcs=false -trimpath -o dist/cann-ebpf-linux-arm64 ./cmd/cann-ebpf
```

构建产物的 SHA-256 以交付包的 `SHA256SUMS.txt` 为准，**构建后不要手工改动二进制**。

### 2.3 发布包与安装

```bash
# 打包（默认 dry-run；--apply 才写磁盘）
sh scripts/make-release.sh --dry-run --binary dist/cann-ebpf-linux-arm64
sh scripts/make-release.sh --apply   --binary dist/cann-ebpf-linux-arm64 \
  --name cann-ebpf-<version> --out dist/cann-ebpf-<version>.tar.gz
```

安装到 `/opt/cann-ebpf`（默认 dry-run；`--apply` 才写主机并校验 SHA-256，不一致返回 7）：

```bash
tar xzf cann-ebpf-<version>.tar.gz
cd cann-ebpf-<version>
sh deploy/install.sh --dry-run
sh deploy/install.sh --apply --binary bin/cann-ebpf
```

安装后布局：`/opt/cann-ebpf/{bin/cann-ebpf,capture-once.sh,profiles,configs,docs}`，以及
`/etc/systemd/system/cann-ebpf.{service,timer}`（每 5 分钟一次短采集，oneshot，不是常驻 daemon）。

卸载 / 升级（升级失败自动回滚到 `/opt/cann-ebpf.bak.<UTC>`）：

```bash
sh deploy/uninstall.sh --dry-run --keep-data
sh deploy/uninstall.sh --apply --keep-data

sh deploy/upgrade.sh --dry-run --binary bin/cann-ebpf
sh deploy/upgrade.sh --apply   --binary bin/cann-ebpf
```

> 把二进制拷到目标机（示例，替换 `target-host`）：
> `scp dist/cann-ebpf-linux-arm64 root@target-host:/opt/cann-ebpf/bin/cann-ebpf`

完整运维说明见 `docs/OPERATIONS.md`。

---

## 3. preflight：只读环境检查

用途：attach 前检查内核/权限/追踪点/npu-smi 是否满足条件。**只读**，不 attach、不改 sysctl、不改服务。
失败返回退出码 4，避免“带病 attach”。

```bash
./cann-ebpf preflight
./cann-ebpf preflight --json
```

输出 schema `cann.preflight.v1`，每项 `pass` / `warn` / `fail`：

| 检查 | pass 条件 | 失败/告警含义 |
|---|---|---|
| privilege | root（或等价 CAP） | 非 root 需要 CAP_BPF/CAP_PERFMON/CAP_SYS_ADMIN |
| btf | `/sys/kernel/btf/vmlinux` 存在 | 缺失告警 |
| tracefs | `/sys/kernel/tracing` 或 debug 路径可读 | 缺失 fail |
| uprobe_events | 存在 | 缺失 fail，uprobe 不可用 |
| `sched/sched_switch` | tracepoint id 可读 | 缺失告警，blocking 降级 |
| `sched/sched_process_exit` | id 可读 | 同上 |
| `syscalls/sys_enter_futex` | id 可读 | 缺失告警，futex 指标缺失 |
| `syscalls/sys_exit_futex` | id 可读 | 同上 |
| perf_event_paranoid | ≤2 | >2 告警（root 仍可能可用） |
| lockdown | `[none]` | integrity/confidentiality fail |
| memlock | unlimited 或 ≥1GB | 偏小告警 |
| nofile | ≥4096 | 偏小告警 |
| npu-smi | 找到可执行文件 | 缺失告警，硬件层禁用 |
| unprivileged_bpf_disabled | 可读 | 信息项 |
| cgroup | v1/v2 可识别 | 信息项 |

退出码：`0` 无 fail（允许 warn）；`2` 参数错误；`4` 存在 fail，需先修复。

`attach` 默认 `--preflight enforce`：preflight 有 fail 会直接中止（退出码 4）。可用
`--preflight warn` 仅告警、`--preflight off` 跳过。

---

## 4. validate 与 discover（只读）

校验 profile/config：

```bash
./cann-ebpf validate --config profiles/vllm-ascend.yaml
./cann-ebpf validate --config profiles/orinfer.yaml
```

只读发现目标进程、库、符号和 offset（不 attach）：

```bash
./cann-ebpf discover --profile vllm-ascend --profiles-dir profiles --dry-run --json > discover.json
```

- `--pid` 指定 PID；省略时按 profile 选择器自动匹配（`vllm-ascend` 默认覆盖 Worker/EngineCore 等）。
- 缺失符号只作为 discovery 信息保留，不会创建假 attach。
- 想确认“profile 是否匹配目标进程”时用它，preflight 不负责这一步。

---

## 5. attach：host span 采集（核心命令）

### 5.1 最短可用命令

```bash
OUT="runs/ebpf-$(date -u +%Y%m%dT%H%M%SZ)"
./cann-ebpf attach \
  --pid auto \
  --profile vllm-ascend \
  --profiles-dir profiles \
  --duration 10s \
  --blocking on \
  --hardware on \
  --metrics auto \
  --output "$OUT" \
  --report auto
```

- `--pid auto`：按 profile 自动发现目标进程；也可显式 `--pid 1234,5678`，`--all` attach 所有匹配进程。
- `--duration`：采集窗口；**按需短窗口**，不要长时间跑（见 5.4）。
- `--output`：证据目录，**不覆盖旧目录**；省略时默认 `runs/ebpf-<unix>`。
- `--report auto`：采集 + teardown + 证据封存完成后，从磁盘重读 `spans.jsonl` 生成 `$OUT/report.html`，
  同一次采集的 `metrics.jsonl` 会自动叠加。生成前检查 `manifest.json` 的 `status=complete`；
  不完整时保留原始证据并返回非零，不把不完整 run 画成成功报告。

### 5.2 常用开关

| 开关 | 默认 | 作用 | 关闭/失败时 |
|---|---|---|---|
| `--blocking auto\|on\|off` | `auto`（跟随 profile） | 最外层 span 的 `offcpu_ns`/`futex_ns`（`sched_switch` + futex） | off-CPU 视图不可用，主 span 采集继续 |
| `--hardware auto\|on\|off` | `auto`（跟随 profile） | npu-smi 粗粒度硬件采样 → `hardware.jsonl` | `hardware.available=false` |
| `--hardware-interval` | profile 值（默认 10s） | 覆盖硬件采样间隔；npu-smi 单次约 1s，**不建议 <2s** | — |
| `--hardware-backend` | `npu-smi` | 选 `npu-smi` 或 `dcmi` | 后端不可用只写 warning，不影响 host span |
| `--metrics auto\|on\|off` | `auto` | 同窗口采集 `cann.metrics.v1`（DCMI/npu-smi/进程/cgroup/system/Prometheus） | 单个源失败只 warning，不丢 span |
| `--metrics-npu-backend` | `auto` | `auto/dcmi/npu-smi/both/off` | 显式后端缺失时报错 |
| `--prometheus [name=]URL` | 无 | 接入 vLLM/网关/exporter，可重复 | — |
| `--metrics-proc` / `--metrics-system` / `--metrics-cgroup` | 关闭/无 | 进程 / 主机 / cgroup v2 指标 | — |
| `--python-batch` | `false` | 纯 eBPF 只读 batch 探针，读 `SchedulerOutput.num_scheduled_tokens` 的 request ID 键 | 不开启就没有 batch 维度关联 |
| `--no-context` | `false` | 不启动插件 socket，只保留 eBPF span | 不能按插件 batch 下钻 |
| `--device-jsonl <path>` | 空 | 导入外部 msPTI `device.jsonl`（及 meta/pidmap） | 无 device 文件不生成设备级结论 |
| `--preflight enforce\|warn\|off` | `enforce` | attach 前环境门禁 | — |
| `--report off\|auto\|<path>` | `off` | 采集成功后生成 HTML | 仅报告失败返回 6，原始证据保留 |
| `--report-max-spans` | `20000` | 自动报告最多嵌入散点/off-CPU 行数，`0` 不限 | 只影响浏览器图表，统计仍用完整 JSONL |

指标与硬件同窗口采样的边界：**单个 metrics source 失败只写 warning**，不丢 span；已存在的同名
`metrics.jsonl`/`metrics.meta.json` 会拒绝覆盖；`hardware.jsonl` 与 metrics 的 npu-smi 采样可能重复，
需要时用 `--metrics-npu-backend dcmi` 或 `--hardware off`。

### 5.3 Python batch（无插件）

```bash
./cann-ebpf attach --pid auto --profile vllm-ascend --profiles-dir profiles \
  --duration 10s --python-batch --no-context --output runs/ebpf-batch
```

这是只读短时 uprobe：不写目标进程、不安装 Python 包、不重启服务，只读 `SchedulerOutput` 的 request ID 键
和 dict 身份。命中后每个 Worker 写一份 `python-batch-<pid>.json`，batch 窗口作为标准 `category=batch` span
写入同一份 `spans.jsonl`。

### 5.4 按时间窗口分批采集（强烈建议）

长时间 `attach` 会产生巨量 span，增加 teardown 成本并让 HTML 变重。经验规则：
**30–60 秒采 host span + metrics；5 分钟以上只采 metrics。**

| 窗口 | 方式 | 能拿到 | 代价 |
|---|---|---|---|
| 30–60s | `attach --metrics on` | host span、off-CPU、TopN、拓扑、基础指标 | 中等；报告默认抽样 20000 点 |
| 6 分钟 | `sample --duration 360s` | 变化点、短稳漂移 | 低；只写 metrics |
| 20–30 分钟 | `sample --duration 1200s/1800s` | KV cache、HBM/内存、队列长稳漂移 | 低；metrics 文件更大 |
| 4–6 档 × 30–60s | `sweep` + 固定 workload | 负载-延迟拐点 | 高；需独立压测窗口 |

长窗口建议用 `scripts/run_metrics_window.sh`（拒绝覆盖已有目录；`OUT` 必须在 `$ROOT/runs/` 下，
`BIN` 默认 `$ROOT/cann-ebpf-review`，按脚本头部约定设置）：

```bash
ROOT=<部署目录> BIN=<cann-ebpf 路径> \
  OUT=<部署目录>/runs/metrics-window-$(date -u +%Y%m%dT%H%M%SZ) \
  DURATION=360s sh scripts/run_metrics_window.sh
```

---

## 6. sample：独立指标采集

`sample` 只采指标，不 attach、不改 `spans.jsonl`/`device.jsonl`/`hardware.jsonl`。

```bash
# 默认源：dcmi_probe 可用则 DCMI，否则 npu-smi
./cann-ebpf sample --out runs/metrics-20261127T101500Z

# 指定进程 + cgroup + 主机 + vLLM Prometheus
./cann-ebpf sample \
  --out runs/metrics-$(date -u +%Y%m%dT%H%M%SZ) \
  --interval 1s --duration 60s \
  --npu-smi --system \
  --pid 1452 --pid 1453 \
  --cgroup /sys/fs/cgroup/vllm.slice \
  --prometheus vllm=http://target-ip:8000/metrics \
  --include '^vllm:'
```

主要参数：

| 参数 | 默认 | 说明 |
|---|---|---|
| `--out` | 必填 | `metrics.jsonl` + `metrics.meta.json` 输出目录，拒绝覆盖已有文件 |
| `--interval` | `1s` | 基础采样间隔 |
| `--duration` | `0` | `0` 表示直到 SIGINT/SIGTERM |
| `--dcmi` / `--npu-smi` | `false` | NPU 指标后端（npu-smi 最小间隔 5s） |
| `--system` | `false` | 主机 CPU/load/memory/PSI/disk/net/thermal/frequency |
| `--pid` | 可重复 | `/proc` 进程指标 |
| `--cgroup` | 可重复 | cgroup v2 路径 |
| `--prometheus` / `--include` | 无 | Prometheus 端点 / 指标名 allowlist 正则，可重复 |

输出：`metrics.jsonl`（一行一个 `cann.metrics.v1` 采样点）、`metrics.meta.json`（窗口、series、时钟、
SHA-256、状态）。

退出码：`0` 正常（至少一条合法样本，部分源失败仅 warning）；`2` 参数错误；`4` IO/采集失败或非 cancel
情况下零样本。收到 SIGINT/SIGTERM 优雅退出，输出有效时退出码 0、`status=canceled`。

指标契约见 `docs/METRICS-SCHEMA.md`。

---

## 7. report：离线单文件 HTML

```bash
# 读 run 目录（spans.jsonl，可选 device.jsonl/hardware.jsonl）生成 HTML
./cann-ebpf report --input runs/ebpf-20261126T000000Z --out report.html

# 叠加独立 metrics 采集（目录或 metrics.jsonl 都行；省略时自动找同目录 metrics.jsonl）
./cann-ebpf report --input runs/ebpf-... --metrics runs/metrics-... --out report.html

# 只有 metrics 的降级报告
./cann-ebpf report --metrics runs/metrics-... --out report.html

# 带 sweep / 可选证据
./cann-ebpf report --input "$OUT" --metrics "$OUT/metrics.jsonl" \
  --sweep "$OUT/sweep.json" --out "$OUT/report.html"
```

报告是纯静态 HTML + 原生 Canvas，**无外部 CDN/JS 依赖**，可离线打开或直接发给他人；只读派生自既有
证据，不修改 `spans.jsonl`/`device.jsonl`/`hardware.jsonl`/`metrics.jsonl`。

常用参数：`--input`（run 目录或 spans.jsonl）、`--metrics`、`--out`（默认 `report.html`）、
`--slow-threshold`（默认 1ms）、`--min-duration`（默认 50us）、`--top`、`--max-lanes`、
`--max-spans`（默认 20000，`0` 全量）、`--buckets`、`--max-series`。

可选证据开关（缺失时对应视图不可用，**不补造**）：

| 参数 | 数据 | 驱动的视图 |
|---|---|---|
| `--sweep sweep.json` | 负载扫描清单 | 负载-延迟拐点 |
| `--ops ops.jsonl` | 每算子 FLOPs/bytes + 显式峰值 | Roofline |
| `--events events.jsonl` | 部署/扩缩容/故障标注 | 变化点/异常带 |
| `--offcpu offcpu.jsonl` | 精确 off-CPU 区间 | off-CPU ribbon 精确模式 |
| `--otel otel.jsonl` | 跨服务 trace | 服务拓扑 |
| `--ab-a` / `--ab-b` / `--ab-workload-id` | 两次同 workload run | A/B 差分 |
| `--roofline-peak-tflops` / `--roofline-peak-gbps` / `--roofline-dtype` | 已验证峰值 | Roofline 点位 |
| `--stacks <run 或 folded>` | `stacks.folded` | kernel 栈火焰图（契约，未上机验证） |

无数据/置信度语义：

- `available=true` 表示数据前提满足；`confidence=exact` 才是有精确设备/区间证据，`inferred` 表示由 host
  或时间关系推断，`partial` 表示窗口/背景负载/样本不完整。
- `available=false` 必须给出 `reason`；不画空坐标轴、不生成示意曲线、不把 0 当作“没发生”。
- 缺 `device.jsonl` → 设备四象限/设备忙率不可用；只有 metrics → metrics-only 降级报告；都没有 → 不画空图。

退出码：`0` 成功；`2` 参数错误；`3` 构建/渲染失败；`4` 证据读取失败。

---

## 8. sweep：并发压测编排

`sweep` 是**独立的压测编排脚本**（`scripts/run_sweep.sh`），不是 `report` 的一部分，分两阶段：

1. **生产阶段**：按并发档跑固定负载 + `sample` 采 metrics，写 `cN/load.json`、`cN/metrics.jsonl`，
   最后汇总 `sweep.json`；
2. **消费阶段**：只有显式给 `--report` 才调用 `cann-ebpf report` 出 HTML；不给就只生产证据。

```bash
# 只生产 sweep 证据（不发报告）；默认 dry-run，加 --yes 才真正发负载
sh scripts/run_sweep.sh \
  --workdir runs/sweep-$(date -u +%Y%m%dT%H%M%SZ) \
  --tiers 1,2,4,8 --duration 30 --max-tokens 64 \
  --base-url http://target-host:8905 --model <served-model> \
  --api-key "$VLLM_API_KEY" --bin ./cann-ebpf --yes

# 生产 + 消费，一条命令出报告
sh scripts/run_sweep.sh \
  --workdir runs/sweep-$(date -u +%Y%m%dT%H%M%SZ) \
  --run runs/ebpf-20261127T174000Z \
  --report runs/ebpf-20261127T174000Z/report-with-sweep.html \
  --tiers 1,2,4,8 --duration 30 --max-tokens 64 \
  --base-url http://target-host:8905 --model <served-model> \
  --api-key "$VLLM_API_KEY" --bin ./cann-ebpf --yes
```

参数：`--workdir`（证据目录）、`--run`（已有 host run，仅 `--report` 时需要）、`--report`（可选，
`auto` 写到 `$RUN/report.html`）、`--tiers`（≥4 档）、`--duration`、`--max-tokens`、
`--base-url`/`--model`/`--api-key`、`--yes`（真正发负载）。

产物：

```text
runs/sweep-<UTC>/
├── sweep.json
├── c1/{metrics.jsonl,load.json}
├── c2/{metrics.jsonl,load.json}
├── c4/{metrics.jsonl,load.json}
└── c8/{metrics.jsonl,load.json}
```

注意：

- **不需要停服务**，但会给服务加测试负载；生产环境请在低峰/授权后执行；
- 每档必须有 `metrics.jsonl` + `load.json`，否则该档缺失；
- 若服务端 `running` 高于客户端并发，报告会标 `partial` 并**拒绝计算容量拐点**；
- `report` 本身只读，不发负载；只有 sweep 的生产阶段发负载。

---

## 9. 可选能力的前提（device / ops / HBM / stacks 等）

### 9.1 device 时间线（`device.jsonl`，需要维护窗口）

- 数据由容器内 **msPTI in-process collector**（`m3/mspti_collector`）产出，schema `cann.device.v1`
  （`API`/`KERNEL`/`MEMCPY`/`HCCL`，含 `correlationId`）。
- msPTI 在同一进程只允许一个 subscriber，且必须在 ACL/torch_npu 初始化前订阅；生产启用只能通过
  **重启服务、容器入口 wrapper 或 `LD_PRELOAD`**（维护窗口）。不要在运行中的容器里直接注入。
- 导入：`attach --device-jsonl /path/device.jsonl`（会抓 `pidmap.json`，让进程退出后仍可复现 join）。
- `diagnose` 按 `(pid,tid,start±100µs)` 匹配 API record，再用 `correlationId` 关联 KERNEL/MEMCPY/HCCL；
  只有存在设备证据才输出 `device_bound`/`launch_starvation`/`copy_bound`/`communication_bound`。
- 当前只在**合成 ACL** 上验证；`msprof --pid` 动态采集已确认段错误。**没有真实 device 证据时，不要写设备侧结论。**

### 9.2 ops（Roofline）

- 需要 `--ops ops.jsonl` 提供每算子 `flops`、`bytes`、`duration`、`dtype`；
- 还需显式、已验证的 `--roofline-peak-tflops` / `--roofline-peak-gbps`；
- 二者缺一就不画 Roofline 点（不是“数据不全也能画”）。

### 9.3 HBM / 硬件（`--hardware`，npu-smi）

- 前提：目标机有 `npu-smi`（host 与容器内均可）。`--hardware on` 时写 `hardware.jsonl`。
- 内容是**粗粒度、wall-clock、run 级**快照：HBM used/free、AICore 利用率、功耗、温度、health。
  它是**整卡**值，**不能逐调用 join**，也不能归因到单个 Worker。
- `hbm_pressure` 判定需要两个条件同时满足：最低 HBM free% < 阈值（默认 5）**且**存在超慢阈值的
  `category=memory` span；只满足一个不会输出。
- 短窗口（10s）通常只有 2 个采样点，不能代表峰值；细粒度需降 `interval_s`，但要先确认 npu-smi 开销。
- 真实 HBM 带宽、ECC、频率等指标采集端还没有，**不要承诺**。

### 9.4 其它可选证据

| 能力 | 需要的输入 | 状态 |
|---|---|---|
| 变化点/异常带 | `events.jsonl` + 长时序 metrics（≥5 分钟、≥30 个有效点） | 开箱即用但需长窗口 |
| 跨服务拓扑 | `otel.jsonl`（OTel trace/service/parent） | 需外部 OTel 导出；缺失只能进程级拓扑 |
| A/B 差分 | 两个完整 run，`workload_id` 一致 | 需两次采集 |
| vLLM 插件上下文 | vLLM general plugin 安装 + 重启激活 | **未安装/未激活** |
| kernel 栈火焰图 | `stacks` + `report --stacks` | A4 契约，**未在昇腾服务器验证**，示例计数非实测 |

---

## 10. 输出文件

一次 `attach` 的 run 目录（`$OUT`）：

| 文件 | 内容 |
|---|---|
| `spans.jsonl` | 原始 host span，一行一个对象（37 函数、entry/exit、可选 `offcpu_ns`/`futex_ns`） |
| `manifest.json` | 运行环境、目标 PID、库 inode/size/mtime、统计、warnings、artifacts SHA-256、`status` |
| `summary.json` / `summary.md` | 派生 function/category/PID/batch/trace 汇总（人类可读 + 机器可读） |
| `metrics.jsonl` | `cann.metrics.v1` 指标采样点（开启 `--metrics` 时） |
| `metrics.meta.json` | 采集窗口、series、时钟、SHA-256、状态 |
| `hardware.jsonl` | npu-smi 8 设备 HBM/AICore/功耗/温度（`--hardware on`） |
| `python-batch-<pid>.json` | 纯 eBPF batch 的逐 Worker 统计、类型确认、warnings（`--python-batch`） |
| `device.jsonl` / `device.jsonl.meta.json` | 导入的 msPTI 设备时间线与 collector 元数据（`--device-jsonl`） |
| `pidmap.json` | 容器 local→host pid/tid 快照 `cann.pidmap.v1` |
| `report.html` | `--report auto` 生成的离线报告 |

`sample` 的 run 目录：`metrics.jsonl` + `metrics.meta.json`。
`report` 的输出：单个 `report.html`。
`sweep` 的产物：`sweep.json` + 每档 `cN/{metrics.jsonl,load.json}`。
`diagnose --out`：`diagnose.json` / `diagnose.md`（`cann.diagnose.report.v1`）。

原始值以 `spans.jsonl`、`manifest.json` 为准。summary 不补造时间、不改原始事件；内存缓冲达上限时从
完整 JSONL 重新生成 summary，并在 warnings 显式记录。

---

## 11. 常见问题（FAQ）

**Q1. `preflight` 返回 4（fail）怎么办？**
先看哪一项 fail：`lockdown` 非 `[none]`、`tracefs`/`uprobe_events` 缺失通常需系统配置或换环境；
`perf_event_paranoid>2`、`memlock`/`nofile` 偏小是告警，root 下可能仍可用。修好前不要 attach。

**Q2. `attach` 报 “automatic discovery found no target” 或 PID 不匹配？**
用 `discover --dry-run` 看 profile 是否匹配目标进程；确认目标服务在跑、`--profile` 选对、容器内 PID
与 host PID 没混用。必要时显式 `--pid`。

**Q3. 报告里没有设备四象限 / 设备忙率？**
需要 `device.jsonl` 并成功 join。没有设备证据时视图应为 `unavailable`，这是设计行为，不是 bug；
不要用 host 时间冒充设备执行。

**Q4. 报告里某些视图显示 `unavailable` / `partial` / `inferred`？**
这是**证据门禁**：缺 sweep/device/ops/OTel/A-B/长时序等输入就不绘制造假曲线。补齐对应证据再重跑
`report`，或接受该视图不可用。

**Q5. `metrics.jsonl` 已存在，attach/sample 报错？**
工具用 `O_EXCL` 拒绝覆盖原始证据。换一个新的 `--output`/`--out` 目录。

**Q6. 10 秒采集只有 2 个硬件采样点，温度/功耗看着不对？**
硬件是周期 wall-clock 快照，短窗口点少、不代表峰值。需要更细粒度时降 `--hardware-interval`，但要先
确认 npu-smi 单次约 1s 的开销。

**Q7. 采集很久，spans 巨大 / HTML 很重？**
按第 5.4 节：30–60s 采 span+metrics，5 分钟以上只采 metrics；或调低 `--report-max-spans`。

**Q8. 退出码怎么读？**
`0` 成功；`2` 参数/用法错误；`3` 未发现目标/证据不满足（`report` 构建失败也用它）；`4` preflight
fail / IO / 零样本；`5` attach 采集失败；`6` 自动报告生成失败（原始证据保留，可手动 `report` 重试）；
`7` 安装/升级 SHA-256 校验失败。

**Q9. `blocking` 打开了但没有 `offcpu_ns` 字段？**
只有最外层 span 采样 blocking；嵌套 span 不采（避免父子重复累计）。字段缺失=未测量，`0`=测得 0。

**Q10. `python-batch` 能推出单次调用耗时吗？**
不能。它只读 `SchedulerOutput.num_scheduled_tokens` 的 request ID 键与 dict 身份，用于 batch 窗口关联，
不能推导单次调用执行时长。

---

## 12. 权限与安全注意事项

- **只读优先**：不停止、不重启目标服务（V4/Orinfer），不修改目标进程、容器、CANN 库、驱动或 sysctl。
- **权限**：需要 root 或 `CAP_BPF`/`CAP_PERFMON`/`CAP_SYS_ADMIN`；非 root 部署未验证。`deploy/*.sh`
  的 `--apply` 需 root，`--dry-run` 不需要。
- **不覆盖证据**：每次运行生成新目录；脚本会拒绝写入/覆盖已有 run 目录和 `metrics.jsonl`。
- **不写业务内容**：不把 prompt、响应、token tensor 或业务文本写入上下文协议；插件字段白名单解码。
- **上下文 socket 权限**：只接受 collector UID 和显式目标 UID；目录 `0755`，socket 默认 `0600`；
  目标为非 root 时 chown 给目标 UID/GID 并设 `0660`。
- **失败即如实**：保留真实错误、warnings、dropped events 和 `status=error`，不伪造成功。
- **维护窗口操作**：msPTI collector 注入、插件激活会重启服务，必须在授权维护窗口执行，并准备好回滚。
- **不要在文档/提交里写真实主机名或 IP**：用 `target-host` / `target-ip`；本仓库文档不应包含真实访问地址。

---

## 13. 相关文档

- `README.md`：项目总览、图表总览、当前状态与已知边界。
- `docs/PREFLIGHT.md`：preflight 检查项、退出码与实测结果。
- `docs/OPERATIONS.md`：安装/卸载/升级/systemd/固定负载/发布包。
- `docs/HANDOFF.md`：交接总览、已完成/未完成、下一步与硬约束。
- `docs/METRICS-SCHEMA.md`：`sample` 命令、`cann.metrics.v1` 契约与退出码。
- `docs/HARDWARE.md`：npu-smi 硬件采样、schema 与边界。
- `docs/M3-DEVICE-JOIN.md` / `docs/M3-COLLECTOR.md`：device 时间线 schema、join 与 collector 注入。
- `docs/STACKS-FLAMEGRAPH.md`：kernel 栈火焰图契约（未上机验证）。
- `docs/DIAGNOSE.md`：`diagnose` 用法、时间窗语义与判定规则。
- `docs/PRODUCT.md`：成品说明（能力矩阵与已验证/未验证边界）。

---

## 附录 A 参数速查

以当前 `cmd/cann-ebpf` 的 `flag` 定义为准；可重复的 flag 可以传多次。`--help` 是最新来源。

### A.1 `attach`

| 参数 | 默认值/取值 | 作用 |
|---|---|---|
| `--pid` | 空（等价 `auto`） | 逗号分隔 PID；`auto` 按 profile 自动发现 |
| `--all` | `false` | attach 所有匹配 profile 的进程 |
| `--profile` | `vllm-ascend` | profile 名或路径 |
| `--profiles-dir` | `profiles` | profile 搜索目录 |
| `--proc-root` | `/proc` | 只读进程发现使用的 procfs 根 |
| `--duration` | `10s` | 采集时长 |
| `--output` | `runs/ebpf-<unix>` | 输出目录，不覆盖旧证据 |
| `--min-duration` | `0` | 丢弃短于该时长的 span |
| `--context-socket` | 空（使用 profile socket） | 覆盖 vLLM batch/request 上下文 socket |
| `--no-context` | `false` | 不启动插件 socket，仅保留 eBPF span 和可选 python batch |
| `--python-batch` | `false` | 启用只读纯 eBPF `SchedulerOutput` batch 探针 |
| `--python-batch-lib` | 自动解析 | 显式指定 `libpython3.12.so` |
| `--python-batch-offsets` | 内嵌固定 profile | CPython/vLLM offset bundle JSON |
| `--python-batch-max-events` | `20000` | 每个 Worker 保留的最大 batch ring-buffer 事件数 |
| `--python-batch-require-type` | `SchedulerOutput` | 要求 batch owner 类型包含的字符串 |
| `--blocking` | `auto`（`auto/on/off`） | off-CPU/futex 采样开关；`auto` 跟随 profile |
| `--hardware` | `auto`（`auto/on/off`） | 粗粒度 NPU 硬件采样开关；`auto` 跟随 profile |
| `--hardware-interval` | `0`（使用 profile 值） | 覆盖硬件采样间隔 |
| `--hardware-backend` | 空（默认 `npu-smi`） | `npu-smi` 或 `dcmi` |
| `--metrics` | `auto`（`auto/on/off`） | 同窗口指标采集开关 |
| `--metrics-npu-backend` | `auto` | `auto/dcmi/npu-smi/both/off` |
| `--metrics-interval` | `1s` | DCMI/进程/cgroup/system/Prometheus 的基础间隔 |
| `--metrics-proc` / `--metrics-system` | `false` | 采目标进程 `/proc` 指标 / 主机指标 |
| `--metrics-cgroup` | 可重复 | cgroup v2 路径 |
| `--prometheus` | 可重复，`[name=]http(s)://host/metrics` | Prometheus 端点 |
| `--prometheus-include` | 可重复，正则 | Prometheus metric family allowlist |
| `--device-jsonl` | 空 | 导入外部 msPTI `device.jsonl` 及同名 meta/pidmap |
| `--preflight` | `enforce`（`enforce/warn/off`） | attach 前环境门禁 |
| `--report` | `off`（`off/auto/<path>`） | 采集成功后生成 HTML；`auto` 写到 run 目录 `report.html` |
| `--report-title` | 空（自动标题） | 自动报告标题覆盖 |
| `--report-max-spans` | `20000` | 自动报告最多嵌入的散点/off-CPU 行数；`0` 不限制 |
| `--json` | `false` | 输出 JSON 采集摘要 |

### A.2 `report`

| 参数 | 默认值/取值 | 作用 |
|---|---|---|
| `--input` | 空；与 `--metrics`/`--stacks` 至少给一个 | run 目录或 `spans.jsonl` |
| `--metrics` | 空；目录或 `metrics.jsonl`；省略时自动找同目录 `metrics.jsonl` | metrics 数据 |
| `--stacks` | 空 | `stacks` run 目录或 `stacks.folded`（火焰图） |
| `--out` | `report.html` | 单文件离线 HTML 输出路径 |
| `--title` | 空（自动标题） | 报告标题 |
| `--min-duration` | `50us` | 时间轴最小显示时长 |
| `--slow-threshold` | `1ms` | slow span 阈值 |
| `--top` | `15` | TopN 函数数量 |
| `--max-lanes` | `64` | 时间轴最多嵌入的 TID 泳道数 |
| `--max-spans` | `20000` | 最多嵌入的散点/off-CPU 行数；`0` 保留全部 |
| `--buckets` | `160` | 时间桶数量 |
| `--device-mode` | `matched`（`matched/none`） | 是否嵌入匹配的设备时间线 |
| `--device-container-id` | 空 | 无 pidmap 时用容器 ID 翻译 device PID/TID |
| `--max-series` | `256` | 最多嵌入的 metric series 数 |
| `--sweep` | 空 | `sweep.json` 负载扫描清单 |
| `--ops` | 空 | 含 per-op FLOPs/bytes 的 `ops.jsonl` |
| `--events` | 空 | 部署、扩缩容或故障标注 `events.jsonl` |
| `--offcpu` | 空 | 精确 off-CPU 区间 `offcpu.jsonl` |
| `--otel` | 空 | 跨服务 OTel span `otel.jsonl` |
| `--ab-a`、`--ab-b` | 空；必须成对 | A/B 两个 run 目录 |
| `--ab-workload-id` | 空 | 两侧共享的固定 workload ID |
| `--roofline-peak-tflops` | `0` | 已验证的参考峰值 TFLOPS |
| `--roofline-peak-gbps` | `0` | 已验证的参考峰值 GB/s |
| `--roofline-dtype` | 空 | 与峰值参数对应的 dtype |
| `--topology-root-service` | 空 | 可选的观测根服务 |

### A.3 `sample`

| 参数 | 默认值/取值 | 作用 |
|---|---|---|
| `--out` | 必填 | `metrics.jsonl`、`metrics.meta.json` 的输出目录 |
| `--interval` | `1s` | 基础采样间隔 |
| `--duration` | `0` | 采集时长；`0` 表示直到 SIGINT/SIGTERM |
| `--dcmi` | `false` | 使用 DCMI helper |
| `--npu-smi` | `false` | 用 `npu-smi` 采 NPU 利用率/HBM/功耗/温度；最小间隔 5s |
| `--system` | `false` | 主机 CPU、load、内存、PSI、磁盘、网络、温度、频率 |
| `--pid` | 可重复 | `/proc` 进程 CPU、RSS、线程、fd、调度延迟、上下文切换、I/O |
| `--cgroup` | 可重复，cgroup v2 路径 | 容器 CPU、throttle、内存、OOM、PID、PSI |
| `--prometheus` | 可重复，`[name=]http(s)://.../metrics` | 接入 vLLM、网关或其他 exporter |
| `--include` | 可重复，正则 | Prometheus 指标名 allowlist |
| `--json` | `false` | 输出 JSON 采集摘要 |

### A.4 其余命令

| 命令 | 关键参数 | 说明 |
|---|---|---|
| `validate` | `--config`、`--json` | 校验配置/profile |
| `preflight` | `--json` | 只读检查根权限、tracefs、BTF、BPF 和 NPU 工具 |
| `discover` | `--pid`、`--profile`、`--profiles-dir`、`--config`、`--proc-root`、`--max-processes`、`--json`、`--dry-run` | 只读列出 PID、库、符号和 offset |
| `attach-tracefs` | `--pid`、`--profile`、`--profiles-dir`、`--duration`、`--output`、`--only`、`--dry-run`、`--json` | 无 uprobe 条件下的 tracefs 后备采集 |
| `trace` | `--input`、`--trace-id`、`--limit`、`--json` | 按 trace_id/batch/parent 聚合链路树 |
| `diagnose` | `--input`、`--run`、`--start-ns`、`--end-ns`、`--last`、`--pid`、`--tid`、`--batch-id`、`--request-id`、`--trace-id`、`--top`、`--slow-threshold`、`--offcpu-ratio`、`--futex-ratio`、`--hbm-free-pct`、`--device-tolerance`、`--device-container-id`、`--json`、`--out` | 只读定位：慢函数、阻塞、硬件和 device verdict |
| `metrics` | `--input`、`--listen`、`--out` | 导出 Prometheus text，或启动 `/metrics` 服务 |
| `stacks` | `--pid`、`--profile`、`--duration`、`--freq`、`--out`、`--raw`、`--kernel-filter`、`--symbols` | 内核栈采样（契约实现，未上机验证） |
| `version` | 无 | 输出版本、平台和构建信息 |

