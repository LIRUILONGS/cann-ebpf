#ifndef CANN_EBPF_MSPTI_COLLECTOR_H
#define CANN_EBPF_MSPTI_COLLECTOR_H

#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

/*
 * Start an in-process msPTI activity session. The caller must invoke this
 * before ACL/torch_npu initializes; msPTI allows only one subscriber.
 *
 * output_path    destination JSONL (cann.device.v1, one object per line)
 * run_id         value copied into every record
 * activities     comma-separated: api,acl_api,runtime_api,kernel,memcpy,hccl,
 *                communication,memory,memset. Empty means api,kernel,memcpy,hccl.
 * max_bytes      0 means the default cap (512 MiB)
 * pid            0 means current process; otherwise the current pid must match
 *
 * Returns 0 on success, non-zero on configuration or msPTI failure.
 */
int cann_mspti_start(const char *output_path, const char *run_id,
                     const char *activities, uint64_t max_bytes, int pid);

/* Flush pending records without ending the session. */
int cann_mspti_flush(void);

/* Flush, unsubscribe and close the output. Safe to call more than once. */
int cann_mspti_stop(void);

/* 1 when a session is active, 0 otherwise. */
int cann_mspti_is_active(void);

#ifdef __cplusplus
}
#endif

#endif
