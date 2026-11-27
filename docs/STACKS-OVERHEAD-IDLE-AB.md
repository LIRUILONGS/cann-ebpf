# Stacks 空闲窗口 CPU 开销 A/B（指示性）

- 日期：2026-11-27（Asia/Shanghai，UTC+08:00）
- 目标机：`root@36.137.90.82` / `bms-84865758`
- 二进制：`/tmp/cann-ebpf-stacks-final4`
- 二进制 SHA256：`f933564c2ba17ace47288f552ca879db409f4c2b394706df9b0e7ca13ce9cf7e`
- 完成标记：`/tmp/stacks-final4.done` 存在
- 范围：10 个 vLLM PID，99 Hz / 10 秒窗口；不重启、不压测、不发推理请求、不改 sysctl、不注入 collector、不杀进程

## 结论先行

**采集流程 PASS；`<1%` CPU 开销不可承诺，性能门槛记为 NOT MET。**

- 6 个配对（P-B）的原始目标 CPU 差值全部为正：`+13.60` 到 `+21.62` target CPU 秒/窗口；按原始窗口 CPU 秒计算为 `+9.99%` 到 `+16.07%`。
- 方向上可观察到一致的 CPU 正差，但三次 B 的原始范围本身已达均值的约 `3.7%`，3 个独立 P 窗口不足以判定统计显著；只能作为指示性信号。
- 采样进程的启动/收尾使 P 窗口墙钟变为 `10.83` 到 `11.05` 秒，而基线为 `10.00` 秒；因此原始差值混入了额外墙钟时间。按实际墙钟归一化为 CPU 秒/秒后，P-B 差值为 `+1.33%` 到 `+5.09%`，六个配对均值 `+2.61%`；P 三次平均 `13.943` CPU 秒/秒，B 三次平均 `13.672` CPU 秒/秒，均值差 `+1.98%`。
- 采样器自身 CPU 时间为 `0.53` 到 `0.59` 秒/次。该口径与目标进程 CPU 增量不同，未合并成单一“总开销”。
- 归一化后的最低配对仍为 `+1.33%`，高于 1%；因此本实验不能支持“空闲窗口 `<1%`”承诺。
- 只有 3 个 P 和 3 个 B，且空闲 vLLM 后台仍有明显活动（loadavg 1m 约 `37.83` 到 `43.48`，10 个目标 PID 基线已消耗约 `13.7` CPU 秒/秒）。这些结果是指示性、非 SLA，不能外推到生产 QPS。

所有采样运行均成功：`samples=1398..1519`、`unique_stacks=207..221`、`filtered=0`、`lost=0`、符号覆盖 `100.00%`，health 采集前后均为 `200`。`stack_fail` 为 `4`、`12`、`9`，属于单次 `bpf_get_stackid` 失败计数，不是整轮采集失败。

## 环境与前置检查

| 项 | 实测值 |
|---|---|
| 内核 | `5.10.0-136.108.0.188.31.oe2203sp1.bclinux.aarch64` |
| `nproc` | `192` |
| `getconf CLK_TCK` | `100` |
| 全机 loadavg（采集前） | `37.83 41.43 42.44` |
| 全机 loadavg（采集后） | `43.48 42.23 42.63` |
| 内存（前，MiB） | total `2062685`，available `1900104`，swap `0` |
| 内存（后，MiB） | total `2062685`，available `1899919`，swap `0` |
| vLLM health（前/后） | `200 / 200` |
| 目标 PID | `3076197 3079391 3081988 3084049 3086317 3088391 3090600 3092708 3094955 3097056` |
| 目标规模 | `10` processes / `1357` threads（来自三次 meta） |
| 其他采样器 | 开始前 `NONE`；结束后 `NONE` |
| 输出目录 | `/tmp/stacks-overhead-p1`、`p2`、`p3` 开始时均不存在 |
| 采集窗口 | `2026-11-27T19:34:52+08:00` 到 `2026-11-27T19:36:05+08:00`，约 73 秒 |

前置命令包括：

```bash
test -f /tmp/stacks-final4.done
sha256sum /tmp/cann-ebpf-stacks-final4
getconf CLK_TCK
uname -r
nproc
cat /proc/loadavg
free -m
curl -sS -o /dev/null -w '%{http_code}' http://127.0.0.1:8905/health
pgrep -af '[c]ann-ebpf-stacks|[s]tacks --pid'
```

`pgrep` 使用不自匹配的表达式，避免当前探针 shell 因命令文本包含搜索模式而误报。10 个 PID 的 `/proc/PID/stat` starttime 签名在采集前后完全一致，未发生 PID 复用或进程重启。

## 方法

1. 每个窗口开始时读取 10 个 PID 的 `/proc/PID/stat` 第 14、15 字段（`utime`、`stime`，单位 clock tick）并求和，窗口结束时再次求和。
2. `target_cpu_s = (ticks_end - ticks_start) / CLK_TCK`；未使用 `ps` 的瞬时 `%CPU`。
3. 窗口顺序固定为 `B1 -> P1 -> B2 -> P2 -> B3 -> P3`；每个窗口后 `sleep 2`。B 窗口为 `sleep 10`，P 窗口运行最终二进制。
4. P 窗口命令（P2/P3 只替换 `--out`）：

```bash
/usr/bin/time -p -o /tmp/stacks-overhead-p1/time.txt \
  /tmp/cann-ebpf-stacks-final4 stacks \
  --pid 3076197 --pid 3079391 --pid 3081988 --pid 3084049 --pid 3086317 \
  --pid 3088391 --pid 3090600 --pid 3092708 --pid 3094955 --pid 3097056 \
  --duration 10s --freq 99 --kernel-filter all \
  --out /tmp/stacks-overhead-p1
```

5. 用 GNU `/usr/bin/time -p` 记录采样器自身 `real/user/sys`；工具存在并实际使用，非 shell 内置 `time`。
6. 每个 P 目录新建，未复用；执行前再次检查目录不存在。
7. 执行脚本和完整 stdout/stderr：`.analysis/stacks-probe/overhead/run_idle_ab.sh`、`.analysis/stacks-probe/overhead/ab-run.log`。

核心只读函数如下（完整脚本见上述路径）：

```bash
read_ticks_sum() {
  local pid statline rest total=0
  for pid in "${PIDS[@]}"; do
    IFS= read -r statline < "/proc/$pid/stat" || return 1
    rest=${statline##*) }
    set -- $rest
    total=$((total + ${12} + ${13}))
  done
  printf '%s' "$total"
}
```

`comm` 可能含空格或括号，因此先截掉最后一个 `) ` 之前的内容；截断后的第 12、13 个字段对应原 stat 的第 14、15 字段。

## 结果

### 窗口数据

`loadavg 1m` 为窗口起→止；5m/15m 原值保存在 `ab-results.tsv`。目标 CPU 是 10 个 PID 的合计，不是整机占比。

| 窗口 | 类型 | loadavg 1m 起→止 | 墙钟 s | 目标 CPU s | 目标 CPU s/s | samples | unique | stack_fail | 工具自身 CPU s |
|---|---|---:|---:|---:|---:|---:|---:|---:|---:|
| B1 | 基线 | 37.83→39.03 | 10.002754 | 139.540 | 13.950158 | — | — | — | — |
| P1 | 采样 | 39.59→38.34 | 11.048243 | 156.170 | 14.135279 | 1519 | 207 | 4 | 0.59 |
| B2 | 基线 | 38.34→41.27 | 10.002973 | 134.550 | 13.451001 | — | — | — | — |
| P2 | 采样 | 41.57→41.27 | 10.825965 | 149.800 | 13.837104 | 1478 | 217 | 12 | 0.53 |
| B3 | 基线 | 41.27→40.00 | 10.002866 | 136.200 | 13.616098 | — | — | — | — |
| P3 | 采样 | 41.12→43.48 | 10.898116 | 151.010 | 13.856523 | 1398 | 221 | 9 | 0.57 |

### Meta 与采样完整性

| 窗口 | meta duration s | 工具 real s | 工具 user/sys s | samples | unique | stack_fail | filtered | lost | 符号覆盖 |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| P1 | 10.022309273 | 11.04 | 0.35 / 0.24 | 1519 | 207 | 4 (0.26%) | 0 | 0 | 100.00% |
| P2 | 10.023795021 | 10.82 | 0.31 / 0.22 | 1478 | 217 | 12 (0.81%) | 0 | 0 | 100.00% |
| P3 | 10.018946624 | 10.89 | 0.36 / 0.21 | 1398 | 221 | 9 (0.64%) | 0 | 0 | 100.00% |

三次 P 均满足：命令返回码 `0`、meta schema `cann.stacks.v1`、target_pids 与指定 10 个 PID 一致、`samples > 0`、`unique_stacks > 0`、`lost = 0`、meta duration 约 10 秒。没有整轮采集失败。

三次 meta 的 warning 类别一致为：

- `sampling 1357 threads across 10 processes`
- `perf event excludes user-mode time; samples are kernel on-CPU only`
- `bpf_get_stackid errors: -17=4 / -17=12 / -17=9`

其中 `stack_fail` 已和最后一次 warning 对齐；`filtered=0`、`lost=0`，不能把它解释为整轮采样失败。

## 配对差值与均值

原始差值严格按窗口目标 CPU 秒计算；归一化差值按 `CPU 秒/墙钟秒` 计算，减少 P 窗口启动/收尾造成的时长偏差。

| 配对 | ΔCPU s（P-B） | 原始相对差 | ΔCPU s/s | 归一化相对差 | 10 秒等效超额 s |
|---|---:|---:|---:|---:|---:|
| P1-B1 | +16.630 | +11.918% | +0.185121 | +1.327% | +1.851 |
| P1-B2 | +21.620 | +16.068% | +0.684278 | +5.087% | +6.843 |
| P2-B2 | +15.250 | +11.334% | +0.386103 | +2.870% | +3.861 |
| P2-B3 | +13.600 | +9.985% | +0.221006 | +1.623% | +2.210 |
| P3-B2 | +16.460 | +12.233% | +0.405522 | +3.015% | +4.055 |
| P3-B3 | +14.810 | +10.874% | +0.240426 | +1.766% | +2.404 |

- 原始配对：均值 `+16.395` CPU 秒，范围 `+13.600` 到 `+21.620` CPU 秒；均值相对差 `+12.069%`，范围 `+9.985%` 到 `+16.068%`。
- 归一化配对：均值 `+0.353743` CPU 秒/秒，范围 `+0.185121` 到 `+0.684278` CPU 秒/秒；均值相对差 `+2.615%`，范围 `+1.327%` 到 `+5.087%`。
- 三次 B：目标 CPU 均值 `136.763333` 秒，范围 `134.550000` 到 `139.540000` 秒；速率均值 `13.672419` CPU 秒/秒，范围 `13.451001` 到 `13.950158`。
- 三次 P：目标 CPU 均值 `152.326667` 秒，范围 `149.800000` 到 `156.170000` 秒；速率均值 `13.942969` CPU 秒/秒，范围 `13.837104` 到 `14.135279`。
- 归一化后全部 6 个配对仍为正，但最小 `+1.327%` 已超过 1%；因此不能把“`<1%`”写成已验证事实。

`10 秒等效超额 s` 只是用归一化速率差乘以 10 秒做的换算，不是额外实测窗口。

## Meta SHA256（服务器 / 本地）

本地文件位于 `.analysis/stacks-probe/overhead/`，与服务器文件逐字节一致。

| 文件 | 服务器 SHA256 | 本地 SHA256 | 一致 |
|---|---|---|---|
| `p1.meta.json` | `64151b52afe6e4b75e08e23dcda8f52b173ac140c883d6f0f91d3bee732803fe` | `64151b52afe6e4b75e08e23dcda8f52b173ac140c883d6f0f91d3bee732803fe` | 是 |
| `p2.meta.json` | `c84de10b878b6281085c3542b5cb695ea8e4e755dfbe5aab3d9686e8d94a87b0` | `c84de10b878b6281085c3542b5cb695ea8e4e755dfbe5aab3d9686e8d94a87b0` | 是 |
| `p3.meta.json` | `be8f91681e6b975be825e81c48d05047c1f79351389e4f8f4ea39ca0e9a35cac` | `be8f91681e6b975be825e81c48d05047c1f79351389e4f8f4ea39ca0e9a35cac` | 是 |

另保留了 `p1/p2/p3.time.txt`、`p*.stderr.txt`、`p*.stdout.txt` 作为原始工具输出；采样目录本身未覆盖，`final4` 与其他证据未改动。

## 限制与解释

- 这是空闲窗口 ABAB，不是随机化重复实验；只有 3 个独立 P 窗口和 3 个独立 B 窗口，6 个配对存在复用，不能给出置信区间或统计显著性。
- “空闲”仅表示没有主动压测/推理请求；目标进程和机器仍有高后台活动。B 窗口每秒已消耗约 `13.5` 到 `14.0` 个 CPU 秒。
- P 窗口墙钟比 B 长 `0.80` 到 `1.05` 秒（进程启动、BPF/perf 初始化、输出与收尾），所以原始差值是“窗口成本”而非纯“采样时间成本”。归一化差值是对时长差异的算术校正，不是另一次受控实验。
- `CLK_TCK=100`，目标 CPU 累计值的最小量化步进为 `0.01` 秒；短窗口对调度抖动很敏感。
- 未测吞吐、延迟、GPU/NPU 利用率或尾延迟；不能外推到固定 QPS、生产高峰或其他机型。
- 工具自身 CPU 与目标进程 CPU 增量口径不同，未相加为总开销。
- 结论为**指示性、非 SLA**；本报告不宣称生产负载下 `<1%`。

## 异常与处置

- 本地下载 meta 时遇到一次 `kex_exchange_identification: Connection closed`，发生在 `p2.time.txt` 之后；下载阶段约 13 秒后重连成功（短于建议的 20–45 秒等待，仅发生在证据下载阶段），3 个 meta 及全部辅助文件均完整，meta SHA256 与服务器一致。
- 本地执行通道曾报告外层 `exit 1`，但同一日志明确写出远端 `REMOTE_RC=0`、`AB_PASS=YES`，且 health、PID、meta 与 SHA 均完整；未为追平外层返回码而重跑或补造数据。
- 没有其他采样器在运行，结束前未发现残留 sampler；未重启/停止 vLLM，未发送推理请求。

## 后续建议

1. 在固定 QPS（例如 0.5x、1x 两档）下做随机化 `ABBA`，每档至少 10 组，窗口延长到 60 秒，同时采集吞吐和尾延迟。
2. 用采样开始/结束标记和同一时钟窗口对齐目标 CPU 读取；或将采样器启动与 BPF/perf 初始化放在计时窗口之外，减少 setup/teardown 混淆。
3. 用 `perf stat -p <10 PIDs>` 对照 `cycles/instructions/task-clock`，并用 `/proc/PID/schedstat` 或 cgroup CPU accounting 复核 tick 口径。
4. 增加“空运行/伪 collector”对照和系统背景负载记录；在生产结论前给出置信区间，不以 3 对短窗口代表 SLA。

## 原始证据

- 报告：`docs/STACKS-OVERHEAD-IDLE-AB.md`
- 原始会话输出：`.analysis/stacks-probe/overhead/ab-run.log`
- 原始窗口 TSV：`.analysis/stacks-probe/overhead/ab-results.tsv`
- 执行脚本：`.analysis/stacks-probe/overhead/run_idle_ab.sh`
- meta：`.analysis/stacks-probe/overhead/p1.meta.json`、`p2.meta.json`、`p3.meta.json`
- 工具 time/stderr/stdout：`.analysis/stacks-probe/overhead/p1.time.txt`、`p1.stderr.txt`、`p1.stdout.txt`（p2/p3 同名）
- 服务器采样目录：`/tmp/stacks-overhead-p1`、`/tmp/stacks-overhead-p2`、`/tmp/stacks-overhead-p3`
