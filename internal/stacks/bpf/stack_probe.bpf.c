// Minimal perf_event stack sampler for cann-ebpf stacks.
//
// Build:
//   clang --target=bpfel -mcpu=v1 -O2 -g -c stack_probe.bpf.c -o stack_probe.bpf.o
//
// The object is architecture-neutral BPF bytecode (bpfel) and is embedded by
// loader.go. It is deliberately self-contained: no libbpf or kernel headers
// are needed on the build host and the legacy bpf_map_def layout is understood
// by cilium/ebpf.
//
// A perf_event program is triggered by a CPU-clock software event opened with
// inherit=1. The event is attached only to target host PIDs, and this program
// additionally rejects any inherited child whose thread-group id is not in
// target_pids. That double filter guarantees we never fall back to host-wide
// sampling.

typedef unsigned char __u8;
typedef unsigned int __u32;
typedef signed int __s32;
typedef unsigned long long __u64;

#define SEC(name) __attribute__((section(name), used))
#define __always_inline inline __attribute__((always_inline))

#define BPF_MAP_TYPE_HASH 1
#define BPF_MAP_TYPE_PERCPU_ARRAY 6
#define BPF_MAP_TYPE_STACK_TRACE 7

#define BPF_ANY 0
#define BPF_NOEXIST 1

#define STACK_DEPTH 127
#define TARGET_PID_MAX 1024
#define STACK_TRACE_MAX 16384
#define STACK_COUNT_MAX 131072
#define STACK_ERROR_MAX 64
#define STAT_MAX 4

#define STAT_SAMPLES 0
#define STAT_FILTERED 1
#define STAT_STACK_FAIL 2
#define STAT_LOST 3

struct bpf_map_def {
	__u32 type;
	__u32 key_size;
	__u32 value_size;
	__u32 max_entries;
	__u32 map_flags;
};

struct stack_key {
	__u32 pid;
	__u32 stack_id;
};

/* Target thread-group ids. One entry is added for every --pid before the
 * perf events are enabled. */
struct bpf_map_def SEC("maps") target_pids = {
	.type = BPF_MAP_TYPE_HASH,
	.key_size = sizeof(__u32),
	.value_size = sizeof(__u8),
	.max_entries = TARGET_PID_MAX,
};

/* bpf_get_stackid stores up to 127 kernel addresses per stack id. Tail words
 * are zero; userspace trims them. */
struct bpf_map_def SEC("maps") stack_traces = {
	.type = BPF_MAP_TYPE_STACK_TRACE,
	.key_size = sizeof(__u32),
	.value_size = STACK_DEPTH * sizeof(__u64),
	.max_entries = STACK_TRACE_MAX,
};

/* Aggregated counts keyed by (tgid, stack id). The value is atomically
 * incremented so concurrent CPUs cannot lose samples. */
struct bpf_map_def SEC("maps") stack_counts = {
	.type = BPF_MAP_TYPE_HASH,
	.key_size = sizeof(struct stack_key),
	.value_size = sizeof(__u64),
	.max_entries = STACK_COUNT_MAX,
};

/* Negative bpf_get_stackid return values keyed by errno value. This lets a
 * real-kernel run distinguish EEXIST from EFAULT, E2BIG and ENOMEM instead of
 * collapsing every failure into one counter. */
struct bpf_map_def SEC("maps") stack_errors = {
	.type = BPF_MAP_TYPE_HASH,
	.key_size = sizeof(__s32),
	.value_size = sizeof(__u64),
	.max_entries = STACK_ERROR_MAX,
};

/* Per-CPU counters: samples, filtered, stack failures, reserved lost. */
struct bpf_map_def SEC("maps") stats = {
	.type = BPF_MAP_TYPE_PERCPU_ARRAY,
	.key_size = sizeof(__u32),
	.value_size = sizeof(__u64),
	.max_entries = STAT_MAX,
};

static void *(*bpf_map_lookup_elem)(void *map, const void *key) = (void *)1;
static long (*bpf_map_update_elem)(void *map, const void *key,
				   const void *value, __u64 flags) = (void *)2;
static __u64 (*bpf_get_current_pid_tgid)(void) = (void *)14;
static long (*bpf_get_stackid)(void *ctx, void *map, __u64 flags) = (void *)27;

static __always_inline void stat_inc(__u32 index)
{
	__u64 *value = bpf_map_lookup_elem(&stats, &index);

	if (value)
		__sync_fetch_and_add(value, 1);
}

static __always_inline void count_stack_error(__s32 code)
{
	__u64 *count = bpf_map_lookup_elem(&stack_errors, &code);
	__u64 zero = 0;

	if (!count) {
		bpf_map_update_elem(&stack_errors, &code, &zero, BPF_NOEXIST);
		count = bpf_map_lookup_elem(&stack_errors, &code);
	}
	if (count)
		__sync_fetch_and_add(count, 1);
}

SEC("perf_event")
int stack_probe(void *ctx)
{
	__u64 pid_tgid = bpf_get_current_pid_tgid();
	__u32 tgid = (__u32)(pid_tgid >> 32);
	struct stack_key key = {};
	__u64 *count;
	__u64 zero = 0;
	long stack_id;

	/* inherit=1 delivers events from child tasks too. Keep only the target
	 * thread group; everything else is accounted as filtered. */
	if (!bpf_map_lookup_elem(&target_pids, &tgid)) {
		stat_inc(STAT_FILTERED);
		return 0;
	}

	/* flags=0 selects full stack comparison. BPF_F_FAST_STACK_CMP compares
	 * hashes only and can surface collisions as -EEXIST. BPF_F_REUSE_STACKID
	 * is deliberately not set: reusing a colliding stack would pollute
	 * attribution. */
	stack_id = bpf_get_stackid(ctx, &stack_traces, 0);
	if (stack_id < 0) {
		count_stack_error((__s32)stack_id);
		stat_inc(STAT_STACK_FAIL);
		return 0;
	}

	key.pid = tgid;
	key.stack_id = (__u32)stack_id;

	count = bpf_map_lookup_elem(&stack_counts, &key);
	if (!count) {
		/* create-if-absent with a zero base, then re-lookup so the
		 * increments below also cover the insertion race. */
		bpf_map_update_elem(&stack_counts, &key, &zero, BPF_NOEXIST);
		count = bpf_map_lookup_elem(&stack_counts, &key);
	}
	if (count) {
		__sync_fetch_and_add(count, 1);
		stat_inc(STAT_SAMPLES);
	}
	return 0;
}

char _license[] SEC("license") = "GPL";