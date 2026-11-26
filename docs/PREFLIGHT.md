# `preflight` 环境检查

日期：2026-11-26
状态：代码 + 单测完成，已在 `bms-84865758` 验证（15 pass / 0 warn / 0 fail）。

## 1. 用途

attach 前只读检查内核/权限/追踪点/npu-smi 是否满足条件，失败直接返回退出码 4，
避免“带病 attach”后在现场猜原因。不 attach、不改 sysctl、不改服务。

## 2. 命令

```bash
./cann-ebpf-linux-arm64-m2 preflight
./cann-ebpf-linux-arm64-m2 preflight --json
```

输出 schema `cann.preflight.v1`，每项 `pass` / `warn` / `fail`。

## 3. 检查项

| 检查 | pass 条件 | 失败/告警含义 |
|---|---|---|
| privilege | root | 非 root 需要 CAP_BPF/CAP_PERFMON/CAP_SYS_ADMIN |
| btf | `/sys/kernel/btf/vmlinux` 存在 | 缺失告警；当前对象非 CO-RE，但契约要求 BTF |
| tracefs | `/sys/kernel/tracing` 或 debug 路径可读 | 缺失 fail |
| uprobe_events | 存在 | 缺失 fail，uprobe 不可用 |
| tracepoint:sched/sched_switch | id 可读 | 缺失告警，blocking 降级 |
| tracepoint:sched/sched_process_exit | id 可读 | 同上 |
| tracepoint:syscalls/sys_enter_futex | id 可读 | 缺失告警，futex 指标缺失 |
| tracepoint:syscalls/sys_exit_futex | id 可读 | 同上 |
| perf_event_paranoid | ≤2 | >2 告警（root 仍可能可用） |
| lockdown | `[none]` | integrity/confidentiality fail |
| memlock | unlimited 或 ≥1GB | 偏小告警 |
| nofile | ≥4096 | 偏小告警 |
| npu-smi | 找到可执行文件 | 缺失告警，硬件层禁用 |
| unprivileged_bpf_disabled | 可读 | 信息项 |
| cgroup | v1/v2 可识别 | 信息项 |

## 4. 退出码

| 码 | 含义 |
|---|---|
| 0 | 没有 fail（允许 warn） |
| 2 | 参数错误 |
| 4 | 存在 fail，需先修复 |

## 5. 上机结果（V4）

```text
preflight host=bms-84865758 kernel=5.10.0-136.108.0.188.31.oe2203sp1.bclinux.aarch64 arch=arm64 pass=15 warn=0 fail=0
PASS privilege / btf / tracefs / uprobe_events
PASS tracepoint:sched/sched_switch id=266
PASS tracepoint:sched/sched_process_exit id=263
PASS tracepoint:syscalls/sys_enter_futex id=331
PASS tracepoint:syscalls/sys_exit_futex id=330
PASS perf_event_paranoid=2, lockdown=[none], memlock=unlimited, nofile=65535
PASS npu-smi=/usr/local/sbin/npu-smi, cgroup v1
RC=0
```

JSON 输出已保存到 `/root/cann-ebpf/m2-test-20261126T151851Z/preflight.json`。

## 6. 边界

- 只检查“能不能 attach”，不验证 profile 是否匹配目标进程（用 `discover --dry-run`）。
- `nofile=65535` 在 10 目标 × 每线程 perf event 的场景要关注；preflight 只告警，不自动调限。
- 不检查容器内 msPTI；M3 生产端仍需要维护窗口。
