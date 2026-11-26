// Pure-eBPF CPython 3.12 scheduler_output POC.
//
// Attach:
//   uprobe   PyObject_GetAttr
//   uretprobe PyObject_GetAttr
//
// The entry program keeps only calls whose attribute name is exactly
// "num_scheduled_tokens". The return program reads the returned dict and
// copies a bounded number of unicode request-id keys into a ring buffer.
//
// This is deliberately a POC:
//   - CPython 3.12 object layout only
//   - combined unicode dict only (ma_values == NULL, dk_kind == 1)
//   - compact ASCII unicode keys only
//   - at most 8 keys per dict access
//   - no prompt, output, or token payload is read
//
// Build:
//   clang --target=bpfel -mcpu=v1 -O2 -g -c cpy_probe.bpf.c -o cpy_probe.bpf.o

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
#define MAX_IDS 8
#define TARGET_NAME_LEN 20

struct bpf_map_def {
	__u32 type;
	__u32 key_size;
	__u32 value_size;
	__u32 max_entries;
	__u32 map_flags;
};

// aarch64 user_regs layout. The kernel's struct pt_regs starts with this
// array for uprobes, so x0 is regs[0] and x1 is regs[1].
struct arm64_user_regs {
	__u64 regs[31];
	__u64 sp;
	__u64 pc;
	__u64 pstate;
};

// CPython 3.12 PyASCIIObject. sizeof == 40 on 64-bit in 3.12.13.
// Unlike older releases, the structure has no wchar_t *wstr member.
struct py_ascii {
	__u64 ob_refcnt;
	__u64 ob_type;
	__s64 length;
	__s64 hash;
	__u32 state;
	__u32 pad;
};

// CPython 3.12 PyDictObject. ma_keys at 32, ma_values at 40.
struct py_dict {
	__u64 ob_refcnt;
	__u64 ob_type;
	__s64 ma_used;
	__u64 ma_version_tag;
	__u64 ma_keys;
	__u64 ma_values;
};

// CPython 3.12 PyDictKeysObject. dk_indices starts at sizeof == 32.
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

struct cpy_event {
	__u64 ts_ns;
	__u64 dict;
	__u32 pid;
	__u32 tid;
	__u32 index;
	__u32 total;
	__u32 nentries;
	__u32 kind;
	__u32 truncated;
	__u32 id_len;
	char id[MAX_ID_LEN];
};

struct cpy_stats {
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

struct bpf_map_def SEC("maps") stats = {
	.type = BPF_MAP_TYPE_ARRAY,
	.key_size = sizeof(__u32),
	.value_size = sizeof(struct cpy_stats),
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

static __always_inline struct cpy_stats *stats_value(void)
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
	// PyUnicode_1BYTE_KIND == 1, compact == 1, ascii == 1.
	if (((head.state >> 2) & 7) != 1)
		return 0;
	if (((head.state >> 5) & 1) != 1)
		return 0;
	if (((head.state >> 6) & 1) != 1)
		return 0;
	if (bpf_probe_read_user(&w0, sizeof(w0), (void *)(name_ptr + sizeof(struct py_ascii))))
		return 0;
	if (bpf_probe_read_user(&w1, sizeof(w1), (void *)(name_ptr + sizeof(struct py_ascii) + 8)))
		return 0;
	if (bpf_probe_read_user(&w2, sizeof(w2), (void *)(name_ptr + sizeof(struct py_ascii) + 16)))
		return 0;
	// "num_sche" "duled_to" "kens\0\0\0\0"
	return w0 == 0x656863735f6d756eULL &&
	       w1 == 0x6f745f64656c7564ULL &&
	       w2 == 0x00000000736e656bULL;
}

SEC("uprobe/PyObject_GetAttr")
int enter_pyobject_getattr(void *ctx)
{
	struct arm64_user_regs *regs = (struct arm64_user_regs *)ctx;
	__u64 pid_tgid = bpf_get_current_pid_tgid();
	struct cpy_stats *st = stats_value();
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
	struct cpy_stats *st = stats_value();
	struct inflight_value *inflight_value;
	__u64 dict_ptr;
	struct py_dict dict;
	struct py_dict_keys keys;
	__u64 entries;
	__s64 nentries;
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
	if (dict.ma_values) {
		if (st)
			st->split_dict++;
		return 0;
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
	total = nentries > 0xffff ? 0xffff : (__u32)nentries;
	limit = total > MAX_IDS ? MAX_IDS : total;
	entries = dict.ma_keys + sizeof(struct py_dict_keys) + index_bytes;

#pragma unroll
	for (i = 0; i < MAX_IDS; i++) {
		struct py_unicode_entry entry;
		struct py_ascii key;
		__u32 state;
		__u32 id_len;
		__u64 key_ptr;
		struct cpy_event *event;
		int j;

		if (i >= limit)
			break;
		if (bpf_probe_read_user(&entry, sizeof(entry), (void *)(entries + (__u64)i * sizeof(entry)))) {
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
		state = key.state;
		if (key.length <= 0 || key.length >= MAX_ID_LEN) {
			if (st)
				st->skipped++;
			continue;
		}
		if (((state >> 2) & 7) != 1 || ((state >> 5) & 1) != 1 || ((state >> 6) & 1) != 1) {
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
		event->pid = (__u32)(pid_tgid >> 32);
		event->tid = (__u32)pid_tgid;
		event->index = (__u32)i;
		event->total = total;
		event->nentries = (__u32)(nentries > 0xffff ? 0xffff : nentries);
		event->kind = keys.dk_kind;
		event->truncated = total > MAX_IDS;
		id_len = (__u32)key.length;
		event->id_len = id_len;
#pragma unroll
		for (j = 0; j < MAX_ID_LEN; j++)
			event->id[j] = 0;
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
