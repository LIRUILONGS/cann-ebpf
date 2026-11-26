# M2：host off-CPU / futex 归因

状态：**已在真实内核上验证**（2026-11-26，`bms-84865758`，V4 生产服务 + 合成锁竞争目标）。
实测数据、命令和证据见 `docs/M2-VALIDATION.md`。本文描述已落地的实现和边界。

## 1. 结论

M2 新增两件事：

1. `sched/sched_switch`：统计调用线程在 CANN 调用期间被切出 CPU 的时间；
2. `syscalls/sys_enter_futex` / `sys_exit_futex`：统计同一线程在 CANN 调用期间的 futex 等待。

结果以 `offcpu_ns` / `futex_ns` 写入标准 `spans.jsonl`，由 `diagnose` 用来判定
`host_lock_contention` / `host_blocked_non_futex` / `host_cpu_running`。

它回答“host 线程是不是被卡住、卡在锁还是其它等待”，**不回答** device kernel/memcpy/HCCL
执行时间，也不回答单 request 延迟。

## 2. BPF 设计

### 2.1 状态

每个进过 CANN uprobe 的线程在 `tid_state`（hash，key=tid）里有一条记录：

```text
struct tid_state {
    offcpu_since;   // 本次被切出的时间戳，0 表示在 CPU 上
    offcpu_acc;     // 最外层 CANN 调用累计 off-CPU
    futex_since;    // futex 等待开始时间戳
    futex_acc;      // 最外层 CANN 调用累计 futex
    inflight;       // CANN 嵌套深度
    pad;
}
```

记录只对进过 CANN probe 的线程创建，因此系统级 tracepoint 对无关线程只需一次 hash 查找。
`tid_state` 没有变长数组下标：5.10 verifier 对 `array[depth]` 这类访问的边界证明很脆弱，
早期版本曾因此被 `permission denied: R2 unbounded memory access` 拒绝。

### 2.2 累计规则

- `on_entry`：成功登记 `inflight` 后调用 `blocking_enter`。
  - `inflight == 0`（最外层）：清零 `offcpu_acc` / `futex_acc`；
  - 每个 entry 都把 `offcpu_since` / `futex_since` 清零——线程能执行 entry 就说明它正在 CPU 上、
    不在 futex 等待里，任何残留的 since 都是陈旧的（例如 tid 复用）；
  - 嵌套只增加 `inflight`，不碰累计值。
- `sched_switch`：`prev_pid` 有 state 时写 `offcpu_since = now`；`next_pid` 有 state 且 since 非 0
  时累加 delta（`inflight > 0` 才计入）。
- `sys_enter_futex`：`op & 0x7f` 为 `FUTEX_WAIT` / `WAIT_BITSET` / `WAIT_REQUEUE_PI` 且
  `inflight > 0` 时记录 `futex_since`；`sys_exit_futex` 累加 delta。
- `on_exit`：**每个 exit 都先调用 `blocking_exit`**（即使 inflight 配对缺失或调用会被采样过滤），
  保证深度不会泄漏。只有当 `inflight` 减到 0（最外层）时才返回累计值和 `measured=1`。
- `sched_process_exit`：删除该 tid 的 state，避免 tid 复用继承死线程的 inflight/累计值。

### 2.3 为什么用 syscall tracepoint 而不是 `futex_wait` kprobe

首次上机时 `futex_wait` kprobe 只捕到约 10% 的等待：内核在部分调用点把 `futex_wait` 内联/克隆了，
符号 kprobe 覆盖不到这些调用点。改成 `sys_enter_futex` / `sys_exit_futex` tracepoint 后，
合成锁竞争窗口的捕获率从 90/862 提升到 791/791，`futex_ns` 中位数与 span 时长一致。

### 2.4 事件格式与“是否测量过”

`struct span_event` 为 48 字节：

```text
start_ns, dur_ns, pid, tid, func_id, flags, offcpu_ns, futex_ns
```

`flags` 的两个 bit 表示值是否被真正测量：

```text
SPAN_FLAG_OFFCPU = 1
SPAN_FLAG_FUTEX  = 2
```

只有最外层 CANN span 会被标记测量。嵌套 span 不写这两个字段（避免父子重复累计）；
JSON 里没有字段 = 未测量，字段为 0 = 测得 0。

### 2.5 加载隔离

`cann.bpf.o` 含 74 个 uprobe 程序 + 4 个 blocking tracepoint 程序。加载时：

- 主 collection 先剪掉 4 个 blocking 程序再加载；但 uprobe 的 exit 程序本身会读写
  `tid_state`/`blocking_cfg`，所以主 collection 仍包含这两张 map，必须通过 verifier。
- blocking 程序单独加载一个 collection，只保留 `tid_state`，并用 `MapReplacements`
  复用主 collection 的同一张 map。

因此 blocking attach 失败（tracefs、syscall tracepoint、权限）只会降级为“没有 offcpu_ns”，
不会导致主 host span 采集失败；但主 collection 里 blocking 代码若 verifier 不通过，会整体加载失败，
所以 BPF 代码必须保持 5.10 可验证（见 2.1）。

## 3. 用户态接线

| 位置 | 变化 |
|---|---|
| `internal/runlog/span.go` | `offcpu_ns` / `futex_ns`（`*int64`），保留字段与校验 |
| `internal/probes/cann.go` | `span_event` 解码 48 字节 |
| `internal/probes/blocking_common.go` | 模式解析、flag 常量、tracepoint 清单、span 赋值 |
| `internal/probes/blocking_linux.go` | sched / syscalls tracepoint attach、blocking map 配置 |
| `internal/probes/loader.go` | 主/blocking collection 分离加载 |
| `internal/probes/run.go` | `--blocking` 解析、blocking 先于 uprobe attach、span 写入 |
| `cmd/cann-ebpf/attach.go` | `--blocking auto|on|off`，输出 `blocking mode=...` |
| `internal/diagnose` | blocking 汇总、无效样本排除、host 判定 |

### 3.1 `--blocking`

- `auto`（默认）：按 profile 的 `probes.sched` / `probes.futex`；`vllm-ascend`、`orinfer` 当前为 `true`。
- `on`：强制开启两者。
- `off`：不加载、不 attach（长跑或怀疑开销时使用；实测该路径不产生任何 blocking 字段）。

blocking attach 顺序在 uprobe 之前，因此采集窗口内每条最外层 span 都带测量标记，不存在启动空窗。

## 4. 判定

`diagnose` 在 sync span 上计算：

```text
offcpu_ratio = Σ offcpu_ns / Σ duration_ns
futex_ratio  = Σ futex_ns  / Σ duration_ns
```

| 条件 | verdict |
|---|---|
| 无有效采样 | `blocking_unknown` |
| offcpu ≥ 0.5 且 futex ≥ 0.3 | `host_lock_contention` |
| offcpu ≥ 0.5 且 futex < 0.3 | `host_blocked_non_futex` |
| offcpu < 0.2 | `host_cpu_running` |
| 其它 | `host_blocking_mixed` |

`diagnose` 会排除 `offcpu_ns`/`futex_ns` 大于 span `duration_ns` 的物理不可能样本（计入
`blocking.invalid_samples`），并写入“当前无法判定”。这是防御手段；当前实现实测为 0 条。

`device_bound`、`launch_starvation` 等仍需 M3 设备侧证据，M2 不输出。

## 5. 已知限制（必须写进报告）

1. sched/syscall tracepoint 的字段偏移按 5.10 aarch64 固定（sched_switch `prev_pid=24`/
   `next_pid=56`、`sched_process_exit.pid=24`、`sys_enter_futex.op=24`）。换内核大版本要重验。
2. 只统计最外层 CANN span；嵌套 span 没有 blocking 字段，不能把 `spans.jsonl` 中缺字段的
   嵌套行当成“没有阻塞”。
3. off-CPU 包含“可运行但被抢占”的时间，不等于锁等待；`host_lock_contention` 需要 futex 占比同时达标。
4. `sched_switch` 是全系统 tracepoint，每个上下文切换都会触发一次；只对已有 state 的线程累计。
   长跑或多目标场景必须做开销基线，必要时 `--blocking off`。
5. 若 `CONFIG_FTRACE_SYSCALLS` 缺失，futex tracepoint attach 会失败，此时只有 offcpu_ns；
   不能把 futex=0 当成“没有锁竞争”。
6. 漏掉 uretprobe 会让该线程 inflight 卡住，后续 span 不再有 blocking 数据，直到线程退出
   （`sched_process_exit` 清 state）或深度超过 64 复位。这是降级，不是伪造。
7. 不能把 `offcpu_ns` 当成 request 延迟或 NPU 时间。

## 6. 验证结果（摘要）

详见 `docs/M2-VALIDATION.md`。

- V4 生产服务 10 秒：`blocking mode=on offcpu=true futex=true attached=4 failed=0`，
  7,561 spans、dropped 0、无效样本 0；`/health` 200→200、service active→active、uprobe/kprobe 残留 0。
  该窗口 off-CPU 1.1%、futex 0.0%，判定 `host_cpu_running`（host 基本在 CPU 上跑，不是锁等待）。
- 合成 pthread mutex 竞争：791 spans，off-CPU 99.7%、futex 99.9%，判定 `host_lock_contention`
  confidence 1.00，且 `futex_ns` 中位数 ≈ span 时长。
- `--blocking off`：6,508 spans，没有任何 offcpu/futex 字段，服务状态不变。

## 7. 复现命令

```bash
# 1. 合成锁竞争（在测试机上）
cd <repo>
gcc -shared -fPIC -O2 -o libascendcl.so fake_cann.c -lpthread
gcc -O2 -o fake_cann_app fake_app.c -L. -lascendcl -lpthread -Wl,-rpath,'$ORIGIN'
./fake_cann_app 25 &
./cann-ebpf-linux-arm64-m2 attach --pid $! --profile m2-synth --profiles-dir profiles \
  --duration 8s --blocking on --no-context --output runs/m2-synth

# 2. V4 短窗口（先确认授权与 health）
OUT="runs/ebpf-m2-$(date -u +%Y%m%dT%H%M%SZ)"
./cann-ebpf-linux-arm64-m2 attach --pid auto --profile vllm-ascend --profiles-dir profiles \
  --duration 10s --blocking on --no-context --output "$OUT"

# 3. 定位报告
./cann-ebpf-linux-arm64-m2 diagnose --input "$OUT"
```

检查点：`attached=4 failed=0`、`spans.jsonl` 中最外层 span 带 `offcpu_ns`/`futex_ns`、
无 `offcpu_ns > duration_ns`、`/health` 与服务状态不变、探针残留为 0。
