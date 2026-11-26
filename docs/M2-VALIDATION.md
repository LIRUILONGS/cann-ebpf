# M2 上机验证记录

日期：2026-11-26（CST 15:18–15:40）
主机：`bms-84865758`（`36.137.90.82`）
操作人：自动化验证，未停止/重启 V4，未修改目标进程、CANN 库、驱动或 sysctl。

## 1. 环境

| 项 | 值 |
|---|---|
| 内核 | `5.10.0-136.108.0.188.31.oe2203sp1.bclinux.aarch64` |
| 架构 | aarch64，192 核 |
| 服务 | `vllm-ds4-8905.service` active，`/health` 200 |
| Worker PID | 1452, 3962, 6568, 8890, 11340, 13505, 15629, 17758 |
| frontend / EngineCore | 4188787 / 4192024 |
| 测试目录 | `/root/cann-ebpf/m2-test-20261126T151851Z` |
| 二进制 | `cann-ebpf-linux-arm64-m2` |
| 二进制 SHA-256 | `8a515b96116ec19a28cbc73f7f629b99fa9f564d57fdcb3075380f37f7cc428e` |
| 编译器（合成目标） | gcc 10.3.1 |
| tracefs | `/sys/kernel/tracing` 可用 |
| futex 符号 | `futex_wait` 存在；`futex_wait_requeue_pi` 不存在 |
| 旧证据 | `/root/cann-ebpf/m4-20261126/...` 未修改（`spans.jsonl` SHA-256 仍为 `fb2625ba...`） |

## 2. V4 真实服务 10 秒（blocking on）

命令：

```bash
./cann-ebpf-linux-arm64-m2 attach \
  --pid auto --profile vllm-ascend --profiles-dir profiles \
  --duration 10s --blocking on --no-context \
  --output runs/m2-blocking-10s-20261126T073824Z
```

结果：

| 检查项 | 结果 |
|---|---|
| blocking attach | `mode=on offcpu=true futex=true attached=4 failed=0` |
| span | 7,561 条；`dropped_events=0` |
| 带 blocking 字段的 span | 7,534（27 条为嵌套 CANN span，按设计不采样） |
| `offcpu_ns > duration_ns` | 0 条 |
| `futex_ns > duration_ns` | 0 条 |
| `offcpu_ns > 0` / `futex_ns > 0` | 656 / 62 条 |
| `/health` | 200 → 200 |
| service | active → active |
| uprobe / kprobe 残留 | 0 / 0 |
| 采集墙钟 | 64 秒（含 attach setup；不是采样窗口） |

`diagnose` 结果：

```text
sync span 样本 5656/5661
host wall 115,313,356,934ns，off-CPU 1,296,602,041ns = 1.1%，futex 89,440ns = 0.0%
verdict = host_cpu_running（off-CPU < 20%，host 基本在 CPU 上执行；设备侧仍未知）
```

解读：该 10 秒窗口的真实流量里，8 个 Worker 的 `Synchronize*` 调用 98.9% 的时间线程仍在 CPU 上
（不是 off-CPU 阻塞），futex 等待几乎为 0。因此 M2 给出 `host_cpu_running` 而不是锁竞争，
这是有证据支持的结论；`device_bound` 仍必须等 M3。

## 3. 合成锁竞争（验证判定闭环）

方法：在测试目录编译一个导出 `aclrtSynchronizeStream` 的假 CANN 库，使用 pthread mutex；
holder 线程持有锁约 9ms、释放约 1ms，主线程循环调用。自定义 `m2-synth` profile 只挂
`aclrtSynchronizeStream`，脚本用 `strace -T` 交叉核对 futex 等待。

命令：

```bash
./fake_cann_app 25 &
./cann-ebpf-linux-arm64-m2 attach --pid $! --profile m2-synth --profiles-dir profiles \
  --duration 8s --blocking on --no-context --output runs/m2-synth-20261126T073752Z
```

结果（`runs/m2-synth-20261126T073752Z`）：

| 项 | 值 |
|---|---|
| blocking attach | `attached=4 failed=0` |
| span | 791（全部 `sync`） |
| span 时长中位数 | 约 8.05ms |
| `offcpu_ns` 中位数 | 约 8.04ms |
| `futex_ns` 中位数 | 约 8.05ms |
| 无效样本（>duration） | 0 |
| `diagnose` | off-CPU 99.7%，futex 99.9%，verdict `host_lock_contention`，confidence 1.00 |

`strace -f -T -e trace=futex` 显示每次 `FUTEX_WAIT_PRIVATE` 阻塞约 7–8ms，与 span/offcpu/futex
三者一致；这证明 off-CPU 与 futex 数据不是“碰巧非零”，而是真实等待。

## 4. `--blocking off`

```bash
./cann-ebpf-linux-arm64-m2 attach --pid auto --profile vllm-ascend --profiles-dir profiles \
  --duration 5s --blocking off --no-context --output runs/m2-off-5s-20261126T074007Z
```

结果：7,145 spans；`blocking mode=off offcpu=false futex=false attached=0 failed=0`；
`spans.jsonl` 中 `offcpu_ns`/`futex_ns` 字段数为 0；`/health` 200→200、service active→active、
uprobe 残留 0。说明关闭路径不加载 blocking 程序、不产生任何 blocking 字段。

## 5. `diagnose` 只读验证

- 对服务器上历史 M4 10 秒证据（6,991 spans）：`status=ok`，正常输出 PID/function/类别与
  “无法判定”说明；原始 `spans.jsonl` SHA-256 未变。
- 对 batch 证据（164 spans、1 个 batch、2 个 request ID）：`status=ok`，正确列出 batch/request；
  无 off-CPU 数据时给出 `blocking_unknown`。
- 对本次 V4 run 使用 `--pid 1452 --last 2s`：命中 67 spans，窗口来源 `last`。

## 6. 验证中发现并修复的问题

以下问题都是上机后才暴露的，代码已修复并重新验证：

1. **futex_ns 大于 span duration（如 2.887s vs 15ms）**
   - 原因：tid 复用时 `tid_state` 残留，`futex_since` 陈旧且跨调用累计。
   - 修复：每次 CANN entry 清零 `offcpu_since`/`futex_since`（线程能执行 entry 就说明在 CPU 上）；
     新增 `sched_process_exit` 删除 tid state；`diagnose` 排除 `>duration` 的不可能样本。

2. **`futex_wait` kprobe 只覆盖约 10% 的等待（90/862）**
   - 原因：内核在部分调用点内联/克隆了 `futex_wait`，符号 kprobe 覆盖不到。
   - 修复：改用 `sys_enter_futex` / `sys_exit_futex` tracepoint，按 `op` 过滤
     `FUTEX_WAIT` / `WAIT_BITSET` / `WAIT_REQUEUE_PI`；合成目标覆盖率达到 791/791。

3. **`futex_wait_requeue_pi` 符号不存在**
   - 原来的 kprobe attach 产生两条 warning；改用 syscall tracepoint 后不再依赖该符号。

4. **5.10 verifier 拒绝变长数组下标**
   - 现象：`load BPF collection: permission denied: R2 unbounded memory access`。
   - 修复：去掉 `tid_state` 的嵌套快照数组，改为“只测最外层调用”；嵌套 span 不写 blocking 字段。
     这样既避开 verifier，也避免父子 span 重复累计。

5. **嵌套深度泄漏导致累计值越滚越大**
   - 原因：exit 找不到 inflight 时提前返回，没有配对 entry 的深度。
   - 修复：每个 exit 都先执行 `blocking_exit`（即使 inflight 缺失或被采样过滤），深度归零时才
     返回测量值；超过 64 层复位。

6. **启动空窗内 span 缺 blocking 字段**
   - 原因：先 attach uprobe、后 configure blocking map。
   - 修复：blocking collection/tracepoint 与 `blocking_cfg` 在 uprobe attach 之前完成。

## 7. 仍未覆盖 / 边界

- 本次 V4 窗口没有真实 futex 竞争（futex≈0），`host_lock_contention` 由合成目标验证；
  生产流量上的锁竞争仍需等实际异常窗口。
- 只统计最外层 CANN span；嵌套 span 缺字段是设计行为，不是数据丢失。
- 没有设备侧时间线，不能输出 `device_bound` / `launch_starvation`。
- 没有单 request 延迟和 `execute_model` 函数级耗时（插件未激活，本轮不做）。
- `sched_switch` 为全系统 tracepoint，长跑开销基线尚未做；生产长跑建议先 `--blocking off`
  或只开短窗口。

## 8. 证据位置

```text
/root/cann-ebpf/m2-test-20261126T151851Z/
  cann-ebpf-linux-arm64-m2            # 二进制，SHA-256 见上
  profiles/                           # 校验过的 profile
  runs/m2-blocking-10s-20261126T073824Z/   # V4 10s，health/service/残留文件 + spans + diagnose
  runs/m2-synth-20261126T073752Z/          # 合成锁竞争
  runs/m2-off-5s-20261126T074007Z/         # --blocking off
  diagnose-m4-10s.md                       # 历史证据只读验证
  diagnose-context.json                    # batch 证据只读验证
```

旧目录 `/root/cann-ebpf/m4-20261126`、`runs/` 等未被覆盖。
