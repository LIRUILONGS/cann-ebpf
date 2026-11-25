# cann-ebpf 开发任务

状态：M0 完成；M1 eBPF 已在 V4 实测（单 PID 2s、8 worker 2s/30s）；M3 msPTI 与 M4 未完成；Orinfer 待运行窗口

## M0 骨架与发现

- [x] Go module / 工作区
- [x] 配置加载、`extends` 合并、JSON Schema 校验
- [x] `/proc` 进程发现：cmdline、maps、cgroup、root、namespace、listen port
- [x] profile 匹配：`any/all/exclude`、priority、`first_match/all_matching`
- [x] ELF dynsym 解析：vaddr→file offset、缺符号 fail-soft
- [x] `discover --dry-run`：JSON/human 输出、候选库、候选符号
- [x] 单元测试：配置合并、match 语义、stat/maps 解析、ELF offset、容器 root 回退
- [x] `linux/arm64` 交叉编译
- [x] 服务器只读验证：V4 vLLM（不 attach）
- [ ] 服务器只读验证：Orinfer（当前未运行，等待授权窗口）

## M1 host core（V4 采集已验证）

- [x] H1 `sync` 与 event/submit/HCCL 核心探针（memory 探针未实现）
- [x] ringbuf / entry-exit 配对
- [x] JSONL + manifest
- [ ] `top` / `why` 初版
- [x] 30 秒试跑（V4，不重启服务）
- [x] V4 eBPF：单 PID 2s 70 spans；8 worker 2s 487 spans；最终件 8 worker 30s 2088 spans（回归批次 2665 spans）；health 前后 200；uprobe 无残留

## M2 off-CPU 与锁归因

- [ ] sched_switch / futex / 可选 pthread_mutex_lock
- [ ] offcpu_ns / futex_ns
- [ ] host_lock_contention / launch_starvation

## M3 msPTI join

状态：未实现；M1 不初始化 msPTI。

- [ ] msPTI collector：API/KERNEL/MEMCPY/HCCL
- [ ] 时钟校准、`(device_id, correlationId)` join
- [ ] 对齐率报告、未匹配原因

## M4 vLLM-Ascend profile

状态：未完成。M1 的 8 worker host 采集不等同于 M4 完成；batch/request 插件、请求级归因和设备侧关联仍未实现。

- [ ] 真实 V4 服务 discovery：`vllm-ds4-8905`
- [ ] 8 worker + engine core + API server 覆盖
- [ ] vLLM 0.23.0rc1 batch/request 插件骨架
- [ ] 等 `healthy` 后再 attach，不在 loading/compile 阶段计时

## M5 V4/Orinfer A/B（需要人工窗口）

状态：等待 Orinfer 运行窗口；当前只有 V4 基线。

- [ ] 先采 V4 稳态
- [ ] 授权窗口停 V4、起 Orinfer
- [ ] 同 workload、同并发、同时长
- [ ] `compare.md`

## M6 生产化

- [ ] Prometheus/OTLP、Grafana
- [ ] systemd、权限收敛、自动 detach
- [ ] runbook、升级/回滚
