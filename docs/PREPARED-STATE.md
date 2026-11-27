# 已准备好的执行件（2026-11-27）

状态：V4.1 正在运行，以下**未执行**；只准备好，等窗口/许可。

## 1. 修复后的 collector

| 环境 | 路径 | SHA-256 |
|---|---|---|
| CANN 9.0.1（V4） | `/opt/cann-ebpf/collectors/9.0.1/libcannmspti.so` | `bd94de8574dfb949fbabb60e7c254ad05e7dd5817ec90904a8baee798c3fb621` |
| CANN 9.2（Orinfer） | `/opt/cann-ebpf/collectors/9.2/libcannmspti.so` | `a449bdd93fc85ff6dc36b266f27d154b2c65b003132a3b4dfc2c594be1b9d77e` |

- 两版均从当前源码（`AutoLifecycle` 修复）构建；
- `cann-mspti-selftest` 10/10 通过；
- `LD_PRELOAD` bash/python 不再崩溃；
- 真实 `torch_npu` 单进程采到 109 API + 1 MEMCPY（退出析构仍崩，已记录）。

## 2. B1 已验证的 stage 状态

- `/data/cache/cann-ebpf/libcannmspti.so` 已替换为 9.0.1 修复版；
- `/etc/cann-ebpf/collector.env` 已写（LD_PRELOAD 保留 jemalloc、mspti、collector，`ONLY_COMM=vllm,python`）；
- **`/etc/vllm-ds4.env` 未追加 block**（`env_has_block=0`），V4.1 未受影响。

## 3. B1 执行（需要先停 V4.1）

```bash
# 1) 停 V4.1（8 卡独占）
systemctl stop orinfer-v41.service
# 2) 执行准备好的验证；脚本会 stage env、启动 V4、等 health+device、merge、attach、diagnose
sh /opt/cann-ebpf/validate-v4.sh --apply --duration 30s
# 3) 回滚 collector env
sh /tmp/cann-ebpf-src/deploy/b1/rollback-collector.sh --apply
```

脚本：`deploy/b1/validate-v4.sh`（仓库）/ `/tmp/validate-v4.sh`（服务器副本）。
预检：V4.1 必须 inactive；V4 必须 inactive；否则脚本拒绝。

## 4. B4 执行（需要先停 V4.1）

```bash
systemctl stop orinfer-v41.service
docker start orinfer-cann920
docker exec -u root orinfer-cann920 sh -c 'grep -q target-host /etc/hosts || echo "127.0.0.1 target-host" >> /etc/hosts'
docker cp /opt/cann-ebpf/collectors/9.2/libcannmspti.so \
  orinfer-cann920:/tmp/b4/libcannmspti92_fixed.so
# 在容器内执行 hccl_allreduce.py（脚本已含 set_device）；env:
#   LD_PRELOAD=/usr/local/Ascend/cann-9.2.0/tools/mspti/lib64/libmspti.so:/tmp/b4/libcannmspti92_fixed.so
#   CANN_MSPTI_OUTPUT=/tmp/b4/device-{pid}.jsonl
#   CANN_MSPTI_ONLY_COMM=python
#   ASCEND_RT_VISIBLE_DEVICES=0,1
docker cp orinfer-cann920:/tmp/b4 /tmp/b4-final
```

2-rank HCCL 已验证可跑（sum=32）；用修复后的 collector 重跑可检查 device records 中的
HCCL/Communication 与 correlationId。

## 5. 仍未解决

- collector 退出析构 SIGSEGV：真实服务停止时可能 exit 139；数据已周期落盘；
- 真实 V4/vLLM 进程下的 API/KERNEL/MEMCPY/HCCL 记录尚未验证；
- 若 msPTI 已有其他订阅者，`msptiSubscribe` 返回 2，当前只提示并禁用采集。
