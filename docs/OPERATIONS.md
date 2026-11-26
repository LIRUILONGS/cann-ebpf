# 部署与运维（M6 骨架）

日期：2026-11-26
状态：安装/卸载/升级/打包脚本与 systemd 单元完成；**默认全部 dry-run**，需 `--apply` 才修改主机。

## 1. 安装布局

```text
/opt/cann-ebpf/
  bin/cann-ebpf
  capture-once.sh
  profiles/*.yaml
  configs/base.yaml
  docs/*.md
/etc/systemd/system/cann-ebpf.service
/etc/systemd/system/cann-ebpf.timer
/var/log/cann-ebpf/runs/ebpf-<UTC>/
```

## 2. 安装 / 卸载 / 升级

```bash
# 先看计划（不写磁盘、不 systemctl）
sh deploy/install.sh --dry-run --binary dist/cann-ebpf-linux-arm64-m3
sh deploy/install.sh --apply   --binary dist/cann-ebpf-linux-arm64-m3

sh deploy/uninstall.sh --dry-run --keep-data
sh deploy/uninstall.sh --apply --keep-data

sh deploy/upgrade.sh --dry-run --binary dist/cann-ebpf-linux-arm64-m3
sh deploy/upgrade.sh --apply   --binary dist/cann-ebpf-linux-arm64-m3
```

规则：

- 安装/升级会校验 `dist/SHA256SUMS.txt` 中的二进制 SHA-256；不一致返回 7；
- 升级失败自动回滚到 `/opt/cann-ebpf.bak.<UTC>`；
- 卸载默认删除 `/var/log/cann-ebpf`，`--keep-data` 保留证据；
- 需要 root；`--dry-run` 不需要。

## 3. systemd 定时采集

`cann-ebpf.timer` 每 5 分钟触发一次 `cann-ebpf.service`，执行 `capture-once.sh`：

- 每次新目录，不覆盖已有证据；
- 先跑 `preflight`，环境 fatal 直接失败；
- 默认 `--blocking on --hardware on --no-context --duration 10s`；
- 不修改目标进程，不重启服务。

```bash
systemctl status cann-ebpf.timer
systemctl list-timers cann-ebpf.timer
journalctl -u cann-ebpf.service -n 50
```

注意：定时采集会持续产生证据，当前没有自动清理；生产部署前应加保留策略（清理 N 天前 runs）。

## 4. 固定负载开销（A4）

`scripts/overhead_fixed_load.py` 生成固定 prompt/并发/max_tokens 的真实请求，同时运行
`attach --blocking off/on`，从 vLLM `/metrics` + agent `/proc` 计算吞吐、p50/p95、CPU、RSS、dropped。

```bash
# 只校验参数，不发请求
python3 scripts/overhead_fixed_load.py --dry-run \
  --prompt-file prompts.jsonl --binary dist/cann-ebpf-linux-arm64-m3 \
  --concurrency 4 --duration 60 --modes off,on --repeats 2

# 真实负载（必须在授权窗口，带 --yes）
python3 scripts/overhead_fixed_load.py --yes \
  --api-key "$VLLM_API_KEY" --prompt-file prompts.jsonl \
  --binary dist/cann-ebpf-linux-arm64-m3 \
  --concurrency 4 --duration 60 --modes off,on --repeats 3 \
  --enforce
```

`--enforce` 在吞吐下降 >3% 或客户端 p95 回归 >5% 时退出 2。
输出：`runs/overhead-fixed/overhead.csv` 与每轮 run 目录。

## 5. 发布包（A5）

```bash
sh scripts/make-release.sh --dry-run --binary dist/cann-ebpf-linux-arm64-m3
sh scripts/make-release.sh --apply   --binary dist/cann-ebpf-linux-arm64-m3 \
  --name cann-ebpf-0.6.0-m2-m3 --out dist/cann-ebpf-0.6.0-m2-m3.tar.gz
```

包内含二进制、profiles、configs、deploy、scripts、docs、adapters、m3 collector 源码与
`SHA256SUMS.txt`。`dist/` 不提交，交付以包和 SHA 为准。

## 6. 已知限制

- 定时采集没有保留/轮转策略；
- `capture-once.sh` 固定 `vllm-ascend` profile，Orinfer 需覆盖 `PROFILE` 环境变量；
- 服务单元是 oneshot 周期采集，不是常驻 daemon；常驻模式仍需后续实现；
- 采集仍需要 root/CAP；非 root 部署未验证。

## 7. 监控与 B1 准备

- Prometheus/Grafana：见 `docs/OBSERVABILITY.md`，资产在 `deploy/prometheus/`、`deploy/grafana/`、`deploy/cann-ebpf-metrics.service`。
- B1 collector 注入：见 `docs/B1-PREP.md`，脚本 `deploy/b1/stage-collector.sh` / `rollback-collector.sh`；当前只 stage，未改 V4 env、未重启。
- B4 HCCL：见 `docs/B4-HCCL-PLAN.md`，需独占卡/备用节点，未在共享卡执行。
