# B1 准备：把 msPTI collector 接入 V4

状态（2026-11-27 更新）：**尝试激活失败，已回滚**。V4 已恢复正常。
失败现象：env-file LD_PRELOAD 注入后容器入口 `bash`、以及 vLLM 的 `python3` 进程均 SIGSEGV；
加 `CANN_MSPTI_ONLY_COMM=vllm,python` 只解决了 shell，python 仍崩溃。当前 collector 不能直接
LD_PRELOAD 进 V4 运行时；需要入口 wrapper 只包住 vLLM 主进程或修复 collector 初始化。详见
`docs/B-STATUS.md` 的窗口结果。

## 1. 原理

V4 由 `/usr/local/libexec/run_vllm_ds4_8905.sh` 用 `docker run --env-file /etc/vllm-ds4.env`
启动。把以下变量加入该 env-file，容器内所有继承进程会 `LD_PRELOAD` 我们的 collector：

```text
LD_PRELOAD=/cache/cann-ebpf/libcannmspti.so
CANN_MSPTI_ACTIVITIES=api,acl_api,runtime_api,kernel,memcpy,hccl
CANN_MSPTI_OUTPUT=/cache/cann-ebpf/device-{pid}.jsonl
CANN_MSPTI_RUN_ID=vllm-collector
CANN_MSPTI_FLUSH_INTERVAL_MS=1000
```

`/data/cache` 已挂到容器 `/cache`；`{pid}` 由 collector 构造函数替换，每个 Worker 写独立文件，
避免多进程写同一路径。

## 2. 准备（不停机）

```bash
# 只拷贝 lib + 写 /etc/cann-ebpf/collector.env，不改 V4 env
sh deploy/b1/stage-collector.sh --stage --lib /tmp/cann-mspti-final-src/build/libcannmspti.so

# 查看将追加的 block
sh deploy/b1/stage-collector.sh --dry-run
```

`--stage` 已完成时：

- `/data/cache/cann-ebpf/libcannmspti.so`
- `/etc/cann-ebpf/collector.env`

## 3. 激活（需要一次 V4 重启）

```bash
# 追加 marked block，并备份 /etc/vllm-ds4.env
sh deploy/b1/stage-collector.sh --apply

# 计划窗口内执行（本脚本不会自己重启）
systemctl restart vllm-ds4-8905.service
```

重启后验证：

```bash
# 容器内应出现多个 device-<pid>.jsonl
ls -l /data/cache/cann-ebpf/device-*.jsonl

# 合并后导入 host 证据
python3 scripts/b1_merge_device.py --input-dir /data/cache/cann-ebpf --out /data/cache/cann-ebpf/merged
./cann-ebpf attach --pid auto --profile vllm-ascend --profiles-dir profiles \
  --duration 10s --device-jsonl /data/cache/cann-ebpf/merged/device.jsonl \
  --output runs/b1-real
./cann-ebpf diagnose --input runs/b1-real --json --out runs/b1-real/diagnose.json
```

## 4. 回滚

```bash
# 先移除 env block（下次重启生效）
sh deploy/b1/rollback-collector.sh --apply

systemctl restart vllm-ds4-8905.service
# 可选：清掉缓存 lib
sh deploy/b1/rollback-collector.sh --apply --purge-cache
```

回滚会删除 marked block 并保留带时间戳的备份。

## 5. 风险与边界

- **必须重启 V4 才生效**；本准备包不会自动重启。
- LD_PRELOAD 会进入容器内所有进程；collector 每个进程都会启动并写自己的文件。
  这会产生多个文件和额外开销，建议只用于验证窗口，验证后回滚。
- 每个文件有自己的 meta；`b1_merge_device.py` 合并 JSONL 并生成一个汇总 meta。
- collector 必须在 ACL 初始化前加载；重启方式满足，运行中 `gdb`/`dlopen` 注入不支持。
- 真实 HCCL 的 `Communication` correlationId 仍需 B4 单独验证；`msptiActivityHccl` 本身无 correlationId。
- 本包不修改 CANN 库/驱动，只把 lib 放到 `/data/cache`（容器内 `/cache`）。
