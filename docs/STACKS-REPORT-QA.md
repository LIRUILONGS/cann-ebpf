# cann-ebpf stacks 报告 QA（final5）

> 日期：2026-11-27
> 对象：`report --stacks` / stacks-only HTML、`internal/report/flamegraph.go`、`internal/report/template.html`
> 原始结果：`.analysis/stacks-probe/ui-qa/results/`
> 截图：`.analysis/stacks-probe/ui-qa/*.png`、`.analysis/stacks-probe/final4/report-stacks-final4-classified.png`
> 方法：24 个 CLI 用例 + 静态依赖/XSS 检查 + Node 18/12 语法检查 + Chrome DOM/截图/交互 + final4 分类数据核对。未改原始 folded/meta，未重启 vLLM，未发推理请求。

## 0. 结论摘要

| 项 | 结果 |
|---|---|
| CLI 用例 | 24 个；11 个成功生成 HTML，13 个按预期非零退出且不生成误导性 HTML |
| 空数据 | `samples=0` / 全零 count / 缺 folded 显示 `unavailable`，不画空火焰图 |
| 非法输入 | 非法/负数/溢出 count、缺列、空行、超长行、坏 meta、错 schema、只有 folded 无 meta 均非零退出 |
| 超深栈 | 5,000 帧深栈生成成功，树被限制到 128 层/4096 节点，出现 `[truncated]` |
| 大输入 | 5,000 唯一栈 / 15,000 样本 / 300,000 加权帧：HTML 约 551 KB，Chrome 可加载 |
| 离线 | 无外部 script/link/img/iframe/@import/HTTP URL；单文件可离线打开 |
| XSS | 恶意帧名/标题只作为文本/数据属性出现；DOM 中没有新增 `<img>` 或第二个 `<script>` |
| 语法 | Node 18 与 Node 12 检查均 `rc=0` |
| final4 分类 | `has_npu461=true`、`has_trs_ioctl=true`、`has_devmm=true`；旧 0.86% 已被 4.61% 取代 |
| 交互 | 搜索高亮、点击缩放、重置、类别过滤均实际生效 |

## 1. 24 个 CLI 用例

输入目录：`.analysis/stacks-probe/ui-qa/inputs/<case>/`；结果：`results/final-results.json`。

| # | 用例 | 输入/操作 | 退出码 | HTML | 结论 |
|---:|---|---|---:|---:|---|
| 1 | `normal` | 正常 3 栈/5 样本 + 完整 meta | 0 | 223,458 | `flamegraph=available` |
| 2 | `empty` | `stacks.folded` 为空 | 0 | 216,241 | `unavailable`，不画空图 |
| 3 | `missing-folded` | 有 meta、无 folded | 0 | 216,270 | `unavailable`，给出缺失原因 |
| 4 | `bad-count` | count=`abc` | 3 | — | 解析失败，不生成 HTML |
| 5 | `negative-count` | count=`-2` | 3 | — | 解析失败 |
| 6 | `missing-column` | 无最后空格/count | 3 | — | 解析失败 |
| 7 | `empty-line` | 第 2 行为空 | 3 | — | 解析失败 |
| 8 | `overflow-count` | count=`2^64` | 3 | — | 溢出失败 |
| 9 | `zero-count` | 所有 count=0 | 0 | 216,249 | `unavailable`，不画空图 |
| 10 | `long-frame` | 单帧名约 5,000 字符 | 0 | 478,485 | 正常生成，帧名保留 |
| 11 | `long-frame-over-limit` | 单行超过 1 MiB | 3 | — | `bufio.Scanner: token too long` |
| 12 | `deep-5000` | 5,000 层深栈 | 0 | 226,750 | 生成成功，`[truncated]` |
| 13 | `duplicate` | 重复折叠行 | 0 | 216,491 | 树按路径合并，样本数保留 |
| 14 | `bad-meta-json` | meta 非法 JSON | 3 | — | 解析失败 |
| 15 | `meta-type-error` | meta 字段类型错误 | 3 | — | 解析失败 |
| 16 | `meta-schema-missing` | 缺 schema | 3 | — | `unsupported schema` |
| 17 | `meta-schema-wrong` | 错 schema | 3 | — | `unsupported schema` |
| 18 | `meta-negative-samples` | samples=-1 | 3 | — | uint64 解析失败 |
| 19 | `meta-duration-string` | duration_s 为字符串 | 3 | — | float 解析失败 |
| 20 | `meta-missing-fields` | 只有 schema + 少量字段 | 0 | 216,183 | 缺省值渲染，不补造数据 |
| 21 | `meta-samples-mismatch` | meta samples 与 folded 合计不一致 | 0 | 216,740 | 按 folded 画图，warning 记录差异 |
| 22 | `only-folded` | 只有 folded，无 meta | 3 | — | 缺 meta 明确报错 |
| 23 | `xss` | 恶意帧名/标题/`</script>` | 0 | 217,311 | 转义为文本，无脚本注入 |
| 24 | `final4` | final4 原始 folded/meta | 0 | 253,069 | 4.61%、`trs_ioctl`、`devmm_*` 可见 |

统计：exit 0 = 11；exit 3 = 13；exit 2/4 = 0。非法输入不会“生成一个看起来正常的空报告”。

## 2. 输入边界详解

### 2.1 空数据与缺文件

- `empty`：folded 存在但无行 -> `Profile.Total=0`，`BuildFlamegraph` 返回 `unavailable`，reason 含 `samples=0`。
- `zero-count`：行存在但 count 全 0 -> `root.Count=0`，同样 `unavailable`。
- `missing-folded`：meta 可读、folded 缺失 -> `LoadRun` 返回 `Profile=nil`，报告 `unavailable`，仍保留 meta/warnings。
- `only-folded`：meta 缺失 -> `LoadRun` 直接报错，CLI exit 3；不会从 folded 猜 meta。

### 2.2 非法 folded

`ReadFolded()` 逐行检查：

- 空行 -> `empty folded line`；
- 没有最后空格分隔 -> `missing stack/count separator`；
- `ParseUint` 失败（abc/-2/2^64）-> `invalid count`；
- 空帧名 -> `empty frame name`；
- 单行超过 1 MiB -> scanner 错误。

这些路径都在写 HTML 之前失败，不会留下半成品报告（`html_exists=false`）。

### 2.3 超深栈与大输入

- `deep-5000`：5,000 帧；树限制 `maxDepth=128`、`maxNodes=4096`；超出部分合并到 `[truncated]`，`TotalSamples` 与 self 合计保持。
- `large`：5,000 唯一栈、20 帧/栈、15,000 样本、300,000 加权帧、folded 约 1,015,000 B；三次生成 722/876/769 ms，HTML 550,995 B；Chrome DOM 1,745,011 B、4,082 节点、max depth 20、`truncated=true`，截图 140,138 B。
- `long-frame`：约 5,000 字符帧名，HTML 478,485 B；浏览器 DOM 1,016,652 B，截图 107,117 B。

### 2.4 meta 边界

- `meta-missing-fields`：缺 duration/coverage 等字段时用零值渲染；报告不补造覆盖率，`unknown_pct` 保持 0 但仍显示 `[unknown]` 帧计数。
- `meta-samples-mismatch`：meta samples 与 folded 合计不一致时，以 folded 为绘图分母，warning 明确写出两个数；不静默改 meta。
- `meta-negative-samples` / `meta-duration-string` / 错 schema / 坏 JSON：直接失败，不降级成“0 样本成功”。

## 3. 离线依赖与 XSS

结果文件：`results/final-static.json`。

| 检查 | 结果 |
|---|---|
| `script_src` | 0 |
| `link` / `img` / `iframe` / `@import` | 0 / 0 / 0 / 0 |
| HTTP URL | 0 |
| `fetch` / `XHR` / WebSocket / EventSource | 0 / 0 / 0 / 0 |
| `json_embed_ok` | true（所有生成用例） |
| Node 18 语法检查 | rc=0 |
| Node 12 语法检查 | rc=0 |

XSS 用例把以下内容作为帧名/标题：

~~~text
<img src=x onerror=alert(1)>
</script><script>alert(1)</script>
quote"frame'back\slash
中文帧😀
__TITLE__ / __BODY_CLASS__ / x__DATA__x
~~~

结果：

- DOM 中 `img=0`、`script=1`（仅报告自己的脚本）；
- `has_literal_script_close=false`、`has_script_open=false`；
- payload 作为文本/数据属性存在，`payload_present=true`；
- 标题渲染为 `&lt;img src=x onerror=alert(4)&gt;`；
- 没有新增 `<img>`，没有第二个 `<script>`。

结论：当前模板对帧名/标题的 HTML 转义和 JSON 嵌入通过 XSS 用例；这不是对所有未来字段的证明，新增字段仍需同样检查。

## 4. 浏览器 DOM、截图与交互

结果文件：`results/final-browser.json`、`results/final-dom-checks.json`、`results/interaction-probe.json`。
浏览器：本机 Chrome headless；截图尺寸以 full-page 为准。

| 用例 | DOM bytes | SVG | g | rect | 截图 bytes | 结论 |
|---|---:|---:|---:|---:|---:|---|
| `normal` | 262,738 | 1 | 72 | 73 | 178,440 | 火焰图/搜索/重置/TopNPU/类别正常 |
| `xss` | 234,332 | 1 | 10 | 11 | 126,635 | 恶意名称只作文本，`img=0` |
| `empty` | 228,123 | 0 | 0 | 0 | 112,267 | `has_status_unavailable=true`，不画空图 |
| `deep-5000` | 276,802 | 1 | 130 | 131 | 131,395 | `has_truncated=true` |
| `meta-missing-fields` | 230,456 | 1 | 4 | 5 | 103,910 | 缺省字段可渲染 |
| `large` | 1,745,011 | 1 | 4,082 | 4,083 | 140,138 | 4,082 节点，`truncated=true` |
| `final4-classified` | 389,485 | 1 | 381 | 382 | 173,051 | `has_npu461=true`、`has_trs_ioctl=true`、`has_devmm=true` |

交互：

| 用例 | 初始 SVG 节点 | 搜索命中 | 搜索变暗 | 点击缩放后节点 | 重置后节点 | 类别关闭变暗 |
|---|---:|---:|---:|---:|---:|---:|
| `normal` | 72 | 5 | 67 | 3 | 72 | 41 |
| `final4-classified` | 381 | 12 | 369 | 2 | 381 | 191 |

结论：搜索高亮、缩放、重置、类别图例过滤都实际改变 DOM/SVG；不是静态图片。

## 5. final4 分类数据核对

结果文件：`results/final4-classified-stats.json`、`results/final4-compare.json`、`results/final4-speed.json`。

| 指标 | 值 |
|---|---:|
| samples | 1,488 |
| unique_stacks | 221 |
| stack_fail | 6 |
| duration_s | 10.034801594 |
| frequency_hz | 99 |
| total_frames | 13,658 |
| npu_frame_pct | 4.612681212476204 |
| npu_frames | 630 |
| category self sum | 1,488 |
| top_npu_count | 40 |

类别 self（分母 1,488）：

| 类别 | samples | pct |
|---|---:|---:|
| other | 1,016 | 68.28% |
| sync | 344 | 23.12% |
| sched | 56 | 3.76% |
| npu | 41 | 2.76% |
| vfs | 17 | 1.14% |
| memory | 12 | 0.81% |
| net | 2 | 0.13% |
| dma | 0 | 0.00% |

Top NPU（本数据无递归，samples 也等于出现的采样栈数）：

| 函数 | samples | 占 1,488 |
|---|---:|---:|
| `drv_ascend_intf_ioctl` | 101 | 6.79% |
| `trs_ioctl` | 80 | 5.38% |
| `trs_id_pool_get` | 36 | 2.42% |
| `trs_res_id_alloc` | 35 | 2.35% |
| `trs_res_id_free` | 29 | 1.95% |
| `trs_id_alloc_ex` | 28 | 1.88% |
| `devmm_svm_ioctl` | 18 | 1.21% |
| `trs_res_mng_id_free` | 18 | 1.21% |
| `devmm_dispatch_ioctl_use_svm_proc` | 17 | 1.14% |
| `trs_id_free_ex` | 17 | 1.14% |

旧/新口径对比（同一份 final4 原始 folded/meta）：

| 口径 | NPU 帧占比 | NPU 类别 self | Top NPU 数 | 结论 |
|---|---:|---:|---:|---|
| 旧硬编码分类 | 0.8566% | 3（0.20%） | 14 | 已废弃 |
| 新共享分类 | 4.6127% | 41（2.76%） | 40 | 最终交付口径 |

报告生成速度：`final4-classified` 三次 65/53/52 ms，HTML 253,069 B；不是慢路径。

注意：

- 4.61% 是 NPU 类帧的加权帧占比（630/13,658），不是 NPU 利用率、算子耗时或 CPU 占用。
- 6.79% 是 `drv_ascend_intf_ioctl` 的样本数/1,488；不是 NPU 总占比。
- 0.86% / 0.9917% / 8.10% 只能作历史对照，不能与 4.61% 混写。

## 6. 已知问题

### P0

| 编号 | 问题 | QA 影响 |
|---|---|---|
| P0-1 | coverage=0 仍 `available` | 现有 24 用例没有“全 unknown 但非空”的独立断言；final4 coverage=1.0 不受影响。验收必须查 meta |
| P0-2 | `stacks` samples=0 仍返回 0 | 本次 QA 只测 `report`；CLI 空采集退出码仍需单独脚本检查 |
| P0-3 | `lost` 未覆盖 map 满 | final4 无 map 满；QA 不能证明通用无丢样 |

### P1

| 编号 | 问题 | QA 影响 |
|---|---|---|
| P1-1 | `meta-missing-fields` 时 `unknown_pct=0` 可能被误读 | 报告仍显示 `[unknown]` 帧计数；验收应看 folded/meta 原值 |
| P1-2 | `only-folded` 失败信息是“缺 meta” | 与文档“可传 folded”需配套说明 |
| P1-3 | `long-frame` HTML 479 KB / DOM 1 MB | 极端帧名会放大报告；正常内核符号远低于此 |
| P1-4 | 深栈/大输入显示 `[truncated]` | 计数保留，但调用链细节被合并；报告已 warning |

### P2

- `empty_note` 在部分非空用例中也为 true（模板中的空状态提示节点存在），不影响火焰图；下一版可只在真正空数据时渲染。
- 浏览器截图以 full-page 为准；早期 `report-stacks-final4-topnpu-1600x1200.png` 有裁切/全黑，不要使用。
- 报告没有自动化无障碍/移动端检查；当前只验证桌面 Chrome。

## 7. 结论

**PASS（报告层）。**

- 正常、空、缺文件、非法 count、坏 meta、超深栈、大输入、重复栈、XSS 都有确定行为；
- 非法输入非零退出且不生成误导性 HTML；
- 报告离线、无外部依赖，XSS 转义通过现有用例；
- final4-classified 的 4.61%、`trs_ioctl`、`devmm_*` 在 DOM 中可见；
- P0-1/P0-2/P0-3 是代码层通用缺口，本次 final4 证据不受影响，但验收不能只看 `flamegraph=available` 或 `stacks` 退出码。
