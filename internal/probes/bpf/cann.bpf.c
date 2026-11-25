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
#define MAX_FUNCTIONS 16

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
	.max_entries = 1,
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

static __always_inline struct probe_stats *stats_value(void)
{
	__u32 zero = 0;
	return bpf_map_lookup_elem(&probe_stats, &zero);
}

static __always_inline struct probe_cfg *cfg_value(__u32 func_id)
{
	return bpf_map_lookup_elem(&probe_cfg, &func_id);
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
	struct probe_stats *stats = stats_value();
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
	struct probe_stats *stats = stats_value();
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

	struct probe_cfg *cfg = cfg_value(func_id);
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
	event->start_ns = start;
	event->dur_ns = dur;
	event->pid = key.pid;
	event->tid = key.tid;
	event->func_id = func_id;
	event->flags = 0;
	bpf_ringbuf_submit(event, 0);

	if (stats)
		stats->emitted++;
	return 0;
}

SEC("uprobe/enter_aclrtSynchronizeStream")
int enter_aclrtSynchronizeStream(void *ctx) { return on_entry(1); }
SEC("uretprobe/exit_aclrtSynchronizeStream")
int exit_aclrtSynchronizeStream(void *ctx) { return on_exit(1); }

SEC("uprobe/enter_aclrtSynchronizeStreamWithTimeout")
int enter_aclrtSynchronizeStreamWithTimeout(void *ctx) { return on_entry(2); }
SEC("uretprobe/exit_aclrtSynchronizeStreamWithTimeout")
int exit_aclrtSynchronizeStreamWithTimeout(void *ctx) { return on_exit(2); }

SEC("uprobe/enter_aclrtSynchronizeEvent")
int enter_aclrtSynchronizeEvent(void *ctx) { return on_entry(3); }
SEC("uretprobe/exit_aclrtSynchronizeEvent")
int exit_aclrtSynchronizeEvent(void *ctx) { return on_exit(3); }

SEC("uprobe/enter_aclrtRecordEvent")
int enter_aclrtRecordEvent(void *ctx) { return on_entry(4); }
SEC("uretprobe/exit_aclrtRecordEvent")
int exit_aclrtRecordEvent(void *ctx) { return on_exit(4); }

SEC("uprobe/enter_aclrtStreamWaitEvent")
int enter_aclrtStreamWaitEvent(void *ctx) { return on_entry(5); }
SEC("uretprobe/exit_aclrtStreamWaitEvent")
int exit_aclrtStreamWaitEvent(void *ctx) { return on_exit(5); }

SEC("uprobe/enter_aclrtMemcpyAsync")
int enter_aclrtMemcpyAsync(void *ctx) { return on_entry(6); }
SEC("uretprobe/exit_aclrtMemcpyAsync")
int exit_aclrtMemcpyAsync(void *ctx) { return on_exit(6); }

SEC("uprobe/enter_aclrtLaunchKernel")
int enter_aclrtLaunchKernel(void *ctx) { return on_entry(7); }
SEC("uretprobe/exit_aclrtLaunchKernel")
int exit_aclrtLaunchKernel(void *ctx) { return on_exit(7); }

SEC("uprobe/enter_HcclAllReduce")
int enter_HcclAllReduce(void *ctx) { return on_entry(8); }
SEC("uretprobe/exit_HcclAllReduce")
int exit_HcclAllReduce(void *ctx) { return on_exit(8); }

char _license[] SEC("license") = "GPL";
