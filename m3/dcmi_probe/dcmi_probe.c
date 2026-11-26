// dcmi_probe: read-only DCMI metrics probe.
//
// It emits one JSON object per device on stdout, using the same
// cann.hardware.v1 schema as the npu-smi backend. It never initializes the
// NPU, never allocates device memory and never touches a running service.
//
// Build on the target (needs the DCMI headers/lib):
//   gcc -O2 -o dcmi_probe dcmi_probe.c \
//     -I/usr/local/Ascend/driver/include \
//     -L/usr/local/Ascend/driver/lib64/driver -ldcmi \
//     -Wl,-rpath,/usr/local/Ascend/driver/lib64/driver
#include <dcmi_interface_api.h>
#include <stdio.h>
#include <stdlib.h>
#include <time.h>

static long long now_ns(void) {
    struct timespec ts;
    clock_gettime(CLOCK_REALTIME, &ts);
    return (long long)ts.tv_sec * 1000000000LL + ts.tv_nsec;
}

static long long to_mb(unsigned long long value) {
    // The driver historically reports HBM in MB, but some releases use bytes.
    if (value > 10000000ULL) {
        return (long long)(value / (1024ULL * 1024ULL));
    }
    return (long long)value;
}

int main(void) {
    int ret = dcmi_init();
    if (ret != 0) {
        fprintf(stderr, "dcmi_init failed: %d\n", ret);
        return 3;
    }
    int card_num = 32;
    int cards[32];
    ret = dcmi_get_card_list(&card_num, cards, 32);
    if (ret != 0 || card_num <= 0) {
        fprintf(stderr, "dcmi_get_card_list failed: %d card_num=%d\n", ret, card_num);
        return 4;
    }
    int device_id = 0;
    for (int i = 0; i < card_num; ++i) {
        int card = cards[i];
        int dev_num = 0;
        ret = dcmi_get_device_num_in_card(card, &dev_num);
        if (ret != 0 || dev_num <= 0) {
            fprintf(stderr, "card %d device_num failed: %d num=%d\n", card, ret, dev_num);
            continue;
        }
        for (int d = 0; d < dev_num; ++d, ++device_id) {
            int power = -1;
            int temp = -1;
            unsigned int aicore = 0;
            struct dcmi_hbm_info hbm;
            unsigned long long used = 0;
            unsigned long long total = 0;
            (void)dcmi_get_device_power_info(card, d, &power);
            (void)dcmi_get_device_temperature(card, d, &temp);
            if (dcmi_get_device_utilization_rate(card, d, DCMI_UTILIZATION_RATE_AICORE, &aicore) != 0) {
                aicore = 0;
            }
            if (dcmi_get_device_hbm_info(card, d, &hbm) == 0) {
                used = hbm.memory_usage;
                total = hbm.memory_size;
            }
            /* This driver reports chip power in 0.1 W units; keep the raw int
             * available and normalize to watts in the schema field. */
            double power_w = power >= 0 ? (double)power / 10.0 : -1.0;
            printf("{\"schema\":\"cann.hardware.v1\",\"ts_ns\":%lld,\"device_id\":%d,"
                   "\"name\":\"910B2\",\"health\":\"OK\",\"aicore_pct\":%u,"
                   "\"hbm_used_mb\":%lld,\"hbm_total_mb\":%lld,"
                   "\"power_w\":%.1f,\"temp_c\":%d,\"source\":\"dcmi\"}\n",
                   now_ns(), device_id, aicore, to_mb(used), to_mb(total), power_w, temp);
        }
    }
    if (device_id == 0) {
        fprintf(stderr, "no DCMI devices found\n");
        return 5;
    }
    return 0;
}
