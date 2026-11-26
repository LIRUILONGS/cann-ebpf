# M3.0：msPTI / msprof 动态采集可行性

日期：2026-11-26
主机：`bms-84865758`（容器 `deepseek-v4-flash-8905`，CANN 9.0.1）
结论：**`msprof --pid` 动态 profiling 在本环境不可用（三种变体均段错误）**；V4 未受影响。
M3 生产端改为容器内 in-process collector，需要维护窗口；在此之前只保留硬件层与 join 代码。

## 1. 环境事实

| 项 | 值 |
|---|---|
| 宿主 | 只有驱动，没有 CANN toolkit；无 host `libmspti.so` |
| 容器 | CANN `9.0.1`，`tools/mspti/{include,lib64,python,samples}`，`libmspti.so` |
| msPTI headers | `mspti.h`、`mspti_activity.h`、`mspti_callback.h`、`mspti_cbid.h`、`mspti_result.h` |
| msPTI samples | `mspti_activity`、`mspti_correlation`、`mspti_external_correlation`、`mspti_hccl_activity`、`python_monitor` |
| msprof | `/usr/local/Ascend/ascend-toolkit/latest/bin/msprof`，help 中列出 `--pid`、`--duration`、`--sys-devices` |
| npu-smi | 25.2.1，host/容器可用（已用于 E 层） |
| torch_npu | 2.10.0.post2 |

## 2. 动态 attach 测试

命令形态：

```bash
docker exec [-u root] deepseek-v4-flash-8905 sh -c '
  source /usr/local/Ascend/ascend-toolkit/set_env.sh
  msprof --pid <pid> --duration 5 --sys-devices 0 --output /tmp/m3probe'
```

| 变体 | 结果 |
|---|---|
| A：默认用户 + set_env，pid 1452（Worker_TP0） | exit 139，Segmentation fault，无输出目录 |
| B：root + set_env，pid 4192024（EngineCore） | exit 139，Segmentation fault |
| C：root + set_env + `--sys-pid-profiling on`，pid 1452 | exit 139，Segmentation fault |

每次前后检查：

- `/health` 200 → 200；
- `vllm-ds4-8905.service` active → active；
- worker 进程仍为 `Sl`，未退出；
- 容器内无残留 `msprof`/`mspti` 进程；
- 没有生成任何 profiling 文件。

## 3. 结论与影响

- `msprof --pid` 在该 CANN 9.0.1 容器镜像中不工作（至少在当前 seccomp/capability/环境组合下）；
  不能作为 M3 的生产端。
- 现有 host span / blocking / 硬件层采集不受影响；动态 attach 失败是 fail-soft。
- 不能因此下“设备侧不可观测”的最终结论：msPTI 的 in-process 路径仍未测试，只是需要维护窗口。
- `msprof` 的应用模式（`msprof <app>`）与 `msopprof` 都是“启动时采集”，不适用于已在运行的 V4；
  除非在维护窗口重启或用单独实例。

## 4. 决策

1. **路线 A（动态 attach）拒绝**：不工作，不再重试；不在生产容器反复尝试可能导致 core dump 的工具。
2. **路线 B（in-process collector）进入准备阶段**：
   - 在容器内用 `libmspti.so` 编译 collector（C 或 Python），输出 `cann.device.v1`；
   - 通过 Unix socket 或 run 目录交给 host `diagnose`；
   - 需要重启 V4 或入口 wrapper，必须走维护窗口；
   - 与 vLLM 插件分开开关、分开描述。
3. **无窗口期间**：
   - 保留 `docs/HARDWARE.md` 的 E 层 run 级判断；
   - 保留 `docs/M3-DEVICE-JOIN.md` 的 join/诊断代码，用合成 `device.jsonl` 验证；
   - 报告里继续明确“没有设备时间线，不能下 device_bound 结论”。

## 5. 下一步（维护窗口清单）

1. 在测试容器（同镜像）编译 msPTI collector，先用合成 ACL 程序验证 API/KERNEL/MEMCPY/HCCL
   记录和 correlationId 真能关联；
2. 校准 host `bpf_ktime_get_ns` 与 msPTI 时间戳（按 device 记录偏移，目标 <100µs）；
3. 在授权窗口重启 V4（或 wrapper 注入），采集 10–30 秒；
4. 用 `diagnose` join：目标慢 Sync 匹配率 ≥95%，未匹配原因分类；
5. 验收后更新 `docs/M3-VALIDATION.md`；
6. 失败则回滚到纯 host + 硬件层，不保留常驻 profiling。

## 6. 证据位置

```text
/root/cann-ebpf/m2-test-20261126T151851Z/runs/m3probe-20261126T083414Z/
/root/cann-ebpf/m2-test-20261126T151851Z/runs/m3probe-variants-20261126T083431Z/
```

每个目录含 `msprof.log`、`msprof-exit.txt`、health/service/worker 前后快照。
