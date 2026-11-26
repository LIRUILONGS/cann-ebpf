# M2：host off-CPU / futex 归因

状态：**代码、单元测试、交叉编译完成；尚未在真实内核上 attach 验证**。

本文明确区分“已完成的代码”和“已上机验证的事实”。除第 1 节外，所有“会/应该”的描述都还是设计预期，
必须以服务器实测结果为准，禁止在交接或报告里写成已验证。

## 1. 结论

M2 新增两件事：

1. `sched_switch` tracepoint：统计目标调用线程在 CANN 调用期间被切出 CPU 的时间；
2. `futex_wait` / `futex_wait_requeue_pi` kprobe：统计同一线程在 CANN 调用期间的 futex 等待时间。

结果以 `offcpu_ns` / `futex_ns` 写入标准 `spans.jsonl`，由 `diagnose` 用来判定
`host_lock_contention` / `host_blocked_non_futex` / `host_cpu_running`。

它回答的是“host 线程是不是被卡住、卡在锁还是其它等待”，**不回答** device kernel/memcpy/HCCL 的
执行时间，也不回答单 request 延迟。

## 2. BPF 侧设计

### 2.1 状态

每个经历过 CANN uprobe entry 的线程在 `tid_state`（hash，key=tid）里有一条记录：

```text
struct tid_state {
    offcpu_since;      // 本次被切出的时间戳，0 表示在 CPU 上
    offcpu_acc;        // 当前 CANN 调用累计 off-CPU
    futex_since;       // futex 等待开始时间戳
    futex_acc;         // 当前 CANN 调用累计 futex 等待
    offcpu_stack[8];   // 嵌套调用的快照
    futex_stack[8];
    inflight;          // 当前 CANN 嵌套深度
}
```

只有进过 CANN probe 的线程才有记录，因此系统级 tracepoint 不需要额外的 target map：
`sched_switch` 对 `prev_pid`/`next_pid` 做一次 hash 查找，查不到就直接返回。

### 2.2 累计规则

- `on_entry`：在 `inflight` 中记录 start 后调用 `blocking_enter`，把当前累计值压入栈并清零，
  使嵌套内层调用有独立的 inclusive 窗口；超过 8 层共享父累计，超过 64 层视为 uprobe 漏配对并复位。
- `sched_switch`：
  - `prev_pid` 有 state → `offcpu_since = now`；
  - `next_pid` 有 state 且 `offcpu_since != 0` → `offcpu_acc += now - offcpu_since`，然后清零。
- `futex_wait*` 进入：若 `inflight > 0` 且尚未在等待，记录 `futex_since`；
  返回：`futex_acc += now - futex_since`。
- `on_exit`：**无论是否被 duration/采样过滤**，都先 `blocking_exit` 排空累计值，保证嵌套深度平衡；
  通过过滤后再把 `offcpu_ns`/`futex_ns` 和 flag 写进 `span_event`。

### 2.3 事件格式与“是否测量过”

`struct span_event` 从 32 字节扩展到 48 字节：

```text
start_ns, dur_ns, pid, tid, func_id, flags, offcpu_ns, futex_ns
```

`flags` 的两个 bit 表示对应值是否被真正测量：

```text
SPAN_FLAG_OFFCPU = 1
SPAN_FLAG_FUTEX  = 2
```

用户态只有在对应 bit 置位时才把值写成 span 指针：

- JSON 里**没有** `offcpu_ns` 字段 = 未测量；
- 字段存在且为 `0` = 测得 0。

这样 `diagnose` 不会把“探针没开”误判成“没有阻塞”。

### 2.4 加载隔离（防止新探针拖垮主路径）

`cann.bpf.o` 现在同时包含 74 个 uprobe 程序和 5 个 blocking 程序。加载时：

- 主 collection **剪掉** 5 个 blocking 程序后再 `NewCollection`，主路径与旧版本完全一致；
- blocking 程序单独加载一个 collection，只保留 `tid_state` map，并通过 `MapReplacements`
  复用主 collection 的同一张 map。

因此 blocking 程序如果被 verifier 拒绝（不同内核/架构），只会得到一条 warning 和“没有 offcpu_ns”，
**不会**导致整个 attach 失败。

## 3. 用户态接线

| 位置 | 变化 |
|---|---|
| `internal/runlog/span.go` | 新增 `offcpu_ns` / `futex_ns`（`*int64`），加入保留字段与校验 |
| `internal/probes/cann.go` | `span_event` 解码扩展为 48 字节 |
| `internal/probes/blocking_common.go` | 模式解析、flag 常量、span 赋值 |
| `internal/probes/blocking_linux.go` | `sched_switch` tracepoint 与 futex kprobe attach |
| `internal/probes/loader.go` | 主/blocking collection 分离加载 |
| `internal/probes/run.go` | 解析 `--blocking`、加载/attach、告警、span 写入 |
| `cmd/cann-ebpf/attach.go` | 新增 `--blocking auto|on|off`；输出 `blocking mode=...` |
| `internal/diagnose` | `blocking` 汇总与 host 判定 |

### 3.1 `--blocking`

- `auto`（默认）：按 profile 的 `probes.sched` / `probes.futex`；`vllm-ascend`、`orinfer` 当前都是 `true`。
- `on`：强制两者开启。
- `off`：两者都不加载、不 attach（长跑或怀疑开销时使用）。

attach 失败、tracefs 不可用、kprobe 符号不存在都只产生 warning；`blocking` 结果会如实标记
`offcpu=false` / `futex=false`。

## 4. 判定

`diagnose` 在 sync span 上计算：

```text
offcpu_ratio = Σ offcpu_ns / Σ duration_ns
futex_ratio  = Σ futex_ns  / Σ duration_ns
```

| 条件 | verdict |
|---|---|
| 无采样 | `blocking_unknown` |
| offcpu ≥ 0.5 且 futex ≥ 0.3 | `host_lock_contention` |
| offcpu ≥ 0.5 且 futex < 0.3 | `host_blocked_non_futex` |
| offcpu < 0.2 | `host_cpu_running` |
| 其它 | `host_blocking_mixed` |

`device_bound`、`launch_starvation` 等仍然只能等 M3 设备侧证据；`diagnose` 不会输出。

## 5. 已知限制（必须写进报告）

1. **sched_switch 上下文偏移按 5.10 aarch64 固定**（prev_pid=24、next_pid=56）。不同内核大版本需要重编
   或改成 CO-RE；当前项目目标就是 `5.10.0-136 openEuler aarch64`。
2. `sched_switch` 是全系统 tracepoint，每个上下文切换都会触发一次；只对已有 `tid_state` 的线程累计。
   长跑或多目标场景必须做开销基线，必要时 `--blocking off`。
3. off-CPU 统计包含“可运行但被抢占”的时间，不只是阻塞等待；因此它不等于锁等待。
4. futex 只覆盖 `futex_wait` / `futex_wait_requeue_pi`。若内核内联或符号缺失，futex 指标缺失，
   但 off-CPU 仍可用；绝不能把 futex=0 当成“没有锁竞争”。
5. 嵌套 CANN span 的 off-CPU 是 inclusive，会重叠；不能跨嵌套函数求和，`diagnose` 已显式提示。
6. 一个 uval 的 entry 未配对（进程被 kill）会让该 tid 的 inflight 卡住；代码在 >64 层时复位并放弃旧值。
7. 不能把 `offcpu_ns` 当成 request 延迟或 NPU 时间。

## 6. 服务器验证步骤（未执行）

前置：以 root 运行；`/sys/kernel/tracing` 可访问；已确认 V4 状态并得到短窗口授权。

```bash
# 1. 先只确认 profile 解析与 blocking 选择，不 attach
./cann-ebpf-linux-arm64-m2 validate --config profiles/vllm-ascend.yaml

# 2. 10 秒短窗口，显式开启 blocking
OUT="runs/ebpf-m2-$(date -u +%Y%m%dT%H%M%SZ)"
./cann-ebpf-linux-arm64-m2 attach \
  --pid auto --profile vllm-ascend --profiles-dir profiles \
  --duration 10s --blocking on --no-context --output "$OUT"

# 3. 检查 attach 输出
#    blocking mode=on offcpu=true futex=true attached=5 failed=0
#    若 attached/failed 与预期不符，记录 warning，不要直接下结论

# 4. 检查 span 是否带测量标记（存在字段即测量过，0 也是有效值）
grep -c '"offcpu_ns"' "$OUT/spans.jsonl"
grep -c '"futex_ns"'  "$OUT/spans.jsonl"

# 5. 生成定位报告
./cann-ebpf-linux-arm64-m2 diagnose --input "$OUT" --json --out "$OUT/diagnose.json"
cat "$OUT/summary.md"

# 6. 采集后检查
systemctl status vllm-ds4-8905 --no-pager
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8905/health
grep -c 'cann_' /sys/kernel/tracing/uprobe_events || true
# kprobe 残留（若脚本使用 kprobe_events；perf_event 方式随进程退出）
```

通过标准（建议）：

- attach 后 `blocking offcpu=true futex=true`，且 `failed=0`；
- 至少一条 sync span 带 `offcpu_ns`；存在锁竞争时 `futex_ns` 非空且比例合理；
- `/health` 前后 200、service 前后 active、uprobe/kprobe 无残留；
- 10 秒窗口吞吐/延迟相对基线无明显回归（具体基线方法见 `PLAN.md` 14.1）。

若 verifier 拒绝 blocking 程序：

- attach 仍然成功，只是 `blocking offcpu=false` 并带 warning；
- 记录内核版本、`dmesg` verifier 日志、以及 warning 原文；
- 不要删除代码或改主路径，先判断是上下文偏移还是 verifier 规则问题。

## 7. 合成锁竞争验证（建议先做）

在非生产机器上编译一个短小的 pthread mutex 竞争程序，导出与 CANN 同名符号不必要；验证
`sched_switch` + `futex_wait` 的正确性只需要：

1. 用 `--blocking on` attach 一个调用 `pthread_mutex_lock` 的合成进程；
2. 观察 `offcpu_ns`、`futex_ns` 是否随竞争强度变化；
3. 用 `strace -c -e futex` 对照 futex 调用次数与耗时。

该验证同样未执行，属于接手后的第一步硬件工作。
