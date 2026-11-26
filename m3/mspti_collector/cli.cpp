// Small launcher/self-test front end for libcannmspti.
//
// Production use is the LD_PRELOAD library; this CLI exists so operators can
// validate arguments and start a child process with the collector preloaded.

#include "collector.h"

#include <cerrno>
#include <chrono>
#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <string>
#include <thread>
#include <vector>

#include <limits.h>
#include <sys/types.h>
#include <sys/wait.h>
#include <unistd.h>

namespace {

struct Options {
    std::string output;
    std::string run_id;
    std::string activities = "api,acl_api,runtime_api,kernel,memcpy,hccl";
    std::string duration = "0";
    std::string max_bytes = "0";
    std::string pid = "0";
    std::string collector_lib;
    std::string mspti_lib;
    std::vector<std::string> command;
};

void Usage() {
    std::fprintf(stderr,
                 "usage: cann-mspti-collector --output FILE --run-id ID [options] [--exec CMD ...]\n"
                 "  --output FILE          destination JSONL (required)\n"
                 "  --run-id ID            run identifier\n"
                 "  --duration SECONDS     stop after N seconds (0 = until exit)\n"
                 "  --activities LIST      api,acl_api,runtime_api,kernel,memcpy,hccl,...\n"
                 "  --max-bytes N          hard output cap; 0 = 512 MiB\n"
                 "  --pid PID              self mode safety check\n"
                 "  --collector-lib PATH   libcannmspti.so used for --exec\n"
                 "  --mspti-lib PATH       libmspti.so used for --exec\n"
                 "  --exec CMD [args...]   fork/exec with LD_PRELOAD and environment\n");
}

bool ParseArg(const std::string &arg, const char *name, std::string *out, int argc,
              char **argv, int *index) {
    const std::string prefix = std::string(name) + "=";
    if (arg == name) {
        if (*index + 1 >= argc) {
            return false;
        }
        *out = argv[++(*index)];
        return true;
    }
    if (arg.compare(0, prefix.size(), prefix) == 0) {
        *out = arg.substr(prefix.size());
        return true;
    }
    return false;
}

bool Parse(int argc, char **argv, Options *options) {
    for (int i = 1; i < argc; ++i) {
        const std::string arg = argv[i];
        if (arg == "--help" || arg == "-h") {
            Usage();
            std::exit(0);
        }
        if (arg == "--exec") {
            for (int j = i + 1; j < argc; ++j) {
                options->command.push_back(argv[j]);
            }
            return !options->command.empty();
        }
        if (ParseArg(arg, "--output", &options->output, argc, argv, &i)) continue;
        if (ParseArg(arg, "--run-id", &options->run_id, argc, argv, &i)) continue;
        if (ParseArg(arg, "--duration", &options->duration, argc, argv, &i)) continue;
        if (ParseArg(arg, "--activities", &options->activities, argc, argv, &i)) continue;
        if (ParseArg(arg, "--max-bytes", &options->max_bytes, argc, argv, &i)) continue;
        if (ParseArg(arg, "--pid", &options->pid, argc, argv, &i)) continue;
        if (ParseArg(arg, "--collector-lib", &options->collector_lib, argc, argv, &i)) continue;
        if (ParseArg(arg, "--mspti-lib", &options->mspti_lib, argc, argv, &i)) continue;
        std::fprintf(stderr, "unknown argument: %s\n", arg.c_str());
        return false;
    }
    return true;
}

std::string ExecutableDir() {
    char path[PATH_MAX + 1];
    const ssize_t length = readlink("/proc/self/exe", path, PATH_MAX);
    if (length <= 0) {
        return ".";
    }
    path[length] = '\0';
    std::string value(path);
    const size_t slash = value.find_last_of('/');
    return slash == std::string::npos ? "." : value.substr(0, slash);
}

std::string DefaultCollectorLib(const std::string &explicit_path) {
    if (!explicit_path.empty()) {
        return explicit_path;
    }
    const char *env = std::getenv("CANN_MSPTI_COLLECTOR_LIB");
    if (env != nullptr && *env != '\0') {
        return env;
    }
    return ExecutableDir() + "/libcannmspti.so";
}

std::string DefaultMsptiLib(const std::string &explicit_path) {
    if (!explicit_path.empty()) {
        return explicit_path;
    }
    const char *env = std::getenv("CANN_MSPTI_MSPTI_LIB");
    if (env != nullptr && *env != '\0') {
        return env;
    }
    const char *ascend = std::getenv("ASCEND_HOME_PATH");
    const std::string root = (ascend != nullptr && *ascend != '\0')
                                 ? ascend
                                 : "/usr/local/Ascend/ascend-toolkit/latest";
    return root + "/tools/mspti/lib64/libmspti.so";
}

int RunExec(const Options &options) {
    const std::string collector_lib = DefaultCollectorLib(options.collector_lib);
    const std::string mspti_lib = DefaultMsptiLib(options.mspti_lib);
    if (access(collector_lib.c_str(), R_OK) != 0) {
        std::fprintf(stderr, "collector library not readable: %s (%s)\n",
                     collector_lib.c_str(), std::strerror(errno));
        return 2;
    }
    if (access(mspti_lib.c_str(), R_OK) != 0) {
        std::fprintf(stderr, "msPTI library not readable: %s (%s)\n", mspti_lib.c_str(),
                     std::strerror(errno));
        return 2;
    }

    const pid_t pid = fork();
    if (pid < 0) {
        std::fprintf(stderr, "fork failed: %s\n", std::strerror(errno));
        return 2;
    }
    if (pid == 0) {
        const std::string preload = mspti_lib + ":" + collector_lib;
        setenv("LD_PRELOAD", preload.c_str(), 1);
        setenv("CANN_MSPTI_OUTPUT", options.output.c_str(), 1);
        setenv("CANN_MSPTI_RUN_ID", options.run_id.c_str(), 1);
        setenv("CANN_MSPTI_ACTIVITIES", options.activities.c_str(), 1);
        setenv("CANN_MSPTI_DURATION", options.duration.c_str(), 1);
        setenv("CANN_MSPTI_MAX_BYTES", options.max_bytes.c_str(), 1);
        std::vector<char *> argv;
        argv.reserve(options.command.size() + 1);
        for (const std::string &item : options.command) {
            argv.push_back(const_cast<char *>(item.c_str()));
        }
        argv.push_back(nullptr);
        execvp(argv[0], argv.data());
        std::fprintf(stderr, "exec %s failed: %s\n", argv[0], std::strerror(errno));
        _exit(127);
    }

    int status = 0;
    while (waitpid(pid, &status, 0) < 0) {
        if (errno != EINTR) {
            std::fprintf(stderr, "waitpid failed: %s\n", std::strerror(errno));
            return 2;
        }
    }
    if (WIFEXITED(status)) {
        return WEXITSTATUS(status);
    }
    if (WIFSIGNALED(status)) {
        std::fprintf(stderr, "child terminated by signal %d\n", WTERMSIG(status));
        return 128 + WTERMSIG(status);
    }
    return 2;
}

int ParseInt(const std::string &value, int fallback) {
    char *end = nullptr;
    const long parsed = std::strtol(value.c_str(), &end, 10);
    return end == value.c_str() ? fallback : static_cast<int>(parsed);
}

int RunSelf(const Options &options) {
    const int pid = ParseInt(options.pid, 0);
    if (cann_mspti_start(options.output.c_str(), options.run_id.c_str(),
                         options.activities.c_str(),
                         static_cast<uint64_t>(std::strtoull(options.max_bytes.c_str(), nullptr, 10)),
                         pid) != 0) {
        return 2;
    }
    const double seconds = std::strtod(options.duration.c_str(), nullptr);
    if (seconds > 0) {
        std::this_thread::sleep_for(
            std::chrono::milliseconds(static_cast<long long>(seconds * 1000.0)));
    }
    cann_mspti_stop();
    return 0;
}

}  // namespace

int main(int argc, char **argv) {
    Options options;
    if (!Parse(argc, argv, &options) || options.output.empty()) {
        Usage();
        return 2;
    }
    if (!options.command.empty()) {
        return RunExec(options);
    }
    std::fprintf(stderr,
                 "cann-mspti: self mode collects only this helper process; use --exec for a target\n");
    return RunSelf(options);
}
