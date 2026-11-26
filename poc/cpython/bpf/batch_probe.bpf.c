// Pure-eBPF vLLM batch POC for the pinned V4 runtime.
//
// Target:
//   CPython 3.12.13 aarch64
//   vLLM 0.23.0+empty / vLLM-Ascend 0.23.0rc1
//   SchedulerOutput.num_scheduled_tokens
//
// Attach:
//   uprobe   PyObject_GetAttr
//   uretprobe PyObject_GetAttr
//
// Batch identity is the returned dict pointer. A new dict closes the previous
// batch and opens a new one. The owner object type name is read from
// PyObject.ob_type -> PyTypeObject.tp_name so the report can reject unrelated
// dict lookups. This is not a function-level execute_model boundary.
//
// Build:
//   clang --target=bpfel -mcpu=v1 -O2 -g -c batch_probe.bpf.c -o batch_probe.bpf.o

typedef unsigned char __u8;
typedef unsigned short __u16;
typedef unsigned int __u32;
typedef unsigned long long __u64;
typedef long long __s64;

#define SEC(name) __attribute__((section(name), used))
#define __always_inline inline __attribute__((always_inline))
#define BPF_ANY 0

#define BPF_MAP_TYPE_HASH 1
#define BPF_MAP_TYPE_ARRAY 2
#define BPF_MAP_TYPE_RINGBUF 27

#define MAX_ID_LEN 48
#define MAX_IDS 64
#define TARGET_NAME_LEN 20
#define TYPE_NAME_LEN 64

#define EVENT_ID 1
#define EVENT_BATCH_BEGIN 2
#define EVENT_BATCH_END 3

struct bpf_map_def {
	__u32 type;
	__u32 key_size;
	__u32 value_size;
	__u32 max_entries;
	__u32 map_flags;
};

// aarch64 user_regs layout for uprobes: x0 is regs[0], x1 is regs[1].
struct arm64_user_regs {
	__u64 regs[31];
	__u64 sp;
	__u64 pc;
	__u64 pstate;
};

// CPython 3.12.13 PyASCIIObject. sizeof == 40 on 64-bit.
struct py_ascii {
	__u64 ob_refcnt;
	__u64 ob_type;
	__s64 length;
	__s64 hash;
	__u32 state;
	__u32 pad;
};

// CPython 3.12.13 PyDictObject. ma_keys at +32, ma_values at +40.
struct py_dict {
	__u64 ob_refcnt;
	__u64 ob_type;
	__s64 ma_used;
	__u64 ma_version_tag;
	__u64 ma_keys;
	__u64 ma_values;
};

// CPython 3.12.13 PyDictKeysObject. dk_indices starts at sizeof == 32.
struct py_dict_keys {
	__s64 dk_refcnt;
	__u8 dk_log2_size;
	__u8 dk_log2_index_bytes;
	__u8 dk_kind;
	__u8 pad;
	__u32 dk_version;
	__s64 dk_usable;
	__s64 dk_nentries;
};

struct py_unicode_entry {
	__u64 me_key;
	__u64 me_value;
};

struct inflight_value {
	__u64 start_ns;
	__u64 object;
};

struct batch_state {
	__u64 dict;
	__u64 object;
	__u64 begin_ns;
	__u64 last_seen_ns;
	char type_name[TYPE_NAME_LEN];
};

struct batch_event {
	__u64 ts_ns;
	__u64 dict;
	__u64 object;
	__u64 batch_begin_ns;
	__u32 pid;
	__u32 tid;
	__u32 event_type;
	__u32 dict_used;
	__u32 index;
	__u32 total;
	__u32 nentries;
	__u32 kind;
	__u32 truncated;
	__u32 id_len;
	char id[MAX_ID_LEN];
	char type_name[TYPE_NAME_LEN];
};

struct batch_stats {
	__u64 entries;
	__u64 name_matches;
	__u64 returns;
	__u64 null_returns;
	__u64 parsed;
	__u64 emitted;
	__u64 skipped;
	__u64 read_errors;
	__u64 split_dict;
	__u64 non_unicode_dict;
	__u64 empty_dict;
	__u64 ringbuf_lost;
	__u64 batches_started;
	__u64 batches_closed;
	__u64 type_read_errors;
};

char _license[] SEC("license") = "GPL";

struct bpf_map_def SEC("maps") events = {
	.type = BPF_MAP_TYPE_RINGBUF,
	.max_entries = 4 * 1024 * 1024,
};

struct bpf_map_def SEC("maps") inflight = {
	.type = BPF_MAP_TYPE_HASH,
	.key_size = sizeof(__u64),
	.value_size = sizeof(struct inflight_value),
	.max_entries = 4096,
};

struct bpf_map_def SEC("maps") current = {
	.type = BPF_MAP_TYPE_HASH,
	.key_size = sizeof(__u64),
	.value_size = sizeof(struct batch_state),
	.max_entries = 4096,
};

struct bpf_map_def SEC("maps") stats = {
	.type = BPF_MAP_TYPE_ARRAY,
	.key_size = sizeof(__u32),
	.value_size = sizeof(struct batch_stats),
	.max_entries = 1,
};

static void *(*bpf_map_lookup_elem)(void *map, const void *key) = (void *)1;
static long (*bpf_map_update_elem)(void *map, const void *key,
				   const void *value, __u64 flags) = (void *)2;
static long (*bpf_map_delete_elem)(void *map, const void *key) = (void *)3;
static __u64 (*bpf_ktime_get_ns)(void) = (void *)5;
static __u64 (*bpf_get_current_pid_tgid)(void) = (void *)14;
static long (*bpf_probe_read_user)(void *dst, __u32 size,
				   const void *src) = (void *)112;
static long (*bpf_probe_read_user_str)(void *dst, __u32 size,
				       const void *src) = (void *)114;
static void *(*bpf_ringbuf_reserve)(void *ringbuf, __u64 size,
				    __u64 flags) = (void *)131;
static void (*bpf_ringbuf_submit)(void *data, __u64 flags) = (void *)132;

static __always_inline struct batch_stats *stats_value(void)
{
	__u32 key = 0;
	return bpf_map_lookup_elem(&stats, &key);
}

static __always_inline int is_target_attr(__u64 name_ptr)
{
	struct py_ascii head;
	__u64 w0 = 0, w1 = 0, w2 = 0;

	if (!name_ptr)
		return 0;
	if (bpf_probe_read_user(&head, sizeof(head), (void *)name_ptr))
		return 0;
	if (head.length != TARGET_NAME_LEN)
		return 0;
	if (((head.state >> 2) & 7) != 1)
		return 0;
	if (((head.state >> 5) & 1) != 1)
		return 0;
	if (((head.state >> 6) & 1) != 1)
		return 0;
	if (bpf_probe_read_user(&w0, sizeof(w0),
				(void *)(name_ptr + sizeof(struct py_ascii))))
		return 0;
	if (bpf_probe_read_user(&w1, sizeof(w1),
				(void *)(name_ptr + sizeof(struct py_ascii) + 8)))
		return 0;
	if (bpf_probe_read_user(&w2, sizeof(w2),
				(void *)(name_ptr + sizeof(struct py_ascii) + 16)))
		return 0;
	// "num_sche" "duled_to" "kens\0\0\0\0"
	return w0 == 0x656863735f6d756eULL &&
	       w1 == 0x6f745f64656c7564ULL &&
	       w2 == 0x00000000736e656bULL;
}

// PyObject.ob_type is at +8. PyTypeObject.tp_name is at +24.
static __always_inline int read_type_name(__u64 object, char *dst)
{
	__u64 type_ptr = 0;
	__u64 name_ptr = 0;

	if (!object)
		return -1;
	if (bpf_probe_read_user(&type_ptr, sizeof(type_ptr), (void *)(object + 8)))
		return -1;
	if (!type_ptr)
		return -1;
	if (bpf_probe_read_user(&name_ptr, sizeof(name_ptr), (void *)(type_ptr + 24)))
		return -1;
	if (!name_ptr)
		return -1;
	if (bpf_probe_read_user_str(dst, TYPE_NAME_LEN, (void *)name_ptr) < 0)
		return -1;
	return 0;
}

static __always_inline void emit_marker(__u32 event_type, __u64 ts_ns,
					__u64 dict, __u64 object,
					__u64 begin_ns,
					struct batch_state *state)
{
	struct batch_stats *st = stats_value();
	struct batch_event *event;
	int i;

	event = bpf_ringbuf_reserve(&events, sizeof(*event), 0);
	if (!event) {
		if (st)
			st->ringbuf_lost++;
		return;
	}
	event->ts_ns = ts_ns;
	event->dict = dict;
	event->object = object;
	event->batch_begin_ns = begin_ns;
	event->pid = (__u32)(bpf_get_current_pid_tgid() >> 32);
	event->tid = (__u32)bpf_get_current_pid_tgid();
	event->event_type = event_type;
	event->dict_used = 0;
	event->index = 0;
	event->total = 0;
	event->nentries = 0;
	event->kind = 0;
	event->truncated = 0;
	event->id_len = 0;
#pragma unroll
	for (i = 0; i < MAX_ID_LEN; i++)
		event->id[i] = 0;
#pragma unroll
	for (i = 0; i < TYPE_NAME_LEN; i++)
		event->type_name[i] = 0;
	if (state) {
#pragma unroll
		for (i = 0; i < TYPE_NAME_LEN; i++)
			event->type_name[i] = state->type_name[i];
	} else if (read_type_name(object, event->type_name)) {
		if (st)
			st->type_read_errors++;
	}
	bpf_ringbuf_submit(event, 0);
	if (st) {
		if (event_type == EVENT_BATCH_BEGIN)
			st->batches_started++;
		else if (event_type == EVENT_BATCH_END)
			st->batches_closed++;
	}
}

SEC("uprobe/PyObject_GetAttr")
int enter_pyobject_getattr(void *ctx)
{
	struct arm64_user_regs *regs = (struct arm64_user_regs *)ctx;
	__u64 pid_tgid = bpf_get_current_pid_tgid();
	struct batch_stats *st = stats_value();
	struct inflight_value value;

	if (st)
		st->entries++;
	if (!is_target_attr(regs->regs[1]))
		return 0;
	value.start_ns = bpf_ktime_get_ns();
	value.object = regs->regs[0];
	if (bpf_map_update_elem(&inflight, &pid_tgid, &value, BPF_ANY))
		return 0;
	if (st)
		st->name_matches++;
	return 0;
}

SEC("uretprobe/PyObject_GetAttr")
int exit_pyobject_getattr(void *ctx)
{
	struct arm64_user_regs *regs = (struct arm64_user_regs *)ctx;
	__u64 pid_tgid = bpf_get_current_pid_tgid();
	struct batch_stats *st = stats_value();
	struct inflight_value *inflight_value;
	struct batch_state *state;
	struct batch_state new_state;
	struct py_dict dict;
	struct py_dict_keys keys;
	__u64 dict_ptr;
	__u64 entries;
	__u64 now;
	__u64 batch_begin;
	__u64 batch_object;
	__s64 nentries;
	__s64 used;
	__u32 used32;
	__u32 total;
	__u32 limit;
	__u32 index_bytes;
	int i;

	inflight_value = bpf_map_lookup_elem(&inflight, &pid_tgid);
	if (!inflight_value)
		return 0;
	dict_ptr = regs->regs[0];
	bpf_map_delete_elem(&inflight, &pid_tgid);
	if (st)
		st->returns++;
	if (!dict_ptr) {
		if (st)
			st->null_returns++;
		return 0;
	}
	if (bpf_probe_read_user(&dict, sizeof(dict), (void *)dict_ptr)) {
		if (st)
			st->read_errors++;
		return 0;
	}
	/* A split dict keeps keys in dk_entries and values in ma_values.
	 * The request ID is the key, so split dicts are readable here too. */
	if (dict.ma_values) {
		if (st)
			st->split_dict++;
	}
	if (!dict.ma_keys) {
		if (st)
			st->empty_dict++;
		return 0;
	}
	if (bpf_probe_read_user(&keys, sizeof(keys), (void *)dict.ma_keys)) {
		if (st)
			st->read_errors++;
		return 0;
	}
	if (keys.dk_kind != 1) {
		if (st)
			st->non_unicode_dict++;
		return 0;
	}
	index_bytes = 1;
	if (keys.dk_log2_index_bytes == 1)
		index_bytes = 2;
	else if (keys.dk_log2_index_bytes == 2)
		index_bytes = 4;
	else if (keys.dk_log2_index_bytes == 3)
		index_bytes = 8;
	else if (keys.dk_log2_index_bytes == 4)
		index_bytes = 16;
	else if (keys.dk_log2_index_bytes != 0) {
		if (st)
			st->read_errors++;
		return 0;
	}
	nentries = keys.dk_nentries;
	if (nentries <= 0) {
		if (st)
			st->empty_dict++;
		return 0;
	}
	now = bpf_ktime_get_ns();
	state = bpf_map_lookup_elem(&current, &pid_tgid);
	if (!state || state->dict != dict_ptr) {
		if (state && state->dict)
			emit_marker(EVENT_BATCH_END, now, state->dict, state->object,
				    state->begin_ns, state);
		new_state.dict = dict_ptr;
		new_state.object = inflight_value->object;
		new_state.begin_ns = inflight_value->start_ns;
		new_state.last_seen_ns = now;
#pragma unroll
		for (i = 0; i < TYPE_NAME_LEN; i++)
			new_state.type_name[i] = 0;
		if (read_type_name(new_state.object, new_state.type_name)) {
			if (st)
				st->type_read_errors++;
		}
		if (bpf_map_update_elem(&current, &pid_tgid, &new_state, BPF_ANY)) {
			if (st)
				st->read_errors++;
		}
		emit_marker(EVENT_BATCH_BEGIN, new_state.begin_ns, dict_ptr,
			    new_state.object, new_state.begin_ns, &new_state);
		state = bpf_map_lookup_elem(&current, &pid_tgid);
	} else {
		state->last_seen_ns = now;
	}
	batch_begin = inflight_value->start_ns;
	batch_object = inflight_value->object;
	if (state) {
		batch_begin = state->begin_ns;
		batch_object = state->object;
	}
	used = dict.ma_used;
	used32 = 0;
	if (used > 0)
		used32 = used > 0xffff ? 0xffff : (__u32)used;
	total = nentries > 0xffff ? 0xffff : (__u32)nentries;
	limit = total > MAX_IDS ? MAX_IDS : total;
	entries = dict.ma_keys + sizeof(struct py_dict_keys) + index_bytes;

#pragma unroll
	for (i = 0; i < MAX_IDS; i++) {
		struct py_unicode_entry entry;
		struct py_ascii key;
		__u32 state_bits;
		__u32 id_len;
		__u64 key_ptr;
		struct batch_event *event;
		int j;

		if (i >= limit)
			break;
		if (bpf_probe_read_user(&entry, sizeof(entry),
					(void *)(entries + (__u64)i * sizeof(entry)))) {
			if (st)
				st->read_errors++;
			continue;
		}
		key_ptr = entry.me_key;
		if (!key_ptr) {
			if (st)
				st->skipped++;
			continue;
		}
		if (bpf_probe_read_user(&key, sizeof(key), (void *)key_ptr)) {
			if (st)
				st->read_errors++;
			continue;
		}
		state_bits = key.state;
		if (key.length <= 0 || key.length >= MAX_ID_LEN) {
			if (st)
				st->skipped++;
			continue;
		}
		if (((state_bits >> 2) & 7) != 1 ||
		    ((state_bits >> 5) & 1) != 1 ||
		    ((state_bits >> 6) & 1) != 1) {
			if (st)
				st->skipped++;
			continue;
		}
		event = bpf_ringbuf_reserve(&events, sizeof(*event), 0);
		if (!event) {
			if (st)
				st->ringbuf_lost++;
			return 0;
		}
		event->ts_ns = bpf_ktime_get_ns();
		event->dict = dict_ptr;
		event->object = batch_object;
		event->batch_begin_ns = batch_begin;
		event->pid = (__u32)(pid_tgid >> 32);
		event->tid = (__u32)pid_tgid;
		event->event_type = EVENT_ID;
		event->dict_used = used32;
		event->index = (__u32)i;
		event->total = total;
		event->nentries = total;
		event->kind = keys.dk_kind;
		event->truncated = total > MAX_IDS;
		id_len = (__u32)key.length;
		event->id_len = id_len;
#pragma unroll
		for (j = 0; j < MAX_ID_LEN; j++)
			event->id[j] = 0;
#pragma unroll
		for (j = 0; j < TYPE_NAME_LEN; j++)
			event->type_name[j] = 0;
		if (bpf_probe_read_user_str(event->id, MAX_ID_LEN,
					    (void *)(key_ptr + sizeof(struct py_ascii))) < 0) {
			event->id_len = 0;
			event->id[0] = 0;
			if (st)
				st->read_errors++;
		}
		bpf_ringbuf_submit(event, 0);
		if (st)
			st->emitted++;
	}
	if (st)
		st->parsed++;
	return 0;
}
