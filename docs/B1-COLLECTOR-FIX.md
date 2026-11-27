# B1 collector 崩溃根因与修复（2026-11-27）

状态：**根因已定位并修复；真实 torch_npu 进程已能采到 API/MEMCPY 记录；进程退出析构仍会崩溃，待修。**

## 1. 根因

不是 msPTI API 缺失（CANN 9.2 根本没有 `msptiInitialize/msptiFinalize`），也不是 vLLM/Orinfer 引擎问题。

`CannMsptiAutoStart` 使用了 `__attribute__((constructor))`，GCC 将其放入 `.init_array`，顺序在
`_GLOBAL__sub_I_collector.cpp` **之前**：

```text
init_array: 0x4150, CannMsptiAutoStart(0x3a74), _GLOBAL__sub_I(0x3ed0), 0x4050
```

因此 `cann_mspti_start()` 在 `g_output_path`、`g_run_id` 等 `std::string` 全局**尚未构造**时执行。
`g_run_id = ""` 会通过未初始化的 `_M_p == nullptr` 写终止符，导致 main 之前 SIGSEGV。
这解释了 bash 和 python3 都 139、`CANN_MSPTI_ONLY_COMM` 只能救 shell（只是提前 return）。

## 2. 修复

`m3/mspti_collector/collector.cpp`：

- 去掉 `__attribute__((constructor/destructor))`；
- 在文件末尾、所有全局之后声明 `AutoLifecycle g_auto_lifecycle;`
  - 构造：调用 `CannMsptiAutoStart()`
  - 析构：调用 `CannMsptiAutoStop()`
- TU 内有保证：全局先构造、后析构；仍在 `main` 前开始订阅；
- `msptiSubscribe` 失败码 2/6/7 增加提示（已有订阅者 / LD_PRELOAD 顺序 / 未初始化）。

## 3. 验证

| 验证 | 结果 |
|---|---|
| CANN 9.2 容器构建 + `cann-mspti-selftest` | 通过 10/10 |
| CANN 9.0.1 构建 + selftest | 通过 |
| 修复前 `LD_PRELOAD` + bash/python | exit 139，无文件 |
| 修复后 `LD_PRELOAD` + bash/python | **exit 0**，jsonl + meta 正常 |
| 真实 `torch_npu` Python（单卡、小 tensor） | **打印 `real npu ok 2.0`；采集到 109 API + 1 MEMCPY** |
| 同一进程退出 | **析构阶段 SIGSEGV（数据已落盘）** |

真实进程验证命令（在 `orinfer-cann920` 容器、device 0、V4.1 正在运行时执行，未停服务）：

```bash
LD_PRELOAD=/usr/local/Ascend/cann-9.2.0/tools/mspti/lib64/libmspti.so:/tmp/b4/libcannmspti92_fixed.so \
CANN_MSPTI_ACTIVITIES=api,acl_api,runtime_api,kernel,memcpy,hccl \
CANN_MSPTI_OUTPUT=/tmp/b4/real-{pid}.jsonl \
CANN_MSPTI_ONLY_COMM=python \
python3 -c 'import torch,torch_npu; torch.npu.set_device(0); x=torch.ones(256,device="npu"); y=x+1; torch.npu.synchronize(); print(float(y[0]))'
```

结果文件：`/tmp/b4/real-1205.jsonl`，kinds `API=109, MEMCPY=1`。

## 4. 仍未完成

1. **退出析构崩溃**：`~AutoLifecycle` 在 Python/torch 已销毁后调用 `cann_mspti_stop()`，
   会 SIGSEGV。需要改成更早的 `atexit`/信号处理，或禁用自动 stop 并让 meta 在进程存活时周期落盘。
2. **真实 V4/vLLM 进程**：尚未用修复后的 lib 启动 V4 验证（需要停 V4.1；当前 V4.1 有真实流量）。
3. **多进程 `{pid}` 与 HCCL correlationId**：未验证。
4. 若 msPTI 已有其他订阅者，`msptiSubscribe` 返回 2，当前只提示并放弃采集。

## 5. 结论

- collector 崩溃不是 vLLM/Orinfer 的 bug，是 **C++ 静态初始化顺序 bug**，已修复；
- 修复后在真实 `torch_npu` 进程可产出 API/MEMCPY 记录；
- 距离 B1 生产可用还差“退出析构”和“真实 V4 验证”。
