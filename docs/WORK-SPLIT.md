# 双任务分工与接口约定（2026-11-26）

本文件用于两个并行任务在同一 `main` 工作区协作，避免互相覆盖未提交改动。

## 0. 当前快照

- 我的最后提交：`5942b37 feat: add preflight checks and capture timing, measure M2 overhead`
- 另一个任务（未提交）：M3 生产端与 join 增强
  - `m3/mspti_collector/`：C++ in-process msPTI collector（`libcannmspti.so`、`cann-mspti-collector`、`synthetic_acl`）
  - `internal/device/pidmap.go`：容器 namespace-local pid/tid → host pid/tid 映射与产物
  - `internal/device/device.go`：`CollectorMeta`（`cann.mspti.collector.v1`）
  - `internal/device/join.go`：PIDMap/TIDMap、相邻区间容差、慢 Sync 匹配率
  - `internal/device/join_test.go`：新增用例
  - `internal/diagnose/`、`cmd/cann-ebpf/diagnose.go`：读取 `device.jsonl.meta.json`、`pidmap.json`，`--device-container-id`
  - `.gitignore`：忽略 collector 构建产物
  - `.tmp-mspti-inspect/`：容器 msPTI 头文件/样例的临时拷贝（**不要提交**）
- 当前状态：`go build ./...` 与 `go test ./internal/device ./internal/diagnose ./cmd/cann-ebpf` 通过；
  C++ collector 尚未在本机（无 CANN）编译，真实设备验证待维护窗口/测试容器。

## 1. 所有权（谁改哪些文件）

| 范围 | 所有者 | 文件 |
|---|---|---|
| M3 生产端 C++ collector、合成 ACL、msPTI 回调/时钟/元数据 | 另一个任务 | `m3/mspti_collector/**` |
| 容器 pid/tid 映射、device schema 扩展、join 算法增强、diagnose device 侧 | 另一个任务 | `internal/device/**`、`internal/diagnose/**`、`cmd/cann-ebpf/diagnose.go` |
| host attach 接线、probes、preflight、hardware、采集时序/teardown | 我 | `internal/probes/**`、`internal/preflight/**`、`internal/hardware/**`、`cmd/cann-ebpf/attach.go`、`cmd/cann-ebpf/main.go`、`cmd/cann-ebpf/preflight*.go` |
| 交接文档、TASKS、README、WORK-SPLIT、验证记录 | 我（不占 m3 README） | `docs/HANDOFF.md`、`docs/TASKS.md`、`docs/README*`、`docs/*.md`（`m3/**/README.md` 除外） |
| `m3/mspti_collector/README.md` | 另一个任务 | — |

规则：

1. 只 `git add` 自己名下的路径；**禁止 `git add -A`**，避免把对方未完成改动一起提交。
2. 不直接改对方文件；需要接口变化时先改本文件“接口约定”，或在提交说明里写明。
3. 提交前跑 `gofmt`、`go test ./...`、`go vet ./...`；C++ 改动需在容器内 `cmake && make`。
4. 任一方重建 `cann.bpf.o`/二进制时，同步更新 HANDOFF 的 SHA。

## 2. 接口约定（跨任务边界）

### 2.1 证据文件（run 目录）

```text
spans.jsonl                 host span（既有）
manifest.json               运行环境/统计/artifacts（既有）
hardware.jsonl               E 层 npu-smi（我）
device.jsonl                msPTI 设备时间线，schema cann.device.v1（另一任务生产）
device.jsonl.meta.json      collector 元数据，schema cann.mspti.collector.v1（另一任务）
pidmap.json                 容器 local→host pid/tid 快照，schema cann.pidmap.v1（另一任务生成，我负责接线落盘）
```

约束：

- `device.jsonl` 的 `kind` 仅 `API|KERNEL|MEMCPY|HCCL`；新增 kind 必须升 schema 并同步 `internal/device`。
- 所有时间戳最终是 ns；`raw_start_ns`/`raw_end_ns` 只作证据保留，不参与 join。
- `pidmap.json` 必须由 host 在目标进程存活时抓取（`device.CapturePIDMaps`），不能事后从已退出进程补。
- 原始文件不覆盖；重复导入同一 run 目录必须报错/告警，不静默替换。

### 2.2 Go 接口

- `device.Options` 增加 `PIDMap/TIDMap`；join 前只对 `KindAPI` 做翻译。
- `diagnose.Options.DeviceContainerID` 用于无 pidmap 快照时的在线解析；有快照时优先快照。
- `attach` 新增（我实现）：
  - `--device-jsonl <path>`：采集结束后把外部 collector 的 `device.jsonl`（及同名 `.meta.json`）
    复制进 run 目录并封存为 manifest artifact；
  - 自动对 `opts.PIDs` 抓 `pidmap.json`（host 存活时），失败只 warning；
  - 不覆盖已存在的 `device.jsonl`/`pidmap.json`。

### 2.3 版本与兼容

- `cann.device.v1` 字段只增不改；删除/改语义必须升 v2。
- `manifest.artifacts.kind` 约定：`device_jsonl`、`device_meta`、`pidmap`、`hardware_samples`。

## 3. 当前任务分配（2026-11-26 更新）

已完成：
- 另一个任务：M3 collector/合成验证（`5e2fc93`）、pidmap、join 增强、diagnose meta/pidmap；
- 我：`attach --device-jsonl` + pidmap 自动抓取/artifact、preflight 门禁（`08b327c`）。

下一阶段分工详见 `docs/NEXT-WORK.md`：

| 范围 | 另一个任务 | 我 |
|---|---|---|
| 无窗口 | A1 M3 join 质量 ✅、A2 collector 加固 ✅ | A3 teardown/规模优化、A4 固定负载脚本、A5 M6 交付骨架、A6 exporter、A7 DCMI、A8 M5 脚本 |
| 维护窗口 | B1 V4 真实设备时间线（主导）、B4 真实 HCCL | B1 host 采集/health/残留/导入、B3 开销门禁、B2 M5 A/B |
| 不做 | 插件/`execute_model`、逐 request 延迟 | — |

### 3.1 我的具体下一步

1. A3 ✅ 系统级 attach + BPF `target_pids` 过滤：74 links，V4 退出 ~4.5s、残留 0、覆盖不变；
2. A4 ✅ `scripts/overhead_fixed_load.py`（dry-run 通过；真实负载待窗口）；
3. A5 ✅ systemd/install/uninstall/upgrade/release + SHA256SUMS（dry-run 通过）；
4. A6 ✅ Prometheus `metrics`；A7 ✅ 硬件 2s 间隔评估；A8 ✅ M5 capture/compare 脚本；
5. 等 B1 后更新 `M3-VALIDATION` 与二进制 SHA。

### 3.2 另一个任务的具体下一步

1. A1 ✅：同名 API 匹配、可匹配分母、reason 明细；final7 可匹配 305/305，`api_name_mismatch=0`；
2. A2 ✅：buffer 生命周期、stop 幂等/并发、时钟重估、tid 热刷新、JSON 转义；自测 + 50ms flush 压力通过；
3. 准备 B1 注入脚本与回滚；B4 真实 HCCL correlation。

## 4. 对 M3 collector 的评审备注（不改对方文件）

以下是我读代码后建议对方在容器内验证时重点确认的点：

1. **回调 buffer 生命周期**：`msptiActivityGetNextRecord` 返回的 record 只在 buffer 有效期内可用，
   写入 JSONL 必须在 buffer 回调内完成或深拷贝；当前实现若跨线程排队需要确认拷贝。
2. **停止时序**：析构函数里 `msptiActivityFlushAll` + unsubscribe 与回调线程可能并发，
   需确认不会死锁；`cann_mspti_stop` 的多次调用要幂等。
3. **时钟偏移**：`CLOCK_REALTIME - CLOCK_MONOTONIC` 偏移在启动时取一次；容器 time namespace
   或 NTP 跳变会漂移，建议每条记录保留 raw 值并定期重估，报告里写校准误差。
4. **pid/tid 映射**：msPTI 的 threadId 是新线程创建时才有；采集后新建线程可能不在快照里，
   需要在 collector 内按需读 `/proc/self/task/*/status` 补映射或在报告中标注未映射。
5. **JSON 转义**：kernel/op name 来自驱动，可能含引号/反斜杠，必须转义；不能写业务 payload。
6. **LD_PRELOAD 顺序**：README 要求 `libmspti.so` 在 `libcannmspti.so` 前；需要在容器内实测
   构造顺序，并确认 collector 构造早于 ACL 初始化。
7. **合成验证**：`synthetic_acl` 依赖 `libopapi/nnopbase`，容器内版本是否齐全要先确认；
   先验证 API→KERNEL/MEMCPY，再验证 HCCL（需要多卡/集合通信时另建）。
8. **失败降级**：mspti 初始化失败不能阻止业务启动；collector 必须只打 warning、禁用自身。

## 5. 合并顺序

1. 另一方先提交 M3 collector/join/diagnose（含测试）；
2. 我 rebase/更新 `attach --device-jsonl` 与 preflight 门禁（依赖 `device.CapturePIDMaps`/`ReadPIDMap`）；
3. 重建 Linux/arm64 二进制，更新 HANDOFF SHA；
4. 在测试容器跑合成 `device.jsonl` → `diagnose` 端到端，再到 V4 维护窗口验证。
