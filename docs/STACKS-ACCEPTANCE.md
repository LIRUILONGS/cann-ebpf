# cann-ebpf stacks 10 秒只读验收清单

适用范围：目标 vLLM 已在运行、负载正在产生真实推理流量时，对宿主 PID 做一次
10 秒内核栈采样，并用离线报告核对。这里的“只读”指不注入业务、不改原始证据、
不重启服务；采样本身需要短暂 attach perf_event/BPF，预检脚本不会 attach。

相关背景和字段定义见 STACKS-FLAMEGRAPH.md。本清单已在 bms-84865758 执行过一次；
更早的 final2 探针为 1420/218/9、0.9917%，已不作为交付基线；第 9 节为 final4 原始证据
与 final5 报告（1,488/221/6、NPU 帧加权 4.61%）。
设备名、PID、端口、内核和输出均不得写死或编造。

## 1. 10 秒前先过预检（只读）

### 1.1 一键预检

~~~bash
sh scripts/stacks_preflight.sh --pid HOST_PID
~~~

将输出保存到证据目录（例如 acceptance-preflight.txt），但不要覆盖已有文件。
脚本退出码要求：

- 0：没有阻断项，可进入采样；
- 1：有 FAIL，停止并记录原因；
- 2：命令参数错误，修正参数后重跑。

脚本只读取 /proc、/sys 和能力位；不 attach、不加载 BPF、不打开 perf event、不写
业务目录。它检查 hostname/uname/nproc/内存、perf_event_paranoid、kptr_restrict、
/proc/kallsyms 前 200 行、BPF fs、BPF syscall、/proc/modules、目标 PID/NSpid、
磁盘和可选工具。

### 1.2 手工核对表

| 项目 | 只读命令/判定 | 不通过时 |
|---|---|---|
| 主机与内核 | uname -a；nproc；记录 UTC 时间 | 记录平台不匹配 |
| perf 权限 | cat /proc/sys/kernel/perf_event_paranoid | 非 root 且大于 1：阻断 |
| 地址可见性 | cat /proc/sys/kernel/kptr_restrict；awk 统计 kallsyms 非零行 | 全为 0：符号化不可用 |
| BPF syscall | kallsyms 有 __arm64_sys_bpf 或对应架构符号；脚本会检查 | 缺失：阻断 |
| BPF fs | awk 匹配 /proc/mounts 的 /sys/fs/bpf 且类型为 bpf | 未挂载：静态验收先阻断；当前实现不 pin，现场确认是否可豁免 |
| 模块符号 | test -r /proc/modules；记录模块数 | 不可读：模块覆盖下降 |
| 目标 PID | test -d /proc/HOST_PID；记录 Name/Tgid/NSpid/State/Threads | 不存在/zombie：阻断 |
| 容器映射 | 宿主 PID 与 NSpid 第一列核对；必要时 docker inspect | 容器 PID 不能直接使用 |
| 磁盘 | df -Pk .，至少 100 MiB | 空间不足：换新目录 |
| 权限/加固 | id；CapEff；lockdown 不是 integrity/confidentiality | EPERM 风险，记录后停止 |

手工核心检查示例：

~~~bash
uname -a
nproc
cat /proc/sys/kernel/perf_event_paranoid
cat /proc/sys/kernel/kptr_restrict
awk 'NR <= 200 && NF >= 3 { n++; if ($1 ~ /^0+$/) z++; else nz++ }
     END { printf "sampled=%d nonzero=%d zero=%d\n", n, nz, z }' /proc/kallsyms
awk '$2 == "/sys/fs/bpf" && $3 == "bpf" { print "bpf_fs=present"; found=1 }
     END { exit !found }' /proc/mounts
awk '/^(Name|Tgid|NSpid|State|Threads):/' /proc/HOST_PID/status
df -Pk .
~~~

root/CAP_PERFMON 在 perf_event_paranoid=2 下通常仍可打开，但容器/seccomp/LSM 仍可能
返回 EPERM；预检通过不等于实际 attach 一定成功，必须记录采样时的真实结果。

### 1.3 容器→宿主 PID

必须用宿主 PID。容器内的 PID 1 在宿主通常是另一个值：

~~~bash
# 按现场 runtime 选择；只读 inspect
docker inspect -f '{{.State.Pid}}' CONTAINER_NAME_OR_ID
# 或
crictl inspect CONTAINER_ID

# 宿主 /proc 视角确认
awk '/^(Name|Tgid|NSpid):/' /proc/HOST_PID/status
~~~

NSpid 通常第一列是宿主 PID；若只有容器内 PID，先回宿主机取 Tgid。Tgid 与传参
不同说明采的是线程，除非有意，否则改传 Tgid。不要按名字盲取第一个 PID；PID
可能复用，vLLM 同机可能有多个 Worker。

## 2. 10 秒采样

选择一个不存在的新目录，示例时间戳按实际 UTC 替换：

~~~bash
cann-ebpf stacks \
  --pid HOST_PID \
  --duration 10s \
  --freq 99 \
  --kernel-filter all \
  --symbols profiles/kernel-symbols.yaml \
  --out runs/stacks-accept-20261127T000000Z
~~~

要求：

- 显式 --pid，不使用 auto，不使用容器内 PID；
- 10s 窗口、99 Hz；--out 是 run 根目录，产物直接写在该目录下；
- 采样期间确认目标有真实推理负载，记录开始/结束 UTC 时间；
- 首次验收不必加 --raw；需要保留原始地址以便另行分析时，另起新 run 并加 --raw；
- 不覆盖同名 run；若目录已存在，换时间戳或停止；
- 不把采样窗口与请求窗口混为一谈，样本不是请求数。

采集后生成离线报告：

~~~bash
cann-ebpf report \
  --stacks runs/stacks-accept-20261127T000000Z \
  --out runs/stacks-accept-20261127T000000Z-report.html
~~~

当前 report 支持只给 --stacks 的独立报告；--stacks 可传 run 目录，或传与
stacks.meta.json 同目录的 stacks.folded（单独 folded 无 meta 会失败）。需要与 span 并列时再补
--input 已有 run；以 report --help 为准。
报告必须是离线 HTML，不访问 CDN、不重新 attach、不改 folded。

## 3. 通过阈值

以下阈值是 8 个 Worker 主线程、10 秒窗口的验收基线；按实际受监控线程数和时长
调整，并在证据中记录调整依据。99 Hz 是每个线程 on-CPU 时的机会，不是整机固定
99 样本/秒；总样本率取决于同时 on-CPU 的线程数。实现设置 exclude_user=1，只在
内核态 on-CPU 时触发，因此用户态 Python/vLLM 时间不进入样本，也不计入 stack_fail。

| 门槛 | 通过条件 | 失败时 |
|---|---|---|
| 样本量 | 8 个 Worker 主线程/10s：samples > 1000；其他规模可按 1000×min(线程数,8)/8×duration/10 调整，最低不少于 200 | 记为 unavailable/insufficient_samples |
| 取栈失败率 | stack_fail / samples < 1%；samples=0 直接失败 | 记为 unavailable/stack_fail |
| 符号覆盖 | symbol_coverage >= 0.80（Meta 是 0..1 比例，即 80%；报告 UI 乘 100 显示） | 记为 unavailable/symbolization_incomplete；报告不得显示虚假完整图 |
| 未知帧 | unknown_pct < 20（Meta 是 0..100 百分比），且与 coverage 基本互补 | 记为 unavailable/unknown_frames |
| 丢样 | lost = 0（v1 hash 路径预期） | 记为 unavailable/data_incomplete，复采 |
| 产物完整性 | folded 非空；sum(count)==samples；unique_stacks>0；meta schema=cann.stacks.v1 | 缺文件/不一致：unavailable |
| 报告 | 报告成功生成且 stacks 区域为 available；无伪造/空图 | 报告不可用则整体 unavailable |
| NPU 链路 | 本窗口按第 4 节判为 observed 才可写“观测到”；称“稳定出现”至少需两个独立 run 均为 observed；未出现记 unobserved，不伪造 | 只可写 weak/unobserved，不能写“稳定出现” |

样本量不是统计显著性检验；它的目的是防止拿几帧空闲噪声做结论。低于门槛时增加
真实负载或延长窗口，但在证据里写清原因，不能静默改阈值。

## 4. NPU 链路判定（不保证每次都有）

候选链路：__arm64_sys_ioctl -> devdrv_* -> dma/smmu。它不是每次都出现，也不是
主机火焰图能证明 AI Core 算子耗时的证据。

在 folded 中按行同时匹配 ioctl、devdrv_ 和 dma/smmu/iommu，按最后一列 count
求和：

~~~bash
awk '/__arm64_sys_ioctl/ && /devdrv_/ && /(dma|smmu|iommu)/ { n += $NF; lines++ }
     END { printf "npu_chain_samples=%d lines=%d\n", n, lines }' \
  runs/stacks-accept-20261127T000000Z/stacks.folded
~~~

判定：

- npu_chain_samples >= 3 且报告 Top NPU 表有同类主机内核函数：记
  npu_chain=observed，可写“本窗口观测到候选链路”；
- npu_chain_samples 为 1-2：记 weak，不写“稳定”；
- 为 0：记 unobserved；不代表 NPU 没工作，只说明这个窗口没采到主机链；
- 若要在交付文档写“跨窗口稳定出现”，至少需要两个独立 run（不同时间戳、同一
  负载类别）都达到 observed；单个 run 不能推出稳定性，也不保证下一次出现；
- 需端到端结论时，必须并列 msPTI/device.jsonl；主机链不是 AI Core 证据。

若使用别的架构，ioctl 符号前缀可能不同；按实际符号调整，不能把 no match 写成
“内核没有 NPU 路径”。

## 5. 失败判定和不可用记录

任何一项硬门槛不满足，都要写 unavailable/原因，并保留原始文件；不得补造火焰图、
不得改小 samples、不得把 [unknown] 猜成函数名。建议记录：

~~~text
status: unavailable
reason: insufficient_samples | stack_fail | symbolization_unavailable |
        lost_samples | perf_open_eperm | folded_missing | report_unavailable
host: <hostname>
kernel: <uname -r>
started_at/ended_at: <UTC>
target_pids: <host Tgid/NSpid>
command: <完整命令>
run_dir: <路径>
meta: <stacks.meta.json 路径>
evidence: <folded/report/sha256>
not_claimed: 不声称已得到完整内核火焰图或 AI Core 结论
next: <补权限/换窗口/回退 span 计时>
~~~

报告显示不可用时，不能在同一区域画一个空白 SVG/空树；应显示原因、需要的权限和
证据路径。错误信息保留原文（例如 EPERM、verifier log），不要改成“环境正常”。

## 6. 回退建议：kprobe/span 计时

如果 stacks 因 perf/BPF 权限、内核限制或无法符号化而不可用，可以回退到已有的
host span 计时路径，获取函数进入/退出的时长；它不能提供内核调用链，也不能替代
本火焰图。

~~~bash
# 现有 host span 路径（uprobe）；输出新目录，不覆盖 stacks 证据
cann-ebpf attach \
  --pid HOST_PID \
  --duration 10s \
  --output runs/span-fallback-20261127T000000Z
~~~

当前仓库没有确认的 kprobe span 子命令，所以优先使用已有 host span 时长路径；
不要编造 kprobe 命令。若现场版本提供 kprobe span 接口，先运行对应 --help 核对
参数，再把探针限定到明确的内核函数并记录 entry/exit 时长。不要把“span 计时”
写成“内核栈火焰图”；回退报告必须标 method=span/kprobe-timing，并说明没有
call chain、样本率、symbol coverage 或 NPU 内核火焰图。

## 7. 证据留存

每个 run 至少保留：

- run 目录（stacks.folded、stacks.meta.json；有 --raw 时含 stacks.jsonl）；
- 离线 report.html（建议放在 run 外；含 stacks unavailable/部分可用状态）；
- 预检输出、完整采集/report 命令、开始/结束 UTC、hostname/uname、目标 PID/Tgid/NSpid；
- 采样时的负载/Worker 说明和是否发生重启；
- 二进制/cann-ebpf 版本、BPF 对象版本；
- stacks.folded、stacks.meta.json、report.html 的 SHA-256；
- 如失败，原始错误输出和不可用记录。

建议命令（在 run 完成后执行，只读哈希）：

~~~bash
sha256sum runs/stacks-accept-20261127T000000Z/stacks.folded \
  runs/stacks-accept-20261127T000000Z/stacks.meta.json \
  runs/stacks-accept-20261127T000000Z-report.html
hostname
uname -a
date -u +%Y-%m-%dT%H:%M:%SZ
~~~

不要覆盖原始目录；复采使用新时间戳。不要把报告 HTML 当原始证据重写 folded/meta，
也不要手工编辑 meta 的 samples/coverage/lost。

## 8. 十秒速查

~~~text
1. sh scripts/stacks_preflight.sh --pid HOST_PID     # 只读，退出 0 才继续
2. 确认 HOST_PID 是宿主 Tgid，NSpid/Tgid 已记录
3. cann-ebpf stacks --pid HOST_PID --duration 10s --freq 99 --out NEW_DIR
4. cann-ebpf report --stacks NEW_DIR --out NEW_DIR-report.html
5. 核对 samples>1000（8 个 Worker 主线程/10s 基线）、stack_fail<1%、
   coverage>=0.80（80%）、unknown<20、lost=0、folded 与报告完整
6. 记录 ioctl/devdrv/DMA/SMMU 链路的 count；没有就写 unobserved
7. 保存 run、meta、命令、时间、host、PID、sha256；不覆盖、不补造
~~~

未能满足任何硬门槛：写 unavailable 和原因，按第 6 节回退 span 计时；不要提交
伪造的火焰图。

## 9. 实机基线（2026-11-27）

> 交付口径提醒：下表已按 final4 原始证据 + final5 报告更新；更早的 final2 探针为 1420/218/9、
> 0.9917%，已不作为交付基线。

在 `bms-84865758`（kernel 5.10.0-136.108.0.188.31.oe2203sp1.bclinux.aarch64）以
root、`perf_event_paranoid=2`、`kptr_restrict=0` 对 vLLM 10 个宿主 PID 做 10 秒
只读采样，结果满足本清单的硬门槛：

| 项目 | 实测 | 判定 |
|---|---|---|
| 目标线程 | 1357 threads / 10 processes | 已枚举 /proc/<pid>/task |
| samples | 1488 | >1000，通过 |
| unique_stacks | 221 | 非空，通过 |
| stack_fail | 6（-17 EEXIST，0.40%） | <1%，通过 |
| filtered / lost | 0 / 0 | 通过 |
| symbol_coverage / unknown_pct | 1.0 / 0 | 通过 |
| Top NPU 帧 | `drv_ascend_intf_ioctl` 101（6.79% of 1,488）、`trs_ioctl` 80（5.38%）、`devmm_svm_ioctl` 18（1.21%） | 观测到，不称稳定 |
| 报告 | 253 KB 离线 HTML，flamegraph=available；NPU 帧加权 4.61%（630/13,658） | 通过 |

这是单次 10 秒功能基线；尚未做固定负载 A/B，不能据此承诺 <1% 开销或 NPU 链路
每次出现。复采必须使用新目录，不覆盖本次证据。
