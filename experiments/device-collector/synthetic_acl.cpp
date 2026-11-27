// Synthetic ACL workload used to validate msPTI correlation and the
// cann.device.v1 join path without touching the production V4/Orinfer process.

#include "collector.h"

#include <aclnnop/aclnn_add.h>
#include <acl/acl.h>

#include <chrono>
#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <string>
#include <thread>
#include <vector>

namespace {

void CheckACL(aclError result, const char *call) {
    if (result != ACL_SUCCESS) {
        std::fprintf(stderr, "ACL call failed: %s (%d)\n", call, static_cast<int>(result));
        std::exit(3);
    }
}

int64_t ShapeSize(const std::vector<int64_t> &shape) {
    int64_t size = 1;
    for (int64_t dim : shape) {
        size *= dim;
    }
    return size;
}

void CreateTensor(const std::vector<float> &host, const std::vector<int64_t> &shape,
                  void **device, aclTensor **tensor) {
    const size_t bytes = host.size() * sizeof(float);
    CheckACL(aclrtMalloc(device, bytes, ACL_MEM_MALLOC_HUGE_FIRST), "aclrtMalloc");
    CheckACL(aclrtMemcpy(*device, bytes, host.data(), bytes, ACL_MEMCPY_HOST_TO_DEVICE),
             "aclrtMemcpy(H2D)");
    std::vector<int64_t> strides(shape.size(), 1);
    for (int64_t i = static_cast<int64_t>(shape.size()) - 2; i >= 0; --i) {
        strides[static_cast<size_t>(i)] = shape[static_cast<size_t>(i + 1)] *
                                          strides[static_cast<size_t>(i + 1)];
    }
    *tensor = aclCreateTensor(shape.data(), shape.size(), ACL_FLOAT, strides.data(), 0,
                              ACL_FORMAT_ND, shape.data(), shape.size(), *device);
    if (*tensor == nullptr) {
        std::fprintf(stderr, "aclCreateTensor failed\n");
        std::exit(3);
    }
}

void OneAdd(aclrtStream stream) {
    const std::vector<int64_t> shape = {4, 2};
    const std::vector<float> lhs = {0, 1, 2, 3, 4, 5, 6, 7};
    const std::vector<float> rhs = {1, 1, 1, 2, 2, 2, 3, 3};
    const std::vector<float> initial = {0, 0, 0, 0, 0, 0, 0, 0};
    void *lhs_device = nullptr;
    void *rhs_device = nullptr;
    void *out_device = nullptr;
    aclTensor *lhs_tensor = nullptr;
    aclTensor *rhs_tensor = nullptr;
    aclTensor *out_tensor = nullptr;
    CreateTensor(lhs, shape, &lhs_device, &lhs_tensor);
    CreateTensor(rhs, shape, &rhs_device, &rhs_tensor);
    CreateTensor(initial, shape, &out_device, &out_tensor);
    float alpha_value = 1.0f;
    aclScalar *alpha = aclCreateScalar(&alpha_value, ACL_FLOAT);
    if (alpha == nullptr) {
        std::fprintf(stderr, "aclCreateScalar failed\n");
        std::exit(3);
    }

    uint64_t workspace_size = 0;
    aclOpExecutor *executor = nullptr;
    CheckACL(aclnnAddGetWorkspaceSize(lhs_tensor, rhs_tensor, alpha, out_tensor,
                                      &workspace_size, &executor),
             "aclnnAddGetWorkspaceSize");
    void *workspace = nullptr;
    if (workspace_size > 0) {
        CheckACL(aclrtMalloc(&workspace, workspace_size, ACL_MEM_MALLOC_HUGE_FIRST),
                 "aclrtMalloc(workspace)");
    }
    CheckACL(aclnnAdd(workspace, workspace_size, executor, stream), "aclnnAdd");
    CheckACL(aclrtSynchronizeStream(stream), "aclrtSynchronizeStream");

    std::vector<float> result(static_cast<size_t>(ShapeSize(shape)), 0.0f);
    CheckACL(aclrtMemcpy(result.data(), result.size() * sizeof(float), out_device,
                         result.size() * sizeof(float), ACL_MEMCPY_DEVICE_TO_HOST),
             "aclrtMemcpy(D2H)");

    aclDestroyTensor(lhs_tensor);
    aclDestroyTensor(rhs_tensor);
    aclDestroyTensor(out_tensor);
    aclDestroyScalar(alpha);
    aclrtFree(lhs_device);
    aclrtFree(rhs_device);
    aclrtFree(out_device);
    if (workspace != nullptr) {
        aclrtFree(workspace);
    }
}

}  // namespace

int main(int argc, char **argv) {
    std::string output = "/tmp/cann-mspti-synthetic/device.jsonl";
    std::string run_id = "synthetic";
    double seconds = 10.0;
    for (int i = 1; i < argc; ++i) {
        const std::string arg = argv[i];
        if (arg == "--output" && i + 1 < argc) {
            output = argv[++i];
        } else if (arg == "--run-id" && i + 1 < argc) {
            run_id = argv[++i];
        } else if (arg == "--duration" && i + 1 < argc) {
            seconds = std::strtod(argv[++i], nullptr);
        } else if (arg == "--help" || arg == "-h") {
            std::printf("usage: synthetic_acl --output device.jsonl --run-id ID --duration SEC\n");
            return 0;
        }
    }

    // msPTI must be subscribed before ACL initializes.
    const char *activities = std::getenv("CANN_MSPTI_ACTIVITIES");
    if (cann_mspti_start(output.c_str(), run_id.c_str(),
                         activities == nullptr ? "api,acl_api,runtime_api,kernel,memcpy,hccl" : activities,
                         0, 0) != 0) {
        return 2;
    }

    CheckACL(aclInit(nullptr), "aclInit");
    CheckACL(aclrtSetDevice(0), "aclrtSetDevice");
    aclrtContext context = nullptr;
    aclrtStream stream = nullptr;
    CheckACL(aclrtCreateContext(&context, 0), "aclrtCreateContext");
    CheckACL(aclrtSetCurrentContext(context), "aclrtSetCurrentContext");
    CheckACL(aclrtCreateStream(&stream), "aclrtCreateStream");

    const auto deadline = std::chrono::steady_clock::now() +
                          std::chrono::milliseconds(static_cast<long long>(seconds * 1000.0));
    int iterations = 0;
    while (std::chrono::steady_clock::now() < deadline) {
        OneAdd(stream);
        ++iterations;
        std::this_thread::sleep_for(std::chrono::milliseconds(10));
    }
    std::printf("synthetic_acl iterations=%d output=%s\n", iterations, output.c_str());

    CheckACL(aclrtDestroyStream(stream), "aclrtDestroyStream");
    CheckACL(aclrtDestroyContext(context), "aclrtDestroyContext");
    CheckACL(aclrtResetDevice(0), "aclrtResetDevice");
    CheckACL(aclFinalize(), "aclFinalize");
    cann_mspti_stop();
    return 0;
}
