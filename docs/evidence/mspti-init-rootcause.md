# msPTI collector SIGSEGV 根因（2026-11-27）

## 结论

1. **CANN msPTI 没有 `msptiInitialize()` / `msptiFinalize()`。** 9.2.0 容器实测：
   - `/usr/local/Ascend/cann-9.2.0/tools/mspti/include/mspti.h` 只 include
     activity/result/callback/cbid，没有 init/finalize 声明；
   - `nm -D libmspti.so` 的 29 个 `mspti*` 导出只有 `msptiSubscribe` /
     `msptiUnsubscribe`，无 `*Initialize` / `*Finalize`；
   - `mspti_result.h` 无 ALREADY_INITIALIZED，只有 `NOT_INITIALIZED = 7`、
     `INVALID_KIND = 8`、`PARAMETER_SIZE_NOT_SUFFICIENT = 9`。
   - 官方 sample 生命周期：`aclInit`/`aclrtSetDevice`/context/stream →
     `msptiSubscribe` → `msptiActivityRegisterCallbacks` → `FlushPeriod`；
     收尾 `msptiUnsubscribe` + `msptiActivityFlushAll`。msPTI 的“初始化”就是
     LD_PRELOAD + `msptiSubscribe` 隐式完成。

2. **真实根因是 C++ 静态初始化顺序，与 msPTI 无关。**
   `CannMsptiAutoStart` 原来是 `__attribute__((constructor))`，GCC 把它排在
   `.init_array` 中 `_GLOBAL__sub_I_collector.cpp` 之前：

   ```
   .init_array: 0x4150, CannMsptiAutoStart(0x3a74), _GLOBAL__sub_I(0x3ed0), 0x4050
   ```

   于是 `cann_mspti_start` 在 `g_output_path` / `g_run_id` / `g_activity_spec`
   等 `std::string` 全局**尚未构造**时运行（内存全 0，`_M_p == nullptr`）。
   `g_output_path = output_path` 因非空会重新分配而侥幸通过；紧接着
   `g_run_id = ""` 走 `_M_set_length` 往 `nullptr[0]` 写终止符 →
   **SIGSEGV，进程在 main 之前就死了**。这解释了 bash 和 python3 都是 139、
   以及 `CANN_MSPTI_ONLY_COMM` 只“修好” shell（只是让构造器提前 return）。

3. 修复：去掉 constructor/destructor 属性，改为文件末尾声明 namespace 作用域的
   `AutoLifecycle` 对象（源码顺序在所有全局之后 → TU 内有序初始化保证全局先构造、
   后析构）。另给 `msptiSubscribe` 失败码加 2/6/7 提示。

## 复现证据（临时容器，无 NPU、无负载）

9.2.0 镜像 `quay.nju.edu.cn/ascend/vllm-ascend:v0.30.0rc2`，源码挂载
`/tmp/cann-mspti-build-src`。

| 场景 | 修复前 | 修复后 |
|---|---|---|
| `LD_PRELOAD=libmspti.so:libcannmspti.so` + `CANN_MSPTI_OUTPUT` 跑 bash | exit 139，无 jsonl/meta | exit 0，jsonl+meta 正常 |
| 同上跑 `python3 -c print` | exit 139 | exit 0 |
| 只 preload collector（libmspti 走 DT_NEEDED） | exit 139 | exit 0 |
| 不设 `CANN_MSPTI_OUTPUT`，或 `ONLY_COMM=nomatch` | exit 0 | exit 0 |
| python ctypes dlopen 后 start/stop/再次 start | — | rc 0/0/0 |
| `cann-mspti-selftest`（9.2.0 / 9.0.1） | passed | passed |

探针标记精确停在 `g_run_id` 赋值前后（`PROBE Q1-outpath-assigned` 后崩溃，
`Q2-runid-assigned` 未打印）。

## 构建/自测结果

- 9.2.0（`vllm-ascend:v0.30.0rc2`）：`cmake -DBUILD_SYNTHETIC_ACL=OFF .. && make -j4`
  通过，`./cann-mspti-selftest` 全部 ok；`sha256([libcannmspti.so]) =
  d35067a0c8482c70d7a44f710d538708ac46af03f9b753e6f3602bcc0b947c74`。
- 9.0.1（`ds4/vllm-ascend:v0.23.0rc1`，V4 基线环境）：同样构建+selftest 通过，
  LD_PRELOAD bash/python 均 exit 0；`sha256 = a5678900eefb325190b963bcbb84d9e09f953742bdfe6788b3181061810b233e`。
- `cann-mspti-collector --exec /bin/true` CLI 路径正常。

## 仍需验证

1. 真实 torch_npu / vLLM 进程（单进程、维护窗口内）LD_PRELOAD 或启动后 dlopen，
   确认 ACL 初始化后的 ACTIVITY（API/KERNEL/MEMCPY/HCCL）真的产出记录——本轮
   按约束未跑 NPU 负载，只有空 jsonl + 正常 meta。
2. 若目标进程里已有其他 msPTI 订阅者（ms_service_profiler / vLLM mspti.mode），
   `msptiSubscribe` 会返回 2；新 hint 已提示，但仍需确认产品侧是否接受“采集放弃”。
3. 多进程 `{pid}` 输出、真实 HCCL 的 `Communication` correlationId。
