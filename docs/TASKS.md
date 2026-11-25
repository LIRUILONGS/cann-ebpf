# cann-ebpf 开发任务

状态：M0 进行中

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

## M1 Orinfer host core

- [ ] H1/M 探针
- [ ] ringbuf / entry-exit 配对
- [ ] JSONL + manifest
- [ ] `top` / `why` 初版
- [ ] 30 秒试跑，不重启 Orinfer

## M2 off-CPU 与锁归因

- [ ] sched_switch / futex / 可选 pthread_mutex_lock
- [ ] offcpu_ns / futex_ns
- [ ] host_lock_contention / launch_starvation

## M3 msPTI join

- [ ] msPTI collector：API/KERNEL/MEMCPY/HCCL
- [ ] 时钟校准、`(device_id, correlationId)` join
- [ ] 对齐率报告、未匹配原因

## M4 vLLM-Ascend profile

- [ ] 真实 V4 服务 discovery：`vllm-ds4-8905`
- [ ] 8 worker + engine core + API server 覆盖
- [ ] vLLM 0.23.0rc1 batch/request 插件骨架
- [ ] 等 `healthy` 后再 attach，不在 loading/compile 阶段计时

## M5 V4/Orinfer A/B（需要人工窗口）

- [ ] 先采 V4 稳态
- [ ] 授权窗口停 V4、起 Orinfer
- [ ] 同 workload、同并发、同时长
- [ ] `compare.md`

## M6 生产化

- [ ] Prometheus/OTLP、Grafana
- [ ] systemd、权限收敛、自动 detach
- [ ] runbook、升级/回滚
