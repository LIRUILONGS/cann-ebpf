# cann-ebpf 方案缺口登记

日期：2026-11-25

> 状态更新（2026-11-27）：本文件保留历史方案缺口清单；当前交付状态以
> `docs/HANDOFF.md` 为准。`sample`、`report --metrics`、延迟热图、次秒偏移热图、
> 函数分位带、尾 cohort、指标矩阵和 vLLM 十视图已落地，并有单测与 Chrome 验收样本。

## 0. 结论

方案已经可以进入 M0，但不是“零缺口”。本轮把会影响方案正确性的文档缺口补齐了；剩下的主要是实现期产物和必须在服务器上实测的能力，不应在文档里假装已经完成。

判断标准：

- **P0**：不补就不能开始 M0，或补错会导致证据不可信。
- **P1**：M1–M3 必须实测/落地，否则结论可能被推翻。
- **P2**：M4–M6 生产化需要，不阻塞当前阶段。

## 1. 本轮已补齐

| 项 | 位置 | 说明 |
|---|---|---|
| 六层采集表 H1/H2/M/C/D/E | `PLAN.md` 7.1 | 用户要求的采集范围已落到符号 |
| handle 生命周期 | `PLAN.md` 7.1/7.2、`base.yaml` | 明确 handle 是关联键，不是独立观测层 |
| 采样 entry/exit 一致性 | `PLAN.md` 7.4 | 采样决定只在 entry 做，exit 读同一记录 |
| 嵌套调用/ACL-Runtime 双计 | `PLAN.md` 7.4 | 内层标 `nested=true`，不计 top-level |
| 卡死 span 主动上报 | `PLAN.md` 7.4 | `stuck_threshold_ms` 心跳，不等 exit |
| PID 复用/容器重启/库 inode 变化 | `PLAN.md` 7.4 | pid+starttime+boot_id；inode 变化重挂 |
| 共享 stream 跨线程归因 | `PLAN.md` 8.2/9 | 记录 `stream_owner_tids[]`，不把等待误判为锁竞争 |
| Orinfer 请求归因限制与升级路径 | `PLAN.md` 8.3 | 明确 Phase A 只能到 `thread`，列出四种升级方案 |
| time namespace/设备时钟 | `PLAN.md` 8.4 | 按 namespace 分别校准，不复用 host offset |
| 上下文 socket 安全 | `PLAN.md` 8.5 | 0600、`SO_PEERCRED`、schema 校验 |
| 队列深度/跨线程判定口径 | `PLAN.md` 9 | inflight submit 数，不是设备硬件队列 |
| manifest/journal/证据完整性 | `PLAN.md` 10.1 | 启动即写、5s journal、fsync、0700/0600、磁盘余量 |
| CLI 退出码契约 | `PLAN.md` 11.1 | 0/2/3/4/5/6/7 |
| 报告固定结构 | `PLAN.md` 11.2 | summary/overhead/join-report |
| `overhead_pct` 定义 | `PLAN.md` 15.1 | 首选框架吞吐，兜底 agent CPU，不混用 |
| 资源保护与 kill switch | `PLAN.md` 15.1、`base.yaml` | RSS/CPU/磁盘三级自保；SIGUSR1/2 |
| 预检清单 | `PLAN.md` 16.2 | lockdown、fd、memlock、cgroup、SELinux、BTF |
| E 层硬件配置 | `base.yaml` `hardware`、`PLAN.md` 7.1 | DCMI/npu-exporter/npu-smi 选择、10s 周期、指标清单 |
| msPTI 按 device join | `PLAN.md` 8.6 | `(device_id, correlationId)`，避免跨卡 correlationId 冲突 |
| dlopen/延迟加载库重扫 | `PLAN.md` 7.4 | 每 10s 重扫 maps，记录 `library_added` |
| 异常退出未配对 span | `PLAN.md` 7.4 | `unmatched_exit`，不伪造结束时间 |
| 插件上下文晚到 | `PLAN.md` 8.5 | 30s 回填窗口，超时 `context_late` |
| 报告模板文件 | `docs/templates/*.md` | summary/overhead/join-report/compare 四份模板已建 |
| 机器可读配置 schema | `schema/config.schema.json` | 4 份 YAML 已通过校验 |
| YAML `off` 布尔坑 | `base.yaml`、`PLAN.md` 6.5 | `mode: "off"` 必须加引号 |

## 2. P0：进入 M0 前仍缺

| # | 缺口 | 为什么重要 | 验收标准 |
|---|---|---|---|
| P0-1 | 构建容器/Dockerfile 与依赖 pin | 现在只有“构建工具”清单，没有可复现构建；宿主直接装 clang/libbpf 会污染环境 | 一条命令产出静态 Go 二进制 + `.bpf.o`；记录 clang/libbpf/Go 版本 |
| P0-2 | 假 CANN 库 fixture | 没有 NPU 的 CI 无法测 attach/entry/exit；M0 不能只靠真机 | 自建 `.so` 导出 CANN 符号，单测覆盖 uprobe 挂载/配对 |
| P0-3 | `discover` 输出 JSON schema | profile 选择、符号、offset、matched_rules 需要机器可校验；否则报告不可复核 | `discover --json` 通过 schema；包含 library inode、cgroup、namespace |
| P0-4 | 预检脚本 | 文档有清单，但没有可执行检查；M0 会在真机上反复踩权限/BTF/fd | `cann-ebpf preflight` 输出 pass/fail/warn，退出码对应 4 |
| P0-5 | 固定负载生成器 | 没有固定 prompt/并发/warmup，3% 开销数字不可复现 | 负载文件、种子、并发、max_tokens 固定；hash 写入 manifest |
| P0-6 | 报告生成器（已完成） | `report` 已能从 spans/device/hardware 只读派生单文件 HTML；本轮增加 metrics 叠加和不可用原因 | `cann-ebpf report` 对缺证据的字段写明确原因；metrics-only 生成降级报告且不补造 span |
| P0-7 | systemd 单元与安装脚本 | 文档只有片段；路径、权限、日志、重启策略未固化 | `deploy/cann-ebpf.service` + `install.sh` + `uninstall.sh` |
| P0-8 | 升级/回滚脚本 | attach 工具升级时若不 detach，旧 uprobe 可能残留 | `upgrade.sh` 先 detach、备份配置、失败回滚；有 dry-run |

## 3. P1：M1–M3 必须实测

| # | 缺口 | 风险 | 验证方法 |
|---|---|---|---|
| P1-1 | msPTI `correlationId` 是否真能关联 API→kernel/memcpy/HCCL | 方案的核心 join 假设；不成立就要改成纯时间窗，置信度下降 | 在一个已知 kernel 上采集，检查 API record 与 kernel record 的 correlationId |
| P1-2 | msPTI `shared`/`exclusive` 的实际行为 | `exclusive` 可能要求进程内初始化/重启；`shared` 可能不支持 | 在测试容器里分别试；记录是否需要重启 |
| P1-3 | Orinfer 入口 hook/反向代理 | 没有它，永远只能回答“哪个线程”，回答不了“哪个请求” | 代理注入 `X-Request-ID`，按时间窗 join，报告标 `batch_window` |
| P1-4 | perf_event per-TID attach 覆盖率 | 只挂主线程会漏 worker；`inherit` 不覆盖已存在线程 | 多线程/多进程假库测试，核对每个 tid 都有事件 |
| P1-5 | PID 复用与库 inode 变化 watchdog | 不处理会串证据或静默失效 | 杀进程重建、替换 `.so`，检查重挂和 manifest `library_changed` |
| P1-6 | DCMI 字段与权限 | E 层没有具体字段；`npu-smi` 解析易受版本/locale 影响 | 用 DCMI 读 HBM/AICore/温度/功耗；不可用则明确降级 npu-exporter |
| P1-7 | HCCL activity 是否可用 | CANN 9.2 不一定提供 HCCL activity；没有就只能看 host 入队 | 跑一次多卡集合通信，检查 msPTI HCCL record |
| P1-8 | Orinfer Prometheus 吞吐基线 | 没有基线就无法计算业务 overhead，watchdog 只能做资源保护 | 找到 Orinfer metrics endpoint；没有就用固定负载 + 客户端计数 |
| P1-9 | time namespace 校准 | 容器与宿主 `CLOCK_MONOTONIC` 可能不同；不校准 join 全错 | 在容器内外同时读时钟，比较 offset；记录到 manifest |
| P1-10 | `aclrtSynchronizeStreamWithTimeout` 的 timeout 单位 | 单位写错会导致超时判定错 | 以 `acl_rt.h` 为准，M0 生成签名表并在单测断言 |

## 4. P2：M4–M6 生产化

| # | 缺口 | 说明 |
|---|---|---|
| P2-1 | vLLM 插件与 vLLM 版本兼容矩阵 | 当前 V4 镜像为 `v0.23.0rc1-nonroot`；插件已按 `NPUModelRunner.execute_model` 适配，但仍需在真实激活窗口验证，并为后续版本做 shim |
| P2-2 | Grafana dashboard JSON | 当前只有面板清单，没有可导入 JSON |
| P2-3 | OTLP exporter schema | M6 才做；需定义 span/metric 映射 |
| P2-4 | 火焰图/stack folded | `report` 已有时间轴/散点/热力图/CDF 及延迟热图/分位带/cohort 等视图；但工具没有调用栈采样，仍缺按栈聚合的 folded stack，不能把现有视图称为火焰图 |
| P2-9 | 真实生产 metrics 基线 | `sample` 的采集链路与单测已完成；还需在目标 NPU 上验证 DCMI/npu-smi 的真实采样间隔、权限、覆盖率和长跑开销 |
| P2-10 | metrics 与 request 对齐 | 当前 metrics 是周期采样，能观察资源趋势，不能证明单 request 关键路径；需要入口请求 ID/遥测配合才能做严格关联 |
| P2-11 | vLLM 十视图严格证据 | 已接入真实 attach/metrics；严格关键路径仍缺 msPTI device 区间，Roofline 缺 per-op FLOPs/bytes，A/B 缺同 workload 双 run，服务拓扑缺 OTel trace；缺证据必须保持 unavailable |
| P2-12 | 干净容量扫描 | 真实六档扫描受到活动背景负载影响；只能保留曲线并标记 partial，不能计算 knee；需在 `running=0` 窗口重跑 |
| P2-13 | histogram 窗口语义 | 已跳过首帧、排除 `_created`、窗口内输出 P50/P95/P99；后续需要把 reset/layout 变更和 scrape gap 的 warning 暴露到报告 |
| P2-5 | 多卡/HCCL 拓扑视图 | 8 卡场景需要按 device 聚合，不只是单卡 |
| P2-6 | Prometheus 指标命名规范 | `cann_ebpf_*` 前缀、label 基数、单位 |
| P2-7 | 术语表/决策记录/备选方案对比 | 交接需要；也应说明为什么不用 bpftrace/msPTI-only |
| P2-8 | 许可证/供应链 | vendored libbpf、第三方许可证、SBOM |

## 5. 不能做的事

以下不是“缺口”，是边界，不能在实现期偷偷突破：

- 不修改原始事件数值、不脱敏、不补造证据。
- 不把 tid 级数据写成 request 级。
- 不为了拿到请求 ID 去读 prompt/响应内容。
- 不在未授权时停止/重启 Orinfer、切换 V4/vLLM、改宿主 CANN/驱动。
- 不因为“指标好看”而隐藏 ringbuf loss、符号缺失、join 失败。
