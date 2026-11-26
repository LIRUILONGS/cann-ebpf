// Minimal self-contained CANN host-side probes.
// Build: clang --target=bpfel -mcpu=v1 -O2 -g -c this.c -o cann.bpf.o
// The target only needs the BPF instruction set (host is aarch64 LE).

typedef unsigned char __u8;
typedef unsigned int __u32;
typedef unsigned long long __u64;

#define SEC(name) __attribute__((section(name), used))
#define __always_inline inline __attribute__((always_inline))
#define BPF_ANY 0

#define BPF_MAP_TYPE_HASH 1
#define BPF_MAP_TYPE_ARRAY 2
#define BPF_MAP_TYPE_RINGBUF 27
#define MAX_FUNCTIONS 64

struct bpf_map_def {
	__u32 type;
	__u32 key_size;
	__u32 value_size;
	__u32 max_entries;
	__u32 map_flags;
};

struct inflight_key {
	__u32 pid;
	__u32 tid;
	__u32 func_id;
	__u32 pad;
};

struct inflight_value {
	__u64 start_ns;
};

struct span_event {
	__u64 start_ns;
	__u64 dur_ns;
	__u32 pid;
	__u32 tid;
	__u32 func_id;
	__u32 flags;
	__u64 offcpu_ns;
	__u64 futex_ns;
};

struct probe_cfg {
	__u32 enabled;
	__u32 sample_rate;
	__u64 min_duration_ns;
};

struct probe_stats {
	__u64 entries;
	__u64 exits;
	__u64 emitted;
	__u64 filtered;
	__u64 ringbuf_lost;
	__u64 missing_inflight;
	__u64 disabled;
	__u64 inflight_update_errors;
};

/* M2 host blocking attribution.
 *
 * A per-thread state entry is created by the first CANN uprobe entry for that
 * thread. sched_switch accumulates off-CPU time while the thread is inside a
 * CANN call; futex_wait kprobes accumulate futex wait time. The accumulators
 * are drained by the matching CANN uretprobe and reported per span.
 *
 * The state key is the thread id (kernel pid). Only threads that entered a
 * CANN probe get an entry, so system-wide tracepoints stay cheap and do not
 * need a separate target map.
 */
#define BLOCKING_MAX_DEPTH 64
#define SPAN_FLAG_OFFCPU 1
#define SPAN_FLAG_FUTEX 2

struct blocking_cfg {
	__u32 offcpu;
	__u32 futex;
};

/* Per-thread blocking state. Only the outermost CANN call is measured; nested
 * calls report no blocking fields (they would otherwise double count the
 * parent window). This keeps the map value layout free of variable-index
 * arrays, which the 5.10 verifier cannot always bound.
 */
struct tid_state {
	__u64 offcpu_since;
	__u64 offcpu_acc;
	__u64 futex_since;
	__u64 futex_acc;
	__u32 inflight;
	__u32 pad;
};

/* Tracepoint context layout for tracepoint/sched/sched_switch. Offsets are
 * fixed by the kernel tracepoint format and are valid on 5.10 aarch64:
 * prev_pid at 24, next_pid at 56.
 */
struct sched_switch_ctx {
	__u64 pad;
	char prev_comm[16];
	__u32 prev_pid;
	int prev_prio;
	long long prev_state;
	char next_comm[16];
	__u32 next_pid;
	int next_prio;
};

/* sched_process_exit: comm at 8, pid at 24. */
struct sched_exit_ctx {
	__u64 pad;
	char comm[16];
	__u32 pid;
	int prio;
};

/* sys_enter_futex: __syscall_nr at 8, uaddr at 16, op at 24. */
struct futex_enter_ctx {
	__u64 pad;
	__u32 syscall_nr;
	__u32 pad2;
	__u64 uaddr;
	__u32 op;
	__u32 pad3;
};

/* futex command values; FUTEX_PRIVATE_FLAG/CLOCK_REALTIME are masked off. */
#define FUTEX_CMD_MASK 0x7f
#define FUTEX_WAIT 0
#define FUTEX_WAIT_BITSET 9
#define FUTEX_WAIT_REQUEUE_PI 11


struct bpf_map_def SEC("maps") events = {
	.type = BPF_MAP_TYPE_RINGBUF,
	.max_entries = 8 * 1024 * 1024,
};

struct bpf_map_def SEC("maps") inflight = {
	.type = BPF_MAP_TYPE_HASH,
	.key_size = sizeof(struct inflight_key),
	.value_size = sizeof(struct inflight_value),
	.max_entries = 65536,
};

struct bpf_map_def SEC("maps") probe_cfg = {
	.type = BPF_MAP_TYPE_ARRAY,
	.key_size = sizeof(__u32),
	.value_size = sizeof(struct probe_cfg),
	.max_entries = MAX_FUNCTIONS,
};

struct bpf_map_def SEC("maps") probe_stats = {
	.type = BPF_MAP_TYPE_ARRAY,
	.key_size = sizeof(__u32),
	.value_size = sizeof(struct probe_stats),
	.max_entries = MAX_FUNCTIONS,
};

struct bpf_map_def SEC("maps") blocking_cfg = {
	.type = BPF_MAP_TYPE_ARRAY,
	.key_size = sizeof(__u32),
	.value_size = sizeof(struct blocking_cfg),
	.max_entries = 1,
};

struct bpf_map_def SEC("maps") tid_state = {
	.type = BPF_MAP_TYPE_HASH,
	.key_size = sizeof(__u32),
	.value_size = sizeof(struct tid_state),
	.max_entries = 8192,
};

static void *(*bpf_map_lookup_elem)(void *map, const void *key) = (void *)1;
static long (*bpf_map_update_elem)(void *map, const void *key,
				   const void *value, __u64 flags) = (void *)2;
static long (*bpf_map_delete_elem)(void *map, const void *key) = (void *)3;
static __u64 (*bpf_ktime_get_ns)(void) = (void *)5;
static __u32 (*bpf_get_prandom_u32)(void) = (void *)7;
static __u64 (*bpf_get_current_pid_tgid)(void) = (void *)14;
static void *(*bpf_ringbuf_reserve)(void *ringbuf, __u64 size,
				    __u64 flags) = (void *)131;
static void (*bpf_ringbuf_submit)(void *data, __u64 flags) = (void *)132;

static __always_inline struct probe_stats *stats_value(__u32 func_id)
{
	return bpf_map_lookup_elem(&probe_stats, &func_id);
}

static __always_inline struct probe_cfg *cfg_value(__u32 func_id)
{
	return bpf_map_lookup_elem(&probe_cfg, &func_id);
}

/* blocking_enter: the outermost CANN call starts a clean window. A thread is
 * running while it executes an entry, so any pending since timestamp is stale
 * (for example a tid reused after thread exit) and is dropped here.
 */
static __always_inline void blocking_enter(__u32 tid)
{
	struct tid_state initial = {};
	struct tid_state *state = bpf_map_lookup_elem(&tid_state, &tid);
	__u32 depth;

	if (!state) {
		bpf_map_update_elem(&tid_state, &tid, &initial, BPF_ANY);
		state = bpf_map_lookup_elem(&tid_state, &tid);
		if (!state)
			return;
	}
	depth = state->inflight;
	state->offcpu_since = 0;
	state->futex_since = 0;
	if (depth >= BLOCKING_MAX_DEPTH) {
		/* Missed uretprobe left the depth stuck: recover on next entry. */
		state->offcpu_acc = 0;
		state->futex_acc = 0;
		state->inflight = 1;
		return;
	}
	if (depth == 0) {
		state->offcpu_acc = 0;
		state->futex_acc = 0;
	}
	state->inflight = depth + 1;
}

/* blocking_exit drains the thread window on every exit, including calls
 * dropped by the duration/sample filter, so the depth cannot leak. Nested
 * calls report no sample: measured=false tells the caller not to set flags.
 */
static __always_inline void blocking_exit(__u32 tid, __u64 *offcpu_ns,
					  __u64 *futex_ns, __u32 *measured)
{
	struct tid_state *state = bpf_map_lookup_elem(&tid_state, &tid);
	__u32 depth;

	*offcpu_ns = 0;
	*futex_ns = 0;
	*measured = 0;
	if (!state)
		return;
	depth = state->inflight;
	if (depth == 0)
		return;
	depth--;
	state->inflight = depth;
	if (depth != 0)
		return;
	*measured = 1;
	*offcpu_ns = state->offcpu_acc;
	*futex_ns = state->futex_acc;
	state->offcpu_acc = 0;
	state->futex_acc = 0;
}

static __always_inline int on_entry(__u32 func_id)
{
	__u64 pid_tgid = bpf_get_current_pid_tgid();
	struct inflight_key key = {
		.pid = (__u32)(pid_tgid >> 32),
		.tid = (__u32)pid_tgid,
		.func_id = func_id,
	};
	struct inflight_value value = {
		.start_ns = bpf_ktime_get_ns(),
	};
	struct probe_stats *stats = stats_value(func_id);
	struct probe_cfg *cfg = cfg_value(func_id);

	if (!cfg || !cfg->enabled) {
		if (stats)
			stats->disabled++;
		return 0;
	}

	if (bpf_map_update_elem(&inflight, &key, &value, BPF_ANY)) {
		if (stats)
			stats->inflight_update_errors++;
		return 0;
	}
	blocking_enter(key.tid);
	if (stats)
		stats->entries++;
	return 0;
}

static __always_inline int on_exit(__u32 func_id)
{
	__u64 pid_tgid = bpf_get_current_pid_tgid();
	struct inflight_key key = {
		.pid = (__u32)(pid_tgid >> 32),
		.tid = (__u32)pid_tgid,
		.func_id = func_id,
	};
	struct probe_stats *stats = stats_value(func_id);
	struct probe_cfg *cfg = cfg_value(func_id);
	__u64 offcpu_ns = 0;
	__u64 futex_ns = 0;
	__u32 measured = 0;

	/* Drain the thread's blocking window on every exit, even when the
	 * per-function inflight key is missing (overwritten or already deleted),
	 * so the nesting depth cannot leak and later spans cannot report a
	 * cumulative value from earlier calls.
	 */
	if (cfg && cfg->enabled)
		blocking_exit(key.tid, &offcpu_ns, &futex_ns, &measured);

	struct inflight_value *value = bpf_map_lookup_elem(&inflight, &key);

	if (!value) {
		if (stats)
			stats->missing_inflight++;
		return 0;
	}

	__u64 now = bpf_ktime_get_ns();
	__u64 start = value->start_ns;
	bpf_map_delete_elem(&inflight, &key);

	if (stats)
		stats->exits++;

	if (!cfg || !cfg->enabled) {
		if (stats)
			stats->disabled++;
		return 0;
	}

	__u64 dur = now > start ? now - start : 0;
	if (dur < cfg->min_duration_ns ||
	    (cfg->sample_rate > 1 &&
	     (bpf_get_prandom_u32() % cfg->sample_rate) != 0)) {
		if (stats)
			stats->filtered++;
		return 0;
	}

	struct span_event *event =
		bpf_ringbuf_reserve(&events, sizeof(struct span_event), 0);
	if (!event) {
		if (stats)
			stats->ringbuf_lost++;
		return 0;
	}
	__u32 cfg_key = 0;
	struct blocking_cfg *bcfg = bpf_map_lookup_elem(&blocking_cfg, &cfg_key);
	event->start_ns = start;
	event->dur_ns = dur;
	event->pid = key.pid;
	event->tid = key.tid;
	event->func_id = func_id;
	event->flags = 0;
	if (bcfg && measured) {
		if (bcfg->offcpu)
			event->flags |= SPAN_FLAG_OFFCPU;
		if (bcfg->futex)
			event->flags |= SPAN_FLAG_FUTEX;
	}
	event->offcpu_ns = offcpu_ns;
	event->futex_ns = futex_ns;
	bpf_ringbuf_submit(event, 0);

	if (stats)
		stats->emitted++;
	return 0;
}

/* sched_switch fires for every context switch on the machine. It only keeps
 * bookkeeping for threads that already own a tid_state entry, so no target map
 * is needed and unrelated threads cost one hash lookup.
 */
SEC("tracepoint/sched/sched_switch")
int on_sched_switch(struct sched_switch_ctx *ctx)
{
	__u64 now = bpf_ktime_get_ns();
	__u32 prev = ctx->prev_pid;
	__u32 next = ctx->next_pid;
	struct tid_state *state;

	state = bpf_map_lookup_elem(&tid_state, &prev);
	if (state)
		state->offcpu_since = now;

	state = bpf_map_lookup_elem(&tid_state, &next);
	if (state && state->offcpu_since) {
		__u64 since = state->offcpu_since;
		state->offcpu_since = 0;
		if (state->inflight && now > since)
			state->offcpu_acc += now - since;
	}
	return 0;
}

/* Drop the per-thread state when the task exits. Without this, a reused tid
 * would inherit inflight/accumulators from a dead thread.
 */
SEC("tracepoint/sched/sched_process_exit")
int on_sched_exit(struct sched_exit_ctx *ctx)
{
	__u32 tid = ctx->pid;

	bpf_map_delete_elem(&tid_state, &tid);
	return 0;
}

/* Futex waits are captured at the syscall tracepoint rather than a kprobe on
 * futex_wait: the kernel may inline or clone futex_wait at individual call
 * sites, which made a symbol-based kprobe miss most waits. The syscall
 * tracepoint fires for every futex call, so filtering by the op field gives
 * complete coverage. FUTEX_WAIT / WAIT_BITSET / WAIT_REQUEUE_PI all block.
 */
SEC("tracepoint/syscalls/sys_enter_futex")
int on_futex_enter(struct futex_enter_ctx *ctx)
{
	__u32 cmd = ctx->op & FUTEX_CMD_MASK;
	__u32 tid;
	struct tid_state *state;

	if (cmd != FUTEX_WAIT && cmd != FUTEX_WAIT_BITSET &&
	    cmd != FUTEX_WAIT_REQUEUE_PI)
		return 0;
	tid = (__u32)bpf_get_current_pid_tgid();
	state = bpf_map_lookup_elem(&tid_state, &tid);
	if (state && state->inflight && !state->futex_since)
		state->futex_since = bpf_ktime_get_ns();
	return 0;
}

SEC("tracepoint/syscalls/sys_exit_futex")
int on_futex_exit(void *ctx)
{
	__u32 tid = (__u32)bpf_get_current_pid_tgid();
	struct tid_state *state = bpf_map_lookup_elem(&tid_state, &tid);

	if (state && state->futex_since) {
		__u64 now = bpf_ktime_get_ns();
		__u64 since = state->futex_since;
		state->futex_since = 0;
		if (state->inflight && now > since)
			state->futex_acc += now - since;
	}
	return 0;
}

#define CANN_PROBE(id, name)                                                    \
	SEC("uprobe/enter_" #name)                                               \
	int enter_##name(void *ctx) { return on_entry(id); }                      \
	SEC("uretprobe/exit_" #name)                                             \
	int exit_##name(void *ctx) { return on_exit(id); }

CANN_PROBE(1, aclrtSynchronizeStream)
CANN_PROBE(2, aclrtSynchronizeStreamWithTimeout)
CANN_PROBE(3, aclrtSynchronizeEvent)
CANN_PROBE(4, aclrtRecordEvent)
CANN_PROBE(5, aclrtStreamWaitEvent)
CANN_PROBE(6, aclrtQueryEventStatus)
CANN_PROBE(7, aclrtQueryEventWaitStatus)
CANN_PROBE(8, aclrtQueryEvent)
CANN_PROBE(9, aclrtSetDevice)
CANN_PROBE(10, aclrtGetDevice)
CANN_PROBE(11, aclrtSetCurrentContext)
CANN_PROBE(12, aclrtCreateStream)
CANN_PROBE(13, aclrtDestroyStream)
CANN_PROBE(14, aclrtCreateEvent)
CANN_PROBE(15, aclrtCreateEventWithFlag)
CANN_PROBE(16, aclrtResetEvent)
CANN_PROBE(17, aclrtDestroyEvent)
CANN_PROBE(18, aclrtMalloc)
CANN_PROBE(19, aclrtFree)
CANN_PROBE(20, aclrtMallocHost)
CANN_PROBE(21, aclrtFreeHost)
CANN_PROBE(22, aclrtHostRegister)
CANN_PROBE(23, aclrtHostUnregister)
CANN_PROBE(24, aclrtGetMemInfo)
CANN_PROBE(25, rtMemGetInfoEx)
CANN_PROBE(26, aclrtMemcpyAsync)
CANN_PROBE(27, aclrtLaunchKernel)
CANN_PROBE(28, rtKernelLaunch)
CANN_PROBE(29, rtKernelLaunchWithFlagV2)
CANN_PROBE(30, rtMemcpyAsync)
CANN_PROBE(31, HcclAllReduce)
CANN_PROBE(32, HcclAlltoAll)
CANN_PROBE(33, HcclAllGather)
CANN_PROBE(34, HcclReduceScatter)
CANN_PROBE(35, HcclBroadcast)
CANN_PROBE(36, HcclSend)
CANN_PROBE(37, HcclRecv)

char _license[] SEC("license") = "GPL";
