# 报告质量审计与修复清单（2026-11-27）

对象：`.analysis/sweep-live-20261127/report.html`（新 sweep 报告）、
`docs/REPORT-REVIEW-SERVER-LIVE.md`（已有评审）、报告生成链路
（`internal/report/report.go`、`internal/report/template.html`、`internal/report/render.go`、
`internal/report/metrics.go`、`internal/report/build_metrics.go`、`internal/report/build_views_v2.go`、
`internal/report/offcpu.go`、`cmd/cann-ebpf/report.go`、`cmd/cann-ebpf/attach_report.go`）、
生成物 `docs/demo/all-charts.html` 与 `docs/demo/report-*.html`。

审计方式：只读代码 + 解析生成物内嵌的 `DATA` JSON + 实测文件体积/节点数；
`go test ./internal/report/ -count=1` 基线通过（`ok ... 3.096s`）。
**本文件只列问题与修复方案，未改任何代码，未 `git commit`。**

---

## 0. 前置事实核对：任务书里的三项前提在仓库中不存在

审计开始先核对"对象是否存在"，否则会写出无法落地的修复项。结论如下（证据可复现）：

| 任务书前提 | 实测 | 证据 |
|---|---|---|
| `internal/report/bundle.go` | **不存在** | `Test-Path` 为 False；`internal/report/` 目录列表中没有该文件 |
| `internal/report/bundle_template.html` | **不存在** | 同上；仓库中只有 `internal/report/template.html` |
| `RenderBundle` / `__REPORTS__` / `__SUMMARY__` | **全仓 0 命中** | 全仓正则搜索只命中 `docs/REPORT-REVIEW-SERVER-LIVE.md:71` 对该符号"不存在"的说明；`git log --all -- "*bundle*"` 无任何提交 |
| `docs/demo/all-reports.html` | **不存在** | `docs/demo/` 只有 `index.html`、`README.md`、`all-charts.html`（13,916 B）、4 份 `report-*.html`；`git log --all -- "*all-reports*"` 无记录 |

因此：

- 任务重点 4「`RenderBundle` 的 `__REPORTS__`/`__SUMMARY__` JSON `</` 转义 XSS」**没有对应代码可修**。
  该结论与评审文档 `docs/REPORT-REVIEW-SERVER-LIVE.md:71` 一致，本次审计独立复核确认。
  下面 **P2-1 / P2-2** 给出仍然成立的转义残留风险（占位符替换顺序、`</` 替换为死代码、测试不可区分），
  这才是这条线上真正需要补的地方。
- 任务重点 5「`all-reports.html` 自包含 vs `all-charts.html` 依赖同目录文件」的"重复"前提不成立；
  但 **`all-charts.html` 依赖同目录 4 份文件 + README 声称"页面内嵌"** 是真实缺陷，见 **P1-8**。

单条复核命令（PowerShell，5.1/7 均可）：

```powershell
$root="C:\Users\liruilong\pentagi\cann-ebpf"
Test-Path "$root\internal\report\bundle.go","$root\internal\report\bundle_template.html","$root\docs\demo\all-reports.html"
Select-String -Path "$root\internal\report\*.go","$root\internal\report\*.html","$root\cmd\cann-ebpf\*.go" -Pattern 'RenderBundle|__REPORTS__|__SUMMARY__'
```

---

## 1. 实测基线（所有数字均来自本轮实测，不是引用文档）

### 1.1 报告产物

| 文件 | bytes | 嵌入 span 点 | `summary.spans` | off-CPU 行 | series | 说明 |
|---|---:|---:|---:|---:|---:|---|
| `.analysis/sweep-live-20261127/report.html` | 8,650,370 | 20,000 | 46,717 | 20,000 / 40,457 | 256 | 本次审计主对象，默认参数 |
| `.analysis/server-live-batch-20261127/report-review-v2.html` | 8,649,046 | 20,000 | 46,717 | 20,000 / 40,457 | 256 | 评审文档所称"修复后" |
| `.analysis/server-live-batch-20261127/report.html` | **18,078,918** | 46,717 | 46,717 | 40,457 | 256 | 评审文档所称"修复前"（未截断） |
| `.analysis/server-live-20261127/report.html` | **40,900,722** | 100,000 | 199,789 | 100,000 / 150,695 | 256 | 旧默认 100k 点，最坏情况 |
| `docs/demo/report-vllm-attach.html` | **11,866,669** | 28,905 | 28,905 | — | 0 | 未截断，仍在对外画廊里 |
| `.analysis/report-auto-sample.html` | 11,864,947 | 28,905 | 28,905 | — | 0 | 同上 |
| `.analysis/live-eval-attach.html` | 11,864,907 | 28,905 | 28,905 | — | 0 | 同上 |
| `docs/demo/report-vllm-sweep.html` | 360,658 | 0 | 0 | — | 119（含 41 条 `_created`/`_info`） | **旧版本生成，见 P0-1** |
| `docs/demo/report-real36-metrics.html` | 705,224 | 0 | 0 | — | 541 | 无 `views_v2` |
| `docs/demo/report-host-device.html` | 2,438,887 | 11,859 | 11,859 | — | 0 | 无 `views_v2` |

结论：`18MB` 是修复前遗留产物，`11.8MB` 是 3 份未重新生成的 attach 产物；
**当前默认已降到 8.65MB**，但 8.65MB 里仍有 4.0MB 是"只画 14 行"的数据（P1-3）。

### 1.2 sweep 报告 `DATA` 体积分解（共 8,418,933 字符）

| 段 | 字符数 | 占比 | 备注 |
|---|---:|---:|---|
| `views_v2` | 4,326,802 | 51.4% | 其中 `off_cpu.rows` = 20,000 行 × ≈201 字符 ≈ 4.02MB |
| `spans` | 3,724,373 | 44.2% | 20,000 点 × ≈181 字符 |
| `metrics` | 277,236 | 3.3% | 256 series / 5,604 点 |
| `views`（v1 热图等） | 47,836 | 0.6% | |
| `heatmap` | 35,866 | 0.4% | 18 行 × 160 列 |
| 其他 | ≈11,000 | 0.1% | |

### 1.3 指标计数链（与评审文档 §1 完全一致，已独立复核）

| 字段 | 值 |
|---|---:|
| `meta_series` | 961 |
| `projected_series` | 991 |
| `filtered_series` | 41 |
| `eligible_series` | 950 |
| `rendered_series` | 256 |
| `truncated_series` | 694 |
| `series_limit` | 256 |
| `metrics.samples` | 27,102 |
| `_created` / `_info` 出现在 `metrics.series` 里的条数 | **0**（过滤生效） |

### 1.4 十视图实测状态（sweep 报告 = 4/10 可用，不是 3/10）

| 视图 | available | confidence | reason 摘要 |
|---|---|---|---|
| `load_latency` | true | partial | 背景负载，knee 不计 |
| `off_cpu` | true | partial | aggregate 模式 |
| `operator_treemap` | true | inferred | host fallback，无 msPTI |
| `service_topology` | true | partial | process scope only |
| `critical_path` | false | — | no device correlation |
| `summed_delay` | false | — | 上游不可用 |
| `slack_cdf` | false | — | 上游不可用 |
| `roofline` | false | — | 缺 `ops.jsonl` |
| `ab_diff` | false | — | 缺 A/B 两次 run |
| `change_point` | false | — | `run too short: 29532 ms < 300000 ms` |

（评审文档 §2 的 3/10 针对更早的 batch run，那次没有 `sweep.json`；两处数字不冲突，
但"3/10"不能作为通用口径，见 P1-6 对"摘要不区分 confidence"的批评。）

### 1.5 画布数量

模板静态画布 9 个（`timeline`/`scatter`/`heatmap`/`latencyHeatmap`/`subsecondHeatmap`/
`aggregateBands`/`cdf`/`quadrant`/`busy`）+ 指标轴 1 个 + **每个 metric series 一个动态画布 256 个**
= 运行时 **266 个 canvas**；HTML 源码里 `<canvas` 字面量为 11（其中 1 个在 JS 字符串模板中）。

### 1.6 六个重点项的一句话现状（导航用）

| 重点 | 现状 | 结论落在 |
|---|---|---|
| 1. `*_created` 指标过滤 | **代码已生效**：`metrics.go:241-245` 调用 `isContextOnlyMetric`（`:316-319`），sweep 报告里 0 条泄漏，41 条只保留在原始 `metrics.jsonl`；但 `docs/demo/report-vllm-sweep.html` 仍泄漏 41 条，且过滤规则只看后缀 | P0-1、P2-4 |
| 2. 991 → 256 截断与提示 | **截断生效、提示不全**：顶部卡有"950 → 256 / 未绘制 694"，但无被丢弃名单，指标面板内无提示，字典序截断会切家族 | P1-1、P1-4 |
| 3. 十视图覆盖摘要 | **摘要只看 available**：4/10 可用里 3 partial + 1 inferred 被平掉，阈值类原因被写成"缺证据" | P1-6 |
| 4. `RenderBundle` / 双 JSON `</` 转义 | **对象不存在**（`bundle.go`/`bundle_template.html`/`RenderBundle` 全无）；单 `DATA` 路径靠 `json.Marshal` 的 HTML 转义保证安全，`</` 替换是死代码，占位符替换顺序可被标题劫持 | §0、P2-1、P2-2 |
| 5. `all-reports.html` vs `all-charts.html` | **`all-reports.html` 不存在**；`all-charts.html` 是 13.9KB 索引，依赖同目录 4 份报告（≈15.4MB），README 却称"内嵌"，加载失败无提示 | P1-8、P2-7 |
| 6. 体积 / 点数 / canvas | **18MB 是修复前遗留、11.8MB 是 3 份旧 attach 产物、当前默认 8.65MB**；8.65MB 中 4.0MB 是只画 14 行的 off-CPU 行；256 series → 266 canvas | P1-3、P1-5、P2-8 |

---

## 2. P0（对外误导，修复成本低，先做）

### P0-1 `docs/demo/` 画廊产物是修复前版本，仍在画 `_created` 平线、仍是 11.8MB

- **位置**
  - `docs/demo/report-vllm-sweep.html`（`generated_at = 2026-11-27T07:59:44Z`）
  - `docs/demo/report-vllm-attach.html`（11,866,669 B，28,905 点未截断）
  - `README.md:589`、`README.md:633`、`docs/demo/README.md:3,7,9`
  - 对照：修复点在 `internal/report/metrics.go:316-319`（`isContextOnlyMetric`）、
    `cmd/cann-ebpf/attach_report.go:18`（`attachReportDefaultMaxSpans = 20_000`）
- **现象**
  - 画廊"并发扫描"标签页里的报告仍有 **41 条** `_created`/`_info` 序列，例如
    `vllm:time_to_first_token_seconds_created`、`vllm:cache_config_info`，
    每条都是 **36 个完全相同的值**（如 1795762902.0160267 × 36）——正是修复要消除的误导性平线；
    该报告 `metrics` 字段还是旧 schema（没有 `rendered_series`/`eligible_series`/`series_limit`）。
  - "Host + vLLM 十视图"标签页是 **11.8MB / 28,905 点**，不是新默认的 20,000 点。
  - `README.md:589` 同时声称"页面内嵌四份已生成的报告"，而产物是修复前生成的，
    `docs/REPORT-REVIEW-SERVER-LIVE.md:67` 已经写"`_created` 已修复"——**产物与声明互相打脸**。
- **根因**
  1. 报告默认值（`20_000`）与过滤逻辑改了，但 `docs/demo/*.html` 是手工提交的产物，没有任何脚本或
     CI 步骤在改代码后重新生成它们；
  2. 画廊不记录"生成时代码版本/参数"，README 也不标注产物时间，无法判断是否过期；
  3. 评审文档只审了 `.analysis/` 下的报告，没审 `docs/demo/` 下对外展示的那 4 份。
- **具体修复**
  1. 用当前代码重新生成 4 份 demo 报告（`report --metrics ... --max-series 256`，
     attach 用 `attach --report auto` 走新默认 20,000），覆盖 `docs/demo/report-*.html`；
  2. 每份产物在 `DATA.notes` 或 footer 里带上"生成命令 + 工具版本 + 默认上限"，
     供 `docs/demo/README.md` 表格引用；
  3. 在 `docs/demo/README.md` 加一张"产物生成时间/参数"表，并写明"改默认值后必须重跑"；
  4. 顺带把 `README.md:589` 的错误表述改掉（见 P1-8）。
  > 注意：重新生成会产生二进制体积变化（11.8MB → ≈8MB），本次审计**未执行**该操作，
  > 因为任务要求"不改代码、不 commit"。
- **验收测试**
  - 脚本断言（PowerShell，逐份检查）：

    ```powershell
    foreach ($f in Get-ChildItem docs/demo/report-*.html) {
      $line = [IO.File]::ReadAllLines($f.FullName) | Where-Object { $_.StartsWith('const DATA = ') }
      $json = $line.Substring(12).TrimEnd().TrimEnd(';')
      $d = $json | ConvertFrom-Json
      $bad = @($d.metrics.series | Where-Object { $_.name -match '_(created|info)$' }).Count
      "{0}: created/info={1} pts={2} bytes={3}" -f $f.Name,$bad,@($d.spans).Count,$f.Length
    }
    ```
    期望：`created/info = 0`；attach 的 `pts <= 20000`；4 份都含
    `rendered_series` / `eligible_series` / `series_limit` 字段。
  - 新增 Go 测试 `TestDemoArtifactsMatchCurrentGenerator`（`internal/report`）：
    读取 `docs/demo/report-*.html`，解出 `DATA`，断言 `metrics.available` 为真时
    `meta_series >= rendered_series`、`filtered_series > 0`、且不存在 `_created`/`_info` 名字。
    （若不想让测试依赖大文件，可只断言字段存在性与名字过滤，文件体积另设阈值。）

---

## 3. P1（影响可信度或可用性，本迭代必修）

### P1-1 截断不可审计，且选择顺序按 series key 字典序（长尾家族成片消失）

- **位置**
  - `internal/report/metrics.go:227-233`（先按 `metricPriority`，同优先级再按 `keys[i]` 字典序排序）
  - `internal/report/metrics.go:248-255`（`EligibleSeries` 赋值 + `keys = keys[:opts.MaxSeries]` + warning）
  - `internal/report/template.html:650-667`（只显示数字，不显示名单）
- **现象**
  - 页面和 warning 只给出"950 → 256，未绘制 694"，**没有任何被丢弃 series 的名字或家族**；
    报告落地后无法核实"关键指标有没有被截掉"，也无法在评审会上复盘限流是否合理。
  - 实测 256 条里 **167 条是 `proc_*`**，分布是 **9 个家族 × 18 个 PID = 162 条**，外加
    `proc_sched_running_seconds` **只剩 5/18 条**——**同一家族被从中间切开**，这正是"按 key 字典序截断"
    的直接证据。被完整保留的 9 个家族恰好是字典序靠前的那批：
    `proc_context_switches_nonvoluntary_total`、`proc_context_switches_voluntary_total`、
    `proc_cpu_system_seconds`、`proc_cpu_user_seconds`、`proc_io_read_bytes`、`proc_io_write_bytes`、
    `proc_page_faults_major_total`、`proc_page_faults_minor_total`、`proc_sched_run_delay_seconds`。
    字典序排在 `proc_sched_running_seconds` 之后的家族会**整族消失**，
    而 4 个近乎冗余的家族（两种 context_switch + 两种 cpu）就占掉 72 个槽位。
  - 同时 `metricPriority`（`metrics.go:560-580`）是一张 24 条前缀的硬编码白名单，
    不在表里的指标一律同优先级 → 谁被保留完全取决于字母序，与"哪个指标对性能判断更重要"无关。
- **根因**
  1. 截断只做 `sort + slice`，没有"每家族保底配额 / 分层选择"策略；
  2. 丢弃结果没有回写 `MetricsData`（只有计数），信息在 `LoadMetrics` 里就地丢失；
  3. 报告没有"指标目录（catalog）"概念，读者无法枚举原始 series。
- **具体修复**
  1. `internal/report/metrics.go`：新增
     ```go
     type DroppedSeries struct {
         ID     string `json:"id"`
         Name   string `json:"name"`
         Source string `json:"source"`
         Reason string `json:"reason"` // limit | context_only
     }
     ```
     在 `LoadMetrics` 里记录被截断/被过滤的 series（`Reason` 区分
     `limit` 与 `context_only`），只存 `ID/Name/Source`（不带点值），
     按 `Name` 稳定排序后写入 `data.Dropped`，并设硬上限（如 4,000 条）防止反向膨胀。
  2. 选择策略改为"分层 + 家族配额"：
     - 第 1 层：`metricPriority` 命中项，全保留；
     - 第 2 层：每个 `family` 至少保留 1 条，按 family 的 `max(|value|)` 或总点数降序。
       `family` 定义要写死并配测试：`Name` 去掉 `:p50/:p95/:p99` 后缀、去掉结尾 `_total`，
       再取前 3 个 `_` 分隔段（`proc_cpu_user_seconds` → `proc_cpu_user`；
       `npu_hbm_used_bytes` → `npu_hbm_used`；`vllm:kv_cache_usage_perc` → `vllm:kv_cache`）；
     - 第 3 层：剩余槽位按 `point 数 × 变化幅度` 填满；
     - 同一 family 内 p50/p95/p99 视为一组，不允许拆开（避免只剩 p50）。
  3. warning 文案补上"被丢弃的家族数量"和"查看名单"的指引，例如
     `metrics series 数量 950 超过上限 256，已截断 694（涉及 27 个家族，名单见报告 metrics 面板 / metrics.dropped）`。
  4. `template.html`：指标面板头部加一行"已绘制 256 / 候选 950；被截断 694（点击展开名单）"，
     用 `<details>` 渲染 `metrics.dropped`，避免只在页面顶部一张卡里提示。
- **验收测试**
  - 新增 `TestLoadMetricsRecordsDroppedSeries`（`internal/report/metrics_test.go`）：
    构造 10 个家族 × 2 条 = 20 条 series，`MaxSeries: 6`；
    断言 `len(Series) == 6`（6 个家族各 1 条）、`len(Dropped) == 14`、
    `len(Series)+len(Dropped) == EligibleSeries`，且 `Dropped` 里没有一条与 `Series` 的 `ID` 重复；
    同时断言一条 warning 说明"家族数 10 > 上限 6，已按重要性截断家族"（当 `MaxSeries < 家族数` 时的降级行为）。
  - 新增 `TestTruncationKeepsOneSeriesPerFamily`：
    12 个家族各 5 条、`MaxSeries: 12` → 断言每个家族恰好保留 1 条，且没有任何家族为 0。
  - 新增 `TestDropListIsStableAndOrdered`：同一输入跑两次，`Dropped` 的 `ID` 序列完全一致。
  - 页面手工验收：在 DevTools Console 执行
    `DATA.metrics.dropped.length`，应等于 `truncated_series + filtered_series`；
    `<details>` 展开后条数一致。

### P1-2 `rebaseMetrics` 只裁剪点、不重算计数 → 页面上的"已绘制 N"可能与实际 series 数不符

- **位置** `internal/report/build_metrics.go:28-37`（先 `rebaseMetrics`，再原样赋给 `data.Metrics`）；
  `rebaseMetrics` 本体 `build_metrics.go:97-152`（`out := *m` 浅拷贝，只重建 `out.Series`/`out.Warnings`，
  `RenderedSeries`/`TruncatedSeries`/`EligibleSeries`/`Coverage`/`IntervalMS` 全部沿用旧值）；
  `template.html:650-656` 用这些计数渲染顶部卡。
- **现象** 指标窗口与 span 窗口不完全重合时（`rebaseMetrics` 会把窗口外的点丢掉，
  见 `build_metrics.go:117-136`），`series` 数组长度会变小，但
  `rendered_series` 仍是 256、`truncated_series` 仍是 694、`coverage` 仍是 0.9825。
  页面于是显示"256 / 950 指标已绘制"，实际画出的可能是 240 条；顶部卡的
  `truncated = metrics.truncated_series || max(0, eligible-rendered)` 也因为字段非 0 而不会自愈。
- **根因** `rebaseMetrics` 是"视图层裁剪"，返回的是浅拷贝，缺少"裁剪后重算派生计数"的步骤；
  同时 `Data.Metrics` 与 `ViewsV2.ChangePoint` 用的是同一个指针（`build_views_v2.go:117-121`），
  改动时容易只改一处。
- **具体修复**
  1. 在 `rebaseMetrics` 末尾（`build_metrics.go:147` 之前）重算：
     ```go
     out.RenderedSeries = len(out.Series)
     if out.EligibleSeries > out.RenderedSeries {
         out.TruncatedSeries = out.EligibleSeries - out.RenderedSeries
     }
     out.Coverage = estimateCoverage(out.Series, out.IntervalMS, float64(out.EndNS-out.StartNS)/1e6)
     ```
     （`IntervalMS` 若在 `LoadMetrics` 里是估算值，重算前需保留；当前 `estimateIntervalMS` 只依赖点距，
     可直接复用。）
  2. 在 `rebaseMetrics` 里对被整条丢空的 series 记一条 warning
     （`"rebase 后有 N 条 series 在 span 窗口内没有点，已从图表移除"`），
     并同步扣减 `EligibleSeries`/`TruncatedSeries` 的语义说明。
  3. 页面兜底（`template.html:650-654`）改为**优先用数组长度**而不是优先用字段：
     `const rendered = series.length;`，字段只作为"生成时口径"显示在 tooltip 里。
- **验收测试**
  - 扩充 `TestRebaseMetricsClipsBothSides`（已有，`build_metrics_test.go:33`）：
    构造 `EligibleSeries: 4, RenderedSeries: 3, TruncatedSeries: 1`，rebase 后只留 2 条 →
    断言 `RenderedSeries == 2`、`TruncatedSeries == 2`、`Coverage` 被重算（不等于旧值）。
  - 新增 `TestRebaseMetricsKeepsCountersConsistent`：断言
    `RenderedSeries == len(Series)` 且 `TruncatedSeries == EligibleSeries - RenderedSeries`
    （对 `Available == true` 的所有分支）。
  - 页面：用 `.analysis/server-live-batch-20261127` 的 run 重新生成报告，
    断言 `DATA.metrics.series.length == DATA.metrics.rendered_series`。

### P1-3 off-CPU aggregate 用"每 span 一行"冒充 lane：20,000 行数据只为画 14 条，占 4.0MB

- **位置**
  - `internal/report/offcpu.go:188-196`（aggregate 模式一行一个 span，`Lane` 字段写 `function · tid · spanID`）
  - `internal/report/build_views_v2.go:82-87`（`MaxRows: input.opts.MaxSpanPoints` —— 与散点共用上限）
  - `internal/report/template.html:2573-2575`（`const maxRows = 14; const visible = rows.slice(0, maxRows)`）
  - `internal/report/template.html:2665-2668`（提示"显示前 14 条 lane，另有 19986 条未绘制"）
- **现象**
  - sweep 报告里 `views_v2.off_cpu.total_rows = 40457`，被 `--max-spans 20000` 截到 **20,000 行**，
    每行 ≈201 字符 ≈ **4.02MB**（占 `DATA` 的 47.8%，是页面体积最大的单项）；
  - 画面上按 `start` 排序取**前 14 行**，其余 19,986 行只是"载荷"，不参与任何计算；
  - 这 14 行是"最早的 14 个 span"，不是"off-CPU 最多的 14 个 lane"，也不是 lane 聚合；
    对外提示却写成"显示前 14 条 lane"——**语义错误**：真实 lane（TID）只有 18 条；
  - 因为 aggregate 模式没有 exact segments，ribbon 实际只画"on/off CPU 占比条"，
    这种图**每 lane 一行**才有意义，20,000 行 span 级数据在视觉上是噪声。
- **根因**
  1. `OffCPUData.Rows` 只有"行"一种粒度，没有"lane 聚合"表示；
  2. `MaxRows` 借用 `MaxSpanPoints`，缺独立的 `--max-offcpu-rows`；
  3. 渲染层用"取前 N"而不是"按重要性取 Top N"，且把 span 行叫作 lane。
- **具体修复**
  1. `internal/report/offcpu.go` 新增 `OffCPUOptions.MaxLanes int`（默认 32）与
     `OffCPUOptions.Mode` 选择；aggregate 模式改为**按 lane 聚合**：
     ```go
     type OffCPULaneRow struct {
         Lane      string  `json:"lane"`
         Count     int     `json:"count"`
         OnCPUMS   float64 `json:"on_cpu_ms"`
         OffCPUMS  float64 `json:"off_cpu_ms"`
         FutexMS   float64 `json:"futex_ms"`
         OffCPUPct float64 `json:"off_cpu_pct"`
     }
     ```
     `Rows` 保留（exact 模式 = 区间行），aggregate 模式写 `Lanes []OffCPULaneRow`，
     行数从 40,457 → 18，体积从 4.0MB → 约 2KB。
  2. `build_views_v2.go:82-87` 不再复用 `MaxSpanPoints`：
     `MaxRows: opts.MaxOffCPURows`（新增 `Options.MaxOffCPURows`，默认 2,000），
     `MaxLanes: 32`；`cmd/cann-ebpf/report.go` 增加 `--max-offcpu-rows`（默认 2000，`0` 表示不限）。
  3. 渲染层：aggregate 模式按 `off_cpu_pct` 降序取 Top 32 lane，标题写"按 off-CPU 占比排序"；
     exact 模式仍按时间取前 N 条并注明"按时间排序取前 N"。
  4. 提示文案修正为"共 18 条 lane，显示 18 条"（aggregate）/
     "共 40,457 条区间行，显示前 14 条"（exact），不再把 span 行叫 lane。
- **验收测试**
  - 新增 `TestBuildOffCPUAggregatesByLane`（`internal/report/offcpu_test.go`）：
    50 个 span、2 个 TID、各带 `offcpu_ns` → 断言 `len(Lanes) == 2`、
    `OffCPUMS` 等于同 lane 内 span 的 off-CPU 之和、`Count == 25`。
  - 新增 `TestBuildOffCPULaneCap`：3 个 TID、`MaxLanes: 2` → 断言只留 2 条且 `Truncated == true`。
  - 新增 `TestReportSizeBudget`（`internal/report`）：
    用 20,000 span + 每 span 1 个 off-CPU 字段构造 `Data`，`Render` 后断言
    `len(html) < 6_000_000`（当前实现会到 ≈8.6MB，修复后应 ≈4.7MB）。
    这是把"18MB/11.8MB"变成可回归断言的唯一办法。
  - 页面：DevTools 断言 `DATA.views_v2.off_cpu.lanes.length <= 32`
    且 `JSON.stringify(DATA).length < 7_000_000`。

### P1-4 指标面板内部没有任何"只画了 256 / 950"的提示，统计表还声称"256 项"

- **位置** `internal/report/template.html:372-379`（`metricsPanel` 标题与 `#metricsMeta`）；
  `template.html:3564-3569`（`metricsMeta` 只写 samples/interval/coverage/clock）；
  `template.html:3619`（统计表标题 `fmtCount(stats.length) + ' 项 · 统计基于完整 metrics 样本'`）。
- **现象** 读者滚到"多指标小倍数"面板时，看到 256 行小图、统计表 256 行，
  面板内**没有任何地方**说明候选有 950 条、被截断 694 条、上限 256；
  只有页面顶部一张"指标覆盖"卡和底部"采集告警"列表提到。
  统计表写"256 项 · 统计基于完整 metrics 样本"，更容易被读成"这次只有 256 个指标"。
  （"完整 metrics 样本"指的是未降采样的点，措辞本身没错，但与"项"放在一起会误导。）
- **根因** 截断信息只写进了 `data.Warnings` 和顶部卡，没有下沉到指标面板的上下文；
  面板头部是静态文案（`每个 series 一行`），不读 `metrics` 元数据。
- **具体修复**
  1. `template.html` 的 `renderMetricsSmallMultiples` 里把 `#metricsMeta` 扩成：
     `256 / 950 已绘制 · 未绘制 694（上限 256，--max-series 可调） · 27,102 samples · 间隔 1 s · 覆盖 98.3% · 时钟 mono`。
  2. `#metricsStatsMeta` 改成
     `256 项（全部候选 950；统计基于未降采样点）`。
  3. 面板 `<p>` 补一句固定说明：
     "小倍数只画前 256/950 条；完整 series 名单与丢弃原因见上方'指标覆盖'卡。"
  4. 与 P1-1 的 `<details>` 名单联动，避免重复实现。
- **验收测试**
  - 新增 Go 测试 `TestRenderedMetricsPanelStatesTruncation`：
    `Render` 一个 `TruncatedSeries > 0` 的 `Data`，断言输出包含
    `'未绘制'`、`'上限'`、`'--max-series'` 三个片段（模板常量，可用 `strings.Contains`）。
  - 页面：`document.querySelector('#metricsMeta').textContent` 必须同时包含
    `已绘制`、`未绘制`、`上限` 三个词。

### P1-5 每个 series 一个 canvas + 每次 `mousemove` 全量重绘 → 266 个 canvas、交互卡顿

- **位置** `internal/report/template.html:3570-3578`（`rowsHtml` 为每条 series 生成
  `<canvas id="metricSeries<index>">`）、`3380-3386`（`height = 62`）、
  `3490-3493`（`redrawMetricSeries` 循环**全部** rows）、
  `3495-3515`（`metricsMove` 每次鼠标移动调用 `redrawMetricSeries`）、
  `541-553`（`fitCanvas` 每次都重设 `canvas.width/height`，即每次重分配后台缓冲）。
- **现象**
  - 256 条 series → 256 个 canvas（加上 10 个固定图 = **266**），
    每个 canvas 在 dpr=2 时后台缓冲约 `2 × 宽 1400 × 高 62 × 4B ≈ 1.4MB`，
    合计 **≈350MB** GPU/CPU 内存；dpr=1 也有 ≈89MB；
  - 鼠标在任一小图上移动，都会对 **256 个 canvas 逐个 `fitCanvas` + 重绘**，
    即每次 `mousemove` 触发 256 次画布重分配 + 5,604 个点重绘 → 必然掉帧；
  - 已有的 `debounce`（`template.html:520-527`）没有被指标面板使用。
- **根因** "一条 series 一个 canvas"的朴素实现 + 十字光标用"全量重绘"实现同步；
  没有对同时可见的小图数量设上限。
- **具体修复**
  1. 小倍数只画前 `MAX_MULTIPLES`（建议 48）条，其余在统计表里保留；
     面板提示"显示前 48 条（候选 950，已绘制 256）"；
     再加一个"按名称搜索/翻页"的轻量 UI（纯前端过滤，不增加载荷）。
  2. `redrawMetricSeries(crosshairT, onlyIndex)`：十字光标只重绘
     `onlyIndex` 与 `#metricsAxis`，其他小图不重绘；
  3. `fitCanvas` 加缓存：宽高未变化时直接复用 `canvas.width/height`，不重设；
  4. 鼠标事件用 `requestAnimationFrame` 合并（或复用已存在的 `debounce(fn, 16)`）；
  5. 若最终仍要展示 256 条，改为**单个 canvas 网格**（一个 canvas 画 N 个小图），
     这样 canvas 数量与 series 数解耦。
- **验收测试**
  - 无前端测试设施，验收用 DevTools（建议后续补 `scripts/report-dom-check.mjs`，本轮不改）：
    ```js
    document.querySelectorAll('canvas').length            // 期望 <= 58
    document.querySelectorAll('.metric-row').length        // 期望 <= 48
    // 在第一个小图上移动 60 次，记录 performance.now() 差值
    // 期望平均每帧 < 4ms，且不出现 > 50ms 的长任务
    ```
  - Go 侧可断言载荷：`TestMetricsPayloadStaysSmall` —— 256×30 点构造 `Data`，
    断言 `len(html) < 2_000_000`，且 `strings.Count(html, "id=\"metricSeries") <= 48`（模板常量）。

### P1-6 十视图覆盖摘要把 partial/inferred 算成"可用"，且把阈值类原因写成"缺证据"

- **位置** `internal/report/template.html:638-683`（`renderCoverageBanner`）；
  关键行 `641-646`（`availableViews` 只数 `available === true`）、
  `678-680`（卡片 `ok/warn` 判定）。
- **现象**
  - sweep 报告顶部只显示 **"4 / 10 视图可用"**，副标题"6 个视图缺证据；报告里保留 unavailable 原因"。
    读者看不出这 4 个里 **3 个是 partial、1 个是 inferred**——而 `confidence` 恰恰是本项目
    "不把推断当实测"的核心承诺（README:585、`views_v2.go:5-10`）。
  - 6 个不可用视图里，`change_point` 的原因是**窗口太短（29,532 ms < 300,000 ms）**，
    `ab_diff` 是**缺第二次 run**，`roofline` 是**缺 ops.jsonl**——统一写成"缺证据"，
    让"再采一次就能有图"和"必须补采另一种证据"混在一起，运营上无法排优先级。
  - `views_v2` 缺失的旧报告（`docs/demo/report-real36-metrics.html`、`report-host-device.html`）
    连"10"都显示不出来：`views.length === 0` → 卡片显示"没有 views_v2 证据"，
    既无 `ok` 也无 `warn` 样式，视觉上像"正常"。
- **根因** 覆盖摘要是**纯前端临时统计**：把 10 个视图压成"可用/不可用"二元，
  没有把 `ViewStatus.Confidence` 纳入汇总；也没有把汇总结果落到 `DATA` 里，
  所以 Go 测试无法断言、文案无法统一。
- **具体修复**
  1. 在 `internal/report/views_v2.go` 增加（属于"数据契约"，可被测试）：
     ```go
     type ViewsV2Coverage struct {
         Total       int `json:"total"`
         Exact       int `json:"exact"`
         Partial     int `json:"partial"`
         Inferred    int `json:"inferred"`
         Unavailable int `json:"unavailable"`
     }
     ```
     在 `buildReportViewsV2` 末尾统一计算并写入 `ReportViewsV2.Coverage`。判定规则写死：
     `available == false` → `Unavailable`；`available == true` 且 `confidence ∈ {exact, partial, inferred}`
     → 对应字段；`available == true` 但 `confidence` 为空 → 记到单独的 `Unknown int` 字段
     （不并入 `exact`，否则"可用但没 confidence"会被静默吞掉）。
     不变量：`Exact + Partial + Inferred + Unavailable + Unknown == Total == 10`。
  2. `template.html:641-646` 改为读 `DATA.views_v2.coverage`，文案：
     `总计 10：exact 0 · partial 3 · inferred 1 · unavailable 6`；
     没有 `coverage` 字段的旧报告回退到现有计算，并标注"(旧报告，无汇总字段)"。
  3. 副标题按原因分两类：`阈值不足`（change_point：窗口/样本数）与`证据缺失`（其余），
     从 `status.reason` 前缀判定，或在 `ViewStatus` 增加 `Kind string`（`threshold|evidence|config`）。
  4. 卡片配色：`unavailable > 0` 一律 `warn`，`partial+inferred > 0` 用中性色而不是 `ok`。
- **验收测试**
  - 新增 `TestViewsV2CoverageCounts`（`internal/report/views_v2_test.go`）：
    `buildReportViewsV2(viewsV2BuildInput{})` → 断言 `Coverage.Total == 10`、
    `Unavailable == 10`、`Exact+Partial+Inferred+Unavailable == Total`。
  - 新增 `TestViewsV2CoverageMatchesStatuses`：对任意 fixture，
    遍历 10 个 view，按 `status.available`/`confidence` 重新数一遍，必须等于 `Coverage` 各字段。
  - 用 sweep fixture（含 `sweep.json` + `offcpu_ns` + trace-less spans）断言
    `Partial == 3 && Inferred == 1 && Unavailable == 6`（与 §1.4 实测一致）。
  - 页面：`document.querySelector('#coverageBanner').textContent` 必须包含
    `partial`、`inferred`、`unavailable` 三个词。

### P1-7 `attach` 自动报告把 `MaxSeries` 硬编码成 256，与 `report --max-series` 不一致

- **位置** `cmd/cann-ebpf/attach_report.go:90`
  `report.LoadMetrics(metricsPath, report.MetricsOptions{MaxSeries: 256})`；
  对照 `cmd/cann-ebpf/report.go:32`（`--max-series` 默认 256、可调）、
  `cmd/cann-ebpf/attach_report.go:18`（`attachReportDefaultMaxSpans` 有具名常量）。
- **现象**
  - 用 `attach` 自动生成报告时，`--max-series` 无法调整，256 是写死的魔法数；
  - 想验证"把上限提到 512 会不会更有用"必须改用 `report` 子命令重跑，两条链路行为不一致；
  - 该硬编码没有测试覆盖（`attach_report_test.go` 里没有断言 series 上限）。
- **根因** 自动报告接入 metrics 时只做了"能读到就挂上"的最小实现，
  参数没有从 `attach` 的 flag 透传，也没有抽具名常量。
- **具体修复**
  1. `attach_report.go` 增加
     ```go
     const attachReportDefaultMaxSeries = 256
     ```
     并把 `generateAttachReport(runDir, outPath, title string, maxSpanPoints int)` 的签名扩展为
     `generateAttachReport(runDir, outPath, title string, maxSpanPoints, maxSeries int)`；
  2. `cmd/cann-ebpf/attach.go` 增加 `--report-max-series`（默认 256），
     与 `--report-max-spans` 并列；
  3. 附带的 `summary`（`attachReportSummary`）增加 `SeriesLimit int` 字段
     （JSON 名 `series_limit`），让自动报告在控制台/JSON 输出里也能看到上限。
- **验收测试**
  - 扩充 `cmd/cann-ebpf/attach_report_test.go`：
    `TestGenerateAttachReportRespectsMaxSeries` —— fixture 造 8 条 series，
    调 `generateAttachReport(..., maxSeries: 3)` → 断言落盘 HTML 的 `DATA.metrics.rendered_series == 3`
    且 `series_limit == 3`、`truncated_series == 5`。
  - `TestAttachReportDefaultsAreNamed`：断言 `attachReportDefaultMaxSeries == 256`
    且 `attachReportDefaultMaxSpans == 20000`（防止再出现散落魔法数）。

### P1-8 `all-charts.html` 依赖同目录 4 份文件，README 却声称"页面内嵌"；加载失败无提示

- **位置** `docs/demo/all-charts.html:335-339`（`<iframe id="reportFrame" src="report-vllm-attach.html">`）、
  `:371-394`（`reports` 映射 4 个文件名）、`:402-413`（`frame.src = report.file`）；
  `README.md:589`；`docs/demo/README.md:3`。
- **现象**
  - `all-charts.html` 只有 **13,916 B**，`iframe` 指向同目录 4 份报告（合计 ≈15.4MB）：
    `report-vllm-attach.html`（11.8MB）、`report-host-device.html`（2.4MB）、
    `report-vllm-sweep.html`（0.36MB）、`report-real36-metrics.html`（0.7MB）。
    单独拷走 `all-charts.html` → 空白框，**没有任何错误提示**；
  - `README.md:589` 写"页面内嵌四份已生成的报告（……）"——**与实现不符**，
    实现是"索引 + 同目录兄弟文件"；`docs/demo/README.md:3` 的"每个报告是单文件 HTML"是对的，
    但和 README 的"内嵌"表述并列时容易让人以为画廊本身是单文件；
  - 画廊顶部的数据规模卡（`all-charts.html:297-318`：28,905 spans、16,006 点、541 series、
    148,466 区间）是**手写常量**，与 4 份报告的 `DATA` 没有绑定关系，改报告不更新卡片就会漂移。
- **根因**
  1. 画廊是"索引页"而非"打包器"，但文档按"打包器"描述；
  2. 没有 `iframe onerror`/超时兜底，也没有加载失败的用户可见提示；
  3. 摘要数字手写，缺少生成步骤。
- **具体修复**
  1. **保留索引方案**（不要在仓库里再生成 17MB 级 `all-reports.html`；`git log` 证明它从未存在过，
     没必要引入），但把文档改准确：
     - `README.md:589` → "打开 `docs/demo/all-charts.html`（索引页，需与同目录 4 份 `report-*.html` 一起分发）"；
     - `docs/demo/README.md` 增加"分发时必须整个 `docs/demo/` 目录一起拷"的显式说明；
  2. `all-charts.html` 增加兜底：
     ```js
     frame.addEventListener('error', showMissing);
     // 或 3s 后检查 frame.contentDocument === null / body 为空 → showMissing
     function showMissing(){ /* 显示"缺少同目录 report-*.html，请整目录分发" + 文件清单 */ }
     ```
  3. 摘要卡改为**运行时从 iframe 的 `DATA` 读取**（`frame.contentWindow.DATA.summary.spans` 等），
     读不到就显示 `--`。
     > 注意：用 `file://` 直接打开时 Chrome 把每个本地文件视为独立 opaque origin，
     > `contentWindow.DATA` 可能被同源策略拒绝；必须保留"读失败则显示 `--`"的分支，
     > 不要为此改成 `--allow-file-access-from-files`。若需要稳定数字，就由生成脚本在写
     > `all-charts.html` 时把统计值填进去（比手写常量可靠）。
  4. 若未来确实需要单文件分发，再加 `report --bundle`（复用现有 `Render`，逐份渲染后内嵌 `srcdoc`），
     但这属于新功能，不进本次修复范围。
- **验收测试**
  - 把 `all-charts.html` 单独复制到空目录打开 → 必须看到"缺少同目录 report-*.html"的明确提示
    （而不是空白 iframe）。这是手工验收项，写进 `docs/demo/README.md` 的自检清单。
  - 同目录完整打开 → 4 个标签页的摘要数字与各报告 `DATA.summary.spans` /
    `DATA.metrics.samples` / `series.length` 一致；与上方卡片文字逐项比对。
  - `README.md` 中不再出现"页面内嵌四份"这类与实现冲突的表述（文本断言可放进文档 lint）。

---

## 4. P2（应修，可排后）

### P2-1 `Render` 的占位符替换顺序可被标题内容劫持

- **位置** `internal/report/render.go:32-34`：
  ```go
  out := strings.ReplaceAll(templateHTML, "__TITLE__", html.EscapeString(title))
  out = strings.ReplaceAll(out, "__BODY_CLASS__", bodyClass)
  out = strings.ReplaceAll(out, "__DATA__", payloadText)
  ```
- **现象** `--title` 传入含 `__DATA__` 的字符串时：第 1 步把 title 写进模板（`html.EscapeString` 不会动
  `__DATA__` 这种 token），第 3 步会把标题里的 `__DATA__` 也替换成**整个 8MB payload**，
  页面直接损坏/体积翻倍；含 `__BODY_CLASS__` 则能改写 `<body class>`；
  含 `__TITLE__` 在第 1 步不会被二次替换（顺序上安全），但整体机制仍不稳健。
  当前 title 只来自 `--title` flag，风险低；一旦将来用 manifest 的 `run_id`/`host`/服务名做标题就是注入面。
- **根因** 用"顺序 `ReplaceAll` 做模板"天然有 token 交叉污染问题，没有"一次性替换"或"占位符唯一化"。
- **具体修复**
  1. 改用 `strings.NewReplacer(...).Replace(templateHTML)`（**单遍替换**，互不交叉）；
  2. 或在插入前把 title 中的 `__` 转义（`strings.ReplaceAll(title, "__", "__\u200b")`，不推荐，会改文字）；
  3. 更彻底：把 title/bodyClass 也作为 JSON 的一部分（`DATA.title` 已有），模板 `<h1>` 用 JS 填，
     模板里只保留 `__DATA__` 一个占位符。
- **验收测试**
  - 新增 `TestRenderTitleCannotInjectPlaceholders`：
    `Options{Title: "__DATA__ __BODY_CLASS__ __TITLE__"}` → `Render` 后断言
    `strings.Count(text, "const DATA = ") == 1`、`strings.Contains(text, "metrics-only") == false`
    （除非真的 metrics-only）、且 `<h1>` 里能看到被 HTML 转义的原始标题文字。

### P2-2 `</` → `<\/` 是死代码，现有测试无法区分"靠 JSON 转义"还是"靠替换"

- **位置** `internal/report/render.go:23`；`internal/report/report_test.go:92-116`。
- **现象**
  - 实测 sweep 报告 8.4MB payload 里**字面 `<` 出现 0 次**（`\u003c` 出现 1 次）：
    因为 `json.Marshal` 默认 `SetEscapeHTML(true)`，`<`/`>`/`&` 已被转成 `\u003c/\u003e/\u0026`，
    `strings.ReplaceAll(payload, "</", "<\\/")` 从未命中；
  - 现有测试 `TestRenderEscapesScriptPayload` 只断言"输出里没有 `</script><script>alert(1)</script>`"，
    在两种机制下都会通过，所以**无法在"有人为了减体积关掉 HTML 转义"时报警**。
- **根因** 安全依赖"JSON 默认转义"这一隐式行为，代码里的注释却把功劳归于 `</` 替换；
  测试断言的是"结果字符串"，不是"机制"。
- **具体修复**
  1. 保留 `</` → `<\/`（纵深防御），但把注释改成"JSON 默认 `<` → `\u003c` 已足够；
     本行是对 `SetEscapeHTML(false)` 的兜底"；
  2. 若未来为体积改用 `json.Encoder.SetEscapeHTML(false)`，必须**显式**把 `<` 转成 `\u003c`
     （或 `\x3c`）：仅替换 `</` 不够——payload 里的 `<!--` 会让 HTML 解析器进入
     "script data escaped" 状态，后续 `</script>` 反而不能闭合（页面白屏/脚本不执行）；
  3. 测试改为"机制断言"：
     ```go
     if strings.ContainsAny(payload, "<>") { t.Fatal("payload must not contain literal angle brackets") }
     ```
- **验收测试**
  - 改 `TestRenderEscapesScriptPayload`：断言从 HTML 中截出的 `const DATA = ...;` 里
    `strings.ContainsAny(payload, "<>")` 为假、`json.Unmarshal(payload, &Data{})` 成功，
    且 `Title` 能原样 round-trip 回 `</title><script>alert(1)</script>`；
  - 新增 `TestRenderPayloadSurvivesScriptEscapeOff`：临时用 `SetEscapeHTML(false)` 的等价路径
    （把 payload 里的 `<` 换成字面量）验证 `</` 替换确实把 `</script>` 变成 `<\/script>`，
    并断言 `<!` / `<script` 也被处理（若采用 `\u003c` 方案则断言无字面 `<`）。

### P2-3 `projected_series` 不一定等于 `filtered + eligible`（空点 series 被双重漏计）

- **位置** `internal/report/metrics.go:234-248`：`data.ProjectedSeries = len(keys)`；
  循环内 `if agg == nil || len(agg.points) == 0 { continue }` **既不进 filtered 也不进 eligible**。
- **现象** 本次 sweep 恰好满足 `991 = 41 + 950`；但只要有 `_created` 之外的 series
  在窗口内**没有任何有效点**（全 NaN/Inf、时间戳全在窗口外），
  `projected > filtered + eligible`，页面上"投影后 991 / 过滤 41 / 候选 950"三者对不上账，
  读者会怀疑计数错误（这正是评审文档 §1 反复解释计数链的原因）。
- **根因** 三个计数在不同阶段赋值，没有不变量约束，也没有"空 series"这一分类。
- **具体修复**
  1. 增加 `data.EmptySeries int` 字段（JSON 名 `empty_series,omitempty`），在 `continue` 分支累加；
  2. 在 `LoadMetrics` 返回前断言/校正：`ProjectedSeries == FilteredSeries + EligibleSeries + EmptySeries`，
     不一致时写 warning（开发期用 `panic` 或测试断言，生产期降级为 warning）；
  3. 页面 `metricSub` 增加"空点 N"一段（仅 `N > 0` 时显示）。
- **验收测试**
  - 新增 `TestLoadMetricsCountsEmptySeries`：造 1 条全 NaN 的 gauge + 2 条正常 series →
    断言 `EmptySeries == 1`、`ProjectedSeries == 3`、`FilteredSeries + EligibleSeries + EmptySeries == 3`。

### P2-4 `_created` 过滤只看名字后缀，`process_start_time_seconds` 这类"常量元数据"仍会画平线

- **位置** `internal/report/metrics.go:316-319`（`strings.HasSuffix(lower, "_created"|"_info")`）。
- **现象** 过滤规则是"名字后缀"，覆盖不到语义同类的常量序列：
  `process_start_time_seconds`、`*_timestamp_seconds`、`node_boot_time_seconds`、
  `vllm:cache_config_info`（已被 `_info` 覆盖）等。它们和 `_created` 一样是"进程/配置元数据"，
  画成曲线就是一条水平线，同样误导；同时 `_created` 之外还有"整窗口零方差"的 gauge，
  当前完全没有识别。
- **根因** 用名字白/黑名单代替"这条序列是否能提供窗口内变化"的判据。
- **具体修复**
  1. 保留后缀规则（快、可解释），追加显式名单：
     `process_start_time_seconds`、`node_boot_time_seconds`、
     以 `_timestamp_seconds`/`_start_time_seconds` 结尾者 → 归入 `context_only`；
  2. 增加"零方差元数据"启发式（可选、默认关闭，避免误杀）：
     窗口内点数 ≥ 5 且 `max == min` 且 `len(points) == samples` 的 gauge，
     在 `stats` 上标注 `flat: true`，页面灰色显示并给出 tooltip，不直接删除——
     保留证据但明确"这不是性能曲线"；
  3. `metrics.meta.json` 若带 `kind: "metadata"` 之类的标注，优先采信（需与采集端对齐，另开任务）。
- **验收测试**
  - 新增 `TestIsContextOnlyMetric` 表驱动：`_created`/`_info`/`process_start_time_seconds` 为真，
    `vllm:request_success_total`、`npu_hbm_used_bytes` 为假；
  - 新增 `TestFlatGaugeIsAnnotatedNotDropped`：常量 gauge 仍出现在 `Series`，但 `Stats[i].Flat == true`。

### P2-5 旧 schema 报告会渲染出 10 张"没有数据"的视图卡，而不是"本报告版本无 views_v2"

- **位置** `internal/report/template.html:2011-2048`（`VIEWS_V2_LABELS.map` + `VIEWS_V2[key] || {}`）；
  `:466`（`VIEWS_V2` 缺省 `{}`）。
- **现象** `docs/demo/report-real36-metrics.html`、`report-host-device.html` 没有 `views_v2`
  → 页面渲染 10 张卡，每张都写 `unavailable` + "没有数据"。
  读者会以为"这次十视图全都不可用"，实际是**报告版本太旧、字段不存在**；
  同时顶部覆盖卡显示"没有 views_v2 证据"且既无 `ok` 也无 `warn` 样式，视觉上像正常。
- **根因** 模板把"缺字段"和"视图不可用"混为一谈。
- **具体修复**
  1. `if (!DATA.views_v2 || !Object.keys(VIEWS_V2).length) { 渲染一条 notice:
     "本报告由旧版本生成，不含 views_v2（十视图）字段；请用当前版本重新生成。" 并 `return`; }`；
  2. 覆盖卡在这种情况下给 `warn` 样式；
  3. 旧报告如果重新生成能拿到 views_v2，就在 demo 里替换掉（与 P0-1 同一动作）。
- **验收测试**
  - `Render(Data{})`（无 `views_v2`）断言输出里 `id="viewsV2Grid"` 的初始化分支走 notice，
    且不产生 10 张静态视图卡：`strings.Count(html, "class=\"view-v2-card ") <= 1`
    （模板里只有 1 处，位于 JS 字符串中）；
  - demo 的两份旧报告重新生成后，DevTools 断言 `DATA.views_v2` 存在且 10 个 key。

### P2-6 `metrics.warnings` 在页面里不可见

- **位置** `internal/report/template.html:3523-3534`、`:3564-3569`
  （`metricsMeta` 只写 samples/interval/coverage/clock）；
  数据侧 `metrics.go:161-163`（meta 读取失败 warning）、`:251-253`（截断 warning）写入 `MetricsData.Warnings`。
- **现象** `BuildWithMetrics` 会把 `m.Warnings` 合并进 `data.Warnings`（`build_metrics.go:34`），
  所以 warn 列表里能看到；但 **`metrics.warnings` 本身没有任何 UI**，
  metrics-only 分支（`build_metrics.go:79`）之外再出现新的 metrics 级 warning 时，
  很容易只写进 `metrics.warnings` 而忘记合并，读者就永远看不到。
- **根因** 双重存储（`data.Warnings` 与 `metrics.warnings`）没有单一渲染出口。
- **具体修复**
  1. 指标面板头部显示 `metrics.warnings` 条数并展开（`<details>`）；
  2. 或者在 `LoadMetrics` 之后统一"只保留一份"：把 metrics warning 全部并入 `data.Warnings`，
     `metrics.warnings` 留空（明确单一来源）。
- **验收测试**
  - 新增 `TestMetricsWarningsSurfaceOnce`：造一个 meta 读取失败 + 截断的场景，
    断言 `data.Warnings` 中两条都在，且 `data.Metrics.Warnings` 为空（或 UI 断言二者合并后无重复）。

### P2-7 demo 画廊摘要卡数字硬编码

- **位置** `docs/demo/all-charts.html:297-318`。
- **现象** 28,905 / 16,006 / 541 / 148,466 / "16.37 s / 18 TID / 9 PID" 全是手写；
  报告重新生成后（例如 attach 从 28,905 点截到 20,000 点）卡片不会跟着变，形成第二套"事实"。
- **根因** 静态 HTML 手工维护。
- **具体修复** 与 P1-8 的第 3 步合并：运行时从 iframe 的 `DATA` 读，
  或在生成脚本里渲染这一段。
- **验收测试** 重生成 demo 后，卡片数字与各报告 `DATA.summary.spans` /
  `DATA.metrics.samples` / `DATA.metrics.series.length` 逐项相等（手工 checklist）。

### P2-8 仓库里仍留着 40.9MB / 18MB 的历史报告，属于"体积问题"的真实来源

- **位置** `.analysis/server-live-20261127/report.html`（40,900,722 B）、
  `.analysis/server-live-batch-20261127/report.html`（18,078,918 B）、
  `.analysis/report-auto-sample.html`、`.analysis/live-eval-attach.html`（各 ≈11.8MB）。
- **现象** 这 4 份合计 ≈83MB，是"18MB/11.8MB"说法的出处；它们由旧默认（0 / 100k 点）生成，
  代码修了但产物没清理。它们不是代码缺陷，但会让"报告体积"审计每次都重新被提起。
- **根因** 派生产物与源码一起提交，缺少"派生目录不纳入版本库/定期清理"的约定
  （`.gitignore` 只忽略了部分路径，见仓库根 `.gitignore`）。
- **具体修复**（不改代码，只做仓库卫生）
  1. 归档到仓库外或压缩后删除（保留 SHA-256 与生成命令在 `docs/` 里）；
  2. 在 `docs/REPORT-REVIEW-SERVER-LIVE.md` 的"体积"一节标注"历史产物已归档，当前默认 8.65MB"。
- **验收测试** `Get-ChildItem -Recurse -Filter *.html .analysis | Where Length -gt 10MB` 结果为空
  （或只剩 1 份明确标注的对照样本）。

---

## 5. 测试缺口（按任务重点 7 汇总，可直接当 issue 列表）

现有测试**已经覆盖**的部分（本轮确认，不需要重写）：
`*_created`/`*_info` 过滤与计数（`metrics_test.go:112-155`）、
`MaxSpanPoints` 抽样保留最慢（`report_test.go:118-151`）、
off-CPU 截断 warning（`report_test.go:153-171`）、
`</script>` 注入（`report_test.go:92-116`，但断言偏弱，见 P2-2）、
可选证据缺失时视图不伪造（`views_v2_integration_test.go:393`）。

缺口与建议用例：

| # | 缺口 | 建议用例 | 必须断言 |
|---|---|---|---|
| T1 | 空数据 | `TestRenderEmptyData` | `Render(Data{})` 不 panic，输出含 `schema` 且无 `undefined` 文本；`$('#footer')` 路径不抛异常（Go 侧只能断言字符串，JS 侧用 DevTools 打开生成物） |
| T2 | 指标不可用 | `TestRenderMetricsUnavailable` | `Metrics.Available=false` + `Reason="no metrics points"` → 输出含该 reason 与"指标不可用"（模板常量），且 `DATA.metrics.series` 为空数组而不是 `null` |
| T3 | 截断 | `TestLoadMetricsRecordsDroppedSeries`（见 P1-1）、`TestReportSizeBudget`（见 P1-3） | `Dropped` 条数 = 694（`limit`）+ 41（`context_only`）= 735，且与 `truncated_series`/`filtered_series` 对账；`len(html)` 有上限 |
| T4 | 转义 | `TestRenderPayloadHasNoAngleBrackets`、`TestRenderTitleCannotInjectPlaceholders`、`TestRenderPayloadRoundTrips` | payload 无字面 `<>`；占位符不交叉；JSON round-trip |
| T5 | 体积 | `TestReportSizeBudget`（20k span + 20k off-CPU 场景 < 6MB）、`TestMetricsPayloadStaysSmall`（256×30 点 < 2MB） | 体积回归可量化 |
| T6 | 计数不变量 | `TestMetricsCountInvariant` | `projected == filtered + eligible + empty`；`rendered == len(series)`；`truncated == eligible - rendered` |
| T7 | 十视图汇总 | `TestViewsV2CoverageCounts`、`TestViewsV2CoverageMatchesStatuses`（见 P1-6） | 汇总与逐视图状态一致，总数恒为 10 |
| T8 | 截断选择策略 | `TestTruncationKeepsOneSeriesPerFamily`、`TestTruncationKeepsPrioritySeries` | 每家族至少 1 条；`metricPriority` 命中项必须全部保留（当前只测了"优先级数值"，没测"截断后的保留结果"） |
| T9 | 默认值 | `TestAttachReportDefaultsAreNamed`（见 P1-7） | 20000 / 256 都是具名常量 |
| T10 | demo 产物 | `TestDemoArtifactsMatchCurrentGenerator`（见 P0-1） | `_created` 为 0；字段齐全；点数 ≤ 20000 |

前端相关的验收（画布数量、交互帧时间、iframe 兜底）**当前没有自动化设施**。
保守做法：在修复清单里保留为手工清单，另开任务加
`scripts/report-dom-check.mjs`（Playwright/Puppeteer，读 `docs/demo/all-charts.html`，
断言 `canvas ≤ 58`、`metric-row ≤ 48`、`#coverageBanner` 含 partial/inferred/unavailable）。
本文件不改动 `scripts/`。

---

## 6. 建议修复顺序

1. **P0-1**（重新生成 demo 产物 + 补字段）——半天，直接消除"对外展示仍在画 `_created` 平线"。
2. **P1-1 / P1-2 / P1-6**（截断名单 + 计数不变量 + 覆盖汇总）——这是"可信度三件套"，
   都在 Go 侧、都可测试，先做能立刻拿到测试红利。
3. **P1-3 / P1-5**（off-CPU 按 lane 聚合 + 小倍数上限）——体积从 8.65MB 降到 ≈4.5MB，
   画布从 266 降到 <60，交互问题一并解决。
4. **P1-4 / P1-7 / P1-8**（面板提示、attach flag、画廊文档）——文案与一致性，成本低。
5. **P2 全部**——按需，其中 P2-1（占位符）与 P2-3（计数不变量）建议紧跟 P1 一起提交。

---

## 7. 附录：本轮实测命令

```powershell
# 0) 从任意报告里取 DATA（不要写死行号：用 StartsWith 找）
function Get-ReportData([string]$path){
  $lines=[IO.File]::ReadAllLines($path)
  $line=$lines | Where-Object { $_.StartsWith('const DATA = ') } | Select-Object -First 1
  $json=$line.Substring(12).TrimEnd().TrimEnd(';')
  $json | ConvertFrom-Json
}

# 1) 体积与点数
$root="C:\Users\liruilong\pentagi\cann-ebpf"
$d=Get-ReportData "$root\.analysis\sweep-live-20261127\report.html"
$d.metrics | Select-Object meta_series,projected_series,filtered_series,eligible_series,rendered_series,truncated_series,series_limit
@($d.spans).Count; @($d.views_v2.off_cpu.rows).Count; @($d.metrics.series).Count
# 本文件引用的 457 是这份报告的行号（模板固定 456 行头部 + 1 行 DATA），换报告请用上面的函数

# 2) payload 里有没有字面 '<'（判断 </ 替换是否命中）
$line=([IO.File]::ReadAllLines("$root\.analysis\sweep-live-20261127\report.html") |
  Where-Object { $_.StartsWith('const DATA = ') } | Select-Object -First 1)
([regex]::Matches($line,'<')).Count        # sweep 报告实测 0

# 3) 画布数量（源码字面量；运行时另计 256 个动态 canvas）
([regex]::Matches([IO.File]::ReadAllText("$root\.analysis\sweep-live-20261127\report.html"),'<canvas')).Count   # 实测 11

# 4) demo 产物是否过期
$j=Get-ReportData "$root\docs\demo\report-vllm-sweep.html"
@($j.metrics.series | Where-Object { $_.name -match '_(created|info)$' }).Count   # 实测 41
$j.generated_at                                                                  # 2026-11-27T07:59:44Z

# 5) 基线测试
go test ./internal/report/ -count=1
```

---

**审计结论一句话**：`*_created` 过滤、991→256 截断、覆盖卡、`DATA` 转义这四件事的**代码**已经成立，
真正的缺口是"截断不可审计 + 计数会漂 + off-CPU 数据粒度浪费 4MB + 十视图摘要不区分 confidence +
对外 demo 产物还是修复前版本"；`RenderBundle`/`all-reports.html` 从来不存在，不应再作为修复项追溯。
