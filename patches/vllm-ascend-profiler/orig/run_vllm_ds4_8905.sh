#!/usr/bin/env bash
set -euo pipefail

container_name="deepseek-v4-flash-8905"
image="ds4/vllm-ascend:v0.23.0rc1-nonroot"
model_dir="/data/models/DeepSeek-V4-Flash-w8a8-mtp"
runtime_uid="$(id -u vllm-ds4)"
runtime_gid="$(id -g vllm-ds4)"

exec /usr/local/bin/docker run --rm \
  --name "${container_name}" \
  --hostname "${container_name}" \
  --pull=never \
  --init \
  --network=host \
  --shm-size=512g \
  --stop-timeout=180 \
  --user "${runtime_uid}:${runtime_gid}" \
  --env-file /etc/vllm-ds4.env \
  --ulimit memlock=-1:-1 \
  --ulimit nofile=1048576:1048576 \
  --cap-add IPC_LOCK \
  --device /dev/davinci0 \
  --device /dev/davinci1 \
  --device /dev/davinci2 \
  --device /dev/davinci3 \
  --device /dev/davinci4 \
  --device /dev/davinci5 \
  --device /dev/davinci6 \
  --device /dev/davinci7 \
  --device /dev/davinci_manager \
  --device /dev/devmm_svm \
  --device /dev/hisi_hdc \
  --mount type=bind,src=/usr/local/dcmi,dst=/usr/local/dcmi,readonly \
  --mount type=bind,src=/usr/local/Ascend/driver/tools/hccn_tool,dst=/usr/local/Ascend/driver/tools/hccn_tool,readonly \
  --mount type=bind,src=/usr/local/bin/npu-smi,dst=/usr/local/bin/npu-smi,readonly \
  --mount type=bind,src=/usr/local/Ascend/driver/lib64,dst=/usr/local/Ascend/driver/lib64,readonly \
  --mount type=bind,src=/usr/local/Ascend/driver/version.info,dst=/usr/local/Ascend/driver/version.info,readonly \
  --mount type=bind,src=/etc/ascend_install.info,dst=/etc/ascend_install.info,readonly \
  --mount type=bind,src=/etc/hccn.conf,dst=/etc/hccn.conf,readonly \
  --mount type=bind,src="${model_dir}",dst=/model,readonly \
  --mount type=bind,src=/data/cache,dst=/cache \
  --mount type=bind,src=/data/log/ascend,dst=/ascend/log \
  --health-cmd='curl -fsS -H "Authorization: Bearer $VLLM_API_KEY" http://127.0.0.1:8905/v1/models >/dev/null' \
  --health-start-period=45m \
  --health-interval=30s \
  --health-timeout=10s \
  --health-retries=5 \
  "${image}" \
  vllm serve /model \
    --host 127.0.0.1 \
    --port 8905 \
    --served-model-name deepseek-v4-flash \
    --max-model-len 1048576 \
    --max-num-batched-tokens 8192 \
    --gpu-memory-utilization 0.9 \
    --max-num-seqs 32 \
    --data-parallel-size 1 \
    --tensor-parallel-size 8 \
    --enable-expert-parallel \
    --tokenizer-mode deepseek_v4 \
    --tool-call-parser deepseek_v4 \
    --enable-auto-tool-choice \
    --reasoning-parser deepseek_v4 \
    --safetensors-load-strategy prefetch \
    --no-enable-prefix-caching \
    --model-loader-extra-config '{"enable_multithread_load":"true","num_threads":128}' \
    --quantization ascend \
    --block-size 128 \
    --speculative-config '{"num_speculative_tokens":1,"method":"mtp","enforce_eager":true}' \
    --compilation-config '{"cudagraph_mode":"FULL_DECODE_ONLY"}' \
    --additional-config '{"ascend_compilation_config":{"enable_npugraph_ex":true,"enable_static_kernel":false},"enable_cpu_binding":true,"enable_dsa_cp":true,"enable_flashcomm1":true,"multistream_overlap_shared_expert":true}'
