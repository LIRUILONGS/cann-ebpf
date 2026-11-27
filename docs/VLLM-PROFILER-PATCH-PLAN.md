# V4 vLLM-Ascend profiler 补丁准备方案（唯一一次维护窗口）

日期：2026-11-27
状态：**仅准备，未应用、未重启**。本文与 `patches/vllm-ascend-profiler/` 是窗口前的 staging 产物。
配套：`patches/vllm-ascend-profiler/{apply.sh,rollback.sh,BACKUP.md,tools/*}`、`docs/V4-SINGLE-WINDOW-PLAN.md`（窗口主流程）、`docs/OPS-FEASIBILITY.md`（FLOPs 来源与转换链路）。

执行边界（本轮严格遵守）：

- **未** stop/restart V4，**未** stop/start 任何生产容器，**未**发压测，**未**执行 profiler；
- **未**修改 `/usr/local/libexec/run_vllm_ds4_8905.sh`、systemd unit、`/etc/vllm-ds4.env` 或容器内任何文件；
- 只做 `docker inspect/ps`、`docker exec ... cat/ls/stat/grep`、`python --version/--help` 级别的只读检查；
- 只在 `/opt/cann-ebpf/patches/`、服务器 `/tmp`、仓库 `patches/`、`docs/` 新增文件；**未** `git add/commit`。

---

## 0. 结论先行

1. **补丁路径**：`patches/vllm-ascend-profiler/`（仓库 staging）；已同步到服务器
   `/opt/cann-ebpf/patches/vllm-ascend-profiler/`（`apply.sh` 默认 dry-run，已在真机跑通 preflight）。
2. **持久化结论（关键）**：`/vllm-workspace/vllm-ascend` **不是** bind mount，而是**镜像层内容**；
   且该服务 **每次启动都会 `docker rm -f` 重建容器**（`ExecStartPre` + `docker run --rm`）。
   因此**在容器内直接改文件 100% 会被下一次重启丢弃**。本方案改用
   **对单文件做只读 bind mount + 打补丁的启动脚本** 来持久化；只要
   `/opt/cann-ebpf/patches/vllm-ascend-profiler/torch_npu_profiler.py` 存在且启动脚本已被补丁替换，
   **重启后仍然生效**（包括镜像被 `ds4-image-build.service` 重建，因为 mount 在运行时施加）。
   若要求“不依赖宿主文件”，则必须重建镜像（见 §2 方案 B），本窗口不采用。
3. **还缺什么**：真实 NPU 采集（禁止，只能在窗口内做）；`ArithmeticUtilization` 在本 CANN 9.0.1 +
   torch_npu 2.10.0.post2 组合下是否真的产出 `cube_fops/vector_fops` 列（有代码依据、无实测样本）；
   `FULL_DECODE_ONLY` + MTP 下 profiler 是否与 ACL graph 冲突；8 rank 输出合并策略；
   `docs/OPS-FEASIBILITY.md` §6 G5 的 Roofline ridge 单位 bug 仍未修（不阻塞采集，阻塞 `bound` 结论）。

---

## 1. 只读检查结果（2026-11-27，root@target-ip）

### 1.1 容器与运行时

| 项 | 实测 |
|---|---|
| 主机 | `target-host`，`aarch64` |
| 容器 | `deepseek-v4-flash-8905`，`Up 4 hours (healthy)` |
| 镜像 | `ds4/vllm-ascend:v0.23.0rc1-nonroot` |
| 容器 Id | `85addc450322e1f096a4d90c27b396a0aea4f18c2450e04487dca2973870123a` |
| 运行用户 | `982:979`（`vllm-ds4`），非 root，**非** privileged |
| 网络 | `host` |
| Health | `--health-start-period=45m`，`curl /v1/models` |
| torch / torch_npu | `2.10.0+cpu` / `2.10.0.post2` |
| vllm-ascend | `0.23.0rc1`，`pip` 显示 **Editable project location: `/vllm-workspace/vllm-ascend`** |
| Docker | `28.5.2`；SELinux `Disabled` |

### 1.2 挂载（`docker inspect … '{{range .Mounts}}'`）

```
bind|/etc/ascend_install.info|/etc/ascend_install.info|RW=false
bind|/data/cache|/cache|RW=true
bind|/usr/local/dcmi|/usr/local/dcmi|RW=false
bind|/usr/local/Ascend/driver/tools/hccn_tool|...|RW=false
bind|/usr/local/Ascend/driver/version.info|...|RW=false
bind|/etc/hccn.conf|/etc/hccn.conf|RW=false
bind|/data/models/DeepSeek-V4-Flash-w8a8-mtp|/model|RW=false
bind|/data/log/ascend|/ascend/log|RW=true
bind|/usr/local/bin/npu-smi|/usr/local/bin/npu-smi|RW=false
bind|/usr/local/Ascend/driver/lib64|...|RW=false
```

**`/vllm-workspace` 不在其中** → 属于镜像层，容器内可见但不是宿主 bind mount。

### 1.3 容器每次重建（决定持久化方案）

`/etc/systemd/system/vllm-ds4-8905.service`：

```
ExecStartPre=-/usr/local/bin/docker rm -f deepseek-v4-flash-8905
ExecStart=/usr/local/libexec/run_vllm_ds4_8905.sh
ExecStop=-/usr/local/bin/docker stop --timeout 180 deepseek-v4-flash-8905
Requires=docker.service ds4-image-build.service ds4-model-verify.service
```

启动脚本 `/usr/local/libexec/run_vllm_ds4_8905.sh`（3019B，`root:root 0755`，
sha256 `2a0f71cf7832d50da306c2282047ea447bd68f764dd47dcacb3b9fd4d03aebfd`）用：

```
exec /usr/local/bin/docker run --rm \
  --name deepseek-v4-flash-8905 ... --user 982:979 --env-file /etc/vllm-ds4.env \
  ... --mount type=bind,src=/data/cache,dst=/cache ... \
  ds4/vllm-ascend:v0.23.0rc1-nonroot \
  vllm serve /model --port 8905 --tensor-parallel-size 8 ... (无 --profiler-config)
```

→ **每次 `systemctl start` 都是全新容器**：`docker rm -f` + `--rm`。容器可写层不保留。

### 1.4 目标模块

```
/vllm-workspace/vllm-ascend/vllm_ascend/profiler/torch_npu_profiler.py
  mode=644 uid=0 gid=0 size=3436
  sha256=0c9ffcaf969a9b1fcb44a51bdd92e69f763a7c5be64a8efbd498d6536b9e09e5
/vllm-workspace/vllm-ascend/vllm_ascend/profiler/__init__.py  (28 行内联)
```

`python3 -c "importlib.util.find_spec('vllm_ascend')"` →
`origin=/vllm-workspace/vllm-ascend/vllm_ascend/__init__.py`，
`submodule_search_locations=['/vllm-workspace/vllm-ascend/vllm_ascend']`。

该目录**没有** `__pycache__`，因此 bind mount 覆盖 `.py` 后不存在旧 `.pyc` 抢先生效的问题
（且目录 `root:755`，容器用户无法写 pyc，Python 会静默跳过缓存）。

### 1.5 上游缺口确认（只读读源码）

`torch_npu_profiler.py` 的 `_create_profiler`：

- `aic_metrics=torch_npu.profiler.AiCMetrics.PipeUtilization`（第 53 行）；
- `torch_npu.profiler.profile(...)` 只传了 `with_stack` / `profile_memory` / `with_modules`，
  **没有** `record_shapes` / `with_flops`（第 61–76 行）；
- 而 `ProfilerConfig` 确有这两个字段（`/vllm-workspace/vllm/vllm/config/profiler.py`）：
  `torch_profiler_record_shapes: bool = False`、`torch_profiler_with_flops: bool = False`、
  `torch_profiler_dir: str`、`torch_profiler_with_stack: bool = True` 等。

`torch_npu.profiler.profile` 的签名（容器内 `inspect.signature` 实测）**接受**
`record_shapes: bool = False` 与 `with_flops: bool = False`；
`AiCMetrics` 枚举含 `ArithmeticUtilization`（另有 `PipeUtilization/Memory/MemoryAccess/...`）；
`ProfilerLevel` 含 `Level1`；`ExportType` 含 `Text`。

CLI 侧：`vllm/engine/arg_utils.py:1494` 注册 `--profiler-config`（`get_field(VllmConfig,"profiler_config")`），
`vllm/utils/argparse_utils.py:405` 对以 `{`/`[` 开头的值做 `json.loads`，
所以 `--profiler-config '{"…"}'` 与现有 `--speculative-config` / `--additional-config` 走同一机制。

### 1.6 输出目录可写性

`/data/cache` 属主 `vllm-ds4:vllm-ds4`；容器内以 `982:979` 执行 `test -w /cache` → **可写**。
`/data/cache/vllm_profile` 目前**不存在**（首采会创建）。
→ profiler 输出写到 `/cache/vllm_profile`，落到宿主 `/data/cache/vllm_profile`，**重启不丢**，
且无需 `docker cp`（比 `docs/OPS-FEASIBILITY.md` §4.1 里的 `/tmp/vllm_profile` 更稳）。

---

## 2. 持久化结论与方案对比

| 方案 | 做法 | 重启后保留 | 代价/风险 | 采用 |
|---|---|---|---|---|
| A. 容器内直接改文件 | `docker exec … vi` / `docker cp` 进运行中容器 | **否**（`docker rm -f` + `--rm` 必丢） | 窗口内还会让“重启前的检查”和“重启后的实际代码”不一致 | ✗ |
| **B. 单文件只读 bind mount** | 打补丁的启动脚本增加 `--mount src=<宿主文件>,dst=<镜像内同路径>,readonly` | **是**（每次 run 都挂载；镜像重建也不影响） | 依赖宿主该文件存在，缺失时 `--mount` **fail-closed**（不会静默建目录，V4 起不来但易发现） | **✓ 本方案** |
| C. 重建镜像 | 改 `/usr/local/share/vllm-ds4/Dockerfile` 加 `COPY`，重 build 同 tag | 是 | 触碰构建上下文与 `ds4-image-build.service`；回滚要回镜像；窗口风险最大 | ✗（留作 B 不可行时的后备） |
| D. `docker commit` 派生镜像 | 起临时容器改完 commit 成同 tag | 是 | 会污染 tag、与 `ds4-image-build.service` 语义冲突、不可审计 | ✗ |

**最终结论：**

- 生产路径 `/vllm-workspace/vllm-ascend/vllm_ascend/profiler/torch_npu_profiler.py`
  **不是** bind mount，**重启不保留**任何容器内改动；
- 采用 **B**：`--mount type=bind,src=/opt/cann-ebpf/patches/vllm-ascend-profiler/torch_npu_profiler.py,dst=<同上>,readonly`，
  该 mount 由 `/usr/local/libexec/run_vllm_ds4_8905.sh` 施加，故 **只要启动脚本是补丁版、且宿主文件在，重启后必定生效**；
- `/opt` 在根文件系统上，重启保留；`/etc`、`/usr/local/libexec` 同理；
- 因此“是否需要重建镜像”的答案是：**本方案不需要**；只有当你拒绝宿主文件依赖时才需要重建镜像（方案 C）。

已在真机用**一次性容器**（同镜像、无 NPU 设备、`--network none`、`--rm`）验证该挂载方式：
容器内该路径读到的是补丁文件（`sha256=971ac14c…f612`，`cann-ebpf-patch v1` 出现 1 次），
而无挂载时读到镜像原件（`0c9ffcaf…09e5`）。

---

## 3. 补丁内容

### 3.1 涉及文件

| 目标 | 类型 | 改动 |
|---|---|---|
| `/opt/cann-ebpf/patches/vllm-ascend-profiler/torch_npu_profiler.py` | **新增**（bind mount 源） | 见 3.2 |
| `/usr/local/libexec/run_vllm_ds4_8905.sh` | **替换**（有备份） | 加 1 个只读 mount、加 `--profiler-config` |
| `/opt/cann-ebpf/patches/vllm-ascend-profiler.applied` | **新增**（标记） | `apply.sh` 写入，`V4-SINGLE-WINDOW-PLAN.md` C14 依赖它 |
| `/etc/vllm-ds4.env`、systemd unit | **不动** | — |

### 3.2 `vllm-ascend-profiler.diff`（模块，+11/−1）

```diff
--- a/vllm_ascend/profiler/torch_npu_profiler.py
+++ b/vllm_ascend/profiler/torch_npu_profiler.py
@@ -15,6 +15,13 @@
 # limitations under the License.
 # This file is a part of the vllm-ascend project.
 #
+
+# [cann-ebpf-patch v1] vllm-ascend profiler passthrough:
+#   1) forward ProfilerConfig.torch_profiler_record_shapes / torch_profiler_with_flops
+#      into torch_npu.profiler.profile(...) (upstream vLLM-Ascend drops both);
+#   2) switch aic_metrics PipeUtilization -> ArithmeticUtilization so that
+#      cube_fops/vector_fops columns are emitted in kernel_details.csv (FLOPs evidence).
+# Rollback = restore the pristine file from .../orig/ (see ./rollback.sh).
 
 from contextlib import suppress
 from typing import Any
@@ -50,7 +57,7 @@
             export_type=torch_npu.profiler.ExportType.Text,
             profiler_level=torch_npu.profiler.ProfilerLevel.Level1,
             msprof_tx=False,
-            aic_metrics=torch_npu.profiler.AiCMetrics.PipeUtilization,
+            aic_metrics=torch_npu.profiler.AiCMetrics.ArithmeticUtilization,  # cann-ebpf: was PipeUtilization
             l2_cache=False,
             op_attr=False,
             data_simplification=True,
@@ -64,6 +71,9 @@
                 torch_npu.profiler.ProfilerActivity.NPU,
             ],
             with_stack=False,
+            # cann-ebpf: upstream vLLM-Ascend never forwarded these two flags.
+            record_shapes=profiler_config.torch_profiler_record_shapes,
+            with_flops=profiler_config.torch_profiler_with_flops,
             profile_memory=profiler_config.torch_profiler_with_memory,
             # NOTE: torch_npu.profiler.with_modules is equivalent to torch.profiler.with_stack.
             # The with_stack option in torch_npu.profiler introduces significant time overhead.
```

要点：

- `ArithmeticUtilization` 是 `cube_fops`/`vector_fops` 的**前置条件**（`add_fops_header()` 只在
  `metric == "ArithmeticUtilization"` 时加列）；
- `record_shapes` 供 `operator_details.csv` / trace 的 shape 回退；`op_summary` 本身通常已带 shape，
  首采需核对（见 §5）；
- `with_flops` 按 vLLM 语义透传，但 **FLOPs 数值以 Ascend `cube_fops/vector_fops` 为准**，
  不依赖 torch 侧 FLOPs 计算（该参数在 NPU 上是否真的计数未验证，属 §7 未验证项）；
- 未改 `record_op_args`（会显著放大输出，本方案不需要）、未改 `profiler_level`（保持 Level1，
  Level1 已保留 `op_summary` 全列）。

### 3.3 `run_vllm_ds4_8905.sh.diff`（启动脚本，+3/−1）

```diff
--- a/usr/local/libexec/run_vllm_ds4_8905.sh
+++ b/usr/local/libexec/run_vllm_ds4_8905.sh
@@ -41,6 +41,7 @@
   --mount type=bind,src="${model_dir}",dst=/model,readonly \
   --mount type=bind,src=/data/cache,dst=/cache \
   --mount type=bind,src=/data/log/ascend,dst=/ascend/log \
+  --mount type=bind,src=/opt/cann-ebpf/patches/vllm-ascend-profiler/torch_npu_profiler.py,dst=/vllm-workspace/vllm-ascend/vllm_ascend/profiler/torch_npu_profiler.py,readonly \
   --health-cmd='curl -fsS -H "Authorization: Bearer $VLLM_API_KEY" http://127.0.0.1:8905/v1/models >/dev/null' \
@@ -69,4 +70,5 @@
     --compilation-config '{"cudagraph_mode":"FULL_DECODE_ONLY"}' \
-    --additional-config '{...}'
+    --additional-config '{...}' \
+    --profiler-config '{"profiler":"torch","torch_profiler_dir":"/cache/vllm_profile","torch_profiler_with_stack":false,"torch_profiler_record_shapes":true,"torch_profiler_with_flops":true}'
```

（`--additional-config` 内容未改，diff 里以 `{...}` 省略。）

### 3.4 `profiler-config.json`

```json
{
  "profiler": "torch",
  "torch_profiler_dir": "/cache/vllm_profile",
  "torch_profiler_with_stack": false,
  "torch_profiler_record_shapes": true,
  "torch_profiler_with_flops": true
}
```

- `torch_profiler_dir` 必须是绝对路径（vLLM 校验）；`/cache` 是 RW bind mount → 持久；
- `torch_profiler_with_stack=false`：`with_modules` 走的是 torch 的 `with_stack`，开销大，关掉；
- 键全部是 `ProfilerConfig` 的真实字段（`validate_patch.py` 校验，未知键会在启动时被 pydantic 拒绝）。

---

## 4. 应用步骤（窗口内，operator 执行）

前置：owner 已批准；`orinfer-v41.service` 已 inactive（腾卡，窗口外完成）；本补丁仅准备到 staging。

```bash
P=/opt/cann-ebpf/patches/vllm-ascend-profiler
cd $P

# 1) dry-run：preflight + 计划（已实测全 PASS，见 §6）
sh apply.sh
# 2) 真装（写文件 + marker，不重启）
sh apply.sh --apply
#    产物：$P/torch_npu_profiler.py、$P/apply.sh 生成的 backup/<UTC>/、$P/../vllm-ascend-profiler.applied

# 3) 确认窗口 preflight 的两个 profiler 项
grep -c -- '--profiler-config' /usr/local/libexec/run_vllm_ds4_8905.sh   # >=1  (C13)
ls -l /opt/cann-ebpf/patches/vllm-ascend-profiler.applied                # 存在    (C14)
bash -n /usr/local/libexec/run_vllm_ds4_8905.sh                          # 语法 OK

# 4) 走 V4-SINGLE-WINDOW-PLAN.md Phase 4–5（唯一一次 stop / start）
systemctl stop vllm-ds4-8905.service     # 一次
systemctl start vllm-ds4-8905.service    # 一次
```

启动后确认补丁真的进了容器（只读探针，不触发采集）：

```bash
docker exec -u root deepseek-v4-flash-8905 sh -c \
  'grep -c "cann-ebpf-patch v1" /vllm-workspace/vllm-ascend/vllm_ascend/profiler/torch_npu_profiler.py; \
   grep -c "AiCMetrics.ArithmeticUtilization" /vllm-workspace/vllm-ascend/vllm_ascend/profiler/torch_npu_profiler.py'
# 期望：1 和 1
```

> 若 `apply.sh --apply` 后**不打算**在本窗口重启：补丁只影响下一次启动，当前进程不受影响（安全）。
> 但若已替换启动脚本又误删了 `$P/torch_npu_profiler.py`，下次启动会因 `--mount` 源缺失而**失败**
> （fail-closed，见 §7 R1）。

---

## 5. 窗口内采集与验证命令

> 只采 **5–10 秒正常业务流量**，**不发压测**；采前采后各查一次 `/health`。

```bash
# 0) 服务 ready 且带 profiler 配置
curl -fsS -H "Authorization: Bearer $VLLM_API_KEY" http://127.0.0.1:8905/health && echo OK

# 1) 开 / 停采集（正常流量 5–10s，无压测）
curl -sS -X POST http://127.0.0.1:8905/start_profile
sleep 8
curl -sS -X POST http://127.0.0.1:8905/stop_profile

# 2) 找输出（宿主直读，无需 docker cp）
ls -1d /data/cache/vllm_profile/*_ascend_pt    # TP=8 → 每个 rank 一个目录
#   目录名含 rank 后缀（dp0_pp0_tp0_dcp0_ep0_rankN），由 get_worker_rank_suffix 生成

# 3) 关键列核对（S5）：必须出现 cube_fops / vector_fops / Input Shapes
for d in /data/cache/vllm_profile/*_ascend_pt; do
  f="$d/ASCEND_PROFILER_OUTPUT/kernel_details.csv"
  echo "== $d"; head -1 "$f" | tr ',' '\n' | grep -E 'Input Shapes|cube_fops|vector_fops|Duration' || echo "MISSING COLUMNS"
done

# 4) 若 ASCEND_PROFILER_OUTPUT 未自动生成，手动 analyse
python3 -c "from torch_npu.profiler.profiler import analyse; analyse('/data/cache/vllm_profile/<...>_ascend_pt')"

# 5) 转 ops.jsonl（转换器若仍在 /tmp；否则从仓库/服务器 staging 取）
python3 /tmp/ops_proto/ops_jsonl.py \
  --input /data/cache/vllm_profile/<...>_ascend_pt/ASCEND_PROFILER_OUTPUT \
  --out   /data/cache/vllm_profile/ops.jsonl \
  --run-id v4-<UTC> --dtype bf16

# 6) health 复核 + profiler 开销痕迹
curl -fsS -H "Authorization: Bearer $VLLM_API_KEY" http://127.0.0.1:8905/health && echo OK
```

**判据（对应 `V4-SINGLE-WINDOW-PLAN.md` S5/S6）：**

- `kernel_details.csv` 表头含 `cube_fops`、`vector_fops` → 补丁生效且指标组正确；
- 若表头**没有**这两列 → 记 `profiler_metric_columns_missing`，**不得**声称采到 FLOPs（见 §7 U2）；
- `ops.jsonl` 每行 `duration_ns>0 && flops>0 && bytes>0`，否则按规则跳过并计数；
- Roofline `bound` 在 `roofline.go:105` 修好前**不发布**。

**回滚（采完即可做，不需要重启）：**

```bash
cd /opt/cann-ebpf/patches/vllm-ascend-profiler
sh rollback.sh            # dry-run
sh rollback.sh --apply    # 还原启动脚本 + 删 marker（--purge 才删 bind mount 源）
```

> 注意：回滚只还原“下一次启动”的行为；已运行的验证 V4 进程仍是补丁代码，直到下一次按计划的
> 重启才回到正常 V4。这与 `V4-SINGLE-WINDOW-PLAN.md` U1（成功终态=验证 V4）一致。

---

## 6. 已完成的验证（不含真实采集）

### 6.1 本地（Windows 工作区）

```
$ python patches/vllm-ascend-profiler/tools/validate_patch.py
[1] patched module imports through stubs                      PASS
[2] record_shapes=True / with_flops=True forwarded            PASS
    aic_metrics == ArithmeticUtilization                      PASS
    with_modules from torch_profiler_with_stack=False         PASS
[3] pristine module has neither kwarg, aic=PipeUtilization    PASS  (gap proof)
[4] profiler-config.json: keys are real fields, values ok     PASS
[5] launcher embeds identical JSON                            PASS
RESULT: OK
```

### 6.2 服务器（`/opt/cann-ebpf/patches/vllm-ascend-profiler`，真机只读）

`sh apply.sh`（dry-run）实测：

```
PASS  staged torch_npu_profiler.py present        PASS  live launcher present
PASS  staged run script present                   PASS  live launcher is pristine (sha matches orig)
PASS  staged py sha256                            PASS  patch marker absent
PASS  staged run sha256                           PASS  staged py compiles
PASS  staged run script bash -n                   PASS  profiler-config.json parses
PASS  hermetic validate_patch.py
preflight blockers: 0
```

`sh tools/server_validate.sh` 实测全 PASS，含：

- `bash -n` 三个脚本；`py_compile` 补丁模块；
- **argv 引用**：用 stub `docker` 捕获 117 个参数，`--profiler-config` 恰好是**一个** argv，
  其 JSON 与 `profiler-config.json` 完全一致；`--mount ...readonly` 作为单个 argv 存在；
- **一次性容器**（同镜像、`--network none`、无 NPU 设备、`--rm`）挂载补丁文件后：
  容器内该路径 `sha256=971ac14c…f612`，`patch_marker_count=1`，`py_compile` 通过；
  不加挂载时读到镜像原件 `0c9ffcaf…09e5`；
- `/data/cache` 对 982:979 可写；`/cache/vllm_profile` 可由容器创建。

**没有**执行：真实 profiler 采集、`/start_profile`、任何 NPU 算子、任何 stop/start。

### 6.3 收尾核验：生产未被触碰（同一时点）

```
sha256sum /usr/local/libexec/run_vllm_ds4_8905.sh
  2a0f71cf7832d50da306c2282047ea447bd68f764dd47dcacb3b9fd4d03aebfd   # 与原件一致
/opt/cann-ebpf/patches/vllm-ascend-profiler.applied                    # 不存在（未 apply）
/opt/cann-ebpf/patches/vllm-ascend-profiler/backup                     # 不存在（dry-run 不建备份）
docker inspect … deepseek-v4-flash-8905  → 85addc45…0123a running started=2026-11-27T06:54:13Z  # 未重启
GET /health → 200
容器内 torch_npu_profiler.py sha256 = 0c9ffcaf…09e5                    # 仍是镜像原件
```

即：补丁仅存在于 `patches/` 与服务器 `/opt/cann-ebpf/patches/` staging；生产脚本、容器、进程均未变。


---

## 7. 持久化风险与边界

| 编号 | 风险 | 影响 | 处置 |
|---|---|---|---|
| R1 | 启动脚本已引用 `--mount` 源，但 `/opt/.../torch_npu_profiler.py` 被删 | `docker run` 直接失败（`--mount` fail-closed），V4 起不来 | `apply.sh` 先装文件后换脚本；`rollback.sh` 先换脚本后 `--purge`；`BACKUP.md` 写明顺序 |
| R2 | 容器每次 `docker rm -f` 重建 | 容器内手改一律丢失（含 `docker cp` 进容器的改法） | 本方案只用宿主 bind mount，不依赖容器层 |
| R3 | `ds4-image-build.service` 可能重建同 tag 镜像 | 镜像内文件被刷新 | bind mount 在 run 时施加，镜像重建不影响补丁（仍为宿主文件）；若改为方案 C 才需注意 |
| R4 | `/opt` 在根分区，若磁盘满/被清理 | 文件丢失 → 见 R1 | 文件仅 4 KB；纳入备份清单 |
| R5 | 目录权限过严导致容器读不到 | 容器内 `PermissionError` | staging 目录已 `0755`，模块 `0644`（已验证） |
| R6 | SELinux | 若 Enforcing 可能拒绝 bind mount | 实测 `Disabled`；若日后开启需加 `:z` |
| R7 | `--profiler-config` 拼错/未知键 | 启动时 pydantic 校验失败 → 服务起不来 | JSON 键已对源码逐项核对；`validate_patch.py` 每次 preflight 复检 |
| R8 | profiler 输出写到 `/cache`（持久盘） | 可能占用 `/data/cache` 空间 | 5–10s 采集；采完移走/清理；`/cache` 与模型缓存同盘需留意余量 |
| R9 | 8 rank 各写一份 | 直接拼接会重复/放大 | 按 rank 选代表或取中位数，`run_id` 加 rank 后缀（`OPS-FEASIBILITY.md` G6） |

---

## 8. 未验证项（**不得**写成已验证）

| 编号 | 未验证 | 原因 | 窗口内如何确认 |
|---|---|---|---|
| U1 | `ArithmeticUtilization` 在本 CANN 9.0.1 + torch_npu 2.10.0.post2 下是否真产出 `cube_fops/vector_fops` 列 | 需真实采集（禁止） | `head -1 kernel_details.csv` 查列 |
| U2 | `record_shapes=True` 是否让 `operator_details.csv`/trace 带上 shape | 需真实采集 | 同上，检查 `Input Shapes` 非空率 |
| U3 | `with_flops=True` 在 NPU 上是否真的产生可用 FLOPs | torch 为 `+cpu` 构建、NPU FLOPs 由 PMU 计数器给出 | 以 `cube_fops/vector_fops` 为准，不依赖该参数 |
| U4 | `FULL_DECODE_ONLY`(ACL graph) + MTP 下 profiler 是否可用/影响图捕获 | 需真实进程 | 采前采后 `/health`、看 vLLM 日志有无 profiler 报错 |
| U5 | profiler 运行时开销幅度 | 需真实负载 | 5–10s 窗口 + 前后延迟对比 |
| U6 | `/start_profile` 在 vLLM 0.23 的鉴权/返回体 | 未调用 | 窗口内 `curl -i` 看状态码 |
| U7 | 8 rank 输出实际目录名与数量 | 需真实采集 | `ls -1d /data/cache/vllm_profile/*_ascend_pt` |
| U8 | `ASCEND_PROFILER_OUTPUT` 是否在 stop 时自动生成 | 版本相关 | 不生成则手动 `analyse` |
| U9 | `ops_jsonl.py` 仍在 `/tmp`（易失） | 服务器临时文件 | preflight 检查，缺失则跳过 ops 转换 |

---

## 9. 还缺什么（交付缺口清单）

1. **真实采集样本**（只能窗口内）：`kernel_details.csv` 的实际列、shape 填充率、8 rank 目录命名。
2. **Roofline ridge 单位 bug**（`internal/report/roofline.go:105`，`OPS-FEASIBILITY.md` G5）未修：
   采到的 `ops.jsonl` 可以先落盘，但 `bound` 标签不可发布。
3. **ops 转换器未纳入持久位置**：现只在 `/tmp/ops_proto/`；建议窗口前复制到
   `/opt/cann-ebpf/ops_jsonl.py`（本轮未做，属“仅新增文件”可选动作）。
4. **collector 父进程过滤 `CANN_MSPTI_ONLY_PARENT_COMM`**（`V4-SINGLE-WINDOW-PLAN.md` G-P2）未实现，
   与本补丁正交，不影响 profiler。
5. **`--decommission-on-success` 决策**（U1）仍需 owner 明确：窗口成功终态是否就是“验证 V4”。
6. **镜像级持久化（方案 C）未准备**：若 owner 不接受宿主文件依赖，需要在窗口前额外准备
   `Dockerfile` 的 `COPY` 变体与重建/回滚步骤（本轮只写到方案对比里，未产出文件）。

---

## 10. 产物清单与校验和

仓库 staging（`patches/vllm-ascend-profiler/`，与服务器 `/opt/cann-ebpf/patches/vllm-ascend-profiler/` 同步）：

| 文件 | sha256 / 说明 |
|---|---|
| `orig/torch_npu_profiler.py` | `0c9ffcaf969a9b1fcb44a51bdd92e69f763a7c5be64a8efbd498d6536b9e09e5`（镜像原件） |
| `orig/run_vllm_ds4_8905.sh` | `2a0f71cf7832d50da306c2282047ea447bd68f764dd47dcacb3b9fd4d03aebfd`（在线原件） |
| `files/torch_npu_profiler.py` | `971ac14c7a1e6fe03d345218558a31ec02fd67915b14d2d19aa0dffa1abdf612` |
| `files/run_vllm_ds4_8905.sh` | `b662ccb698d0905d99b388ddb3b633e910ca7179656b6e35545fa0070fc08ee5` |
| `vllm-ascend-profiler.diff` / `run_vllm_ds4_8905.sh.diff` | 由 `tools/gen_diff.py` 生成 |
| `profiler-config.json` | 见 §3.4 |
| `apply.sh` / `rollback.sh` | 默认 dry-run，`--apply` 才动文件；从不重启 |
| `BACKUP.md` | 备份布局、手动回退、顺序硬约束 |
| `tools/validate_patch.py` / `tools/server_validate.sh` | 参数级 / 真机只读验证 |

服务器侧已写入（**仅新增**）：

```
/opt/cann-ebpf/patches/vllm-ascend-profiler/{README.md,BACKUP.md,apply.sh,rollback.sh,
  profiler-config.json,*.diff,orig/,files/,tools/}
```

未创建 `/opt/cann-ebpf/patches/vllm-ascend-profiler.applied`（apply 前不应存在）。
