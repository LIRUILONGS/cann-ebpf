# 内核栈火焰图 10 秒 Quickstart（昇腾 arm64 / vLLM）

> 面向已经拿到 arm64 二进制、要在昇腾机器上对运行中的 vLLM 做一次 10 秒只读采样的运维。
> `<...>` 是占位符；先替换再执行。最终交付二进制是 `final5`，不是 `final4`。
## 0. 先看清楚边界
- 只采 **host kernel on-CPU**：回答采样时 CPU 正在执行哪条内核调用链。
- **不覆盖** Python/vLLM 用户态栈，**不覆盖** AI Core/算子执行时间；它不是算子火焰图。
- 不是 `perf record`：不产生 `perf.data`，不读取业务文本，不修改目标进程。
- `99 Hz` 是每个已打开线程在 on-CPU 时的采样机会，不是“每 CPU 99 Hz”，也不是整机固定 99 样本/秒；总样本取决于同时 on-CPU 的线程数。
- `--kernel-filter npu` 保留“整条栈中至少含一个 NPU 分类帧”的栈，不会只截取 NPU 帧。

两个 NPU 指标不能混写：

| 指标 | 定义 / 分母 | 参考值（只对应指定 run） |
|---|---|---|
| NPU 帧占比（帧加权） | NPU 类别帧样本次数 / `total_frames` | final4-classified：**4.61%**（630/13658）；final2 旧报告：**0.9917%**（13209） |
| Top NPU 样本% | 含该函数的采样栈数 / `total_samples` | final2：`drv_ascend_intf_ioctl` 115/1420=**8.10%**，self=0；final4-classified：`drv_ascend_intf_ioctl` 101/1488=6.79%、`trs_ioctl` 80/1488=5.38%、`devmm_svm_ioctl` 18/1488=1.21% |

- final4 的 0.86% 是旧硬编码分类结果（117/13658），**已废弃**；同一份 final4 数据用新共享分类是 4.61%（630/13658）。
- `0.9917%` 是 final2 正式 10 秒采样的旧报告口径；`4.61%` 是 final4 验证 run 的新分类口径，不能把两者当成同一个 run。
- 绝不能把 Top NPU 的 8.10% 写成“NPU 占 8.1%”：它的分母是采样栈数，不是帧数。
## 1. 准备二进制和环境
```bash
REPO=/path/to/cann-ebpf-repo
BIN=/path/to/cann-ebpf-linux-arm64-stacks-final5
EXPECT_SHA=156df5a79d25a7d4e9733e382c0258b9c944579e275f061e6aef2f776e383cac
cd "$REPO"

test -x "$BIN" || { echo "BIN 不可执行：$BIN"; exit 1; }
ACTUAL_SHA=$(sha256sum "$BIN" | awk '{print $1}')
test "$ACTUAL_SHA" = "$EXPECT_SHA" || { echo "BIN SHA256 不匹配：$ACTUAL_SHA"; exit 1; }
"$BIN" stacks --help >/dev/null 2>&1 || { echo "这个二进制没有 stacks 子命令"; exit 1; }
```
`stacks --help` 应看到 `--pid`（可重复或 `auto`）、`--profile`、`--duration`、`--freq`、`--kernel-filter`、`--raw`、`--symbols`、`--out`。`final4` 二进制已废弃，不能用于生成 4.61% 的最终报告。
| 先决条件 | 现场要求 |
|---|---|
| 权限 | root，或 `CAP_PERFMON` + `CAP_BPF`（容器还需按 runtime 授予；必要时 `CAP_SYS_ADMIN`） |
| perf | `perf_event_paranoid=2` 时 root/CAP_PERFMON 通常可打开；seccomp/LSM/lockdown 仍可能 EPERM |
| 符号 | `kptr_restrict=0` 或 `CAP_SYSLOG`；`/proc/kallsyms` 前 200 行有非零地址；`/proc/modules` 可读 |
| BPF | `/sys/fs/bpf` 以 `bpf` 类型挂载；内核可见 bpf syscall |
| 平台 | Linux arm64、内核 5.10（已在昇腾 910 验证）；BTF 缺失通常是警告，不是本实现的硬阻断 |
| 容量 | `target_pids=1024`、`stack_traces=16384`、`stack_counts=131072`、`stack_errors=64`、perf events=4096 |
| 磁盘 | 当前目录至少 100 MiB（10 秒口径；长窗口更多） |
## 2. 找到 10 个宿主 PID
先在**宿主机**执行，不要在容器 PID namespace 里猜 PID：
```bash
ps -eo pid,ppid,comm,args --sort=pid | grep -E 'vllm|VLLM::Worker_TP|VLLM::EngineCor' | grep -v grep
```
`comm` 最长 15 字符，现场通常显示 `VLLM::EngineCor`（不是 `EngineCore`）。典型目标是 1 个 frontend + 1 个 `VLLM::EngineCor` + 8 个 `VLLM::Worker_TP`，共 10 个宿主 Tgid；以现场实际为准，不要盲取第一个 PID。
把下面 `<...>` 替换成 10 个宿主 Tgid（必须显式 10 个，不允许 `auto` 代替验收）：
```bash
PIDS=(
  '<HOST_TGID_1>' '<HOST_TGID_2>' '<HOST_TGID_3>' '<HOST_TGID_4>' '<HOST_TGID_5>'
  '<HOST_TGID_6>' '<HOST_TGID_7>' '<HOST_TGID_8>' '<HOST_TGID_9>' '<HOST_TGID_10>'
)
for P in "${PIDS[@]}"; do
  test -d "/proc/$P" || { echo "PID 不存在：$P"; exit 1; }
  awk '/^(Name|Tgid|NSpid|State|Threads):/' "/proc/$P/status"
done
PID_ARGS=()
for P in "${PIDS[@]}"; do PID_ARGS+=(--pid "$P"); done
```
要求：`Tgid` 等于传入的 PID（否则你传的是线程），`State` 不是 `Z`，`NSpid` 能核对宿主映射。容器内看到的 PID 不能直接传给 `--pid`。
## 3. 建立不可覆盖的证据目录
```bash
STAMP=$(date -u +%Y%m%dT%H%M%SZ)
EVID="runs/stacks-accept-$STAMP"
RUN="$EVID/run"
test ! -e "$EVID" || { echo "证据目录已存在：$EVID"; exit 1; }
mkdir -p "$EVID"
echo "EVID=$EVID"
```
不要复用旧目录，也不要覆盖旧 `stacks.folded`、`stacks.meta.json` 或 `stacks.jsonl`。
## 4. 运行预检（不 attach）
```bash
sh scripts/stacks_preflight.sh "${PID_ARGS[@]}" > "$EVID/preflight.txt" 2>&1
PREFLIGHT_EXIT=$?
echo "preflight_exit=$PREFLIGHT_EXIT"
if [ "$PREFLIGHT_EXIT" -ne 0 ]; then
  cat "$EVID/preflight.txt"
  echo "预检未通过：1=有阻断项，2=参数错误；不要继续采样"
  exit 1
fi
```
预检只读；退出 0 只代表静态检查通过，实际 perf/BPF attach 仍可能因 seccomp/LSM/容器权限返回 EPERM。`/sys/fs/bpf` 未挂载会被预检按阻断处理，这是验收策略；不要为了通过而改生产 sysctl。
## 5. 显式 10 PID 正式采集（10 秒）
```bash
"$BIN" stacks \
  --pid "${PIDS[0]}" --pid "${PIDS[1]}" --pid "${PIDS[2]}" --pid "${PIDS[3]}" --pid "${PIDS[4]}" \
  --pid "${PIDS[5]}" --pid "${PIDS[6]}" --pid "${PIDS[7]}" --pid "${PIDS[8]}" --pid "${PIDS[9]}" \
  --duration 10s --freq 99 --kernel-filter all \
  --symbols profiles/kernel-symbols.yaml --out "$RUN" \
  > "$EVID/stacks.stdout.log" 2> "$EVID/stacks.stderr.log"
STACKS_EXIT=$?
echo "stacks_exit=$STACKS_EXIT"
cat "$EVID/stacks.stdout.log"
tail -n 30 "$EVID/stacks.stderr.log" || true
```
正式验收不要加 `--raw`（需要原始地址时另起新 `RUN`）。正常摘要类似：
```text
stacks out=... samples=1420 unique=218 stack_fail=9 coverage=100.00% folded=... meta=...
```
注意：`stacks` 对 `samples=0` 仍可能写出 run 并返回 0；退出码不是验收结论，必须做第 7 节检查。
## 6. 生成离线 HTML 报告
```bash
"$BIN" report --stacks "$RUN" --out "$EVID/report.html" \
  > "$EVID/report.stdout.log" 2> "$EVID/report.stderr.log"
REPORT_EXIT=$?
echo "report_exit=$REPORT_EXIT"
cat "$EVID/report.stdout.log"
tail -n 30 "$EVID/report.stderr.log" || true
```
`report` **没有** `--symbols`；`--symbols` 只属于 `stacks`。报告分类使用共享 `stacks.ClassifyName`/`DefaultSymbolRules`，stacks-only 空面板已隐藏。
```bash
ls -lh "$EVID/report.html"
xdg-open "$EVID/report.html" 2>/dev/null || echo "无图形桌面时，把 $EVID/report.html 复制到工作站浏览器打开"
```
打开后重点看 `#flamegraphPanel`、顶部 KPI、采集告警和 `Top NPU 帧` 表。`flamegraph=available` 只表示有非空树可绘制，**不等于** coverage/unknown/stack_fail 达标；低覆盖仍可能显示图，验收要以第 7 节的 meta 检查为准。
## 7. 判断成功或失败（必须看 meta 和 folded）
```bash
python3 - "$RUN" <<'PY'
import json, pathlib, sys
run = pathlib.Path(sys.argv[1])
meta = json.loads((run / "stacks.meta.json").read_text())
lines = [x for x in (run / "stacks.folded").read_text().splitlines() if x.strip()]
folded = sum(int(x.rsplit(" ", 1)[1]) for x in lines)
samples = int(meta.get("samples", 0))
unique = int(meta.get("unique_stacks", 0))
fail = int(meta.get("stack_fail", 0))
lost = int(meta.get("lost", 0))
coverage = float(meta.get("symbol_coverage", 0))
unknown = float(meta.get("unknown_pct", 100))
checks = [
    ("schema=cann.stacks.v1", meta.get("schema") == "cann.stacks.v1"),
    ("collector=perf_event_open+bpf_get_stackid", meta.get("collector") == "perf_event_open+bpf_get_stackid"),
    ("samples>0", samples > 0),
    ("samples>=1000（10 PID/10s 参考门槛）", samples >= 1000),
    ("unique_stacks>0", unique > 0),
    ("sum(folded)==samples", folded == samples),
    ("stack_fail<1%", samples > 0 and fail * 100 < samples),
    ("symbol_coverage>=0.80", coverage >= 0.80),
    ("unknown_pct<20", unknown < 20),
    ("lost==0", lost == 0),
]
print("samples=%d unique=%d folded_sum=%d" % (samples, unique, folded))
print("warnings=%s" % " | ".join(meta.get("warnings", [])))
for name, ok in checks:
    print(("PASS " if ok else "FAIL ") + name)
raise SystemExit(0 if all(ok for _, ok in checks) else 1)
PY
```
- **通过**：上列全部 PASS，且报告 stdout 有 `flamegraph=available`；Top NPU 表非空，或现场确认本窗口 unobserved 并记录。
- **失败**：任一项 FAIL；`samples=0`、`symbol_coverage < 0.80`、`unknown_pct >= 20`、`stack_fail/samples >= 1%`、`lost != 0` 都应记为 unavailable 并复采。
- `samples >= 1000` 是 10 PID/10 秒参考门槛；线程数/窗口不同按验收文档换算，不能静默改阈值。
- `lost=0` 是当前计数器读数，不是“绝对没有丢样”的证明；报告 `available` 也不等于验收通过。CLI 不会自动强制这些阈值。
- 若 `report` 输出 `flamegraph=unavailable`，记录原因（缺 folded、样本为 0、SVG 渲染失败等），不要手工补图。
## 8. 证据保全
```bash
sha256sum "$RUN/stacks.folded" "$RUN/stacks.meta.json" "$EVID/report.html" > "$EVID/SHA256SUMS"
hostname > "$EVID/host.txt"
uname -a >> "$EVID/host.txt"
date -u +%Y-%m-%dT%H:%M:%SZ > "$EVID/finished_at.txt"
cp /proc/sys/kernel/perf_event_paranoid "$EVID/perf_event_paranoid.txt" 2>/dev/null || true
cp /proc/sys/kernel/kptr_restrict "$EVID/kptr_restrict.txt" 2>/dev/null || true
```
至少保留：`preflight.txt`、`stacks.stdout.log`、`stacks.stderr.log`、`report.stdout.log`、`report.stderr.log`、完整命令、PID/Tgid/NSpid、host/uname、负载说明、SHA256。旧证据目录不得覆盖；复采使用新时间戳。
## 9. 参考口径（不要混写）
| 证据 | samples / unique / stack_fail | total_frames | NPU 帧占比（帧加权） | Top NPU 样本% |
|---|---|---|---|---|
| `report-stacks-live-final2.html`（正式 10 秒采样，旧报告/旧分类） | 1420 / 218 / 9（-17 EEXIST） | 13209 | 0.9917%（旧口径） | `drv_ascend_intf_ioctl` 115/1420=8.10%，self=0 |
| `final4/report-stacks-final4.html`（旧硬编码分类） | 1488 / 221 / 6 | 13658 | 0.86%（**已废弃口径**） | `drv_ascend_intf_ioctl` 101/1488=6.79% |
| `final4/report-stacks-final4-classified.html`（新共享分类） | 1488 / 221 / 6 | 13658 | **4.61%（630/13658）** | `drv_ascend_intf_ioctl` 101=6.79%；`trs_ioctl` 80=5.38%；`devmm_svm_ioctl` 18=1.21% |

- final2 是正式 10 秒采样基线；其 `npu_frame_pct=0.9917%` 是旧报告值，不能与 final4 的 4.61% 混写成同一 run/同一口径。
- final4 的 0.86% 和 4.61% 是同一份 final4 数据在旧硬编码分类与新共享分类下的两个结果；前者已废弃，不能用于新报告。
- 所有 Top NPU 样本% 的分母都是 `total_samples`；“包含该函数的采样栈比例”不等于帧占比，也不等于 CPU/NPU 占用率。
- `.analysis/stacks-probe/stacks.meta.json` 是旧 3 秒探针（62 样本、484 次 `-14 EFAULT`），不是最终基线；不要把 `-14` 当作正式 10 秒结果。
## 10. 常见错误
| 现象 | 处理 |
|---|---|
| `EPERM` / `operation not permitted` | 查 root/CAP_PERFMON/CAP_BPF/CAP_SYS_ADMIN、`perf_event_paranoid`、容器 seccomp/LSM/lockdown；不要直接改生产 sysctl。 |
| `ENOENT` | 内核拒绝 software CPU-clock 事件或配置不可用；确认 5.10 arm64 的 `PERF_TYPE_SOFTWARE`/`PERF_COUNT_SW_CPU_CLOCK`，不要伪造结果。 |
| `ESRCH` | 目标线程已退出/重启；重新发现宿主 Tgid，使用新 `RUN`，不要复用旧 meta。 |
| 符号全 0 / unknown 100% | 查 `kptr_restrict`、`CAP_SYSLOG`、`/proc/kallsyms`、`/proc/modules`；报告必须标 unavailable，不能猜函数名。 |
| samples=0 | `stacks` 可能仍返回 0；确认真实推理负载、宿主 Tgid、`filtered`/`stack_fail`/warnings，换新目录复采。 |
| 线程/map 容量报错 | 上限：target_pids 1024、stack_traces 16384、stack_counts 131072、perf events 4096；缩小 PID 集合或分批，不要删证据。 |
| `-17 EEXIST` | `bpf_get_stackid` 栈冲突，会计入 `stack_fail`；final2=9、final4=6 均在 1% 内，超阈值要排查而非改小 samples。 |
## 11. 明确不要做
- 不要停止、重启或信号 vLLM。
- 不要压测或制造合成负载；采样窗口必须是现场真实推理流量。
- 不要覆盖旧证据、手改 `meta`/`folded`/报告，或把旧报告当新证据。
- 不要把 host kernel stack 当 AI Core/算子火焰图，也不要把 Top NPU 8.10% 写成“NPU 占 8.1%”。
- 不要用 `--pid auto` 代替正式验收的显式 PID。
- 不要用 `--kernel-filter npu` 过滤后的 `samples`/`unique_stacks` 直接当整轮基线（`stack_fail`/`filtered` 仍是整轮计数）。
- 不要承诺 <1% 开销；`stacks` 尚没有同负载 A/B 的吞吐结论。
- 不要为了“通过”而修改 `perf_event_paranoid`、`kptr_restrict` 或伪造符号。
