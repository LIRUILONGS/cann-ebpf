# A3：uprobe attach 规模与 detach 优化

日期：2026-11-26
状态：代码完成并在 V4 验证。

## 1. 问题

旧实现按 `(pid, function)` 为 10 个目标各挂 37 个函数、每个函数 entry+uretprobe：
**740 个 perf event**。在 5.10 上关闭一个 uprobe 约 **65ms**，且内核基本串行，导致：

- 采集 10 秒，命令却要 ~110 秒才返回；
- 进程退出阶段关闭剩余 fd 约 40–50 秒；
- `closeLinks` 有界等待 1 秒只是把等待移到进程退出，没有根本解决。

实测（同一主机，`--blocking off --hardware off`）：

| links | 退出耗时（capture 之后） |
|---:|---:|
| 20（1 函数 ×10 pid） | ~1.5s |
| 60（3 函数 ×10 pid） | ~4.6s |
| 740（37 函数 ×10 pid） | ~48s |

## 2. 方案：系统级 attach + BPF PID 过滤

- 对目标进程的库按 realpath 去重，每个 `(library, function)` **只挂一对** entry/uretprobe，
  `link.UprobeOptions.PID` 留 0（system-wide）；
- 新增 BPF map `target_pids`（hash u32→u8），attach 前写入目标 tgid；
  `on_entry`/`on_exit` 先查该 map，非目标进程直接返回；
- 目标数量不再影响链接数：V4 上 37 个函数 → **37 对 = 74 个 perf event**；
- `Attachment` 增加 `Scope="system"`，`PID=0` 表示系统级；blocking tracepoint 同样标 system。

为什么可行：实测 system-wide uprobe 在目标进程被 `taskset` 绑到 CPU 100 时仍能命中
（296/296 spans），不是只命中 CPU0；因此不需要 per-CPU attach。

## 3. V4 验证

### 3.1 覆盖对比（同一时段，10 秒）

| 模式 | spans | category | wall | 退出估算 |
|---|---:|---|---:|---:|
| process（旧） | 1,470 | handle 915 / sync 552 / event 2 / submit 1 | 63.3s | ~50s |
| system（新） | 1,618 | handle 1,261 / sync 357 | 16.7s | ~4.5s |

两个模式都覆盖 8 个 Worker（1452/3962/6568/8890/11340/13505/15629/17758）；
差异来自两次窗口的业务流量，不是漏采。

### 3.2 blocking + hardware 最终验证

```text
attachments 41（37 uprobe + 4 blocking），scope 全 system，errors 0
spans 1783；with_offcpu/with_futex 1529
blocking mode=on offcpu=true futex=true attached=4 failed=0
hardware samples=1 devices=8（无 "signal: killed" 告警）
wall 18.5s；uprobe/kprobe 残留 0；/health 200→200；service active→active
```

10 秒采集总墙钟从 ~110s 降到 ~18s；采集窗口后退出清理约 4.5s，满足 <10s。

## 4. 边界

- `target_pids` 是 attach 时的快照；采集期间**新建**的进程/Worker 不会被采到（旧实现同样只覆盖 attach 时列举的 pid）。
- 系统级 uprobe 需要 root（本来就需要）。
- 不同容器若映射不同 inode 的同名库，会按 realpath 分别 attach；链接数仍远小于 per-pid。
- 依赖 `target_pids` 过滤；如果 map 配置失败，attach 会直接报错而不是静默全系统采集。
- 不是所有内核都允许 system-wide uprobe；失败时 attach 会报错，需回退到 per-pid 版本（可后续加开关）。
