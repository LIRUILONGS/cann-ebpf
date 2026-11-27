# 主机内核栈火焰图（cann-ebpf stacks）

> 状态：A4 文档契约（2026-11-27）。stacks 采集与 report --stacks 已实现；
> 本文已按当前接口核对；实机基线见第 12 节。示例 JSON 只用于说明格式，计数以实机证据为准。
> 更早的 final2 探针为 1420/218/9、0.9917%，已不作为交付基线；第 12 节为 final4 原始证据
> 与 final5 报告（1,488 样本 / 221 栈 / stack_fail=6 / NPU 帧加权 4.61%）。
> 参数/路径/输出布局以最终二进制的 --help 和当前实现为准。
>
> 一句话：这是对目标 vLLM 主机进程做 99 Hz 内核栈统计采样，回答“采样时刻 CPU
> 正在跑哪条内核调用链”；它不是 AI Core 算子 profiler，也不是请求追踪器。

## 1. 是什么、不是什么

| 问题 | span tracer（attach/trace） | stack sampler（stacks） | AI Core/设备 profiler（msPTI/device.jsonl） |
|---|---|---|---|
| 一次 CANN 调用大概多长 | 函数进入/退出的精确时间跨度 | 不能直接得到单次调用时长 | 不同类数据 |
| 此刻内核在跑哪条调用链 | 通常没有完整内核栈 | 有，按调用链聚合 | 没有主机内核链 |
| MatMul/FlashAttention 耗时 | 不能从主机内核函数推出 | 看不到 AI Core 内部 | 需要 msPTI/device 证据 |
| 请求/批次身份 | 可从 span 上下文关联 | 没有请求身份；样本不是请求 | 视设备证据而定 |
| 阻塞/off-CPU | 可由 span/offcpu 表达 | 只采 on-CPU 的内核时刻 | 不是主机 off-CPU 数据 |

覆盖：目标 vLLM 主机进程的内核栈、NPU 驱动/DMA/SMMU/ioctl/网络/锁/调度路径，
以及被采样时处于 on-CPU 的内核工作。

不覆盖：AI Core 的 MatMul/FlashAttention/Cube/Vector；Python/PyTorch/vLLM 用户栈
（v1 不做用户栈）；off-CPU 完整时长（需 offcpu.jsonl）；逐请求/batch/算子时间线；
HBM/Cube 等硬件指标。AI Core 仍需 msPTI/device.jsonl，两者只能并列报告。

火焰图宽度是“采样次数占比”的近似，不是请求数或精确耗时；不能用它单独证明
NPU 算子慢，也不能把它写成毫秒级函数计时。

## 2. 架构

~~~text
for each target host PID:
  read /proc/<host-pid>/task -> target host TIDs
    -> perf_event_open(PERF_TYPE_SOFTWARE, PERF_COUNT_SW_CPU_CLOCK,
                       99 Hz, inherit=1, exclude_user=1) per target TID
  -> BPF_PROG_TYPE_PERF_EVENT
  -> target_pids 按 TGID 过滤（不匹配 filtered++）
  -> bpf_get_stackid(ctx, &stack_traces, flags=0 全量比较)（失败 stack_fail++）
  -> stack_traces + stack_counts
  -> 用户态 drain：/proc/kallsyms + /proc/modules 符号化
  -> folded（root-first，唯一链聚合）
  -> report --stacks：离线 HTML 火焰图 + Top NPU 内核函数表
~~~

- 定时器：先枚举目标进程的宿主线程，逐 TID 打开 PERF_TYPE_SOFTWARE +
  PERF_COUNT_SW_CPU_CLOCK，默认 99 Hz，inherit=1；不调用 perf 命令，不产生/
  读取 perf.data，也不打开全系统事件。采样开始时已有的线程逐一打开，采样期间
  新建的后代任务由 inherit=1 覆盖。
- 只采内核态：perf attr 设置 exclude_user=1 + exclude_hv=1，不设置 exclude_kernel。
  用户态 on-CPU 时间不触发事件，避免 arm64 用户态 FP=0 造成的 -EFAULT 噪声；代价是
  样本只代表“内核态 on-CPU”时间，不代表进程总 CPU 时间。实测 10s 采样中
  stack_fail 从 484/546（未排除用户态）降到 9/1429（排除后）。
- 过滤：宿主 TGID 写入 target_pids；BPF 程序取当前任务的高 32 位 TGID 查表，
  不匹配只计 filtered。inherit=1 会把新建子任务的事件也送进来，target_pids 是
  最终闸门；子进程不在 target_pids 时不会混入目标样本。
- 取栈：bpf_get_stackid(ctx, &stack_traces, flags=0 全量比较) 只获取内核栈，
  不设置用户栈标志；stack_traces 保存地址，stack_counts 按 (TGID, stack_id)
  保存次数。原始地址是内层→外层，符号化后反转为 root-first。
- 符号化：/proc/kallsyms 提供内核符号，/proc/modules 提供模块范围和名称；先判
  模块范围，再取最近符号+偏移，找不到写 [unknown]。
- 报告：离线 HTML，不联网、不重新 attach、不改 folded/meta。

filtered 高不一定是故障：可能只是 inherit 的子进程不在 target_pids。只有“PID
正确 + 负载活跃 + samples 仍不足”时才按采样失败处理。取栈失败计入 stack_fail；
v1 hash map 不预期传输丢样，lost 应为 0，若引入 ring buffer/队列必须如实记录。
kallsyms_sha256 是符号化所读 kallsyms 的 SHA-256，用于复现，不是业务摘要。

## 3. 采样语义

- 99 Hz 是每个已打开线程的 PERF_COUNT_SW_CPU_CLOCK 软件时钟采样率，不是每请求
  样本；线程 on-CPU 时约 99 次/秒，inherit=1 覆盖采样开始后新建的后代任务，
  target_pids 仍是最终闸门。总样本率取决于同时 on-CPU 的受监控线程数，不是固定
  99 次/秒，也不是 99 × CPU 数。
- 样本数不是请求数、batch 数或精确耗时。
- CPU_CLOCK 只统计 on-CPU；睡眠、等锁、等 I/O 不在火焰图中。off-CPU 必须用
  offcpu.jsonl 和独立视图解释，不能把没有样本误判成没有阻塞。
- exclude_user=1 后只在内核态 on-CPU 时触发；用户态 Python/vLLM 时间不采样，
  也不计入 stack_fail。需要用户栈时必须另做用户态栈采样，不能把本工具的空白解释为
  “用户态没有工作”。
- 一个栈出现 count 次只表示采样命中 count 次，近似反映 on-CPU 相对占比；不能
  断言耗时等于 count/99 秒，不能关联 request/batch，不能替代 span 的 duration_ns，
  也不能计算函数 p50/p95/p99。

8 个 Worker 主线程全部持续 on-CPU 的 10 秒示例，理论样本上限约为
8 × 99 × 10 = 7,920；30 秒约为 23,760。这只是“8 条线程”的示例，不是整机上限：
更多受监控线程 on-CPU 时上限更高。实际值受 vLLM 空闲、等设备、调度、过滤和
丢样影响，通常远低于上限。

## 4. 产物 schema

### 4.1 布局

--out 是 run 根目录；stacks.folded、stacks.meta.json 和可选的 stacks.jsonl
直接写在该目录下，不是 --out/stacks/ 子目录：

~~~text
runs/stacks-YYYYMMDDTHHMMSSZ/
  stacks.folded
  stacks.meta.json
  stacks.jsonl          # 仅 --raw
  report.html           # 可选派生物；建议写到 run 外
~~~

report --stacks 接收这个 run 目录，也接受与 stacks.meta.json 同目录的 stacks.folded 路径；
单独一个没有 meta 的 folded 会报错。不要移动、重命名或改写原始文件。
报告建议写到 run 外（例如 runs/stacks-YYYYMMDDTHHMMSSZ-report.html），或另建派
生目录；至少不要覆盖/改写原始证据。

### 4.2 stacks.folded

每行格式为 root;...;leaf count，最后一列是样本次数。示意（不是实测）：

~~~text
swapper/0;do_idle;cpuidle_enter_state 183
vllm-worker;__arm64_sys_ioctl;devdrv_ioctl;devdrv_submit;dma_map_sg 7
vllm-worker;[unknown];[unknown] 2
~~~

帧从左到右是 root→leaf，用分号分隔；count 是最后一个空格后的非负整数；同一
唯一链合并一行；找不到符号的帧写 [unknown]，不能删帧或伪造名称。folded 只保留
聚合计数，不是逐样本时间序列。

### 4.3 stacks.meta.json

schema 固定为 cann.stacks.v1。示例值全是占位符，不能据此宣称已有覆盖率。
symbol_coverage 是 0..1 比例（0.924 表示 92.4%，报告 UI 乘 100 显示）；
unknown_pct 是 0..100 百分比，两者单位不同，不能混写。

~~~json
{
  "schema": "cann.stacks.v1",
  "tool_version": "<binary version>",
  "collector": "perf_event_open+bpf_get_stackid",
  "host": "<host>",
  "kernel": "<kernel-release>",
  "arch": "arm64",
  "started_at": "2026-11-27T00:00:00Z",
  "ended_at": "2026-11-27T00:00:30Z",
  "duration_s": 30.0,
  "frequency_hz": 99,
  "target_pids": [123456],
  "kernel_filter": "all",
  "samples": 6320,
  "unique_stacks": 187,
  "stack_fail": 4,
  "filtered": 128,
  "lost": 0,
  "symbol_coverage": 0.924,
  "unknown_pct": 7.6,
  "kallsyms_sha256": "<sha256>",
  "warnings": []
}
~~~

| 字段 | 类型/单位 | 含义 |
|---|---|---|
| schema | string | 固定 cann.stacks.v1；其他 schema 不能当作本格式。 |
| tool_version | string，可省略 | 二进制版本，便于复现/排障。 |
| collector | string | 应为 perf_event_open+bpf_get_stackid。 |
| host | string | 主机标识，按团队规则保护。 |
| kernel | string | 内核 release；与 kallsyms/modules 同窗口。 |
| arch | string，可省略 | 实际架构，目标通常 arm64。 |
| started_at / ended_at | RFC3339Nano UTC | 采样起止；跨主机对齐注意时钟。 |
| duration_s | float 秒 | 实际采样窗口，不是请求持续时间。 |
| frequency_hz | int Hz | 本次 CPU clock 频率，默认 99。 |
| target_pids | int 数组 | 宿主 TGID，不是容器内 PID；建议传 Tgid。 |
| kernel_filter | string，可省略 | 符号化/报告分类模式（all/npu），不是 PID 过滤。 |
| samples | uint64 | 通过过滤且成功取栈的样本数，是 folded 计数来源。 |
| unique_stacks | int | folded 唯一链数量，不等于样本数。 |
| stack_fail | uint64 | 匹配目标但取栈失败次数。 |
| filtered | uint64 | inherit 后不满足 target_pids 的事件数。 |
| lost | uint64 | 采集/传输真正丢失数；v1 预期 0。 |
| symbol_coverage | float 0..1 | 非 [unknown] 内核帧占比，按样本权重；UI 乘 100 显示。 |
| unknown_pct | float 0..100 | 未符号化帧占比 = 100 × (1 - symbol_coverage)，允许舍入差。 |
| kallsyms_sha256 | hex，可省略 | 本次 kallsyms 哈希；缺失须写 warnings。 |
| warnings | string 数组 | 影响判读的非致命事项（符号缺失、窗口短、map 近满等）。 |

建议关系：filtered + stack_fail + samples ≈ 本次收到的定时器事件（filtered 是非
目标事件；lost 另计）；
sum(folded count) == samples（符号化不丢样本）；unique_stacks == 非空行数。
若实现采用不同计数边界，报告必须解释，不能通过改小 samples 隐藏损失。

### 4.4 stacks.jsonl（仅 --raw）

~~~json
{"pid":123456,"tid":0,"count":3,"addrs":[18446744073709551615,18446744073709551614]}
~~~

addrs 是 bpf_get_stackid 返回的地址序列（实现保留内层→外层），count 是聚合次数。
v1 的 stack_counts key 是 (TGID, stack_id)，因此 raw 的 tid 当前固定为 0，不提供
逐线程归因；不要把它当成真实 TID。该文件不包含用户栈或业务 payload；地址仍按
原始证据保护。report 优先使用 folded/meta，不要把 raw 当请求明细。

## 5. 使用

### 5.1 构建

BPF 源文件是 internal/stacks/bpf/stack_probe.bpf.c；编译出的对象由 Go 的
go:embed 嵌入二进制：

~~~bash
clang --target=bpfel -mcpu=v1 -O2 -g -c \
  internal/stacks/bpf/stack_probe.bpf.c \
  -o internal/stacks/bpf/stack_probe.bpf.o

CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
  go build -buildvcs=false -trimpath \
  -o dist/cann-ebpf-linux-arm64 ./cmd/cann-ebpf
~~~

BPF 对象必须与二进制一起发布/嵌入；版本不匹配时不得静默使用旧对象。

### 5.2 预检与宿主 PID

~~~bash
sh scripts/stacks_preflight.sh --pid HOST_PID
~~~

容器内 PID 不能直接使用。宿主侧只读映射和确认：

~~~bash
docker inspect -f '{{.State.Pid}}' CONTAINER_NAME_OR_ID   # 按 runtime 选择
awk '/^(Name|Tgid|NSpid):/' /proc/HOST_PID/status
~~~

宿主 /proc 视角下 NSpid 通常第一列是宿主 PID；Tgid 与传入 PID 不同表示传的是线程，
除非有意采该线程否则改传 Tgid。不要按进程名盲取第一个 PID；PID 可能复用。

### 5.3 采集

先选不存在的目录，避免覆盖旧证据。默认 duration=30s、freq=99、
kernel-filter=all；也可以显式写到命令里：

~~~bash
cann-ebpf stacks \
  --pid HOST_PID \
  --duration 30s \
  --freq 99 \
  --kernel-filter all \
  --symbols profiles/kernel-symbols.yaml \
  --out runs/stacks-20261127T000000Z
~~~

--pid 可重复指定多个宿主 Tgid；--pid auto 是当前实现提供的进程树自动选择，验收
仍要求显式 PID。频率范围 1..999，--out 是目录。符号规则文件缺失时使用内置规则。
需要原始地址时加 --raw；不需要时不要生成额外大文件。采样期间必须有真实推理负载；
空闲窗口不能用于证明 NPU 内核路径有问题。

--kernel-filter npu 保留“包含至少一个 NPU 帧”的整条栈，保留 el0_sync/ioctl 等
上下文，不会只截取 NPU 帧；meta 会记录保留/丢弃的栈数和样本数，stack_fail/filtered
仍是整轮计数。默认 all 不做过滤。

### 5.4 报告

~~~bash
cann-ebpf report \
  --stacks runs/stacks-20261127T000000Z \
  --out report.html
~~~

当前 report 支持只给 --stacks 的独立报告；需要与 span 并列时再加 --input 已有
run（以 report --help 为准）：

~~~bash
cann-ebpf report --input SPAN_RUN_DIR \
  --stacks runs/stacks-20261127T000000Z --out report.html
~~~

报告必须离线；读不到 stacks 时明确显示不可用，不能画空火焰图或回填猜测符号。

## 6. 先决条件

| 条件 | 合格判定 | 不满足的结果 |
|---|---|---|
| 权限 | root 或 CAP_PERFMON/CAP_BPF/CAP_SYS_ADMIN 等 | perf_event_open/BPF 加载 EPERM |
| perf_event_paranoid | 非 root 通常 <=1；root/CAP_PERFMON 通常可放宽 | 容器/seccomp/LSM 仍可能 EPERM，需实测 |
| kptr_restrict | 0，或有 CAP_SYSLOG 且实际地址可见 | kallsyms 全 0，coverage=0，报告不可用 |
| /proc/kallsyms | 可读，前 200 行至少一个非零地址 | 只有 [unknown] |
| /proc/modules | 可读，覆盖目标 NPU/驱动模块 | 模块帧 unknown，coverage 降低 |
| BPF fs | /sys/fs/bpf 存在并以 bpf 类型挂载 | 预检阻断；当前实现不 pin，但验收先按不可用处理，现场确认是否可豁免 |
| BPF syscall | kallsyms 有 bpf 系统调用符号 | 无法加载 perf_event BPF |
| 宿主 PID | 存活、非 zombie、NSpid 已映射 | 空数据/错误进程 |
| 架构/构建 | arm64 目标；BPF 对象与内核匹配 | verifier/加载失败，不能降级造假 |
| 磁盘 | 至少 100 MiB（10 秒口径，长窗口更多） | 写入失败/截断 |
| lockdown | 不是 integrity/confidentiality | BPF/perf 被阻止 |

kptr_restrict=0 不保证一定可见；WSL/加固环境也可能掩码。kptr_restrict=2 通常
即使 root/CAP_SYSLOG 也不显示地址。最终以 kallsyms 实际样例为准：

~~~bash
awk 'NR <= 200 && NF >= 3 { n++; if ($1 ~ /^0+$/) z++; else nz++ }
     END { printf "sampled=%d nonzero=%d zero=%d\n", n, nz, z }' /proc/kallsyms
~~~

nonzero=0 时，报告写 symbolization=unavailable，不能写成 100% coverage。perf 或
BPF EPERM 时记录 errno、UID/CapEff、lockdown 和时间；不要在生产机直接改 sysctl。

## 7. 失败模式与判读

以下排查均只读；不要通过重启服务、改 sysctl 或伪造图来“修复”。

| 现象 | 原因 | 只读排查 | 处理 |
|---|---|---|---|
| stack_fail/samples 高 | 栈不可用/过深、map/哈希条件、BPF 版本 | cat /proc/sys/kernel/perf_event_paranoid；ulimit -l；dmesg；journalctl -k -n 40 --no-pager | 修权限/加载/配置；否则标 unavailable，不出空图 |
| filtered 高 | inherit 的子进程不在 target_pids，或 PID 错 | awk '/^(Tgid|NSpid):/' /proc/PID/status；ps -eL -o pid,tid,comm | 样本够则记录子进程过滤；不够则补 PID 后新 run |
| coverage 低/unknown 高 | kptr、符号表/模块不匹配、模块未加载 | kallsyms awk 统计；cat /proc/modules；检查 kallsyms_sha256 | 报告显示部分/完全不可用； [unknown] 不能改名 |
| lost > 0 | map/队列满、drain 慢、其他 BPF 负载 | bpftool map show（可选）；dmesg；看 warnings | v1 应为 0；否则降低窗口/频率、复采或标数据不完整 |
| perf_event_open EPERM | 非 root、paranoid、lockdown、capability、memlock/fd | id；CapEff；paranoid；lockdown；ulimit -n/-l | 只在获批权限/窗口解决；记录 EPERM，不伪造成功 |
| 无图/空报告 | folded 缺失、0 samples、路径错、报告 unavailable | ls -l RUN；wc -l；python3 -m json.tool meta；report 错误 | 保留失败证据，按不可用交付；不补造 |
| NPU 帧少 | 驱动路径短、on-CPU 少、filter 收窄、负载不在 ioctl/DMA | 统计 devdrv/davinci/dma/smmu/iommu；对照 msPTI | 只说明本次主机采样；不能断言 AI Core 慢/未工作 |
| 无 Python 函数 | v1 只有内核栈 | 看报告边界说明；确认没看错视图 | 设计边界，不是失败；用户栈另立实验 |
| 样本远低于“同时 on-CPU 线程数 × 99 × 时长” | 空闲/等设备/调度/过滤/丢样 | samples/duration_s；/proc/PID/stat；filtered/lost | 先加真实负载或延长窗口；空闲窗口不作性能结论 |
| PID 中途消失/重启 | 容器/Worker 重启、PID 复用 | test -d /proc/PID；对比 Tgid/NSpid/时间 | 该 run 不能归因到新进程；重新确认 PID |

### 7.1 ioctl→devdrv→DMA/SMMU 链路怎么判

典型候选形态是 __arm64_sys_ioctl -> devdrv_* -> dma/smmu，不保证每个负载/窗口都
出现。判读：同一 folded 行同时含 ioctl、devdrv_ 和 dma/smmu/iommu，按 count 求和；
记录 npu_chain_samples 与总 samples。若 npu_chain_samples >= 3 且 Top NPU 表有
同类函数，可写“本窗口观测到”；否则写 unobserved/weak，不写“稳定出现”。主机链
只能说明主机侧驱动路径，端到端仍要 msPTI/device 证据。

## 8. 开销边界

8 个 Worker 主线程持续 on-CPU、99 Hz、10 秒的名义上限约 7,920 个采样机会，30 秒
约 23,760；这是 8 条线程的示例，不是整机上限。实际开销来自 perf 定时器、BPF
执行、栈遍历和 map 更新；不是固定常数。

~~~text
observed_samples_per_s = samples / duration_s
nominal_upper_bound    = min(enumerated_threads, nproc) * frequency_hz
occupancy_ratio        = observed_samples_per_s / nominal_upper_bound
~~~

occupancy_ratio 低可能是等设备/off-CPU/过滤，也可能采样失败；必须结合 filtered、
stack_fail、lost 和 workload。没有目标昇腾机器、目标内核、真实 vLLM workload 的
开/关 A/B 实测前，不能承诺“开销 <1%”或“对吞吐无影响”。记录实际 samples/duration、
unique_stacks、folded 大小，用固定负载 A/B 比较吞吐/P99/CPU/错误率；异常时停止
并保留现状。现有固定负载脚本只是测量手段，结果不能跨机器复用。

## 9. 与 msPTI 的边界

| 证据 | 回答 | 不能回答 |
|---|---|---|
| stacks（99 Hz） | on-CPU 主机内核函数链、驱动/DMA/SMMU/锁热点 | AI Core 内部耗时、请求延迟、off-CPU 全貌 |
| host span | CANN 函数时长和调用 | 完整内核链、设备执行 |
| msPTI/device.jsonl | 设备/算子时间线、执行/传输区间 | 主机内核栈、Python 栈 |
| offcpu.jsonl | 明确 off-CPU 区间 | on-CPU 内核链 |

报告把“主机内核火焰图”和“设备/AI Core 时间线”分栏，分别写采样率、样本数、窗口。
Top NPU 内核函数表是主机内核函数聚合，不是 MatMul/FlashAttention 算子表。不要把
count 与 duration_ns/device duration 混算，也不要把样本占比换算为请求数；同一 run
可并列，但缺失一层要标 unavailable。off-CPU 结论以 offcpu.jsonl 为准。

## 10. 安全与合规

- 只读业务侧：短暂内核采样；不注入、不改内存、不读写 prompt/响应/tensor/业务文本。
- 不覆盖原始证据：已有 run、spans/device/folded/meta 不改写；报告为新派生文件，复采新目录。
- 不改系统配置：不自动改 perf_event_paranoid/kptr_restrict/sysctl/服务/容器；权限走审批。
- 不重启服务；失败保留原因和证据，不用重启换漂亮图。
- 目标限定：显式宿主 PID + target_pids 过滤；不把全系统噪声写成目标结论。
- 容器 PID 先映射宿主 PID；报告记录 Tgid/NSpid/时间。
- 正常/异常结束都检查 perf/BPF 残留；不要留下后台采样器。
- --raw 的内核地址/主机信息按证据规则保护；不上传、不手改、不用于业务内容分析。

## 11. 常见问题

**没有火焰图？** 查 meta 是否存在、samples>0、folded 是否非空、report 路径；无数据
或 meta 标 unavailable 时报告必须显示不可用，不能画空图。

**只有 [unknown]？** 多半是 kptr/加固隐藏地址、权限不足、/proc/modules 不可读、模块
未加载或符号表不匹配。看 symbol_coverage/unknown_pct/kallsyms_sha256/modules；地址
不可见时写 symbolization=unavailable。

**NPU 帧少？** 只看到 on-CPU 主机代码；很多时间可能在内核外/用户态/off-CPU/等设备。
这不等于 NPU 没工作或算子慢；AI Core 看 msPTI/device.jsonl，主机侧可延长窗口、确认
真实负载、检查 target_pids/filtered。

**看不到 Python？** v1 只采内核栈，不采用户栈；不要从内核帧猜 Python 调用。

**样本数不是 99×时长？** 99 Hz 是每个已打开线程的机会，不是目标承诺；总样本率
取决于同时 on-CPU 的线程数。目标 off-CPU、线程不足/不等设备、过滤、丢样都会降低。
结合 samples/duration、filtered、stack_fail、lost。

**宽度能当请求延迟吗？** 不能。样本没有 request ID 和精确起止，宽度只是相对占比；
请求延迟用 span/msPTI。

## 12. 当前状态与实测基线（2026-11-27）

已在昇腾 910 主机 `bms-84865758`（kernel 5.10.0-136.108.0.188.31.oe2203sp1.bclinux.aarch64）
做 10 秒只读采样，目标为 vLLM 主进程/EngineCore/8 个 Worker 共 10 个宿主 PID：

> 交付口径提醒：本节使用 final4 原始证据 + final5 报告；更早的 final2 探针为 1420/218/9、0.9917%，
> 已不作为交付基线。详见 `docs/STACKS-CODE-AUDIT.md`。

- 线程：1357 个目标线程；`filtered=0`、`lost=0`。
- 样本：1488 个，221 条唯一栈；`stack_fail=6`（0.40%，全部为 -17 EEXIST）。
- 符号：`symbol_coverage=1.0`、`unknown_pct=0`；kallsyms SHA256
  `e2393f6308f6554c1d2c654e7c00bc4d5b7fc9b9dd53beaca508419997a0f39d`。
- 报告：253 KB 离线 HTML（final5 生成），火焰图 available；NPU 帧加权占比 4.61%
  （630/13,658）；Top NPU 为 `drv_ascend_intf_ioctl`（101 样本，6.79% of 1,488）、
  `trs_ioctl`（80，5.38%）、`devmm_svm_ioctl`（18，1.21%）。
- 产物：final4 原始 `stacks.folded`、`stacks.meta.json`（未写 `stacks.jsonl`）；final5 只重生成报告。

这组数字是单次 10 秒基线，不是吞吐/开销结论；尚未做固定负载 A/B，不能承诺 <1%
开销。仍需在正式窗口确认：长时间采样的开销、NPU 链路命中率、多个 run 的可重复性。
当前实现已核对：--out 是扁平 run 目录；BPF 对象为 `stack_probe.bpf.o` 并嵌入二进制；
report --stacks 支持独立 stacks-only；Meta 的 symbol_coverage 是 0..1。
