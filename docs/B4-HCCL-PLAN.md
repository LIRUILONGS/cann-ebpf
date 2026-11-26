# B4 真实 HCCL correlationId 方案（未执行）

状态：**方案与检查清单，未在共享卡上执行**。当前 V4 占用 8 卡、HBM 余量 ~6.3%，在共享卡上跑
HCCL 有 OOM/影响线上请求风险。

## 1. 目标

验证两点：

1. 真实 HCCL 集合通信（AllReduce/AllGather 等）能否被 msPTI 记录为设备区间；
2. `correlationId` 是否可用：预期 `msptiActivityHccl` 没有 correlationId，只有
   `Communication` 类活动有；没有时只能按时间窗/通信域降级，不能伪造精确关联。

## 2. 环境要求

- **独占卡**：2 或 8 张未被 V4 使用的 NPU，或独立测试节点；
- CANN 9.0.1 + msPTI 头文件/库；
- collector 以 `LD_PRELOAD` 在 HCCL 测试进程启动前加载；
- 预留足够 HBM（建议 ≥16GB/卡空闲）。

## 3. 路线 A：CANN `hccl_test`

容器内有源码 `/usr/local/Ascend/ascend-toolkit/latest/tools/hccl_test`：

```bash
cd /usr/local/Ascend/ascend-toolkit/latest/tools/hccl_test
make -j4          # 产物在 opbase_test/ 或当前目录，按 README
# 2 rank 小消息，独占卡时执行
LD_PRELOAD=/opt/cann-ebpf/libcannmspti.so \
CANN_MSPTI_ACTIVITIES=api,acl_api,runtime_api,kernel,memcpy,hccl \
CANN_MSPTI_OUTPUT=/tmp/b4/device-{pid}.jsonl \
./hccl_test -p 2 -b 8K -e 8M -f 1
```

## 4. 路线 B：torch_npu + torch.distributed（更易造数）

```python
# /tmp/hccl_allreduce.py
import os, torch, torch.distributed as dist
dist.init_process_group("hccl")
x = torch.ones(4096, device="npu")
for _ in range(20):
    dist.all_reduce(x)
dist.barrier()
print("rank", dist.get_rank(), "ok", float(x[0]))
```

```bash
LD_PRELOAD=... CANN_MSPTI_OUTPUT=/tmp/b4/device-{pid}.jsonl \
torchrun --nproc_per_node=2 /tmp/hccl_allreduce.py
```

## 5. 验收

- `device.jsonl` 中出现 HCCL/Communication 记录；
- 统计 `kind` 分布，确认是否含 `correlation_id` 字段；
- 对无 correlationId 的记录，diagnose 必须标注时间窗/通信域降级，不得输出精确函数级结论；
- 记录测试卡数、消息大小、迭代次数、耗时和 HBM 峰值。

## 6. 为什么现在不做

- 共享卡上 HCCL 会与 V4 争 HBM/通信带宽，可能造成线上请求失败；
- 构建 `hccl_test` 需要额外依赖（如 MPI）且当前无独占卡；
- 等有备用节点/独占卡或维护窗口时，与 B1 一起执行。
