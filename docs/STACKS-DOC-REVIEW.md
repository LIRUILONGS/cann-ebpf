# cann-ebpf 内核栈火焰图文档一致性核对

> 日期：2026-11-27
> 范围：`README.md`、`docs/STACKS-FLAMEGRAPH.md`、`docs/STACKS-ACCEPTANCE.md`、`docs/STACKS-QUICKSTART.md`、`cmd/cann-ebpf/stacks.go`、`cmd/cann-ebpf/report.go`、`internal/stacks/*`、`internal/report/*`、`scripts/stacks_preflight.sh` 及现有 `.analysis/stacks-probe` 证据。
> 方法：用当前源码构建的临时二进制实测 `stacks --help`、`report --help`、参数错误和报告降级路径；逐项对照代码常量、JSON 字段、脚本检查项和现有产物。审计时未修改现有文档、源码或 profile；后续修正见下。

> 后续修正（2026-11-27）：README 的 stacks 二进制名、1420/9 基线、NPU 帧口径已按 final5/1488/6/4.61% 更新；STACKS-FLAMEGRAPH 与 STACKS-ACCEPTANCE 已加 final2 历史探针说明。本文 P1-1/P1-2/P1-3/P1-4/P1-5 的文档问题已部分修复；代码 P0 仍未修。

## 0. 结论摘要

- `docs/STACKS-QUICKSTART.md` 已按 final5 交付二进制和 final4-classified 新口径编写；两套 NPU 口径（final2 的 0.9917%、final4 的 0.86% 旧分类 / 4.61% 新分类）已分开说明。`report --symbols` 未出现在 Quickstart 的 report 命令中。
- 现有 README 的 stacks 章节仍有错误二进制名、过期“没有调用栈采样”表述和 NPU 指标措辞问题；STACKS-FLAMEGRAPH 的状态声明与其自身实机基线矛盾；STACKS-ACCEPTANCE 的门槛文字与 CLI 实际行为不完全一致。
- 代码侧有 3 个会影响交付结论的问题：低/零符号覆盖仍把火焰图标为 `available`；`stacks` 在 `samples=0` 时仍返回 0；BPF `lost` 计数没有覆盖 `stack_counts` 插入/查找失败。
- **final4 二进制已废弃，final5 为交付二进制。** final4 的原始 `stacks.folded` / `stacks.meta.json` 证据仍可复用；废弃的是 final4 二进制和旧硬编码分类报告。

## 1. 交付二进制与证据口径

| 对象 | 值 | 结论 |
|---|---|---|
| 交付二进制 | `dist/cann-ebpf-linux-arm64-stacks-final5` | **使用此文件** |
| final5 SHA256 | `156df5a79d25a7d4e9733e382c0258b9c944579e275f061e6aef2f776e383cac` | 与任务给定值一致；大小 15,581,010 bytes，ELF 64-bit ARM aarch64 static |
| final4 二进制 | `dist/cann-ebpf-linux-arm64-stacks-final4`，SHA256 `f933564c2ba17ace47288f552ca879db409f4c2b394706df9b0e7ca13ce9cf7e` | **已废弃，不能用于生成 4.61% 最终报告**；不含共享分类/空面板修复 |
| final4 原始证据 | `.analysis/stacks-probe/final4/stacks.folded`、`stacks.meta.json` | 可继续作为原始证据，用 final5 对其生成新报告 |
| final4 旧验证记录 | `.analysis/stacks-probe/final4/VERIFICATION.md:11`、`:13` 指向 final4 二进制/SHA；`:181`、`:216` 写 0.86%；`:217` 是旧 Top NPU 结果 | 过期记录，不可作为最终口径引用；新口径见 `VERIFICATION-classified.md` |
| 旧 3 秒探针 | `.analysis/stacks-probe/stacks.meta.json`：samples=62、unique_stacks=30、stack_fail=484（-14 EFAULT）、duration_s=3.003557349 | **不是最终基线**，只用于 schema/失败模式参照 |

## 2. 总一致性核对表（文档声明 vs 代码/实测事实）

| 核对项 | 文档声明 | 代码/实测事实 | 结论 |
|---|---|---|---|
| stacks 子命令二进制 | README 的 stacks 示例使用 `cann-ebpf-linux-arm64-m3`（`README.md:555`、`:562`、`:565`、`:588`、`:589`） | 当前交付二进制是 `dist/cann-ebpf-linux-arm64-stacks-final5`；m3 二进制实测没有 `main.cmdStacks` | **不一致，P1**；README 命令会失败，Quickstart 已用 final5 |
| stacks flags | Quickstart 列出 `--pid`、`--profile`、`--duration`、`--freq`、`--kernel-filter`、`--raw`、`--symbols`、`--out` | `cmd/cann-ebpf/stacks.go:78-85` 与实测 `stacks --help` 均有这 8 个 flag | **一致** |
| report `--symbols` | 当前 report 文档/命令不应使用；Quickstart `docs/STACKS-QUICKSTART.md:113` 明确“report 没有 --symbols” | `cmd/cann-ebpf/report.go:20-45` 无该 flag；实测 `report ... --symbols` 输出 `flag provided but not defined: -symbols`，退出 2 | **一致；report 确实没有 --symbols** |
| run 目录布局 | STACKS-FLAMEGRAPH `:95-103`：`--out` 是扁平 run 目录，直接放 `stacks.folded`、`stacks.meta.json`、可选 `stacks.jsonl` | `internal/stacks/types.go:17-34`、`internal/stacks/run.go:20-42`；Quickstart 第 3 节也用 `RUN="$EVID/run"` | **一致** |
| meta schema | STACKS-FLAMEGRAPH `:157-178` 列出 schema/tool_version/collector/host/kernel/arch/started_at/ended_at/duration_s/frequency_hz/target_pids/kernel_filter/samples/unique_stacks/stack_fail/filtered/lost/symbol_coverage/unknown_pct/kallsyms_sha256/warnings | `internal/stacks/types.go:74-96` 字段名与 JSON tag 完全对应；`schema=cann.stacks.v1`（`types.go:18-19`） | **基本一致**；`kernel_filter` 在代码中无 `omitempty`，文档写“可省略”不准确（P2） |
| threads / processes | STACKS-ACCEPTANCE `:258` 记录“1357 threads / 10 processes” | `stacks.meta.json` **没有** `threads`/`processes` 字段；它们只来自采集输出/warnings 文本（旧探针 warning 为 `sampling 134 threads across 1 processes`） | **文档展示值可以，但不能当 schema 字段；P2** |
| 语义边界 | README `:547-549`、FLAMEGRAPH `:51-54`、Quickstart 第 0 节均写 host kernel on-CPU-only，不覆盖用户态/AI Core | `internal/stacks/collector_linux.go` 设置 `exclude_user=1`、`exclude_hv=1`；BPF 采集 `bpf_get_stackid` 内核栈 | **主体一致**；README `:595` 的 NPU 指标措辞错误（P1） |
| 验收阈值 | ACCEPTANCE `:125-131`：samples>1000、stack_fail/samples<1%、samples=0 失败、coverage>=0.80、unknown<20、lost=0、folded sum==samples | CLI 和 preflight 不会自动执行这些阈值；`stacks` 仅在 coverage<0.5 时打 warning；report 低覆盖仍 `available`；`samples=0` 返回 0 | **不一致，P0** |
| 退出码 | README `:580` 仅写 stacks：参数 2、采集/加载 3、符号化/写盘 4 | `cmd/cann-ebpf/stacks.go:172-175`、`:186-195`、`:215-219` 对应；report 的退出码只在 `cmd/cann-ebpf/report.go` 中，README 没写 | **stacks 一致；report 文档缺失，P2** |
| 报告可用性 | ACCEPTANCE `:127` 要求低覆盖标 `symbolization_incomplete`，`不得显示虚假完整图`；FLAMEGRAPH `:297` 要求 nonzero=0 时 `symbolization=unavailable` | `internal/report/flamegraph.go:275` 无条件 `data.Status.Available = true`；实测 coverage=0、unknown=100 仍 `flamegraph=available` | **不一致，P0，影响交付结论** |
| 1% 开销承诺 | FLAMEGRAPH `:338-341`、ACCEPTANCE `:267-268` 明确不能承诺 `<1%` 开销 | 文档没有承诺；Quickstart 也写“不要承诺 <1% 开销” | **一致，合格** |
| 缺数据画图 | README `:597-598` 要求 SVG 失败或无数据时显示不可用原因而不是空图 | report 缺 folded 但有 meta 时返回 0、`flamegraph=unavailable`；samples=0 时 `unavailable`；但低覆盖 bug 仍画图 | **主体合格；“低覆盖仍画图”是 P0 例外** |

## 3. CLI flag 逐项核对

### 3.1 `cann-ebpf stacks --help`（实际退出 0）

| flag | 代码位置 | 实际语义 | README | STACKS-FLAMEGRAPH | STACKS-ACCEPTANCE |
|---|---|---|---|---|---|
| `--pid` | `cmd/cann-ebpf/stacks.go:78` | 可重复；支持 `auto`；不能与显式 PID 混用；无 PID 报错 | 有（`:556`、`:562`） | 有（`:218`、`:238`、`:246`） | 有（`:87`、`:97`） |
| `--profile` | `stacks.go:79` | 默认 `vllm-ascend`，仅用于 `--pid auto` 发现 | 有（`:562`） | **未写** | **未写** |
| `--duration` | `stacks.go:80` | 默认 `30s`，必须 `>0` | 有（`:557`） | 有（`:239`） | 有（`:88`） |
| `--freq` | `stacks.go:81` | 默认 `99`，范围 1..999 | 有（`:557`） | 有（`:240`） | 有（`:89`） |
| `--kernel-filter` | `stacks.go:84` | `all` 或 `npu` | 有（`:575`） | 有（`:241`、`:251`） | 有（`:90`） |
| `--raw` | `stacks.go:83` | 额外写 `stacks.jsonl` | 有（`:551`、`:565`） | 有（`:102`、`:248`） | 有（`:100`） |
| `--symbols` | `stacks.go:85` | 默认 `profiles/kernel-symbols.yaml`；文件缺失用内置规则 | 有（`:566`） | 有（`:242`） | 有（`:91`） |
| `--out` | `stacks.go:82` | 默认 `runs/stacks-<UTC timestamp>`；扁平 run 目录 | 有（`:558`） | 有（`:243`） | 有（`:92`） |

**代码有但 stack-specific 文档没写的 flag：`--profile`**（README 有，FLAMEGRAPH/ACCEPTANCE 没有）。它不是 10 秒显式 PID 验收链路的必需项，属 P2 文档补齐项。

参数错误实测（均退出 2）：

- 不传 `--pid`：`at least one --pid is required`
- `--duration 0s`：`--duration must be positive`
- `--freq 0`：`--freq must be in 1..999`
- `--pid auto` 加显式 PID：`--pid auto cannot be combined with explicit --pid values`

### 3.2 `cann-ebpf report --help`（实际退出 2）

`cmd/cann-ebpf/report.go:20-45` 的真实 flags（共 26 个）：

```text
--input --metrics --stacks --out --title --min-duration --slow-threshold --top
--max-lanes --max-spans --buckets --device-mode --device-container-id --max-series
--sweep --ops --events --offcpu --otel --ab-a --ab-b --ab-workload-id
--roofline-peak-tflops --roofline-peak-gbps --roofline-dtype --topology-root-service
```

- stacks 相关文档只使用 `--stacks`、`--out`、`--input`；这三个都真实存在。
- 其余 flags 是通用 report 功能，stacks 文档未提属正常；但按“代码里有但文档没写要列出”的要求，在此完整列出。
- **report 无 `--symbols`**。实测命令与结果：

```text
report --stacks <dummy> --symbols profiles/kernel-symbols.yaml
flag provided but not defined: -symbols
EXIT=2
```

- `report --help` 本身返回 2（`report.go:46-48` 把 `flag.ErrHelp` 当解析错误返回），与 `stacks --help` 返回 0 不同。脚本不能用“`report --help` 成功”判断二进制可用；P2。

## 4. 产物路径与 stacks.meta.json schema

### 4.1 产物路径

`--out` 是**扁平 run 目录**，不是 `--out/stacks/`：

```text
runs/stacks-<UTC timestamp>/
  stacks.folded        # 必需；root-first，分号分隔，末列 count
  stacks.meta.json     # 必需；schema=cann.stacks.v1
  stacks.jsonl         # 仅 --raw；原始地址
  report.html          # 可选派生物，建议写到 run 外
```

代码依据：`internal/stacks/types.go:17-34`、`internal/stacks/run.go:20-42`。`WriteRun` 在 `stacks.folded`、`stacks.meta.json` 或 `stacks.jsonl` 任一已存在时拒绝覆盖（`run.go:31-42`）。

注意：STACKS-FLAMEGRAPH `:106` 和 STACKS-ACCEPTANCE `:112-113` 写 `report --stacks` 可直接传 `stacks.folded`；实际加载仍依赖同目录的 `stacks.meta.json`（只传一个没有 meta 的 folded 实测报错退出 3）。文档应写明“folded 与其同目录 meta 必须成套保留”，否则 P1。

### 4.2 meta 字段、单位与取值范围

以 `internal/stacks/types.go:74-96` 为准：

| JSON 字段 | 类型/单位 | 约束/说明 | 文档核对 |
|---|---|---|---|
| `schema` | string | 固定 `cann.stacks.v1` | FLAMEGRAPH `:159` 一致 |
| `tool_version` | string，omitempty | 二进制版本 | `:160` 一致 |
| `collector` | string | 固定 `perf_event_open+bpf_get_stackid`（`types.go:30-33`） | `:161` 一致 |
| `host` | string | 主机标识 | `:162` 一致 |
| `kernel` | string | 内核 release | `:163` 一致 |
| `arch` | string，omitempty | 目标通常 `arm64` | `:164` 一致 |
| `started_at` / `ended_at` | RFC3339Nano UTC | 采样起止 | `:165` 一致 |
| `duration_s` | float，秒 | 实际采样窗口 | `:166` 一致 |
| `frequency_hz` | int，Hz | 默认 99 | `:167` 一致 |
| `target_pids` | int 数组 | 宿主 TGID，不是容器内 PID | `:168` 一致 |
| `kernel_filter` | string | `all`/`npu`；代码无 `omitempty`，始终写出 | `:169` 写“可省略”，不准确（P2） |
| `samples` | uint64 | 通过过滤且成功取栈的样本数；folded 计数来源 | `:170` 一致 |
| `unique_stacks` | int | folded 唯一链数量 | `:171` 一致 |
| `stack_fail` | uint64 | 匹配目标但取栈失败次数 | `:172` 一致 |
| `filtered` | uint64 | inherit 后不满足 `target_pids` 的事件数 | `:173` 一致 |
| `lost` | uint64 | 采集/传输丢失；代码注释说“reserved for map/ring drain loss”（`types.go:60-63`），v1 预期 0 | `:174` 与代码注释一致，但 BPF 未覆盖 map 插入失败（见 P0） |
| `symbol_coverage` | float，**0..1** | 已符号化帧占比；UI 乘 100 显示 | `:175` 一致 |
| `unknown_pct` | float，**0..100** | `100 × (1 - symbol_coverage)` | `:176` 一致 |
| `kallsyms_sha256` | hex string，omitempty | 本次 kallsyms 哈希 | `:177` 一致 |
| `warnings` | string 数组，omitempty | 非致命告警 | `:178` 一致 |

**没有 `threads` / `processes` 字段。** 1357 threads / 10 processes 是采集时枚举的现场事实或 warning 文本，不是 `stacks.meta.json` 的 JSON key；任何脚本不能从 meta 读这两个字段。

### 4.3 folded / raw

- folded：每行 `root;...;leaf count`，根在左、叶在右；`sum(folded count) == samples`；`unique_stacks == 非空行数`（FLAMEGRAPH `:111-122`、`:182`）。
- raw：`stacks.jsonl` 的 Sample 为 `pid`、`tid`、`count`、`addrs`；v1 `tid=0`，`addrs` 是 `bpf_get_stackid` 的内层→外层顺序（`types.go:47-58`）。
- `symbol_coverage` 计算：`internal/stacks/symbolize.go:148-176`；`unknown_pct = 100 * (1 - symbol_coverage)`。

## 5. 语义边界核对

| 声明 | 代码/文档依据 | 结论 |
|---|---|---|
| 只覆盖 host kernel on-CPU | perf attr `exclude_user=1` + `exclude_hv=1`；README `:547-549`、FLAMEGRAPH `:51-54`、Quickstart 第 0 节 | 一致 |
| 不覆盖 Python/vLLM 用户态 | 用户态 on-CPU 不触发事件；README `:549`、ACCEPTANCE `:121` | 一致 |
| 不覆盖 AI Core/算子 | 只采 `bpf_get_stackid` 内核栈，不是算子 profiler；README `:537-538`、`:600`、FLAMEGRAPH `:8-9` | 一致（README `:537` 的“没有调用栈采样”是另一处过期表述，见 P1） |
| 不是 `perf record` | 不产生 `perf.data`；使用 `perf_event_open` + BPF map 聚合；FLAMEGRAPH `:43-45`、Quickstart 第 0 节 | 一致 |
| 99 Hz 不是“每 CPU 99 Hz” | 每个已打开目标线程在 on-CPU 时有 99 次/秒采样机会；样本数取决于同时 on-CPU 的线程数；FLAMEGRAPH `:239-240`、Quickstart 第 0 节 | 一致 |
| NPU 帧占比是帧样本加权，不是栈命中率 | `npu_frame_pct = NPU 类帧样本计数 / total_frames`；final2 denominator 13209，final4 13658 | Quickstart 正确；**README `:595` 写“按样本加权”错误，P1** |
| `--kernel-filter npu` 保留整条含 NPU 帧的栈 | README `:575-576`；`stacks.go:196-213` 对 folded 过滤并记录保留/丢弃栈数/样本数；`stack_fail`/`filtered` 仍是整轮计数 | 一致 |

## 6. 退出码与错误行为

### 6.1 代码事实

| 命令/场景 | 退出码 | 代码位置 |
|---|---|---|
| `stacks --help` | 0 | `cmd/cann-ebpf/stacks.go:136-140` |
| `stacks` 参数错误（无 PID、duration<=0、freq 越界、auto+显式混用、非法 kernel-filter） | 2 | `stacks.go:86-107`；实测均为 2 |
| `stacks` 采集/加载失败 | 3 | `stacks.go:172-175` |
| `stacks` 符号化失败（含 nil profile） | 4 | `stacks.go:186-195` |
| `stacks` 写盘失败（含拒绝覆盖） | 4 | `stacks.go:215-219` |
| `stacks` 采集到 `samples=0` | **0**（仅 warning） | `stacks.go:224-240` |
| `stacks` `symbol_coverage < 0.5` | **0**（仅 warning） | `stacks.go:227-229` |
| `report --help` | 2 | `report.go:46-48` |
| `report` 参数错误（无 input/metrics/stacks 等） | 2 | `report.go:46-60` |
| `report` input/metrics 加载失败 | 4 | `report.go:76-104` |
| `report` build 失败 | 3 | `report.go:136-139` |
| `report` stacks 加载失败 | 4 | `report.go:140-147` |
| `report` render 失败 | 3 | `report.go:148-151` |
| `report` 写 HTML 失败 | 4 | `report.go:153-155` |
| `report` 缺 folded 但有 meta | **0**，`flamegraph=unavailable` | 实测；`report.go:165-174` 依 `Status.Available` |
| `report` 正常但低覆盖（coverage=0/unknown=100） | **0**，`flamegraph=available` | 实测；`internal/report/flamegraph.go:275` 无条件置 true |
| `report` 正常但 `samples=0` | **0**，`flamegraph=unavailable` | 实测 |

### 6.2 与文档的差异

- README `:580` 只写 stacks 退出码，没写 report 退出码（P2）。
- ACCEPTANCE `:126` 写“samples=0 直接失败”，但 `stacks` 实际退出 0（P0）。
- 文档没有说明 `report --help` 返回 2；脚本若用 `--help` 做可用性判断会误判（P2）。
- `stacks` 对低覆盖只 warning（阈值 0.5，即 50%，不是验收要求的 0.80），不能把退出码当符号化质量结论（P1/P0 关联）。

## 7. 验收命令与阈值核对

### 7.1 命令

- ACCEPTANCE `:86-93` 的 10 秒命令：`cann-ebpf stacks --pid HOST_PID --duration 10s --freq 99 --kernel-filter all --symbols profiles/kernel-symbols.yaml --out runs/...`。flags 全部存在。
- ACCEPTANCE `:97` 明确“显式 --pid，不使用 auto”，正确；但示例只有一个 `--pid HOST_PID`，没有体现 10 个目标需重复 `--pid`（P2）。Quickstart 第 2、5 节已给出 10 个显式 PID 的可复制写法。
- ACCEPTANCE `:107-109` 的 report 命令：`report --stacks <run> --out <html>`，与真实 flags 一致。
- `scripts/stacks_preflight.sh` 只做静态环境检查（权限/CAP、`perf_event_paranoid`、lockdown、BPF syscall、`/sys/fs/bpf`、BTF、kallsyms/modules、宿主 PID/Tgid/NSpid、磁盘）；不 attach、不产生样本、不验证 samples/coverage/lost（脚本 `:166-410`、`:425`）。

### 7.2 阈值是否被执行

| ACCEPTANCE 门槛 | 文档位置 | CLI / preflight 实际 | 结论 |
|---|---|---|---|
| samples > 1000（8 Worker/10s 参考） | `:125` | 不强制；只打印 samples；`samples=0` 仍返回 0 | 人工门槛 |
| `stack_fail / samples < 1%`，`samples=0` 直接失败 | `:126` | 不强制；`samples=0` 返回 0 | **不一致，P0** |
| `symbol_coverage >= 0.80` | `:127` | 不强制；仅在 `<0.5` 时 warning；report 低覆盖仍 available | **不一致，P0** |
| `unknown_pct < 20` | `:128` | 不强制 | 人工门槛 |
| `lost = 0` | `:129` | 不强制；且 BPF counter 未覆盖 map 插入失败 | **证据不足，P0** |
| folded 非空、`sum(count)==samples`、`unique_stacks>0`、schema v1 | `:130` | report 对 sum 不一致只加 warning（`flamegraph.go:290`），不失败 | 人工门槛 |
| 报告 stacks 区域 `available` | `:131` | 低覆盖仍 available；`available` 只表示有非空树可绘制 | **不能直接当验收通过** |
| NPU 链路 observed/unobserved 判定 | `:132` | 文档人工判定 | 合格 |

**没有承诺 `<1%` 开销**：FLAMEGRAPH `:338-341` 和 ACCEPTANCE `:267-268` 都明确不能承诺；Quickstart 第 11 节也写了“不要承诺 <1% 开销”。这点合格，不应在交付材料中新增性能承诺。

**不应把缺失数据画成空图**：README `:597-598` 要求显示不可用原因而不是空图；report 缺 folded/0 samples 时确实 `unavailable`。但 `internal/report/flamegraph.go:275` 对低/零 coverage 仍置 `available`，与 ACCEPTANCE `:127` 的“报告不得显示虚假完整图”冲突，是 P0。

## 8. NPU 两套口径与 final5 交付口径

### 8.1 三组 run 数字（不可混写）

| 证据 | samples / unique_stacks / stack_fail | duration | total_frames | NPU 帧占比（帧加权） | Top NPU 样本% |
|---|---|---|---|---|---|
| `report-stacks-live-final2.html` 嵌入 JSON（正式 10 秒，旧报告/旧分类） | 1420 / 218 / 9（-17 EEXIST） | 10.021198406s | 13209 | **0.99174805057158%** | `drv_ascend_intf_ioctl` 115/1420=**8.10%**，self=0 |
| `final4/report-stacks-final4.html`（旧硬编码分类，已废弃） | 1488 / 221 / 6 | 10.034801594s | 13658 | **0.8566407966027236%**（0.86%，117/13658） | `drv_ascend_intf_ioctl` 101/1488=6.79% |
| `final4/report-stacks-final4-classified.html`（新共享分类） | 1488 / 221 / 6 | 10.034801594s | 13658 | **4.612681212476204%**（630/13658） | `drv_ascend_intf_ioctl` 101=6.79%；`trs_ioctl` 80=5.38%；`devmm_svm_ioctl` 18=1.21% |

- `0.86%` 与 `4.61%` 是**同一份 final4 数据**在旧硬编码分类与新共享分类下的两个结果；0.86% 已废弃，不能用于新报告。
- `0.9917%` 是 final2 正式 10 秒采样的旧报告口径；不能与 final4 的 4.61% 当成同一 run/同一口径。
- `8.10%` 是 final2 的 Top NPU **样本栈比例**（分母 `total_samples=1420`），不是帧占比；绝不能写成“NPU 占 8.1%”。
- `.analysis/stacks-probe/stacks.meta.json` 是旧 3 秒探针（62 样本、484 次 `-14 EFAULT`），不是最终基线；不要把 `-14` 当正式 10 秒结果。

### 8.2 Quickstart 是否写清

**已写清。** `docs/STACKS-QUICKSTART.md`：

- `:14-17` 用表格区分“NPU 帧占比（帧加权）”与“Top NPU 样本%”，并给出两者分母。
- `:19-21` 明确 0.86% 已废弃、0.9917% 与 4.61% 不能混写、8.10% 不能写成“NPU 占 8.1%”。
- `:167-177` 再用三行表列出 final2 / final4-old / final4-classified 的 samples、total_frames、NPU 帧占比和 Top NPU 样本%。
- `:25-26` 使用 final5 和正确 SHA；`:34` 明确 final4 二进制废弃。
- `:113` 明确 report 没有 `--symbols`，并说明报告分类使用共享 `stacks.ClassifyName` / `DefaultSymbolRules`。

### 8.3 共享分类与空面板修复

- `internal/stacks/classify.go:11-64`：`DefaultSymbolRules` 已包含 `devmm_`、`trs_`、`hdcdrv_`、`hdccom_`、`pcivnic_`、`dms_`、`tsagent_`、`vdavinci`、`dbl_`、`ka_` 等规则。
- `internal/stacks/classify.go:99-120`：`ClassifyName`。
- `internal/report/flamegraph.go:568-584`：报告优先使用 frame Category，否则调用 `stacks.ClassifyName`，再走旧 fallback。
- `internal/report/template.html:27-34`、`:4051-4059`：stacks-only 隐藏 `viewsV2Panel`、`coverageBanner`、`metricsPanel`、`cdfPanel`、`quadrantPanel`、`busyPanel`。
- 实测：final2/final4-old HTML 只隐藏 `viewsV2Panel`、`coverageBanner`；final4-classified HTML 隐藏全部六个空面板。**final4 二进制不含这些修复，所以不能用 final4 生成 4.61% 的最终报告；必须用 final5。**

## 9. 发现的问题分级（P0 / P1 / P2）

### P0（影响交付结论，必须处理或明确豁免）

| 编号 | 文件:行 | 问题 | 建议改法 |
|---|---|---|---|
| P0-1 | `internal/report/flamegraph.go:275`；`docs/STACKS-ACCEPTANCE.md:127`；`docs/STACKS-FLAMEGRAPH.md:297` | 低/零符号覆盖仍 `Available=true`。实测 coverage=0、unknown=100、folded 全 `[unknown]` 时 report 退出 0，stdout `flamegraph=available`，内嵌 JSON `status.available=true`、`confidence=unknown`。 | 在 `BuildFlamegraph`/`LoadFlamegraph` 中按 coverage、unknown、samples 设置 `Available=false` 并给出 `symbolization_incomplete`/`unknown_frames` 原因；验收以 meta 检查为准，不能只看 `available`。 |
| P0-2 | `cmd/cann-ebpf/stacks.go:224-240`；`docs/STACKS-ACCEPTANCE.md:126` | `samples=0` 仍写出 run 并返回 0，和“samples=0 直接失败”矛盾。退出码不能证明验收通过。 | 至少让 `stacks` 在 `samples=0` 时返回非零（建议 4 或新增明确错误码），或在文档/验收清单中明确“CLI 返回 0 ≠ 验收通过，必须脚本检查”；二选一并保持一致。 |
| P0-3 | `internal/stacks/bpf/stack_probe.bpf.c:32-42`、`:160-170`；`internal/stacks/types.go:60-63`；`docs/STACKS-ACCEPTANCE.md:129` | `STAT_LOST` 定义了但 `stack_counts` lookup/update/lookup 失败路径不增加 `lost` 或 `stack_fail`；`lost=0` 不能证明没有丢样。 | 在 `bpf_map_update_elem`/二次 lookup 失败时 `stat_inc(STAT_LOST)`；或文档把 `lost` 定义为“仅 ring/传输丢失”，并把 map 插入失败纳入 `stack_fail` 或 warnings。 |
| P0-4 | `dist/cann-ebpf-linux-arm64-stacks-final4`；`.analysis/stacks-probe/final4/VERIFICATION.md:11`、`:13`、`:181`、`:216`、`:217` | final4 二进制已废弃，不含共享分类/空面板修复；用 final4 无法生成 4.61% 的最终报告。旧 VERIFICATION 仍以 final4 SHA 和 0.86% 为结论。 | 交付物固定 final5（`dist/cann-ebpf-linux-arm64-stacks-final5`，SHA `156df5a7...`）；final4 原始证据可保留，但 final4 二进制/旧分类报告标注 deprecated；更新 VERIFICATION 指向 `VERIFICATION-classified.md`。 |

### P1（会造成误读或操作失败，但不必然改变代码结论）

| 编号 | 文件:行 | 问题 | 建议改法 |
|---|---|---|---|
| P1-1 | `README.md:555`、`:562`、`:565`、`:588`、`:589` | stacks/report 示例仍用 `cann-ebpf-linux-arm64-m3`；实测 m3 二进制没有 `main.cmdStacks`，命令会失败。 | 改指向 final5 或通用 `./cann-ebpf`；并在 stacks 章节开头说明“需 stacks 版二进制”。 |
| P1-2 | `README.md:537`、`:813` | 仍写“没有调用栈采样/没有调用栈采样就不叫火焰图”，与 545 行后已交付的 stacks 功能直接矛盾。 | 删除或限定到旧 M1/M2 报告；stacks 章节单独说明已支持内核栈采样。 |
| P1-3 | `README.md:595` | “NPU 帧占比（按样本加权）”定义错误；实际是 `NPU 类帧样本计数 / total_frames`，不是按 samples 加权。 | 改为“NPU 帧占比（帧加权，NPU 类帧次数 / total_frames）”。 |
| P1-4 | `docs/STACKS-FLAMEGRAPH.md:3-5` 与 `:390-405` | 开头声称“本文不声称已在昇腾服务器验证”，结尾又给出昇腾 910 实机 10 秒基线（1420/218/9、1357 线程）。 | 删除开头“不声称已验证”或改为“文档契约部分未验证；实机基线见第 12 节”。 |
| P1-5 | `docs/STACKS-ACCEPTANCE.md:7-8` 与 `:250-268` | 开头说“本清单不声称已在昇腾机器执行过”，第 9 节又列 `bms-84865758` 实机基线和判定“通过”。 | 统一状态：要么写“已在 bms-84865758 执行（见第 9 节）”，要么把第 9 节标为“历史参考”。 |
| P1-6 | `docs/STACKS-FLAMEGRAPH.md:106`；`docs/STACKS-ACCEPTANCE.md:112-113` | 写 `report --stacks` 可直接传 `stacks.folded`；实际仍依赖同目录 `stacks.meta.json`，单传没有 meta 的 folded 失败。 | 改为“可传 run 目录，或传与 `stacks.meta.json` 同目录的 `stacks.folded` 路径；meta 必须成套保留”。 |
| P1-7 | `.analysis/stacks-probe/final4/VERIFICATION.md:181`、`:216`、`:217` | final4 旧验证仍把 NPU 帧占比写 0.86%、Top NPU 写旧值；与 final4-classified 4.61%/6.79% 冲突。 | 标注该文件为旧硬编码分类记录；最终报告以 `report-stacks-final4-classified.html` 为准。 |
| P1-8 | `docs/STACKS-ACCEPTANCE.md:130-131` | 把 `available` 当“通过”条件；实际 available 只表示有非空树，低覆盖也可 available。 | 把 `available` 改为“报告可渲染”，另加“coverage/unknown/stack_fail 达标”作为通过条件；说明 CLI 不自动强制。 |

### P2（澄清/补全类，不影响当前结论）

| 编号 | 文件:行 | 问题 | 建议改法 |
|---|---|---|---|
| P2-1 | `README.md:560` | 写 `EngineCore`，实际 comm 为 `VLLM::EngineCor`（15 字符截断）。 | 写“通常显示 `VLLM::EngineCor`（不是 EngineCore）”。 |
| P2-2 | `README.md:580` | 只列 stacks 退出码，未列 report 退出码（report 参数 2、build/render 3、input/metrics/stacks/write 4、help 也返回 2）。 | 在 report 小节补充退出码表。 |
| P2-3 | `docs/STACKS-FLAMEGRAPH.md:54` | `9/1429` 未说明分母；final2 `samples=1420`、`stack_fail=9`，ACCEPTANCE `:261` 按 `9/1420=0.63%`。若 1429 是 attempts（1420+9），应写明。 | 写成 `stack_fail=9、samples=1420（attempts=1429）`，避免误读为 samples=1429。 |
| P2-4 | `docs/STACKS-FLAMEGRAPH.md:364` | 说“报告记录 Tgid/NSpid/时间”；`FlamegraphMeta` 只有 `target_pids`、host/kernel、起止时间等，没有 NSpid。 | 改成“证据目录单独记录 PID/Tgid/NSpid；报告只记录 target_pids 等 meta 字段”。 |
| P2-5 | `docs/STACKS-FLAMEGRAPH.md:169`；`internal/stacks/types.go:86` | 文档说 `kernel_filter` “可省略”，代码 JSON tag 无 `omitempty`，实际总会写出。 | 改为“总是写出，值 all/npu”。 |
| P2-6 | `docs/STACKS-FLAMEGRAPH.md`、`docs/STACKS-ACCEPTANCE.md` | 未记录 stacks 的 `--profile` flag（README 有）；它仅用于 `--pid auto`，不是 10 秒显式 PID 必需。 | 在 stacks flag 说明中补一行“`--profile` 默认 vllm-ascend，仅 --pid auto 使用”。 |
| P2-7 | `docs/STACKS-ACCEPTANCE.md:86-93`、`:97` | 示例只有一个 `--pid HOST_PID`，未体现 10 个目标要重复 `--pid`；虽然“显式 PID”原则正确。 | 在命令模板中写明“重复 10 次 `--pid`”或给出 `PID_ARGS` 示例；与 Quickstart 第 5 节对齐。 |
| P2-8 | `docs/STACKS-QUICKSTART.md` 已正确；`README.md:577-578` | README 的 final2 基线文字正确但未标注 `npu_frame_pct=0.9917%` 与 Top NPU 8.10% 的分母差异。 | 在 README 该段补一句“NPU 帧占比按 total_frames 加权；Top NPU 样本%按 total_samples”。 |
| P2-9 | `cmd/cann-ebpf/report.go:46-48` | `report --help` 返回 2，和一般 CLI `--help` 返回 0 不同。 | 文档/脚本注明 `report --help` 退出 2；或代码单独处理 `flag.ErrHelp` 返回 0。 |
| P2-10 | `docs/STACKS-ACCEPTANCE.md:258` | `1357 threads / 10 processes` 不在 `stacks.meta.json` schema 中。 | 标注“采集输出/现场记录，不是 meta 字段”；不要让脚本从 meta 读取。 |

## 10. 仅文档问题 vs 影响交付结论

**仅文档问题（不改代码也能通过修正文档解决）：**

- README 的 m3 二进制名、`EngineCore`、`按样本加权`、“没有调用栈采样”、report 退出码缺失。
- STACKS-FLAMEGRAPH 的开头状态声明、`9/1429` 分母说明、`folded` 直传说明、`Tgid/NSpid` 表述、`kernel_filter` 可省略表述。
- STACKS-ACCEPTANCE 的“未在昇腾执行过”与实机基线矛盾、单 PID 示例、`available` 作为通过条件的表述。
- final4 `VERIFICATION.md` 的旧二进制/旧口径标注。

**会影响交付结论（不能只改文字）：**

- **P0-1**：低/零 coverage 仍 `available`，会让验收人把不可用符号化报告当作通过证据。
- **P0-2**：`samples=0` 仍退出 0，退出码不能作为成功依据。
- **P0-3**：`lost` 计数未覆盖 `stack_counts` map 插入/查找失败，`lost=0` 不能证明无丢样。
- **P0-4**：final4 二进制不含共享分类/空面板修复；用它生成的报告不能代表 4.61% 的最终口径。final5 才是交付二进制。
- **口径混写风险**：把 final2 的 `0.9917%` 与 final4 的 `4.61%` 当成同一 run，或把 Top NPU `8.10%` 写成“NPU 占 8.1%”，都会直接污染交付结论。Quickstart 已分开写明，旧文档/旧 VERIFICATION 仍需按本核对表修正。

## 11. 最小修复清单（按优先级）

1. **交付前必须做**：统一 final5 二进制与 SHA；报告只使用 final5 生成；final4 二进制和旧分类报告标记 deprecated。确保 final4 原始 folded/meta 只作为输入证据，不作为最终报告。
2. **代码侧 P0**：修复 `internal/report/flamegraph.go:275` 的 available 判定；修复 `stacks.go:224-240` 的 `samples=0` 返回码；修复 `stack_probe.bpf.c:160-170` 的 lost 计数，并同步 `types.go:60-63` 的语义。
3. **验收侧**：`docs/STACKS-ACCEPTANCE.md` 增加“CLI 不强制阈值、必须用 meta/folded 脚本判定”的说明；把 `samples=0`、coverage、unknown、stack_fail、lost、sum(folded)==samples 的检查落到可执行脚本或明确人工 checklist。
4. **文档侧**：按第 9 节逐条修改 README、STACKS-FLAMEGRAPH、STACKS-ACCEPTANCE；不要修改现有 README/docs 原文的要求由交付负责人统一执行，本 Review 只列问题。
5. **Quickstart 侧**：保持 `docs/STACKS-QUICKSTART.md` 的 final5/SHA、report 无 `--symbols`、两套 NPU 口径说明；现场按第 7 节 meta 检查，不能只看 report 的 `flamegraph=available`。

## 12. 复核命令（可复制）

```bash
# 1) 确认真实 flags；report 不应有 --symbols
"$BIN" stacks --help
"$BIN" report --help

# 2) 确认 report 拒绝 --symbols（预期退出 2）
"$BIN" report --stacks /tmp/stacks-run --symbols profiles/kernel-symbols.yaml; echo exit=$?

# 3) 确认 run 目录 schema（只读）
python3 - <<'PY'
import json, pathlib
p = pathlib.Path("runs/stacks-.../stacks.meta.json")
m = json.loads(p.read_text())
need = ["schema","collector","host","kernel","started_at","ended_at","duration_s",
        "frequency_hz","target_pids","kernel_filter","samples","unique_stacks",
        "stack_fail","filtered","lost","symbol_coverage","unknown_pct"]
missing = [k for k in need if k not in m]
print("missing:", missing)
print("schema:", m.get("schema"), "coverage:", m.get("symbol_coverage"), "unknown_pct:", m.get("unknown_pct"))
print("has threads/processes:", "threads" in m, "processes" in m)
PY
```

> 结论：`docs/STACKS-QUICKSTART.md` 已可直接作为运维一页式 Quickstart 使用（final5、SHA、report 无 `--symbols`、两套 NPU 口径均已写清）。`docs/STACKS-DOC-REVIEW.md` 中列出的 P0 代码/交付问题在修复或由验收人明确豁免前，不能用 report 的 `flamegraph=available` 或 `stacks` 退出码单独证明交付通过。