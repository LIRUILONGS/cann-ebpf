// In-process msPTI collector for the cann-ebpf M3 device timeline.
//
// The library is intended to be loaded through LD_PRELOAD before the target
// runtime initializes ACL. Its constructor starts msPTI from environment
// variables; the destructor flushes and closes the JSONL file.

#include "collector.h"

#include <mspti.h>

#include <algorithm>
#include <atomic>
#include <cerrno>
#include <chrono>
#include <condition_variable>
#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <ctime>
#include <dirent.h>
#include <fstream>
#include <map>
#include <mutex>
#include <sstream>
#include <string>
#include <thread>
#include <unistd.h>
#include <vector>

namespace {

constexpr size_t kBufferSize = 8 * 1024 * 1024;
constexpr uint64_t kDefaultMaxBytes = 512ULL * 1024ULL * 1024ULL;
constexpr const char *kCollectorVersion = "0.1.0";

struct ActivityConfig {
    bool api = true;
    bool acl_api = false;
    bool runtime_api = false;
    bool kernel = true;
    bool memcpy = true;
    bool hccl = true;
    bool communication = false;
    bool memory = false;
    bool memset = false;
};

std::mutex g_write_mutex;
std::mutex g_meta_mutex;
std::mutex g_meta_file_mutex;
std::mutex g_flush_mutex;
std::mutex g_flush_wait_mutex;
std::condition_variable g_flush_wait_cv;
std::mutex g_lifecycle_mutex;
std::mutex g_stop_mutex;
std::condition_variable g_stop_cv;
std::atomic<bool> g_stop_in_progress(false);
std::atomic<bool> g_stop_complete(false);
std::FILE *g_file = nullptr;
std::string g_output_path;
std::string g_run_id;
std::string g_activity_spec;
std::string g_clock_spec = "auto";
std::string g_container_id;
std::string g_warnings;
msptiSubscriberHandle g_subscriber = nullptr;
std::atomic<bool> g_running(false);
std::atomic<bool> g_auto_started(false);
std::atomic<bool> g_truncated(false);
std::atomic<bool> g_flush_thread_stop(false);
std::atomic<uint64_t> g_flush_generation(0);
uint64_t g_max_bytes = kDefaultMaxBytes;
uint64_t g_bytes_written = 0;
uint64_t g_records_written = 0;
uint64_t g_records_ignored = 0;
uint64_t g_records_skipped = 0;
int g_pid_filter = 0;
uint64_t g_start_unix_ns = 0;
uint64_t g_stop_unix_ns = 0;
std::atomic<int64_t> g_realtime_minus_monotonic_ns(0);
std::atomic<int64_t> g_clock_offset_start_ns(0);
std::atomic<int64_t> g_clock_offset_last_ns(0);
std::atomic<int64_t> g_clock_drift_ns(0);
std::atomic<uint64_t> g_clock_refresh_count(0);
std::atomic<uint64_t> g_clock_last_refresh_unix_ns(0);
std::mutex g_pid_cache_mutex;
std::map<int, int> g_tid_to_host;
std::mutex g_tid_refresh_mutex;
std::atomic<uint64_t> g_tid_map_entries(0);
std::atomic<uint64_t> g_tid_map_refresh_count(0);
std::atomic<uint64_t> g_unmapped_tid_records(0);
std::mutex g_callback_mutex;
std::condition_variable g_callback_cv;
std::atomic<uint64_t> g_callbacks_active(0);
std::atomic<uint64_t> g_callback_buffers_processed(0);
std::atomic<uint64_t> g_callback_records_processed(0);

void AddWarning(const std::string &warning);

uint64_t NowClockNS(clockid_t clock_id) {
    struct timespec ts {};
    clock_gettime(clock_id, &ts);
    return static_cast<uint64_t>(ts.tv_sec) * 1000000000ULL +
           static_cast<uint64_t>(ts.tv_nsec);
}

uint64_t NowUnixNS() { return NowClockNS(CLOCK_REALTIME); }

std::string ReadContainerID() {
    std::ifstream file("/proc/self/cgroup");
    std::string line;
    while (std::getline(file, line)) {
        size_t pos = line.find("/docker/");
        if (pos != std::string::npos) {
            pos += 8;
            size_t end = line.find('/', pos);
            return line.substr(pos, end == std::string::npos ? std::string::npos : end - pos);
        }
        pos = line.find("docker-");
        if (pos != std::string::npos) {
            pos += 7;
            size_t end = line.find(".scope", pos);
            return line.substr(pos, end == std::string::npos ? std::string::npos : end - pos);
        }
    }
    return "";
}

// /proc/<pid>/status exposes the PID in every namespace on the NSpid line,
// outermost first. When the target runs in a container, msPTI reports the
// innermost pid while eBPF host spans use the outermost one.
bool ReadNSpidPair(const std::string &status_path, int *host_pid, int *local_pid) {
    std::ifstream file(status_path.c_str());
    std::string line;
    while (std::getline(file, line)) {
        if (line.compare(0, 6, "NSpid:") != 0) {
            continue;
        }
        std::istringstream values(line.substr(6));
        std::vector<int> pids;
        int pid = 0;
        while (values >> pid) {
            pids.push_back(pid);
        }
        if (pids.empty()) {
            return false;
        }
        if (host_pid != nullptr) {
            *host_pid = pids.front();
        }
        if (local_pid != nullptr) {
            *local_pid = pids.back();
        }
        return true;
    }
    return false;
}

int ReadNSpidFirst(const std::string &status_path) {
    int host_pid = 0;
    if (!ReadNSpidPair(status_path, &host_pid, nullptr)) {
        return 0;
    }
    return host_pid;
}

int HostPID() {
    static const int host_pid = ReadNSpidFirst("/proc/self/status");
    return host_pid == 0 ? static_cast<int>(getpid()) : host_pid;
}

int HostTID(int local_tid) {
    if (local_tid <= 0) {
        return local_tid;
    }
    {
        std::lock_guard<std::mutex> lock(g_pid_cache_mutex);
        const auto cached = g_tid_to_host.find(local_tid);
        if (cached != g_tid_to_host.end()) {
            return cached->second;
        }
    }
    char path[128];
    std::snprintf(path, sizeof(path), "/proc/self/task/%d/status", local_tid);
    int host_tid = ReadNSpidFirst(path);
    if (host_tid == 0) {
        // Do not cache the fallback: a thread that has just been created may
        // become visible in /proc slightly later, and the next record should
        // retry the lookup instead of permanently reporting a stale local tid.
        g_unmapped_tid_records.fetch_add(1, std::memory_order_relaxed);
        return local_tid;
    }
    {
        std::lock_guard<std::mutex> lock(g_pid_cache_mutex);
        g_tid_to_host[local_tid] = host_tid;
        g_tid_map_entries.store(g_tid_to_host.size(), std::memory_order_relaxed);
    }
    return host_tid;
}

// Refresh the local-thread -> host-thread map from /proc/self/task. msPTI
// reports the container-local tid; eBPF host spans use the outermost tid.
// Threads created after startup are only visible here, so callers run this
// periodically as well as once at start.
void RefreshTIDMap() {
    std::lock_guard<std::mutex> refresh_lock(g_tid_refresh_mutex);
    DIR *directory = opendir("/proc/self/task");
    if (directory == nullptr) {
        AddWarning("cannot open /proc/self/task for tid mapping");
        return;
    }
    std::map<int, int> updates;
    struct dirent *entry = nullptr;
    while ((entry = readdir(directory)) != nullptr) {
        const char *name = entry->d_name;
        if (name[0] < '0' || name[0] > '9') {
            continue;
        }
        char *end = nullptr;
        const long parsed = std::strtol(name, &end, 10);
        if (end == name || *end != '\0' || parsed <= 0 || parsed > 2147483647L) {
            continue;
        }
        char path[128];
        std::snprintf(path, sizeof(path), "/proc/self/task/%ld/status", parsed);
        const int host_tid = ReadNSpidFirst(path);
        if (host_tid > 0) {
            updates[static_cast<int>(parsed)] = host_tid;
        }
    }
    closedir(directory);
    {
        std::lock_guard<std::mutex> lock(g_pid_cache_mutex);
        for (const auto &item : updates) {
            g_tid_to_host[item.first] = item.second;
        }
        g_tid_map_entries.store(g_tid_to_host.size(), std::memory_order_relaxed);
    }
    g_tid_map_refresh_count.fetch_add(1, std::memory_order_relaxed);
}

// Re-read the CLOCK_REALTIME - CLOCK_MONOTONIC offset. The offset is not a
// constant when NTP steps or a container time namespace adjusts the wall
// clock; a periodic refresh keeps the normalization error bounded and makes
// the drift visible in the sidecar.
void RefreshClockOffset() {
    const uint64_t realtime = NowClockNS(CLOCK_REALTIME);
    const uint64_t monotonic = NowClockNS(CLOCK_MONOTONIC);
    const int64_t offset = realtime > monotonic
                               ? static_cast<int64_t>(realtime - monotonic)
                               : 0;
    const uint64_t prior = g_clock_refresh_count.fetch_add(1, std::memory_order_relaxed);
    if (prior == 0) {
        g_clock_offset_start_ns.store(offset, std::memory_order_relaxed);
    }
    g_clock_offset_last_ns.store(offset, std::memory_order_relaxed);
    g_clock_drift_ns.store(
        offset - g_clock_offset_start_ns.load(std::memory_order_relaxed),
        std::memory_order_relaxed);
    g_realtime_minus_monotonic_ns.store(offset, std::memory_order_relaxed);
    g_clock_last_refresh_unix_ns.store(realtime, std::memory_order_relaxed);
}

void JsonEscape(std::string &out, const char *value) {
    if (value == nullptr) {
        return;
    }
    for (const unsigned char *p = reinterpret_cast<const unsigned char *>(value);
         *p != '\0'; ++p) {
        switch (*p) {
            case '"': out += "\\\""; break;
            case '\\': out += "\\\\"; break;
            case '\b': out += "\\b"; break;
            case '\f': out += "\\f"; break;
            case '\n': out += "\\n"; break;
            case '\r': out += "\\r"; break;
            case '\t': out += "\\t"; break;
            default:
                if (*p < 0x20) {
                    char escaped[7];
                    std::snprintf(escaped, sizeof(escaped), "\\u%04x", *p);
                    out += escaped;
                } else {
                    out += static_cast<char>(*p);
                }
        }
    }
}

bool ActivityEnabled(const ActivityConfig &cfg, msptiActivityKind kind) {
    switch (kind) {
        case MSPTI_ACTIVITY_KIND_API: return cfg.api;
        case MSPTI_ACTIVITY_KIND_ACL_API: return cfg.acl_api;
        case MSPTI_ACTIVITY_KIND_RUNTIME_API: return cfg.runtime_api;
        case MSPTI_ACTIVITY_KIND_KERNEL: return cfg.kernel;
        case MSPTI_ACTIVITY_KIND_MEMCPY: return cfg.memcpy;
        case MSPTI_ACTIVITY_KIND_HCCL: return cfg.hccl;
        case MSPTI_ACTIVITY_KIND_COMMUNICATION: return cfg.communication || cfg.hccl;
        case MSPTI_ACTIVITY_KIND_MEMORY: return cfg.memory;
        case MSPTI_ACTIVITY_KIND_MEMSET: return cfg.memset;
        default: return false;
    }
}

const char *MemcpyKindName(msptiActivityMemcpyKind kind) {
    switch (kind) {
        case MSPTI_ACTIVITY_MEMCPY_KIND_HTOH: return "MEMCPY_HTOH";
        case MSPTI_ACTIVITY_MEMCPY_KIND_HTOD: return "MEMCPY_HTOD";
        case MSPTI_ACTIVITY_MEMCPY_KIND_DTOH: return "MEMCPY_DTOH";
        case MSPTI_ACTIVITY_MEMCPY_KIND_DTOD: return "MEMCPY_DTOD";
        default: return "MEMCPY_UNKNOWN";
    }
}

void AddWarning(const std::string &warning) {
    std::lock_guard<std::mutex> lock(g_meta_mutex);
    if (!g_warnings.empty()) {
        g_warnings += "; ";
    }
    g_warnings += warning;
}

bool WriteRecord(const char *kind, const char *name, int pid, int tid,
                 int device_id, long long stream_id, uint64_t start_ns,
                 uint64_t end_ns, uint64_t correlation_id, const char *source) {
    if (kind == nullptr || start_ns == 0 || end_ns < start_ns) {
        std::lock_guard<std::mutex> lock(g_meta_mutex);
        ++g_records_skipped;
        return false;
    }

    // msPTI reports CLOCK_REALTIME nanoseconds on this CANN version, while
    // bpf_ktime_get_ns() is CLOCK_MONOTONIC. Normalize to the host clock and
    // retain the raw values as extra evidence fields. CANN_MSPTI_CLOCK can
    // force auto|realtime|monotonic when a different toolkit changes this.
    uint64_t normalized_start = start_ns;
    uint64_t normalized_end = end_ns;
    bool converted = false;
    bool looks_realtime = g_clock_spec == "realtime";
    if (g_clock_spec == "auto") {
        const uint64_t now = NowUnixNS();
        const uint64_t diff = start_ns > now ? start_ns - now : now - start_ns;
        looks_realtime = diff < 86400ULL * 1000000000ULL;
    }
    const int64_t clock_offset_ns =
        g_realtime_minus_monotonic_ns.load(std::memory_order_relaxed);
    if (looks_realtime && clock_offset_ns > 0 &&
        start_ns > static_cast<uint64_t>(clock_offset_ns) &&
        end_ns > static_cast<uint64_t>(clock_offset_ns)) {
        normalized_start = start_ns - static_cast<uint64_t>(clock_offset_ns);
        normalized_end = end_ns - static_cast<uint64_t>(clock_offset_ns);
        converted = true;
    }

    std::string line;
    line.reserve(320);
    line += "{\"schema\":\"cann.device.v1\"";
    if (!g_run_id.empty()) {
        line += ",\"run_id\":\"";
        JsonEscape(line, g_run_id.c_str());
        line += "\"";
    }
    line += ",\"kind\":\"";
    JsonEscape(line, kind);
    line += "\"";
    if (name != nullptr && *name != '\0') {
        line += ",\"name\":\"";
        JsonEscape(line, name);
        line += "\"";
    }
    if (pid != 0) {
        line += ",\"pid\":" + std::to_string(pid);
    }
    if (tid != 0) {
        line += ",\"tid\":" + std::to_string(tid);
    }
    if (device_id >= 0) {
        line += ",\"device_id\":" + std::to_string(device_id);
    }
    if (stream_id >= 0) {
        line += ",\"stream_id\":" + std::to_string(stream_id);
    }
    line += ",\"start_ns\":" + std::to_string(normalized_start);
    line += ",\"end_ns\":" + std::to_string(normalized_end);
    line += ",\"duration_ns\":" + std::to_string(normalized_end - normalized_start);
    if (converted) {
        line += ",\"clock\":\"monotonic_ns\"";
        line += ",\"raw_start_ns\":" + std::to_string(start_ns);
        line += ",\"raw_end_ns\":" + std::to_string(end_ns);
    }
    line += ",\"correlation_id\":" + std::to_string(correlation_id);
    line += ",\"source\":\"";
    JsonEscape(line, source);
    line += "\"}\n";

    std::lock_guard<std::mutex> lock(g_write_mutex);
    if (g_file == nullptr) {
        return false;
    }
    if (g_max_bytes > 0 && g_bytes_written + line.size() > g_max_bytes) {
        if (!g_truncated.exchange(true)) {
            std::fprintf(stderr,
                         "cann-mspti: max_bytes %llu reached; further records are dropped\n",
                         static_cast<unsigned long long>(g_max_bytes));
        }
        std::lock_guard<std::mutex> meta_lock(g_meta_mutex);
        ++g_records_skipped;
        return false;
    }
    const size_t written = std::fwrite(line.data(), 1, line.size(), g_file);
    if (written != line.size()) {
        std::lock_guard<std::mutex> meta_lock(g_meta_mutex);
        ++g_records_skipped;
        return false;
    }
    g_bytes_written += written;
    std::lock_guard<std::mutex> meta_lock(g_meta_mutex);
    ++g_records_written;
    return true;
}

void EmitAPIRecord(msptiActivityApi *record) {
    if (record == nullptr) {
        return;
    }
    const int local_pid = static_cast<int>(record->pt.processId);
    const int local_tid = static_cast<int>(record->pt.threadId);
    const int host_pid = HostPID();
    const int host_tid = HostTID(local_tid);
    if (g_pid_filter != 0 && local_pid != g_pid_filter && host_pid != g_pid_filter) {
        return;
    }
    WriteRecord("API", record->name, host_pid, host_tid, -1, -1, record->start,
                record->end, record->correlationId, "mspti");
}

void EmitKernelRecord(msptiActivityKernel *record) {
    if (record == nullptr) {
        return;
    }
    const int device_id = record->ds.deviceId == MSPTI_INVALID_DEVICE_ID
                              ? -1
                              : static_cast<int>(record->ds.deviceId);
    const long long stream_id = record->ds.streamId == MSPTI_INVALID_STREAM_ID
                                    ? -1
                                    : static_cast<long long>(record->ds.streamId);
    WriteRecord("KERNEL", record->name, 0, 0, device_id, stream_id,
                record->start, record->end, record->correlationId, "mspti");
}

void EmitMemcpyRecord(msptiActivityMemcpy *record) {
    if (record == nullptr) {
        return;
    }
    const int device_id = record->deviceId == MSPTI_INVALID_DEVICE_ID
                              ? -1
                              : static_cast<int>(record->deviceId);
    const long long stream_id = record->streamId == MSPTI_INVALID_STREAM_ID
                                    ? -1
                                    : static_cast<long long>(record->streamId);
    WriteRecord("MEMCPY", MemcpyKindName(record->copyKind), 0, 0, device_id,
                stream_id, record->start, record->end, record->correlationId,
                "mspti");
}

void EmitHcclRecord(msptiActivityHccl *record) {
    if (record == nullptr) {
        return;
    }
    const int device_id = record->ds.deviceId == MSPTI_INVALID_DEVICE_ID
                              ? -1
                              : static_cast<int>(record->ds.deviceId);
    const long long stream_id = record->ds.streamId == MSPTI_INVALID_STREAM_ID
                                    ? -1
                                    : static_cast<long long>(record->ds.streamId);
    const std::string name = record->name == nullptr ? "HCCL" : record->name;
    WriteRecord("HCCL", name.c_str(), 0, 0, device_id, stream_id, record->start,
                record->end, 0, "mspti_hccl_no_correlation");
}

void EmitCommunicationRecord(msptiActivityCommunication *record) {
    if (record == nullptr) {
        return;
    }
    const int device_id = record->ds.deviceId == MSPTI_INVALID_DEVICE_ID
                              ? -1
                              : static_cast<int>(record->ds.deviceId);
    const long long stream_id = record->ds.streamId == MSPTI_INVALID_STREAM_ID
                                    ? -1
                                    : static_cast<long long>(record->ds.streamId);
    WriteRecord("HCCL", record->name, 0, 0, device_id, stream_id, record->start,
                record->end, record->correlationId, "mspti_communication");
}

void UserBufferRequest(uint8_t **buffer, size_t *size, size_t *maxNumRecords) {
    if (buffer == nullptr || size == nullptr || maxNumRecords == nullptr) {
        return;
    }
    void *memory = nullptr;
    if (posix_memalign(&memory, 8, kBufferSize) != 0 || memory == nullptr) {
        *buffer = nullptr;
        *size = 0;
        *maxNumRecords = 0;
        return;
    }
    *buffer = static_cast<uint8_t *>(memory);
    *size = kBufferSize;
    *maxNumRecords = 0;
}

// Tracks callbacks that are still inside msPTI code. stop() waits for this to
// reach zero before closing the output file; otherwise a late callback could
// write through a FILE* that has already been closed.
struct CallbackScope {
    CallbackScope() { g_callbacks_active.fetch_add(1, std::memory_order_relaxed); }
    ~CallbackScope() {
        if (g_callbacks_active.fetch_sub(1, std::memory_order_acq_rel) == 1) {
            g_callback_cv.notify_all();
        }
    }
};

// msPTI owns the buffer passed to UserBufferComplete. It must be released
// exactly once after all records have been consumed; records themselves are
// only valid while this callback is running, so every field is copied into a
// std::string line before the next msptiActivityGetNextRecord call.
struct BufferScope {
    explicit BufferScope(uint8_t *buffer) : buffer_(buffer) {}
    ~BufferScope() { std::free(buffer_); }
    uint8_t *buffer_;
};

void UserBufferComplete(uint8_t *buffer, size_t /*size*/, size_t validSize) {
    if (buffer == nullptr) {
        return;
    }
    CallbackScope callback_scope;
    BufferScope buffer_scope(buffer);
    g_callback_buffers_processed.fetch_add(1, std::memory_order_relaxed);
    if (validSize > 0) {
        msptiActivity *record = nullptr;
        while (true) {
            const msptiResult status = msptiActivityGetNextRecord(buffer, validSize, &record);
            if (status == MSPTI_ERROR_MAX_LIMIT_REACHED) {
                break;
            }
            if (status != MSPTI_SUCCESS) {
                AddWarning("msptiActivityGetNextRecord failed");
                break;
            }
            g_callback_records_processed.fetch_add(1, std::memory_order_relaxed);
            switch (record->kind) {
                case MSPTI_ACTIVITY_KIND_API:
                case MSPTI_ACTIVITY_KIND_ACL_API:
                case MSPTI_ACTIVITY_KIND_RUNTIME_API:
                    EmitAPIRecord(reinterpret_cast<msptiActivityApi *>(record));
                    break;
                case MSPTI_ACTIVITY_KIND_KERNEL:
                    EmitKernelRecord(reinterpret_cast<msptiActivityKernel *>(record));
                    break;
                case MSPTI_ACTIVITY_KIND_MEMCPY:
                    EmitMemcpyRecord(reinterpret_cast<msptiActivityMemcpy *>(record));
                    break;
                case MSPTI_ACTIVITY_KIND_HCCL:
                    EmitHcclRecord(reinterpret_cast<msptiActivityHccl *>(record));
                    break;
                case MSPTI_ACTIVITY_KIND_COMMUNICATION:
                    EmitCommunicationRecord(
                        reinterpret_cast<msptiActivityCommunication *>(record));
                    break;
                default:
                    std::lock_guard<std::mutex> lock(g_meta_mutex);
                    ++g_records_ignored;
                    break;
            }
        }
    }
    {
        std::lock_guard<std::mutex> lock(g_write_mutex);
        if (g_file != nullptr) {
            std::fflush(g_file);
        }
    }
}

ActivityConfig ParseActivities(const std::string &spec) {
    ActivityConfig cfg;
    if (spec.empty()) {
        return cfg;
    }
    cfg = ActivityConfig{};
    cfg.api = false;
    cfg.kernel = false;
    cfg.memcpy = false;
    cfg.hccl = false;
    size_t start = 0;
    while (start <= spec.size()) {
        const size_t comma = spec.find(',', start);
        std::string token = spec.substr(
            start, comma == std::string::npos ? std::string::npos : comma - start);
        for (char &ch : token) {
            if (ch == ' ' || ch == '\t') {
                ch = '\0';
            }
        }
        token.erase(std::remove(token.begin(), token.end(), '\0'), token.end());
        if (token == "api") cfg.api = true;
        else if (token == "acl_api") cfg.acl_api = true;
        else if (token == "runtime_api") cfg.runtime_api = true;
        else if (token == "kernel") cfg.kernel = true;
        else if (token == "memcpy") cfg.memcpy = true;
        else if (token == "hccl") cfg.hccl = true;
        else if (token == "communication") cfg.communication = true;
        else if (token == "memory") cfg.memory = true;
        else if (token == "memset") cfg.memset = true;
        if (comma == std::string::npos) {
            break;
        }
        start = comma + 1;
    }
    return cfg;
}

int EnableOne(msptiActivityKind kind, const char *label) {
    const msptiResult result = msptiActivityEnable(kind);
    if (result != MSPTI_SUCCESS) {
        AddWarning(std::string("enable ") + label + " failed: " +
                   std::to_string(static_cast<int>(result)));
        return -1;
    }
    return 0;
}

int EnableActivities(const ActivityConfig &cfg) {
    int enabled = 0;
    if (cfg.api) enabled += EnableOne(MSPTI_ACTIVITY_KIND_API, "API") == 0 ? 1 : 0;
    if (cfg.acl_api) enabled += EnableOne(MSPTI_ACTIVITY_KIND_ACL_API, "ACL_API") == 0 ? 1 : 0;
    if (cfg.runtime_api) enabled += EnableOne(MSPTI_ACTIVITY_KIND_RUNTIME_API, "RUNTIME_API") == 0 ? 1 : 0;
    if (cfg.kernel) enabled += EnableOne(MSPTI_ACTIVITY_KIND_KERNEL, "KERNEL") == 0 ? 1 : 0;
    if (cfg.memcpy) enabled += EnableOne(MSPTI_ACTIVITY_KIND_MEMCPY, "MEMCPY") == 0 ? 1 : 0;
    if (cfg.hccl) {
        enabled += EnableOne(MSPTI_ACTIVITY_KIND_HCCL, "HCCL") == 0 ? 1 : 0;
        // Communication records carry correlationId; plain HCCL records do not.
        enabled += EnableOne(MSPTI_ACTIVITY_KIND_COMMUNICATION, "COMMUNICATION") == 0 ? 1 : 0;
    }
    if (cfg.communication) enabled += EnableOne(MSPTI_ACTIVITY_KIND_COMMUNICATION, "COMMUNICATION") == 0 ? 1 : 0;
    if (cfg.memory) enabled += EnableOne(MSPTI_ACTIVITY_KIND_MEMORY, "MEMORY") == 0 ? 1 : 0;
    if (cfg.memset) enabled += EnableOne(MSPTI_ACTIVITY_KIND_MEMSET, "MEMSET") == 0 ? 1 : 0;
    return enabled;
}

void WriteMetaFile(bool final_write) {
    if (g_output_path.empty()) {
        return;
    }
    std::lock_guard<std::mutex> file_lock(g_meta_file_mutex);
    // A periodic flush racing with stop must not overwrite the final sidecar
    // with an older snapshot. The final writer always wins.
    if (!final_write && !g_running.load()) {
        return;
    }
    std::string meta_path = g_output_path + ".meta.json";
    std::FILE *file = std::fopen(meta_path.c_str(), "w");
    if (file == nullptr) {
        std::fprintf(stderr, "cann-mspti: open meta %s failed: %s\n", meta_path.c_str(),
                     std::strerror(errno));
        return;
    }

    std::string warnings;
    uint64_t bytes = 0;
    uint64_t written = 0;
    uint64_t ignored = 0;
    uint64_t skipped = 0;
    uint64_t started = 0;
    uint64_t stopped = 0;
    {
        std::lock_guard<std::mutex> lock(g_meta_mutex);
        warnings = g_warnings;
        bytes = g_bytes_written;
        written = g_records_written;
        ignored = g_records_ignored;
        skipped = g_records_skipped;
        started = g_start_unix_ns;
        stopped = g_stop_unix_ns;
    }

    std::string escaped;
    std::fprintf(file, "{\n");
    std::fprintf(file, "  \"schema\": \"cann.mspti.collector.v1\",\n");
    std::fprintf(file, "  \"version\": \"%s\",\n", kCollectorVersion);
    std::fprintf(file, "  \"pid\": %d,\n", static_cast<int>(getpid()));
    std::fprintf(file, "  \"container_id\": \"");
    escaped.clear();
    JsonEscape(escaped, g_container_id.c_str());
    std::fprintf(file, "%s\",\n", escaped.c_str());
    std::fprintf(file, "  \"run_id\": \"");
    escaped.clear();
    JsonEscape(escaped, g_run_id.c_str());
    std::fprintf(file, "%s\",\n", escaped.c_str());
    std::fprintf(file, "  \"activities\": \"");
    escaped.clear();
    JsonEscape(escaped, g_activity_spec.c_str());
    std::fprintf(file, "%s\",\n", escaped.c_str());
    std::fprintf(file, "  \"clock\": \"");
    escaped.clear();
    JsonEscape(escaped, g_clock_spec.c_str());
    std::fprintf(file, "%s\",\n", escaped.c_str());
    std::fprintf(file, "  \"realtime_minus_monotonic_ns\": %lld,\n",
                 static_cast<long long>(
                     g_realtime_minus_monotonic_ns.load(std::memory_order_relaxed)));
    std::fprintf(file, "  \"clock_offset_start_ns\": %lld,\n",
                 static_cast<long long>(
                     g_clock_offset_start_ns.load(std::memory_order_relaxed)));
    std::fprintf(file, "  \"clock_offset_last_ns\": %lld,\n",
                 static_cast<long long>(
                     g_clock_offset_last_ns.load(std::memory_order_relaxed)));
    std::fprintf(file, "  \"clock_drift_ns\": %lld,\n",
                 static_cast<long long>(
                     g_clock_drift_ns.load(std::memory_order_relaxed)));
    std::fprintf(file, "  \"clock_refresh_count\": %llu,\n",
                 static_cast<unsigned long long>(
                     g_clock_refresh_count.load(std::memory_order_relaxed)));
    std::fprintf(file, "  \"clock_last_refresh_unix_ns\": %llu,\n",
                 static_cast<unsigned long long>(
                     g_clock_last_refresh_unix_ns.load(std::memory_order_relaxed)));
    std::fprintf(file, "  \"start_unix_ns\": %llu,\n",
                 static_cast<unsigned long long>(started));
    std::fprintf(file, "  \"stop_unix_ns\": %llu,\n",
                 static_cast<unsigned long long>(stopped));
    std::fprintf(file, "  \"records_written\": %llu,\n",
                 static_cast<unsigned long long>(written));
    std::fprintf(file, "  \"records_ignored\": %llu,\n",
                 static_cast<unsigned long long>(ignored));
    std::fprintf(file, "  \"records_skipped\": %llu,\n",
                 static_cast<unsigned long long>(skipped));
    std::fprintf(file, "  \"output_bytes\": %llu,\n",
                 static_cast<unsigned long long>(bytes));
    std::fprintf(file, "  \"max_bytes\": %llu,\n",
                 static_cast<unsigned long long>(g_max_bytes));
    std::fprintf(file, "  \"truncated\": %s,\n", g_truncated.load() ? "true" : "false");
    std::fprintf(file, "  \"callback_buffers_processed\": %llu,\n",
                 static_cast<unsigned long long>(
                     g_callback_buffers_processed.load(std::memory_order_relaxed)));
    std::fprintf(file, "  \"callback_records_processed\": %llu,\n",
                 static_cast<unsigned long long>(
                     g_callback_records_processed.load(std::memory_order_relaxed)));
    std::fprintf(file, "  \"callbacks_active\": %llu,\n",
                 static_cast<unsigned long long>(
                     g_callbacks_active.load(std::memory_order_relaxed)));
    std::fprintf(file, "  \"tid_map_entries\": %llu,\n",
                 static_cast<unsigned long long>(
                     g_tid_map_entries.load(std::memory_order_relaxed)));
    std::fprintf(file, "  \"tid_map_refresh_count\": %llu,\n",
                 static_cast<unsigned long long>(
                     g_tid_map_refresh_count.load(std::memory_order_relaxed)));
    std::fprintf(file, "  \"unmapped_tid_records\": %llu,\n",
                 static_cast<unsigned long long>(
                     g_unmapped_tid_records.load(std::memory_order_relaxed)));
    std::fprintf(file, "  \"stop_complete\": %s,\n",
                 g_stop_complete.load(std::memory_order_relaxed) ? "true" : "false");
    std::fprintf(file, "  \"warnings\": \"");
    escaped.clear();
    JsonEscape(escaped, warnings.c_str());
    std::fprintf(file, "%s\"\n", escaped.c_str());
    std::fprintf(file, "}\n");
    std::fclose(file);
}

uint64_t EnvU64(const char *name, uint64_t fallback) {
    const char *value = std::getenv(name);
    if (value == nullptr || *value == '\0') {
        return fallback;
    }
    char *end = nullptr;
    const unsigned long long parsed = std::strtoull(value, &end, 10);
    if (end == value) {
        return fallback;
    }
    return static_cast<uint64_t>(parsed);
}

double EnvDouble(const char *name, double fallback) {
    const char *value = std::getenv(name);
    if (value == nullptr || *value == '\0') {
        return fallback;
    }
    char *end = nullptr;
    const double parsed = std::strtod(value, &end);
    if (end == value) {
        return fallback;
    }
    return parsed;
}

}  // namespace

extern "C" int cann_mspti_start(const char *output_path, const char *run_id,
                                const char *activities, uint64_t max_bytes,
                                int pid) {
    std::lock_guard<std::mutex> lifecycle_lock(g_lifecycle_mutex);
    if (output_path == nullptr || *output_path == '\0') {
        std::fprintf(stderr, "cann-mspti: output path is required\n");
        return 2;
    }
    if (g_running.load()) {
        std::fprintf(stderr, "cann-mspti: collector already running\n");
        return 3;
    }
    {
        std::lock_guard<std::mutex> stop_lock(g_stop_mutex);
        if (g_stop_in_progress.load()) {
            std::fprintf(stderr, "cann-mspti: previous stop is still draining\n");
            return 3;
        }
        if (g_callbacks_active.load(std::memory_order_acquire) != 0) {
            std::fprintf(stderr, "cann-mspti: previous callbacks are still active\n");
            return 3;
        }
        g_stop_complete.store(false);
    }
    if (pid != 0 && pid != static_cast<int>(getpid())) {
        std::fprintf(stderr, "cann-mspti: pid %d does not match current pid %d\n", pid,
                     static_cast<int>(getpid()));
        return 4;
    }

    g_output_path = output_path;
    g_run_id = run_id == nullptr ? "" : run_id;
    g_activity_spec = (activities == nullptr || *activities == '\0')
                          ? "api,acl_api,runtime_api,kernel,memcpy,hccl"
                          : activities;
    g_max_bytes = max_bytes == 0 ? kDefaultMaxBytes : max_bytes;
    g_pid_filter = pid;
    const char *clock_mode = std::getenv("CANN_MSPTI_CLOCK");
    g_clock_spec = (clock_mode == nullptr || *clock_mode == '\0') ? "auto" : clock_mode;
    if (g_clock_spec != "auto" && g_clock_spec != "realtime" && g_clock_spec != "monotonic") {
        std::fprintf(stderr, "cann-mspti: invalid CANN_MSPTI_CLOCK=%s\n", g_clock_spec.c_str());
        return 2;
    }
    g_container_id = ReadContainerID();
    g_realtime_minus_monotonic_ns.store(0);
    g_clock_offset_start_ns.store(0);
    g_clock_offset_last_ns.store(0);
    g_clock_drift_ns.store(0);
    g_clock_refresh_count.store(0);
    g_clock_last_refresh_unix_ns.store(0);
    g_callback_buffers_processed.store(0);
    g_callback_records_processed.store(0);
    g_tid_map_entries.store(0);
    g_tid_map_refresh_count.store(0);
    g_unmapped_tid_records.store(0);
    {
        std::lock_guard<std::mutex> lock(g_pid_cache_mutex);
        g_tid_to_host.clear();
    }
    {
        std::lock_guard<std::mutex> lock(g_meta_mutex);
        g_warnings.clear();
        g_bytes_written = 0;
        g_records_written = 0;
        g_records_ignored = 0;
        g_records_skipped = 0;
        g_start_unix_ns = NowUnixNS();
        g_stop_unix_ns = 0;
    }
    RefreshClockOffset();
    RefreshTIDMap();
    // Publish identity and container mapping immediately; a stopped or crashed
    // process still leaves a usable sidecar, and diagnose can run while the
    // service is live. The file is rewritten at stop with final counters.
    WriteMetaFile(true);
    g_truncated.store(false);
    g_flush_thread_stop.store(false);

    g_file = std::fopen(output_path, "w");
    if (g_file == nullptr) {
        std::fprintf(stderr, "cann-mspti: open %s failed: %s\n", output_path,
                     std::strerror(errno));
        return 5;
    }
    std::setvbuf(g_file, nullptr, _IOFBF, 1024 * 1024);

    // NOTE: CANN msPTI has no msptiInitialize()/msptiFinalize(). mspti.h only
    // exposes the subscriber API (verified on CANN 9.2.0: nm -D libmspti.so
    // shows msptiSubscribe/msptiUnsubscribe but no *Initialize/*Finalize, and
    // mspti_result.h has no ALREADY_INITIALIZED code). msPTI is initialized
    // implicitly by msptiSubscribe inside a process that already has
    // libmspti.so loaded through LD_PRELOAD; that is the whole "init" step.
    const msptiResult subscribe_result = msptiSubscribe(&g_subscriber, nullptr, nullptr);
    if (subscribe_result != MSPTI_SUCCESS) {
        const char *hint = "";
        switch (static_cast<int>(subscribe_result)) {
            // MSPTI_ERROR_MULTIPLE_SUBSCRIBERS_NOT_SUPPORTED: another msPTI
            // client (e.g. ms_service_profiler) already owns this process.
            case 2: hint = " (another msPTI subscriber is already registered)"; break;
            case 6: hint = " (libmspti.so must be listed first in LD_PRELOAD)"; break;
            // MSPTI_ERROR_NOT_INITIALIZED exists only in CANN >= 9.2 and means
            // msPTI could not bootstrap from the preloaded libmspti.so.
            case 7: hint = " (msPTI is not initialized; check LD_PRELOAD order)"; break;
            default: break;
        }
        std::fprintf(stderr,
                     "cann-mspti: msptiSubscribe failed: %d%s\n",
                     static_cast<int>(subscribe_result),
                     hint);
        std::fclose(g_file);
        g_file = nullptr;
        return 6;
    }
    const msptiResult callback_result =
        msptiActivityRegisterCallbacks(UserBufferRequest, UserBufferComplete);
    if (callback_result != MSPTI_SUCCESS) {
        std::fprintf(stderr, "cann-mspti: register callbacks failed: %d\n",
                     static_cast<int>(callback_result));
        msptiUnsubscribe(g_subscriber);
        g_subscriber = nullptr;
        std::fclose(g_file);
        g_file = nullptr;
        return 7;
    }

    const ActivityConfig config = ParseActivities(g_activity_spec);
    if (EnableActivities(config) == 0) {
        std::fprintf(stderr, "cann-mspti: no activity kind could be enabled\n");
        msptiUnsubscribe(g_subscriber);
        g_subscriber = nullptr;
        std::fclose(g_file);
        g_file = nullptr;
        return 8;
    }
    msptiActivityFlushPeriod(1000);
    g_running.store(true);
    g_flush_thread_stop.store(false);
    const uint64_t flush_generation = g_flush_generation.fetch_add(1) + 1;
    g_flush_wait_cv.notify_all();
    const double flush_interval_ms = EnvDouble("CANN_MSPTI_FLUSH_INTERVAL_MS", 1000.0);
    if (flush_interval_ms > 0.0) {
        std::thread([flush_interval_ms, flush_generation]() {
            const auto interval = std::chrono::milliseconds(
                static_cast<long long>(flush_interval_ms));
            std::unique_lock<std::mutex> wait_lock(g_flush_wait_mutex);
            while (!g_flush_thread_stop.load() &&
                   g_flush_generation.load() == flush_generation) {
                if (g_flush_wait_cv.wait_for(
                        wait_lock, interval,
                        [flush_generation] {
                            return g_flush_thread_stop.load() ||
                                   g_flush_generation.load() != flush_generation;
                        })) {
                    break;
                }
                wait_lock.unlock();
                cann_mspti_flush();
                wait_lock.lock();
            }
        }).detach();
    }

    const double duration_seconds = EnvDouble("CANN_MSPTI_DURATION", 0.0);
    if (duration_seconds > 0.0) {
        std::thread([duration_seconds, flush_generation]() {
            std::this_thread::sleep_for(
                std::chrono::milliseconds(static_cast<long long>(duration_seconds * 1000.0)));
            if (g_flush_generation.load() == flush_generation) {
                cann_mspti_stop();
            }
        }).detach();
    }
    return 0;
}

extern "C" int cann_mspti_flush(void) {
    {
        std::lock_guard<std::mutex> stop_lock(g_stop_mutex);
        if (g_stop_in_progress.load() || g_stop_complete.load() || !g_running.load()) {
            return 0;
        }
    }
    std::lock_guard<std::mutex> flush_lock(g_flush_mutex);
    {
        std::lock_guard<std::mutex> stop_lock(g_stop_mutex);
        if (g_stop_in_progress.load() || g_stop_complete.load() || !g_running.load()) {
            return 0;
        }
    }
    const msptiResult result = msptiActivityFlushAll(1);
    {
        std::lock_guard<std::mutex> lock(g_write_mutex);
        if (g_file != nullptr) {
            std::fflush(g_file);
        }
    }
    RefreshClockOffset();
    RefreshTIDMap();
    // Refresh the live sidecar so an operator copying it mid-run sees the
    // current record and byte counts, not only container identity.
    WriteMetaFile(false);
    return result == MSPTI_SUCCESS ? 0 : 9;
}

extern "C" int cann_mspti_stop(void) {
    std::lock_guard<std::mutex> lifecycle_lock(g_lifecycle_mutex);
    {
        std::unique_lock<std::mutex> stop_lock(g_stop_mutex);
        if (g_stop_in_progress.load()) {
            g_stop_cv.wait(stop_lock, [] { return !g_stop_in_progress.load(); });
            return 0;
        }
        if (g_stop_complete.load()) {
            return 0;
        }
        g_stop_in_progress.store(true);
    }
    const bool was_running = g_running.exchange(false);
    g_flush_thread_stop.store(true);
    g_flush_wait_cv.notify_all();

    if (was_running) {
        // Serialize with cann_mspti_flush(). Once stop_in_progress is set no
        // new flush can enter, so FlushAll/unsubscribe cannot race a periodic
        // flush from the helper thread.
        std::lock_guard<std::mutex> flush_lock(g_flush_mutex);
        msptiActivityFlushAll(1);
        if (g_subscriber != nullptr) {
            msptiUnsubscribe(g_subscriber);
            g_subscriber = nullptr;
        }
    }

    bool callbacks_drained = false;
    {
        std::unique_lock<std::mutex> callback_lock(g_callback_mutex);
        callbacks_drained = g_callback_cv.wait_for(
            callback_lock, std::chrono::seconds(5),
            [] { return g_callbacks_active.load(std::memory_order_acquire) == 0; });
    }
    if (!callbacks_drained) {
        // Never fclose() while a callback may still hold the FILE*. Leaking the
        // descriptor until process exit is safer than a use-after-free.
        AddWarning("mspti stop timed out waiting for active callbacks; output not closed");
    } else {
        std::lock_guard<std::mutex> lock(g_write_mutex);
        if (g_file != nullptr) {
            std::fflush(g_file);
            std::fclose(g_file);
            g_file = nullptr;
        }
    }

    RefreshClockOffset();
    const int64_t clock_drift_ns =
        g_clock_drift_ns.load(std::memory_order_relaxed);
    if (clock_drift_ns > 100000000LL || clock_drift_ns < -100000000LL) {
        AddWarning("clock offset drift exceeded 100ms; timestamps may be skewed");
    }
    {
        std::lock_guard<std::mutex> lock(g_meta_mutex);
        g_stop_unix_ns = NowUnixNS();
    }
    {
        // Mark the session complete before writing the final sidecar so the
        // sidecar itself reports stop_complete=true. Keep stop_in_progress set
        // until the sidecar is durable; a concurrent stop waits on that flag
        // rather than returning while meta is still being written.
        std::lock_guard<std::mutex> stop_lock(g_stop_mutex);
        g_stop_complete.store(true);
    }
    WriteMetaFile(true);
    {
        std::lock_guard<std::mutex> stop_lock(g_stop_mutex);
        g_stop_in_progress.store(false);
    }
    g_stop_cv.notify_all();
    return callbacks_drained ? 0 : 10;
}

extern "C" int cann_mspti_is_active(void) { return g_running.load() ? 1 : 0; }

// Auto-start is deliberately NOT __attribute__((constructor)). GCC emits such
// a function into .init_array ahead of this translation unit's
// _GLOBAL__sub_I_collector.cpp, so it ran before the std::string / std::map
// globals were constructed. The first `g_run_id = ...` then stored the empty
// string terminator through an uninitialized (null) _M_p and every
// LD_PRELOADed process died with SIGSEGV before main. A namespace-scope object
// declared after every global is constructed after them (ordered dynamic
// initialization within the TU) and destroyed before them.
static void CannMsptiAutoStart() {
    const char *output = std::getenv("CANN_MSPTI_OUTPUT");
    if (output == nullptr || *output == '\0') {
        return;
    }
    // LD_PRELOAD also reaches the shell entrypoint and helper binaries. msPTI
    // initialization is only valid inside the CANN/Python workload; starting it
    // in bash was observed to SIGSEGV before vLLM could run.
    const char *only_comm = std::getenv("CANN_MSPTI_ONLY_COMM");
    if (only_comm != nullptr && *only_comm != '\0') {
        std::ifstream comm_file("/proc/self/comm");
        std::string comm;
        if (std::getline(comm_file, comm)) {
            std::stringstream wanted(only_comm);
            std::string token;
            bool matched = false;
            while (std::getline(wanted, token, ',')) {
                if (!token.empty() && comm.find(token) != std::string::npos) {
                    matched = true;
                    break;
                }
            }
            if (!matched) {
                return;
            }
        }
    }
    // {pid} lets a service-level LD_PRELOAD give every worker its own file;
    // otherwise all ranks would race on one output path.
    std::string output_path(output);
    const std::string pid_token = "{pid}";
    const auto pid_pos = output_path.find(pid_token);
    if (pid_pos != std::string::npos) {
        output_path.replace(pid_pos, pid_token.size(), std::to_string(static_cast<long long>(getpid())));
    }
    const char *run_id = std::getenv("CANN_MSPTI_RUN_ID");
    const char *activities = std::getenv("CANN_MSPTI_ACTIVITIES");
    const uint64_t max_bytes = EnvU64("CANN_MSPTI_MAX_BYTES", 0);
    const int pid = static_cast<int>(EnvU64("CANN_MSPTI_PID", 0));
    if (cann_mspti_start(output_path.c_str(), run_id, activities, max_bytes, pid) == 0) {
        g_auto_started.store(true);
    }
}

static void CannMsptiAutoStop() {
    if (g_auto_started.load()) {
        cann_mspti_stop();
    }
}

namespace {

struct AutoLifecycle {
    AutoLifecycle() { CannMsptiAutoStart(); }
    ~AutoLifecycle() { CannMsptiAutoStop(); }
};

// Declared last on purpose: it must be constructed after g_output_path,
// g_run_id, g_activity_spec, ... (and therefore destroyed before them).
AutoLifecycle g_auto_lifecycle;

}  // namespace
