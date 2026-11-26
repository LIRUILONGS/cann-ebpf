// Minimal API-level selftest for libcannmspti. It exercises the lifecycle
// contract without requiring a real ACL workload or msPTI subscription:
// idempotent stop, flush before start, and configuration error handling.

#include "collector.h"

#include <cstdio>
#include <cstdlib>
#include <string>
#include <thread>
#include <unistd.h>
#include <vector>

namespace {

int g_failures = 0;

void Check(bool condition, const char *name) {
    if (condition) {
        std::printf("ok   %s\n", name);
        return;
    }
    std::printf("FAIL %s\n", name);
    ++g_failures;
}

}  // namespace

int main() {
    Check(cann_mspti_is_active() == 0, "is_active before start");
    Check(cann_mspti_flush() == 0, "flush before start");
    Check(cann_mspti_stop() == 0, "stop before start");
    Check(cann_mspti_stop() == 0, "stop is idempotent");

    char template_path[] = "/tmp/cann-mspti-selftest-XXXXXX";
    const int fd = mkstemp(template_path);
    if (fd >= 0) {
        close(fd);
    }

    setenv("CANN_MSPTI_CLOCK", "invalid-clock-mode", 1);
    Check(cann_mspti_start(template_path, "selftest", nullptr, 0, 0) == 2,
          "invalid clock is rejected");
    unsetenv("CANN_MSPTI_CLOCK");

    const int started = cann_mspti_start(template_path, "selftest", nullptr, 0, 0);
    if (started == 0) {
        std::vector<int> results(8, -1);
        std::vector<std::thread> threads;
        threads.reserve(results.size());
        for (size_t i = 0; i < results.size(); ++i) {
            threads.emplace_back([&results, i] { results[i] = cann_mspti_stop(); });
        }
        for (auto &thread : threads) {
            thread.join();
        }
        bool all_ok = true;
        for (int result : results) {
            all_ok = all_ok && result == 0;
        }
        Check(all_ok, "concurrent stop returns 0");
        Check(cann_mspti_is_active() == 0, "is_active after concurrent stop");
    } else {
        std::printf("skip concurrent stop: start returned %d (LD_PRELOAD?)\n", started);
    }

    Check(cann_mspti_start("/nonexistent/cann-mspti/selftest.jsonl", "selftest",
                           nullptr, 0, 0) == 5,
          "unwritable output is rejected");
    Check(cann_mspti_stop() == 0, "stop after failed start");
    Check(cann_mspti_stop() == 0, "second stop after failed start");

    if (fd >= 0) {
        unlink(template_path);
        const std::string meta_path = std::string(template_path) + ".meta.json";
        unlink(meta_path.c_str());
    }

    if (g_failures != 0) {
        std::fprintf(stderr, "%d selftest failure(s)\n", g_failures);
        return 1;
    }
    std::printf("all selftests passed\n");
    return 0;
}
