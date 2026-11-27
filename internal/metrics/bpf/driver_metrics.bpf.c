// NPU driver-layer metrics collected with kprobes/tracepoints.
//
// Build:
//   clang --target=bpfel -mcpu=v1 -O2 -g -c \
//     internal/metrics/bpf/driver_metrics.bpf.c \
//     -o internal/metrics/bpf/driver_metrics.bpf.o
//
// This object is self-contained: it does not include kernel or libbpf
// headers. The target is Linux/arm64 (Ascend 910B2), and the argument access
// below uses the arm64 pt_regs layout. On any other architecture the Go
// loader refuses to attach instead of guessing register positions.

typedef unsigned char __u8;
typedef unsigned int __u32;
typedef signed int __s32;
typedef unsigned long long __u64;

#define SEC(name) __attribute__((section(name), used))
#define __always_inline inline __attribute__((always_inline))

#define BPF_ANY 0
#define BPF_MAP_TYPE_HASH 1
#define BPF_MAP_TYPE_ARRAY 2

#define DRIVER_MAX_PROBES 64
#define DRIVER_MAX_TARGET_PIDS 4096
#define DRIVER_MAX_INFLIGHT 131072
#define DRIVER_MAX_IOCTL_PENDING 16384
#define DRIVER_MAX_COUNTS 131072
#define DRIVER_MAX_HISTS 65536
#define DRIVER_HIST_BUCKETS 16

#define DRIVER_MODE_LATENCY 1
#define DRIVER_MODE_IOCTL 2
#define DRIVER_MODE_SIZE_ARG 3
#define DRIVER_MODE_SIZE_VMA 4

struct bpf_map_def {
	__u32 type;
	__u32 key_size;
	__u32 value_size;
	__u32 max_entries;
	__u32 map_flags;
};

struct inflight_key {
	__u32 probe_id;
	__u32 tid;
};

struct inflight_value {
	__u64 start_ns;
	__u64 cgroup_id;
	__u64 size;
	__u32 tgid;
	__u32 cmd;
	__u32 fd;
	__u32 pad;
};

struct ioctl_pending_value {
	__u64 ts_ns;
	__u32 cmd;
	__u32 fd;
};

struct count_key {
	__u32 probe_id;
	__u32 cmd;
	__u32 tgid;
	__u32 tid;
	__u32 fd;
	__u32 pad;
	__u64 cgroup_id;
};

struct count_value {
	__u64 count;
	__u64 bytes;
};

struct hist_value {
	__u64 buckets[DRIVER_HIST_BUCKETS];
	__u64 sum_ns;
	__u64 count;
};

struct driver_stats {
	__u64 entries;
	__u64 exits;
	__u64 update_errors;
	__u64 missing_inflight;
	__u64 count_drops;
	__u64 hist_drops;
	__u64 pending_drops;
};

struct bpf_map_def SEC("maps") target_pids = {
	.type = BPF_MAP_TYPE_HASH,
	.key_size = sizeof(__u32),
	.value_size = sizeof(__u8),
	.max_entries = DRIVER_MAX_TARGET_PIDS,
};

struct bpf_map_def SEC("maps") inflight = {
	.type = BPF_MAP_TYPE_HASH,
	.key_size = sizeof(struct inflight_key),
	.value_size = sizeof(struct inflight_value),
	.max_entries = DRIVER_MAX_INFLIGHT,
};

struct bpf_map_def SEC("maps") ioctl_pending = {
	.type = BPF_MAP_TYPE_HASH,
	.key_size = sizeof(__u32),
	.value_size = sizeof(struct ioctl_pending_value),
	.max_entries = DRIVER_MAX_IOCTL_PENDING,
};

struct bpf_map_def SEC("maps") counts = {
	.type = BPF_MAP_TYPE_HASH,
	.key_size = sizeof(struct count_key),
	.value_size = sizeof(struct count_value),
	.max_entries = DRIVER_MAX_COUNTS,
};

struct bpf_map_def SEC("maps") histograms = {
	.type = BPF_MAP_TYPE_HASH,
	.key_size = sizeof(struct count_key),
	.value_size = sizeof(struct hist_value),
	.max_entries = DRIVER_MAX_HISTS,
};

struct bpf_map_def SEC("maps") stats = {
	.type = BPF_MAP_TYPE_ARRAY,
	.key_size = sizeof(__u32),
	.value_size = sizeof(struct driver_stats),
	.max_entries = 1,
};

static void *(*bpf_map_lookup_elem)(void *map, const void *key) = (void *)1;
static long (*bpf_map_update_elem)(void *map, const void *key,
				   const void *value, __u64 flags) = (void *)2;
static long (*bpf_map_delete_elem)(void *map, const void *key) = (void *)3;
static __u64 (*bpf_ktime_get_ns)(void) = (void *)5;
static __u64 (*bpf_get_current_pid_tgid)(void) = (void *)14;
static __u64 (*bpf_get_current_cgroup_id)(void) = (void *)80;
static long (*bpf_probe_read_kernel)(void *dst, __u32 size,
				     const void *src) = (void *)113;

/* arm64 struct pt_regs: regs[31] followed by sp, pc, pstate. */
struct arm64_pt_regs {
	__u64 regs[31];
	__u64 sp;
	__u64 pc;
	__u64 pstate;
};

static __always_inline struct driver_stats *stats_value(void)
{
	__u32 zero = 0;

	return bpf_map_lookup_elem(&stats, &zero);
}

static __always_inline int is_target_tgid(void)
{
	__u32 tgid = (__u32)(bpf_get_current_pid_tgid() >> 32);

	return bpf_map_lookup_elem(&target_pids, &tgid) != 0;
}

static __always_inline __u64 read_argument(void *ctx, __s32 index)
{
	struct arm64_pt_regs *regs = (struct arm64_pt_regs *)ctx;

	switch (index) {
	case 0:
		return regs->regs[0];
	case 1:
		return regs->regs[1];
	case 2:
		return regs->regs[2];
	case 3:
		return regs->regs[3];
	case 4:
		return regs->regs[4];
	case 5:
		return regs->regs[5];
	default:
		return 0;
	}
}

/* struct vm_area_struct starts with vm_start (offset 0) and vm_end (8) on
 * Linux 5.10; this is independent of the driver's private layout. The read
 * is done through bpf_probe_read_kernel so the verifier never sees an
 * untrusted direct dereference.
 */
static __always_inline __u64 read_vma_size(void *vma)
{
	__u64 start = 0;
	__u64 end = 0;
	__u64 end_addr;

	if (!vma)
		return 0;
	if (bpf_probe_read_kernel(&start, sizeof(start), vma) != 0)
		return 0;
	end_addr = (__u64)vma + sizeof(__u64);
	if (bpf_probe_read_kernel(&end, sizeof(end), (void *)end_addr) != 0)
		return 0;
	return end > start ? end - start : 0;
}

static __always_inline __u32 duration_bucket(__u64 duration_ns)
{
	if (duration_ns <= 1000ULL)
		return 0;
	if (duration_ns <= 2000ULL)
		return 1;
	if (duration_ns <= 5000ULL)
		return 2;
	if (duration_ns <= 10000ULL)
		return 3;
	if (duration_ns <= 25000ULL)
		return 4;
	if (duration_ns <= 50000ULL)
		return 5;
	if (duration_ns <= 100000ULL)
		return 6;
	if (duration_ns <= 250000ULL)
		return 7;
	if (duration_ns <= 500000ULL)
		return 8;
	if (duration_ns <= 1000000ULL)
		return 9;
	if (duration_ns <= 2500000ULL)
		return 10;
	if (duration_ns <= 5000000ULL)
		return 11;
	if (duration_ns <= 10000000ULL)
		return 12;
	if (duration_ns <= 25000000ULL)
		return 13;
	if (duration_ns <= 50000000ULL)
		return 14;
	if (duration_ns <= 100000000ULL)
		return 15;
	return 16;
}

static __always_inline int driver_entry(__u32 probe_id, __u32 mode,
					__s32 arg_index, void *ctx)
{
	__u64 pid_tgid = bpf_get_current_pid_tgid();
	__u64 now = bpf_ktime_get_ns();
	__u32 tgid = (__u32)(pid_tgid >> 32);
	__u32 tid = (__u32)pid_tgid;
	struct inflight_key key = {
		.probe_id = probe_id,
		.tid = tid,
	};
	struct inflight_value value = {
		.start_ns = now,
		.cgroup_id = bpf_get_current_cgroup_id(),
		.tgid = tgid,
	};
	struct driver_stats *st;

	if (!bpf_map_lookup_elem(&target_pids, &tgid))
		return 0;

	if (mode == DRIVER_MODE_IOCTL) {
		struct ioctl_pending_value *pending =
			bpf_map_lookup_elem(&ioctl_pending, &tid);
		if (pending && now >= pending->ts_ns &&
		    now - pending->ts_ns < 2000000000ULL) {
			value.cmd = pending->cmd;
			value.fd = pending->fd;
		}
	} else if (mode == DRIVER_MODE_SIZE_ARG) {
		value.size = read_argument(ctx, arg_index);
	} else if (mode == DRIVER_MODE_SIZE_VMA) {
		void *vma = (void *)read_argument(ctx, arg_index);
		value.size = read_vma_size(vma);
	}

	st = stats_value();
	if (bpf_map_update_elem(&inflight, &key, &value, BPF_ANY)) {
		if (st)
			__sync_fetch_and_add(&st->update_errors, 1);
		return 0;
	}
	if (st)
		__sync_fetch_and_add(&st->entries, 1);
	return 0;
}

static __always_inline int driver_exit(__u32 probe_id)
{
	__u64 pid_tgid = bpf_get_current_pid_tgid();
	__u64 now = bpf_ktime_get_ns();
	__u32 tgid = (__u32)(pid_tgid >> 32);
	__u32 tid = (__u32)pid_tgid;
	struct inflight_key key = {
		.probe_id = probe_id,
		.tid = tid,
	};
	struct inflight_value *inflight_value;
	struct inflight_value value = {};
	struct count_key count_key = {};
	struct count_value *count_value;
	struct hist_value *hist_value;
	struct driver_stats *st;
	__u64 duration_ns;
	__u32 bucket;

	if (!bpf_map_lookup_elem(&target_pids, &tgid))
		return 0;

	inflight_value = bpf_map_lookup_elem(&inflight, &key);
	st = stats_value();
	if (!inflight_value) {
		if (st)
			__sync_fetch_and_add(&st->missing_inflight, 1);
		return 0;
	}
	value = *inflight_value;
	bpf_map_delete_elem(&inflight, &key);
	if (st)
		__sync_fetch_and_add(&st->exits, 1);

	duration_ns = now > value.start_ns ? now - value.start_ns : 0;

	count_key.probe_id = probe_id;
	count_key.cmd = value.cmd;
	count_key.tgid = value.tgid;
	count_key.tid = tid;
	count_key.fd = value.fd;
	count_key.cgroup_id = value.cgroup_id;

	count_value = bpf_map_lookup_elem(&counts, &count_key);
	if (!count_value) {
		struct count_value zero = {};
		bpf_map_update_elem(&counts, &count_key, &zero, BPF_ANY);
		count_value = bpf_map_lookup_elem(&counts, &count_key);
	}
	if (count_value) {
		__sync_fetch_and_add(&count_value->count, 1);
		if (value.size)
			__sync_fetch_and_add(&count_value->bytes, value.size);
	} else if (st) {
		__sync_fetch_and_add(&st->count_drops, 1);
	}

	hist_value = bpf_map_lookup_elem(&histograms, &count_key);
	if (!hist_value) {
		struct hist_value zero = {};
		bpf_map_update_elem(&histograms, &count_key, &zero, BPF_ANY);
		hist_value = bpf_map_lookup_elem(&histograms, &count_key);
	}
	if (hist_value) {
		bucket = duration_bucket(duration_ns);
#define DRIVER_ADD_BUCKET(index)                                               \
		if (bucket == index)                                             \
			__sync_fetch_and_add(&hist_value->buckets[index], 1)
		DRIVER_ADD_BUCKET(0);
		DRIVER_ADD_BUCKET(1);
		DRIVER_ADD_BUCKET(2);
		DRIVER_ADD_BUCKET(3);
		DRIVER_ADD_BUCKET(4);
		DRIVER_ADD_BUCKET(5);
		DRIVER_ADD_BUCKET(6);
		DRIVER_ADD_BUCKET(7);
		DRIVER_ADD_BUCKET(8);
		DRIVER_ADD_BUCKET(9);
		DRIVER_ADD_BUCKET(10);
		DRIVER_ADD_BUCKET(11);
		DRIVER_ADD_BUCKET(12);
		DRIVER_ADD_BUCKET(13);
		DRIVER_ADD_BUCKET(14);
		DRIVER_ADD_BUCKET(15);
#undef DRIVER_ADD_BUCKET
		__sync_fetch_and_add(&hist_value->sum_ns, duration_ns);
		__sync_fetch_and_add(&hist_value->count, 1);
	} else if (st) {
		__sync_fetch_and_add(&st->hist_drops, 1);
	}
	return 0;
}

/* syscalls/sys_enter_ioctl follows the generic syscall tracepoint layout on
 * Linux 5.10 arm64:
 *   common fields (8 bytes)
 *   __syscall_nr (4 bytes at offset 8, followed by 4 bytes of padding)
 *   args[0] = fd  (8-byte slot at offset 16)
 *   args[1] = cmd (8-byte slot at offset 24)
 *   args[2] = arg (8-byte slot at offset 32)
 * The upper 4 bytes of each unsigned long slot are zero for the 32-bit fd/cmd
 * values on arm64. Keep the structure aligned with the tracepoint payload;
 * the loader validates the tracefs format before attaching the tracepoint.
 */
struct ioctl_enter_ctx {
	__u64 pad;
	__u64 syscall_nr;
	__u32 fd;
	__u32 pad2;
	__u32 cmd;
	__u32 pad3;
	__u64 arg;
};

SEC("tracepoint/syscalls/sys_enter_ioctl")
int on_sys_enter_ioctl(struct ioctl_enter_ctx *ctx)
{
	__u64 pid_tgid = bpf_get_current_pid_tgid();
	__u32 tgid = (__u32)(pid_tgid >> 32);
	__u32 tid = (__u32)pid_tgid;
	struct ioctl_pending_value value = {
		.ts_ns = bpf_ktime_get_ns(),
		.cmd = ctx->cmd,
		.fd = ctx->fd,
	};
	struct driver_stats *st;

	if (!bpf_map_lookup_elem(&target_pids, &tgid))
		return 0;
	if (bpf_map_update_elem(&ioctl_pending, &tid, &value, BPF_ANY)) {
		st = stats_value();
		if (st)
			__sync_fetch_and_add(&st->pending_drops, 1);
	}
	return 0;
}

SEC("tracepoint/syscalls/sys_exit_ioctl")
int on_sys_exit_ioctl(void *ctx)
{
	__u32 tid = (__u32)bpf_get_current_pid_tgid();

	bpf_map_delete_elem(&ioctl_pending, &tid);
	return 0;
}

/* Generic syscalls/sys_enter_mmap layout: trace_entry (8 bytes), syscall nr
 * (8 bytes), then six unsigned long args. args[1] is len and args[4] is fd.
 * Userspace resolves fd -> /dev/davinciN only when the fd is still open.
 */
struct mmap_enter_ctx {
	__u64 pad;
	__u64 syscall_nr;
	__u64 args[6];
};

SEC("tracepoint/syscalls/sys_enter_mmap")
int on_sys_enter_mmap(struct mmap_enter_ctx *ctx)
{
	__u64 pid_tgid = bpf_get_current_pid_tgid();
	__u32 tgid = (__u32)(pid_tgid >> 32);
	__u32 tid = (__u32)pid_tgid;
	__u64 length = ctx->args[1];
	__u32 fd = (__u32)ctx->args[4];
	struct count_key key = {
		.probe_id = 33,
		.tgid = tgid,
		.tid = tid,
		.fd = fd,
		.cgroup_id = bpf_get_current_cgroup_id(),
	};
	struct count_value *value;
	struct driver_stats *st;

	if (!bpf_map_lookup_elem(&target_pids, &tgid) || length == 0 ||
	    fd == 0 || fd == 0xffffffffU)
		return 0;
	value = bpf_map_lookup_elem(&counts, &key);
	if (!value) {
		struct count_value zero = {};
		bpf_map_update_elem(&counts, &key, &zero, BPF_ANY);
		value = bpf_map_lookup_elem(&counts, &key);
	}
	if (value) {
		__sync_fetch_and_add(&value->count, 1);
		__sync_fetch_and_add(&value->bytes, length);
	} else {
		st = stats_value();
		if (st)
			__sync_fetch_and_add(&st->count_drops, 1);
	}
	return 0;
}

#define DRIVER_PROBE_LATENCY(id, name)                                         \
	SEC("kprobe/" #name)                                                    \
	int driver_enter_##id(void *ctx)                                        \
	{                                                                       \
		return driver_entry(id, DRIVER_MODE_LATENCY, 0, ctx);            \
	}                                                                       \
	SEC("kretprobe/" #name)                                                 \
	int driver_exit_##id(void *ctx)                                         \
	{                                                                       \
		return driver_exit(id);                                           \
	}

#define DRIVER_PROBE_IOCTL(id, name)                                           \
	SEC("kprobe/" #name)                                                    \
	int driver_enter_##id(void *ctx)                                        \
	{                                                                       \
		return driver_entry(id, DRIVER_MODE_IOCTL, 0, ctx);              \
	}                                                                       \
	SEC("kretprobe/" #name)                                                 \
	int driver_exit_##id(void *ctx)                                         \
	{                                                                       \
		return driver_exit(id);                                           \
	}

#define DRIVER_PROBE_SIZE_ARG(id, name, arg)                                   \
	SEC("kprobe/" #name)                                                    \
	int driver_enter_##id(void *ctx)                                        \
	{                                                                       \
		return driver_entry(id, DRIVER_MODE_SIZE_ARG, arg, ctx);         \
	}                                                                       \
	SEC("kretprobe/" #name)                                                 \
	int driver_exit_##id(void *ctx)                                         \
	{                                                                       \
		return driver_exit(id);                                           \
	}

#define DRIVER_PROBE_SIZE_VMA(id, name, arg)                                   \
	SEC("kprobe/" #name)                                                    \
	int driver_enter_##id(void *ctx)                                        \
	{                                                                       \
		return driver_entry(id, DRIVER_MODE_SIZE_VMA, arg, ctx);         \
	}                                                                       \
	SEC("kretprobe/" #name)                                                 \
	int driver_exit_##id(void *ctx)                                         \
	{                                                                       \
		return driver_exit(id);                                           \
	}

/* IDs are part of the Go ABI; do not renumber. */
DRIVER_PROBE_IOCTL(1, drv_ascend_intf_ioctl)
DRIVER_PROBE_IOCTL(2, devmm_svm_ioctl)
DRIVER_PROBE_IOCTL(4, hdcdrv_ioctl)
DRIVER_PROBE_IOCTL(5, trs_ioctl)
DRIVER_PROBE_LATENCY(6, hdcdrv_send)
DRIVER_PROBE_LATENCY(7, hdcdrv_recv)
DRIVER_PROBE_LATENCY(8, hdcdrv_msg_chan_send)
DRIVER_PROBE_LATENCY(9, hdcdrv_get_w_sq_desc)
DRIVER_PROBE_LATENCY(10, devdrv_msg_ring_doorbell)
DRIVER_PROBE_LATENCY(11, devdrv_set_sq_doorbell)
DRIVER_PROBE_SIZE_ARG(12, devdrv_dma_map_page, 3)
DRIVER_PROBE_SIZE_ARG(13, devdrv_dma_map_single, 2)
DRIVER_PROBE_SIZE_ARG(14, dma_map_page_attrs, 3)
DRIVER_PROBE_SIZE_ARG(15, iommu_dma_map_page, 3)
DRIVER_PROBE_SIZE_ARG(16, devdrv_dma_unmap_page, 2)
DRIVER_PROBE_SIZE_ARG(17, dma_unmap_page_attrs, 2)
DRIVER_PROBE_SIZE_ARG(18, iommu_dma_unmap_page, 2)
DRIVER_PROBE_SIZE_ARG(19, alloc_iova, 1)
DRIVER_PROBE_SIZE_ARG(20, alloc_iova_fast, 1)
DRIVER_PROBE_SIZE_ARG(21, iommu_dma_alloc_iova, 1)
DRIVER_PROBE_LATENCY(22, free_iova)
DRIVER_PROBE_LATENCY(23, free_iova_fast)
DRIVER_PROBE_SIZE_VMA(24, devmm_svm_mmap, 1)
DRIVER_PROBE_SIZE_VMA(25, devmm_svm_munmap, 1)
DRIVER_PROBE_SIZE_ARG(26, xsmem_alloc, 1)
DRIVER_PROBE_SIZE_ARG(27, xsmem_free, 1)
DRIVER_PROBE_SIZE_ARG(28, xsmem_pool_alloc, 1)
DRIVER_PROBE_SIZE_ARG(29, xsmem_pool_free, 1)
DRIVER_PROBE_SIZE_ARG(30, hdccom_alloc_mem, 1)
DRIVER_PROBE_LATENCY(31, devdrv_dma_done_task)
DRIVER_PROBE_LATENCY(32, hdcdrv_copy_sq_desc_to_remote)

char _license[] SEC("license") = "GPL";
