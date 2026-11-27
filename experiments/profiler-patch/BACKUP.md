# Backup & restore notes

## What `apply.sh --apply` backs up

Before touching anything it creates:

```
/opt/cann-ebpf/patches/vllm-ascend-profiler/backup/<UTC>/
  run_vllm_ds4_8905.sh            # byte copy of the live launcher (mode preserved)
  run_vllm_ds4_8905.sh.sha256     # its hash
  image-id.txt                    # ds4/vllm-ascend:v0.23.0rc1-nonroot image id
  container-id.txt                # current container id (if running)
```

The patched module is **not** a modification of a host file: it is a new file
dropped at `<patch-dir>/torch_npu_profiler.py` and bind-mounted read-only into the
container. The pristine module is immutable inside the image
(`/vllm-workspace/vllm-ascend/...`, sha256 `0c9ffcaf…09e5`), and a copy is kept at
`orig/torch_npu_profiler.py`, so no container-side backup is needed.

## Why a container-side backup is unnecessary (and useless)

The unit runs:

```
ExecStartPre=-/usr/local/bin/docker rm -f deepseek-v4-flash-8905
ExecStart=/usr/local/libexec/run_vllm_ds4_8905.sh   # docker run --rm ...
```

So the container is destroyed and recreated on **every** start. Any edit inside the
container's writable layer (e.g. `docker exec … vi …`) is lost at the next restart.
Persistence therefore comes only from (a) the image, or (b) a bind mount. This
bundle uses (b).

## Restore

```bash
# Restore launcher only (files; no restart). Picks the newest backup.
sh rollback.sh              # dry-run
sh rollback.sh --apply

# From a specific backup
sh rollback.sh --backup /opt/cann-ebpf/patches/vllm-ascend-profiler/backup/20261127T111107Z --apply
```

Rollback restores `/usr/local/libexec/run_vllm_ds4_8905.sh`, runs `bash -n`, checks
the restored hash equals the pristine `2a0f71cf…ebfd`, and removes
`/opt/cann-ebpf/patches/vllm-ascend-profiler.applied`.

`--purge` additionally deletes `<patch-dir>/torch_npu_profiler.py` (the bind-mount
source). Do this only after the launcher no longer references it, otherwise the
next start fails the bind mount.

## Manual fallback (if the scripts are unavailable)

```bash
BK=/opt/cann-ebpf/patches/vllm-ascend-profiler/backup/<UTC>
install -m 0755 "$BK/run_vllm_ds4_8905.sh" /usr/local/libexec/run_vllm_ds4_8905.sh.new
mv -f /usr/local/libexec/run_vllm_ds4_8905.sh.new /usr/local/libexec/run_vllm_ds4_8905.sh
bash -n /usr/local/libexec/run_vllm_ds4_8905.sh
rm -f /opt/cann-ebpf/patches/vllm-ascend-profiler.applied
```

## Ordering rules (hard)

- Install the staged module **before** the launcher references it (`apply.sh`
  does this).
- Remove the launcher reference **before** purging the module (`rollback.sh`
  does this).
- Never edit `/etc/vllm-ds4.env` or the systemd unit for this patch; neither is
  touched.
- `--mount type=bind` fails closed if the source path is missing (it does not
  silently create a directory), so a missing file blocks V4 start — recover by
  running `apply.sh --apply` or `rollback.sh --apply` before starting.
