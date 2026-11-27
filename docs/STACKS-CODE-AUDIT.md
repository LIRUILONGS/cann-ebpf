# cann-ebpf stacks 代码审计（final5）

> 日期：2026-11-27
> 审计对象：`cmd/cann-ebpf/stacks.go`、`internal/stacks/**`、`internal/report/flamegraph.go`、`internal/report/template.html`、`profiles/kernel-symbols.yaml`
> 交付二进制：`dist/cann-ebpf-linux-arm64-stacks-final5`
> 真机：`bms-84865758` / `36.137.90.82`，kernel `5.10.0-136.108.0.188.31.oe2203sp1.bclinux.aarch64`，8 × 910B2
> 方法：源码逐路径核对 + Linux 5.10 主线 `kernel/events/core.c`、`kernel/fork.c` 对照 + final4/final5 现有证据复核 + 单元/race/vet 测试。未重启 vLLM、未发推理请求、未改 sysctl、未覆盖旧证据。

## 0. 结论摘要

| 结论 | 判定 | 说明 |
|---|---|---|
| `inherit=1` 是否导致同一线程重复计数 | **不会** | 现有线程在打开 perf event 之前就已创建，fork 时没有可继承的事件；枚举后新建线程只继承创建者的一份事件，没有直接打开的重复事件 |
| 新线程是否一定被采到 | **不保证** | 枚举之后、创建者 perf event 打开之前创建的线程会漏采；这是覆盖缺口，不是重复计数 |
| perf event / fd 生命周期 | **基本正确** | 逐 TID 打开，`CLOEXEC`，失败关闭已打开项，defer + 显式关闭幂等；未重试 `EINTR` |
| BPF map 容量与 final4 使用量 | **充裕** | 221 条唯一栈 / 131,072 条计数槽；`stack_traces` 16,384；无 map 满证据 |
| `lost=0` 是否能证明无丢样 | **不能完全证明** | `stack_counts` 插入/二次查找失败路径没有累加 `lost` 或 `stack_fail`；本次未触发，但语义有缺口 |
| 符号覆盖率定义 | **帧加权，不是栈加权** | `knownFrames / totalFrames`，final4 为 13,658 加权帧；不是“100% 的栈都有符号” |
| 火焰图是否 AI Core/算子火焰图 | **不是** | 只采 host kernel on-CPU 栈；AI Core/MatMul/FlashAttention 需要 msPTI/device 证据 |
| final5 证据是否可用 | **可用，但必须按口径引用** | 1,488 样本、221 栈、`stack_fail=6`、coverage 100%、NPU 帧加权 4.61%；不是 NPU 利用率 |
| 是否可直接承诺 `<1%` CPU 开销 | **不能** | 空闲 A/B 归一化均值 `+2.61%`，见 `docs/STACKS-OVERHEAD-IDLE-AB.md` |

## 1. 采样链路

~~~text
目标 host PID（TGID）
  -> /proc/<pid>/task 枚举 TID
  -> 每个 TID 一个 perf_event_open(PERF_TYPE_SOFTWARE, PERF_COUNT_SW_CPU_CLOCK)
       freq=99Hz, inherit=1, exclude_user=1, exclude_hv=1, disabled=1
  -> PERF_EVENT_IOC_SET_BPF(stack_probe)
  -> 全部打开成功后 PERF_EVENT_IOC_ENABLE
  -> BPF:
       bpf_get_current_pid_tgid() -> tgid
       target_pids[tgid] ? 否 -> filtered++ / 返回
       bpf_get_stackid(ctx, stack_traces, flags=0)
         失败 -> stack_errors[errno]++ / stack_fail++ / 返回
       stack_counts[(tgid, stack_id)]++ / samples++
  -> 到时 DISABLE + CLOSE
  -> drain stack_counts + stack_traces + stack_errors + stats
  -> Symbolize(kallsyms + modules + rules)
  -> WriteRun(stacks.folded / stacks.meta.json / 可选 stacks.jsonl)
  -> report --stacks 生成离线 HTML
~~~

关键常量（`internal/stacks/bpf/stack_probe.bpf.c`、`internal/stacks/loader.go`）：

| 项 | 值 | 说明 |
|---|---:|---|
| `STACK_DEPTH` | 127 | `PERF_MAX_STACK_DEPTH` |
| `TARGET_PID_MAX` | 1,024 | `target_pids` 上限 |
| `STACK_TRACE_MAX` | 16,384 | 唯一栈上限 |
| `STACK_COUNT_MAX` | 131,072 | `(tgid, stack_id)` 计数槽 |
| `STACK_ERROR_MAX` | 64 | errno 种类上限 |
| `maxPerfEvents` | 4,096 | Go 侧逐 TID 打开上限 |
| `maxFrequencyHz` | 999 | CLI/采集上限 |

## 2. `inherit=1` 是否重复计数（核心结论）

### 2.1 内核语义

对 Linux 5.10 主线源码逐路径核对：

1. `kernel/fork.c: copy_process()` 对所有 clone 路径调用 `perf_event_init_task(p)`，包括 `CLONE_THREAD` 创建的线程。
2. `kernel/events/core.c: perf_event_init_context()` 以 `current` 为 parent，遍历 parent 的 pinned/flexible event group，调用 `inherit_task_group()`。
3. `inherit_task_group()` 只检查 `event->attr.inherit`；在 5.10 主线中没有 `CLONE_THREAD` / `inherit_thread` 分支。也就是 `inherit=1` 对所有子任务（进程或线程）都生效。
4. `perf_event_alloc(parent_event=...)` 在 `overflow_handler == bpf_overflow_handler` 时复制 `parent_event->prog`，所以继承事件仍会执行同一个 BPF 程序。
5. `PERF_EVENT_IOC_ENABLE` 走 `_perf_ioctl()` -> `perf_event_for_each_child()`，先 `func(event)` 再对 `child_list` 每个继承事件调用 `_perf_event_enable()`。父事件 enable 会传播到已继承但尚未 enable 的子事件。

目标内核是 `5.10.0-136.108.0.188.31.oe2203sp1.bclinux.aarch64`。BCLinux 厂商补丁未逐行 diff；上述路径是 5.10 主线语义，若厂商回移植改变继承逻辑，需要在目标内核源码上复核。当前证据没有出现“同一线程双份样本”的迹象。

### 2.2 为什么现有线程不会重复

采集顺序是：

~~~text
enumerateTargetThreads()  -> 得到 T1..Tn
openPerfEvents()          -> 对每个 Ti 打开一个 event
enablePerfEvents()        -> 统一 enable
~~~

对枚举时已存在的线程 Ti：

- Ti 的创建发生在 `openPerfEvents()` 之前；
- 创建时父线程还没有本次采集的 perf event，因此 fork 不会产生继承事件；
- 采集只为 Ti 直接打开一个 event；
- 所以 Ti 只有一个 event，不会双计。

这一点与“每个线程都打开 inherit=1”并不矛盾：继承只发生在 event 已经存在之后的 fork，不会追溯之前已经创建的子线程。

### 2.3 新线程什么时候会被采到

| 时间点 | 新线程 Tnew 的状态 | 是否采样 | 计数次数 |
|---|---|---|---|
| 枚举前创建 | 被 `enumerateTargetThreads()` 枚举 | 直接 event | 1 |
| 枚举后、创建者 event 打开前创建 | 没有直接 event；fork 时创建者还没有 event | **漏采** | 0 |
| 创建者 event 打开后、enable 前创建 | 继承创建者 event（disabled） | enable 传播后采样 | 1 |
| enable 后创建 | 继承创建者 event（enabled） | 直接采样 | 1 |
| 创建者本身是漏采的新线程 | 它的子线程同样漏采 | 漏采 | 0 |

因此：

- **没有重复计数**；
- **存在一个枚举/打开之间的漏采窗口**；
- `collector_linux.go` 中“threads created during BPF setup are still seen”的注释只对“创建者 event 已打开”之后成立，对上述窗口不成立。

`inherit=1` 还会让目标进程 fork 出的子进程继承 event。BPF 用 TGID 过滤，子进程的 tgid 不在 `target_pids`，会计入 `filtered` 后返回 0；不会污染目标进程样本。

### 2.4 是否需要 `inherit_thread`

不需要，也不应依赖。Linux 5.10 主线的 `perf_event_init_context()` 没有检查 `inherit_thread`；uapi 中该位存在，但在这条路径上没有区分线程/进程。设置它不会改变本工具在 5.10 上的行为，反而会让代码看起来依赖一个未生效的语义。

## 3. perf_event_open 与 fd 生命周期

| 检查项 | 实现 | 评价 |
|---|---|---|
| 打开方式 | `unix.PerfEventOpen(&attr, target.TID, -1, -1, PERF_FLAG_FD_CLOEXEC)` | 逐 TID，`cpu=-1`，不采全机 |
| 属性 | `PERF_TYPE_SOFTWARE` / `PERF_COUNT_SW_CPU_CLOCK`；`Freq`；`inherit`、`disabled`、`exclude_user`、`exclude_hv` | 与文档一致 |
| 上限 | `maxPerfEvents=4096`；`enumerateTargetThreads` 超过即报错 | 防止 fd 爆炸 |
| 失败清理 | 打开失败时 `closePerfEvents(events)` 关闭已打开项 | 正确 |
| 采集后清理 | `defer closePerfEvents(events)` + 显式 `closePerfEvents(events)` | `close()` 把 fd 置 -1，二次关闭安全 |
| enable 失败 | 返回错误；defer 关闭已 enable 的事件 | 已 enable 的会被 disable+close；未 enable 的直接 close |
| `EINTR` | `PerfEventOpen` / `IoctlSetInt` 没有重试循环 | 可能把一次可重试的 EINTR 变成采集失败；P2 |
| `EMFILE` / `ENFILE` | `perfEventOpenError` 对 EMFILE 给出 ulimit 提示 | 可诊断 |
| `ESRCH` | 提示目标线程已退出 | 但会中止整轮，不会跳过该线程；P2 |
| `EEXIST` | 提示已有匹配 event | 这是 `perf_event_open` 的 EEXIST；与 `bpf_get_stackid` 的 -17 是两回事 |
| mmap | 不使用 perf ring buffer，不 mmap | BPF 直接写 map，避免 ring 丢失 |
| 退出清理 | 正常路径和错误路径都走 defer | 没有发现 fd 泄漏路径 |

`stackPerfEventAttr()` 的 `Sample` 字段承载频率，`Bits` 设置 `PerfBitFreq`；`attr.Size` 用 `unsafe.Sizeof(attr)`。这部分与 `x/sys/unix` 的结构一致。

## 4. BPF map、失败计数与丢样

### 4.1 `bpf_get_stackid`

- `flags=0`：完整栈比较；没有设置 `BPF_F_FAST_STACK_CMP`，避免只比哈希导致碰撞合并。
- 没有设置 `BPF_F_REUSE_STACKID`：碰撞时返回负值，不静默复用错误栈。
- final4 的 `-17 EEXIST` 是栈哈希碰撞/已有同 ID 不同栈导致的失败，不是 perf_event_open 的 EEXIST。
- `stack_errors` 按 errno 累加，`formatStackErrors()` 输出 `-17=6` 这种可复核文本。

### 4.2 计数器语义

| 计数器 | 累加位置 | 分母/含义 |
|---|---|---|
| `samples` | `stack_counts` 成功自增后 | 成功归因的样本数 |
| `filtered` | `target_pids` 未命中时 | 继承来的非目标 tgid 事件；不是丢样 |
| `stack_fail` | `bpf_get_stackid < 0` | 取栈失败；final4 = 6 |
| `lost` | 当前 BPF 代码中**没有累加点** | 预留；不能证明 map 满/插入失败 |

### 4.3 已知缺口：`stack_counts` 插入/查找失败

当前 BPF 逻辑：

~~~c
count = bpf_map_lookup_elem(&stack_counts, &key);
if (!count) {
    bpf_map_update_elem(&stack_counts, &key, &zero, BPF_NOEXIST);
    count = bpf_map_lookup_elem(&stack_counts, &key);
}
if (count) {
    __sync_fetch_and_add(count, 1);
    stat_inc(STAT_SAMPLES);
}
return 0;
~~~

如果 `update` 失败（例如 map 满）且二次 `lookup` 也失败，样本会被静默丢弃：

- `samples` 不增加；
- `stack_fail` 不增加；
- `lost` 不增加；
- 用户只看到 `samples` 比预期少，无法从 meta 判断是 map 满还是目标没在跑。

final4 只有 221 条唯一栈、10 个 tgid，`stack_counts` 使用量约 221（远低于 131,072），没有触发该路径。因此 final4 的 `lost=0` 对本次证据可信；但不能把它当成所有运行的通用保证。建议下一版在 `count == NULL` 分支执行 `stat_inc(STAT_LOST)`，或者在文档中把 `lost` 定义为“仅 ring/传输丢失”，并把 map 插入失败纳入 `stack_fail`。

### 4.4 `readBPFStats`

- 使用 `ebpf.PossibleCPU()` 分配 per-CPU 数组，逐 index `Lookup` 后求和。
- `stats` 是 `PERCPU_ARRAY`，key 为 0..3；读取失败会追加 warning 并标记 `statsOK=false`。
- 如果 BPF `samples` 与 drain 后的总数不一致，Collect 会追加 warning，然后把 `bpfStats.Samples` 覆盖为 drained total，保证 meta 与 folded 同分母。
- `readStackCounts` 在事件关闭后执行，不存在边采边 drain 的竞态。

## 5. 线程枚举、PID 复用与 namespace

### 5.1 枚举

`enumerateTargetThreads()`：

- 读取 `/proc/<pid>/task`；
- 非目录项、非法 TID、空 task 目录都直接报错；
- TID 去重、排序；
- 超过 `maxPerfEvents` 报错，不退化为全机采样。

这意味着：目标进程在枚举时有一个线程刚好退出，`/proc/<pid>/task` 还可能列出它；`perf_event_open` 会返回 `ESRCH`，整轮采集失败，而不是跳过该线程继续。对稳定运行的 vLLM 可接受；对频繁创建/销毁线程的进程不够健壮（P2）。

### 5.2 PID 复用

`perf_event_open` 按调用时刻的 PID 找 task。若目标线程在 `readdir` 与 `perf_event_open` 之间退出、PID 被复用，工具可能 attach 到同号的新 task。代码没有读取 `/proc/<pid>/stat` 的 `starttime` 做二次校验。

缓解：

- 使用稳定运行的 vLLM PID；
- 采样前后用 `ps -o pid,lstart,comm -p <pid>` 核对；
- 不要对快速重启的进程使用 `--pid auto` 后长时间等待；
- 下一版可在打开前记录 starttime，打开后复核。

这是窄窗口风险，final4 没有出现异常样本；记录为 P2。

### 5.3 容器 namespace

`bpf_get_current_pid_tgid()` 返回内核/初始 PID namespace 的 tgid；`target_pids` 里放的是用户传入的 PID。若在容器内用 namespace PID 运行，BPF 过滤会全部 miss，表现为 `filtered` 增长、`samples=0`，而不是采错进程。

因此：

- `--pid` 必须是宿主 PID；
- 在容器内运行要先做 PID 翻译（`nsenter`/host `/proc`），或直接在宿主 namespace 运行；
- `docs/STACKS-FLAMEGRAPH.md` 已写明 host PID 约束。

### 5.4 `--pid auto`

`selectVLLMProcessTree()`：

- 只从 `vllm`、`VLLM::Worker_TP`、`EngineCore`、`vllm serve`、`vllm.entrypoints`、`vllm.worker` 这些显式身份启动；
- 递归加入子进程；
- 遇到 orinfer 进程及其子树直接排除；
- 选不到时返回错误，不退化为全机采样；
- 超过 512 个 PID 报错。

这比“按 profile 泛匹配”更安全。风险是会把 vLLM 进程下的非 vLLM 子进程一起纳入；它们只影响样本组成，不会导致全机采样。

## 6. 符号化与覆盖率

### 6.1 数据源

- `/proc/kallsyms`：内核与模块符号；
- `/proc/modules`：模块基址/大小；
- `profiles/kernel-symbols.yaml`：分类前缀；缺失时用内置 `DefaultSymbolRules()`；
- `kallsyms_sha256`：记录本次符号表哈希，便于复现。

### 6.2 覆盖率公式

`Symbolize()` 中：

~~~text
totalFrames = sum(len(stack.Frames) * stack.Count)
knownFrames = sum(stack.Count for each non-unknown frame)
coverage    = knownFrames / totalFrames
unknown_pct = 100 * (1 - coverage)
~~~

所以：

- 这是**帧加权覆盖率**，不是栈覆盖率；
- `coverage=1.0` 表示每个加权帧都解析到了符号，不表示每条栈都完整；
- final4 的 `totalFrames=13,658`，`coverage=1.0`，与 1,488 个样本不是同一分母。

### 6.3 解析规则与限制

- `nearestKernelSymbol()` / `nearestModuleSymbol()` 取“地址之前最近的符号”，不检查符号大小；地址落在符号间隙时可能归到前一个符号。这是 kallsyms 方案的通用限制。
- 模块地址先用 `/proc/modules` 的 base/size 判断归属，再在模块内找符号；找不到模块符号则 `[unknown]`。
- `parseKallsymsData()` 跳过 0 地址；如果全是 0（`kptr_restrict`），返回空符号集并写 warning，不会伪造 100% 覆盖。
- `isSamplerFrame()` 会移除 `perf_swevent_`、`__perf_event_overflow`、`bpf_get_stackid`、`perf_event_`、`bpf_prog_` 等采样器自身帧；如果整条栈都是这些帧，会保留并标为 `sample`，不会产生空栈。
- `stackIdentity()` 只用帧名做唯一键，不用 module/offset/addr；同名不同 offset 的栈会合并。这是折叠格式的取舍，报告里应理解为“同名调用链聚合”。

### 6.4 分类

`ClassifyName(name, module, rules)`：

1. module 含 `devdrv`/`davinci`/`hisi` -> `npu`；
2. 按 YAML 顺序匹配前缀，第一个命中者生效；
3. 默认 `other`。

`profiles/kernel-symbols.yaml` 的 NPU 前缀包含 `devdrv_`、`drv_`、`davinci`、`hisi_`、`svm_`、`ascend`、`aicore`、`aicpu`、`hdc_`，以及 2026-11-27 由 kallsyms 实证补充的 `hdcdrv_`、`hdccom_`、`devmm_`、`trs_`、`pcivnic_`、`dms_`、`tsagent_`、`vdavinci`、`dbl_`、`ka_`。

报告侧 `flamegraphFrameCategory()` 先调用共享的 `stacks.ClassifyName()`，再走 `hccl`/`memory`/`sync` 等报告兜底；这修复了“采集侧分类扩展、报告侧仍用旧硬编码”的不一致。

## 7. folded 格式与报告

### 7.1 folded

- 每行 `root;...;leaf count`，最后一个空格分隔 count；
- `foldedFrameName()` 把 `;` 替换为 `:`、换行替换为空格；帧名内的空格保留；
- `WriteFolded()` 按行排序、同名栈聚合，输出确定性；
- `ReadFolded()` 拒绝空行、缺分隔符、非法/溢出 count、空帧名；允许 count=0；
- `bufio.Scanner` 上限 1 MiB/行；超长行报错而不是截断；
- 重复栈在 `ReadFolded()` 中保留，`BuildFlamegraph()` 的树按路径合并；样本总数不丢。

### 7.2 火焰图树

- 最大深度 128、最大节点 4096；
- 超限时把剩余子树合并为 `[truncated]`，样本计数保留；
- `TotalSamples=root.Count`，`TotalFrames` 是加权帧数；
- `NPUFramePct = npuFrames * 100 / totalFrames`；
- `TopNPU` 的 `Samples` 是该函数在栈中的出现次数 × 样本数；本数据没有递归，所以 101 也等于“出现的采样栈数”；
- `SelfPct` 的分母是 `TotalSamples`，不是 `TotalFrames`。

### 7.3 报告可用性（已知 P0）

`buildFlamegraphWithLimits()` 在 `total > 0` 且 `root.Count > 0` 时无条件设置：

~~~go
data.Status.Available = true
~~~

它没有用 `SymbolCoverage` 或 `unknownFrames == totalFrames` 做门禁。实测 coverage=0、unknown=100、folded 全是 `[unknown]` 时，report 仍输出 `flamegraph=available`，只把 `confidence` 标为 `unknown`。

影响与豁免：

- final4/final5 交付证据的 coverage 是 100%，不受该分支影响；
- 对“kptr_restrict 导致全 unknown”的现场，验收不能只看 `flamegraph=available`，必须检查 meta 的 `symbol_coverage`/`unknown_pct`；
- 下一版应在 `BuildFlamegraph()` 中把“全 unknown”或 coverage 低于阈值的情况标为 unavailable，并给出 `symbolization_incomplete`/`unknown_frames`。

### 7.4 XSS 与离线

- 报告是单个 HTML，内嵌一个 `<script>` 和一个 `<style>`；
- 无外部 `script src`、`link`、`img`、`iframe`、`@import`、HTTP URL；
- `html/template` 对标题/文本做 HTML 转义，JSON 数据通过模板嵌入；
- QA 的 XSS 用例把 `<img src=x onerror=...>`、`</script><script>...` 作为帧名/标题，DOM 中只作为文本/数据属性出现，没有新增 `<img>` 或第二个 `<script>`；
- Node 18 与 Node 12 语法检查均 `rc=0`。

## 8. CLI 与退出码

| 命令/场景 | 退出码 | 说明 |
|---|---:|---|
| `stacks` 参数错误 | 2 | flag 解析、缺 PID、非法 duration/freq/filter |
| `stacks` 采集/BPF 加载失败 | 3 | perf/BPF/权限/目标不存在 |
| `stacks` 符号化/写盘失败 | 4 | kallsyms/modules/写 run |
| `stacks` `samples=0` | **0** | 会写 run 并打 warning；与“samples=0 直接失败”的验收文字不一致（P0） |
| `report` 参数错误 | 2 | 含 `flag.ErrHelp`；`report --help` 也返回 2 |
| `report` stacks 证据加载失败 | 3 | meta 缺失/损坏、folded 解析失败 |
| `report` 其他渲染失败 | 4 | 模板/写文件 |

`report` 没有 `--symbols`；符号规则只在 `stacks` 采集时使用。`report --stacks` 可以传 run 目录，也可以传与 `stacks.meta.json` 同目录的 `stacks.folded`；单独传一个没有 meta 的 folded 会失败。

`--kernel-filter npu` 是符号化之后的整栈过滤：

- 保留包含至少一个 NPU 类帧的整条栈；
- `meta.Samples` / `meta.UniqueStacks` 改为过滤后的值；
- `stack_fail` / `filtered` 仍是整轮计数；
- warning 记录 `kept X/Y stacks and A/B samples`。

## 9. final5 证据可信度

final4 原始证据（`.analysis/stacks-probe/final4/stacks.folded`、`stacks.meta.json`）由 final4 采集；final5 只改分类/报告/模板，采集器与 BPF 对象未变。因此用 final5 对同一份原始 folded/meta 生成 4.61% 报告是成立的；不要把 final4 二进制本身当交付物。

| 指标 | final4 原始证据 | 说明 |
|---|---:|---|
| host | `bms-84865758` | 昇腾 910B2 裸机 |
| kernel | `5.10.0-136.108.0.188.31.oe2203sp1.bclinux.aarch64` | 与 perf/BPF 语义核对目标一致 |
| target_pids | 10 个 | 主进程/EngineCore/8 Worker |
| threads | 1,357 | 来自采集 warning，不是 meta schema 字段 |
| duration_s | 10.034801594 | 实际墙钟 |
| frequency_hz | 99 | 每线程 on-CPU 机会 |
| samples | 1,488 | 成功归因样本 |
| unique_stacks | 221 | 唯一折叠栈 |
| stack_fail | 6 | 全部 -17 EEXIST；6/1,488=0.403%，6/(1,488+6)=0.402% |
| filtered | 0 | 没有继承来的非目标 tgid 事件 |
| lost | 0 | 本次无 map 满证据；见 4.3 缺口 |
| symbol_coverage | 1.0 | 帧加权 |
| unknown_pct | 0 | 帧加权 |
| total_frames | 13,658 | 加权帧总数 |
| NPU 帧数 | 630 | 630/13,658 = 4.61% |
| Top NPU | `drv_ascend_intf_ioctl` 101 / `trs_ioctl` 80 / `devmm_svm_ioctl` 18 | 101/1,488 = 6.79% |

### 9.1 为什么 10 秒只有 1,488 样本

99 Hz 是“每个已打开线程在 on-CPU 时的采样机会”，不是整机每秒 99 个样本。理论上限是：

~~~text
99 Hz × 10 s × 1,357 threads = 1,343,430 次线程机会
~~~

实际只有 1,488 次成功样本，原因包括：

- `exclude_user=1`：只在内核态 on-CPU 时触发，Python/vLLM 用户态时间不计；
- 大多数线程在 10 秒窗口内大部分时间 off-CPU；
- 只有进入内核（ioctl、DMA、调度、锁、网络）的线程才会产生样本；
- 8 个 Worker 不一定同时有内核工作。

因此不能用“1,488 远小于 1,343,430”推断采集失败；要看 `stack_fail`、`filtered`、`lost`、coverage 和样本分布。

### 9.2 口径区分

- **4.61%**：NPU 类帧的加权帧占比，分母 `total_frames=13,658`；不是 NPU 利用率，也不是算子耗时占比。
- **6.79%**：`drv_ascend_intf_ioctl` 的样本计数 / `total_samples=1,488`；本数据没有递归，所以也等于“出现的采样栈比例”。
- **0.86%**：旧报告用旧硬编码分类得到的 NPU 帧占比，已废弃；不要引用。
- **0.9917%**：final2 旧报告的 NPU 帧占比，和 final4 不是同一 run；不要混写。
- **8.10%**：final2 的 `drv_ascend_intf_ioctl` 样本/栈命中率，不是 NPU 总占比。

### 9.3 边界

- 只覆盖 host kernel on-CPU 栈；
- 不覆盖 AI Core / MatMul / FlashAttention / Cube / Vector；
- 不覆盖 Python/PyTorch/vLLM 用户栈；
- 不覆盖 off-CPU 完整时长（需 `offcpu.jsonl`）；
- 不能从 host 栈推出单次请求延迟或 NPU 算子耗时；
- 不能把 `drv_ascend_intf_ioctl` 的出现解释成“NPU 忙 6.79%”。

## 10. 已知问题分级

### P0（影响交付结论，本次已明确豁免或已解决）

| 编号 | 问题 | 本次影响 | 处理 |
|---|---|---|---|
| P0-1 | coverage=0 仍 `flamegraph=available` | final4 coverage=1.0，不影响本次证据；零覆盖现场会误标 | 下一版修 `BuildFlamegraph()`；验收必须查 meta |
| P0-2 | `stacks` samples=0 仍返回 0 | final4 samples=1,488，不影响本次；空采集退出码不能单独证明成功 | 下一版改为非零或文档明确“0 ≠ 验收通过” |
| P0-3 | `lost` 未覆盖 `stack_counts` map 满 | final4 使用 221/131,072，未触发；通用场景不能把 lost=0 当无丢样证明 | 下一版在失败分支累加 `lost` 或 `stack_fail` |
| P0-4 | final4 二进制/旧 VERIFICATION 过期 | 已用 final5 + final4 原始证据生成 4.61% 报告；final4 二进制标注 deprecated | 交付固定 final5；见 `VERIFICATION-classified.md` |

### P1（会造成误读或操作失败）

| 编号 | 问题 | 处理 |
|---|---|---|
| P1-1 | README stacks 示例仍用 `cann-ebpf-linux-arm64-m3` | 改为 final5；m3 没有 `cmdStacks` |
| P1-2 | README 仍写“没有调用栈采样” | 限定到旧 span 报告；stacks 章节单独说明 |
| P1-3 | README 写“NPU 帧占比（按样本加权）” | 改为“帧加权，NPU 类帧次数 / total_frames” |
| P1-4 | STACKS-FLAMEGRAPH/ACCEPTANCE 开头“不声称已验证”与实机基线矛盾 | 改为“契约已核对；实机基线见对应章节” |
| P1-5 | 文档说 `report --stacks` 可直接传 folded，但 meta 必须成套 | 写明“与 meta 同目录的 folded 路径” |
| P1-6 | final4 `VERIFICATION.md` 仍写 0.86%/旧 Top NPU | 标注 deprecated，指向 `VERIFICATION-classified.md` |
| P1-7 | `threads/processes` 不是 meta 字段 | 标注来自采集 warning/现场记录 |

### P2（健壮性/可维护性）

| 编号 | 问题 | 处理 |
|---|---|---|
| P2-1 | `EINTR` 未重试 | 采集失败时记录 errno，必要时重跑 |
| P2-2 | 单个线程 `ESRCH` 中止整轮 | 稳定进程可接受；高频线程进程建议跳过并 warning |
| P2-3 | PID 复用无 starttime 校验 | 采样前后 `ps` 核对；下一版加校验 |
| P2-4 | map 满路径无 lost | 见 P0-3 |
| P2-5 | `--profile` 只在 `--pid auto` 使用，部分文档未写 | 补一行 |
| P2-6 | `report --help` 返回 2 | 脚本不要把它当失败；或单独处理 `flag.ErrHelp` |
| P2-7 | 超长行 1 MiB 上限 | 文档写明；正常内核栈远低于此 |
| P2-8 | `stackIdentity` 忽略 module/offset | 同名不同 offset 合并；在文档中说明 |

## 11. 已执行测试

~~~bash
/tmp/go1.24.6/bin/go test ./... -count=1
/tmp/go1.24.6/bin/go vet ./...
/tmp/go1.24.6/bin/go test -race ./internal/stacks ./internal/report -count=1
gofmt -l cmd internal
sh -n scripts/stacks_preflight.sh
dash -n scripts/stacks_preflight.sh
~~~

结果：

- `go test ./...` 通过；
- `go vet ./...` 通过；
- race 通过；
- `gofmt -l` 无输出；
- `sh -n` / `dash -n` 通过；
- `git diff --check` 仅报 `docs/OPERATIONS.md:102: trailing whitespace.`，是工作区既有改动，非本功能引入。

## 12. 审计结论

**PASS（带明确边界）。**

- 采集链路、perf event 生命周期、BPF map 容量、符号化、folded 格式和报告主路径可用；
- `inherit=1` 不会对现有线程重复计数；新线程存在枚举/打开窗口的漏采，应在报告中作为覆盖限制说明；
- final4/final5 证据足以支持“host kernel 栈中观测到 NPU 驱动/资源管理路径”的结论；
- 不足以支持“AI Core 算子耗时”“NPU 利用率”“请求级归因”或“<1% CPU 开销”的结论；
- P0-1/P0-2/P0-3 是通用代码缺口，本次交付证据不受影响，但不能被解释为通用保证；下一版应修复。
