# 最终测试与构建验收（2026-11-28）

## 验收结论

**PASS。** `gofmt`、`go test`、`go vet`、linux/arm64 冷构建以及两份真实 metrics 数据的 HTML 报告生成全部成功，无失败项。本次验收未修改业务代码，未提交 git，未重启或访问服务器。

- 仓库：`C:\Users\liruilong\pentagi\cann-ebpf`
- 分支：`main`
- HEAD：`540d88c5a23eaddac8448dcd2c557cf143b0f393`（短 SHA `540d88c`）
- WSL Go：`go1.24.11 linux/amd64`
- 目标平台：`linux/arm64`
- 验收时间：2026-11-28 03:07（Asia/Shanghai）
- 工作区初始/结束状态：仅有两个既有未跟踪备份文件 `cmd/cann-ebpf/attach.go.orig`、`internal/hardware/npusmi.go.orig`；未改动、未加入暂存区。

## 1. 格式、测试与静态检查

所有命令均从 WSL 中的仓库根目录 `/mnt/c/Users/liruilong/pentagi/cann-ebpf` 执行。

| 检查项 | 命令 | 退出码 | 结果 |
|---|---|---:|---|
| 格式检查 | `gofmt -l cmd internal m3` | 0 | 无输出，未发现需要格式化的文件 |
| 单元测试 | `/tmp/go1.24/bin/go test -count=1 ./...` | 0 | 21 个包通过；其中 `cmd/cpy-batch-poc`、`cmd/cpy-poc`、`schema` 无测试文件 |
| 静态检查 | `/tmp/go1.24/bin/go vet ./...` | 0 | 无输出，未发现问题 |

`go test` 实际通过的核心包包括：`cmd/cann-ebpf`、`internal/config`、`internal/contextfeed`、`internal/device`、`internal/diagnose`、`internal/discovery`、`internal/elfutil`、`internal/export`、`internal/hardware`、`internal/metrics`、`internal/preflight`、`internal/probes`、`internal/report`、`internal/runlog`、`internal/stacks`、`internal/tracectx`、`internal/tracefs`、`poc/cpython`。

## 2. linux/arm64 冷构建

执行命令：

```bash
cd /mnt/c/Users/liruilong/pentagi/cann-ebpf
mkdir -p /tmp/cann-ebpf-accept-20261128
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 /tmp/go1.24/bin/go build \
  -buildvcs=false -trimpath -ldflags '-s -w' \
  -o /tmp/cann-ebpf-accept-20261128/cann-ebpf-linux-arm64 \
  ./cmd/cann-ebpf
```

- 退出码：`0`
- 产物：`/tmp/cann-ebpf-accept-20261128/cann-ebpf-linux-arm64`
- 大小：`11,403,448` bytes
- SHA256：`e5a5f6f1fd1d94856e872e0b59c85a5ae933725368ec0da25435705290e6c556`
- `file`：`ELF 64-bit LSB executable, ARM aarch64, version 1 (SYSV), statically linked, BuildID[sha1]=db54d4d3e4b62732bbc0cfc88bebdc1ef90d180a, stripped`

同时核验了已有交付二进制：

| 文件 | 大小 | SHA256 | `file` 结论 |
|---|---:|---|---|
| `dist/cann-ebpf-linux-arm64-driverfix-20261128` | 11,403,448 bytes | `16d1c2fc11c5eaa1b6d475805ce2db88aba32f5a52bf8a1a3873fd4714596d3a` | ELF 64-bit ARM aarch64，静态链接，stripped |
| `dist/cann-ebpf-linux-arm64-3f517b7` | 16,058,348 bytes | `4b6afda0e1f60e351e4cf2c809b2e0092d8f6f668e27e995da2ce0158a956bcd` | ELF 64-bit ARM aarch64，静态链接，含 debug_info |

## 3. 真实 metrics 数据生成 HTML

两份输入均为既有真实采集数据；报告命令使用新构建的二进制，输出到 `/tmp/cann-ebpf-accept-20261128/`，未写入仓库。为避免默认 `--max-series 256` 截断实际 series，按数据规模分别设置为 2000/5000。

### 3.1 全量 DCMI/HCCS/Orinfer 指标

命令：

```bash
/tmp/cann-ebpf-accept-20261128/cann-ebpf-linux-arm64 report \
  --metrics .analysis/metrics-full-20261127T184802Z \
  --max-series 2000 \
  --out /tmp/cann-ebpf-accept-20261128/full-report.html
```

- 退出码：`0`
- 命令输出：`report out=/tmp/cann-ebpf-accept-20261128/full-report.html spans=0 device_intervals=0 metrics_series=1429 flamegraph=none bytes=1943032`
- 输入：`.analysis/metrics-full-20261127T184802Z/metrics.jsonl`
- 输入大小：`11,907,362` bytes
- 输入 SHA256：`2a635ac7c0d9f98bb1cb285f5184278b948d75358f07d52e1201218124228723`
- 输出大小：`1,943,032` bytes
- 输出 SHA256：`4ab54d91b99bb64cdadfcd20a293211ff346b435e4f6fd8124abdda2d0b7a2a9`
- HTML 结构：`<!doctype html>`；`<html` 1 个；`</html>` 1 个；`<canvas` 11 个

### 3.2 驱动层真机验证指标

命令：

```bash
/tmp/cann-ebpf-accept-20261128/cann-ebpf-linux-arm64 report \
  --metrics .analysis/driver-fix-20261128T190100Z \
  --max-series 5000 \
  --out /tmp/cann-ebpf-accept-20261128/driver-report.html
```

- 退出码：`0`
- 命令输出：`report out=/tmp/cann-ebpf-accept-20261128/driver-report.html spans=0 device_intervals=0 metrics_series=4599 flamegraph=none bytes=6314551`
- 输入：`.analysis/driver-fix-20261128T190100Z/metrics.jsonl`
- 输入大小：`15,660,250` bytes
- 输入 SHA256：`a46f1829a90c5700144b4bcd4097101b429478a9db43d7c3b7e735e288a0be8c`
- 输出大小：`6,314,551` bytes
- 输出 SHA256：`1e30fa122d3b911c8a4a94df25340fcd6ab93bd3ae7e7e1289ab4a1b90885b39`
- HTML 结构：`<!doctype html>`；`<html` 1 个；`</html>` 1 个；`<canvas` 11 个

## 4. 失败项与边界

- 失败项：无。
- `go test`、`go vet`、构建及两次报告命令均以退出码 0 结束。
- 两个 metrics 输入均没有 `spans.jsonl`，因此报告正确显示 `spans=0 device_intervals=0 flamegraph=none`。这是 metrics-only 数据源的预期边界，不是命令失败。
- 本次没有执行服务器连接、采集、压测、重启或 Git 提交；`docs/VERIFICATION-20261128.md` 是本次唯一新增的验收文件。

## 5. 2026-11-28 最终真机采集与 UI 验收

在目标机 `bms-84865758`（`36.137.90.82`）上使用最终 arm64 二进制做了一次 44.80 秒只读采集。没有发压测请求，没有重启 Orinfer、vLLM 或 LiteLLM。

- 二进制：`dist/cann-ebpf-linux-arm64-final-20261128`，11,403,448 bytes，SHA256 `405fe87f0274a6eba22d0bca35f69b29418121a1059de54b02bb5577fe3eb18b`
- DCMI helper：目标机 `dcmi_probe_ext`；旧 helper 只输出 7 个基础指标，最终采集使用新版扩展 helper，输出 38 个 DCMI 指标
- 采集命令：`sample --pid 2796550 --system --dcmi-ext --hccs --driver --prometheus orinfer=http://127.0.0.1:8000/metrics --interval 1s --duration 45s`
- 窗口：44.80 秒
- 原始点：213,261
- meta series：7,063
- 报告投影 series：10,859（含 histogram 分位展开）
- 矩阵组：186
- 来源：`dcmi` 38 个指标、`driver-ebpf` 28、`npu-smi` 9、`proc` 16、`prometheus:orinfer` 17、`system` 51
- 覆盖：98.3%
- `metrics.jsonl`：105,344,565 bytes，SHA256 `b3d32cfe7fb7b82dbebe6997a4547a01c0187d0e0627673f09c941a0f25247a1`
- `report.html`：16,593,142 bytes，SHA256 `d79c331359792132a7897101d45c74405a572aa3b05f6c8e349bb15c8dc95219`
- 本地证据：`.analysis/final-live-20261128T1929Z/`

驱动层最终快照：

- `npu_driver_source_info`：`status=degraded`，24 probes attached，9 failed，ioctl tracepoints=true，mmap tracepoint=true。
- 最后一次 `npu_driver_metric_status` 快照：54 个 `available`、26 个 `unavailable`。
- `devmm_svm_ioctl`、`devmm_svm_mmap` 因同名模块符号为 `ambiguous_kernel_symbol` 被跳过，不猜模块、不补零。
- `xsmem_*`、`devmm_svm_munmap` 等缺失符号为 `symbol_not_found`；`npu_hdc_alloc_bytes` 为 `size_signature_unverified`；部分 DMA 探针为 `kprobe_attach_failed`。

报告 UI 验收修复：

- 驱动组默认只画变化最大的 12 条 series，并提供“显示全部 N 条”按钮；原始 series 不删除。
- driver 图例包含 handler、kind、cmd、tid，避免大 series 组显示成同名图例。
- 默认显示全部指标组，不再用“只看有变化”隐藏 health/ECC/HCCS 异常项。
- DCMI 与 npu-smi 页签分离为 `DCMI`、`NPU-smi`。
- metrics-only 首屏显示报告模式、series 数、采样窗口，不再显示误导性的 `0/0/0`。
- metrics-only 隐藏无数据的延迟 CDF 面板，并补上“指标矩阵”页内导航。

最终截图：

- `.analysis/final-live-20261128T1929Z/report-top.png`
- `.analysis/final-live-20261128T1929Z/report-driver-detail.png`
- `.analysis/final-live-20261128T1929Z/report-hccs-detail.png`
- `.analysis/final-live-20261128T1929Z/report-ecc-detail.png`
