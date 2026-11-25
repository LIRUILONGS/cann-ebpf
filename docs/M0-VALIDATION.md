# M0 服务器只读验证

日期：2026-11-25

## 目标

验证 `cann-ebpf discover --dry-run` 能在不 attach、不重启服务的前提下，发现目标进程、容器 root 下的 CANN 库、ELF 符号和 uprobe offset。

## 环境

| 项 | 值 |
|---|---|
| 主机 | `bms-84865758` |
| 架构 | `aarch64` |
| 目标服务 | `vllm-ds4-8905.service` |
| 服务状态 | `active`，`/health` 返回 HTTP 200 |
| 容器 | `deepseek-v4-flash-8905` |
| 镜像 | `ds4/vllm-ascend:v0.23.0rc1-nonroot` |
| CANN（目标进程 root 内） | `9.0.1` |
| 二进制 | `cann-ebpf 0.1.0-m0 linux/arm64` |

## 执行命令

```bash
cd /root/cann-ebpf
./dist/cann-ebpf-linux-arm64 validate --config profiles/vllm-ascend.yaml
./dist/cann-ebpf-linux-arm64 discover \
  --profile vllm-ascend \
  --profiles-dir profiles \
  --dry-run > discover-vllm-fixed.txt
```

## 结果

- `discover` 退出码为 `0`。
- 匹配到 18 个 V4 进程：8 个 `VLLM::Worker_TP`、8 个 `python3` 子进程、1 个 `vllm` frontend、1 个 `VLLM::EngineCor`。
- 共发现 72 个库路径，全部通过 `/proc/<pid>/root` 解析到容器内文件。
- 共输出 666 个可用符号、1998 个未命中符号。
- 典型可用符号：`aclrtSynchronizeStream` 在 `libascendcl.so` 中为 `off=0xe2b8`；`HcclAllReduce` 在 `libhccl.so` 中为 `off=0x2447f8`。
- `libtccl`/`libhccl` 中未定义的 ACL 导入符号不再被误报为可用；修复前 `libhccl.so` 会把 `aclrtSynchronizeStream` 报成 `OK`，修复后为 `MISS`。

## 未验证项

- Orinfer 当前未运行，历史 PID `1691816` 不存在；没有启动或重启 Orinfer，因此本轮只完成 V4 discovery。
- M0 没有 attach，不采集事件，不修改目标进程。
- `libhccl.so` 的 `Hccl*` 符号已解析，但真正的 HCCL activity 和 msPTI 关联仍属于 M3。

## 结论

V4 的进程发现、容器 root 库解析、ELF dynsym 解析和 profile 匹配已通过只读验证。Orinfer 验证等待其重新运行后的授权窗口。
