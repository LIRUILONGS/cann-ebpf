// dcmi_probe: read-only DCMI metrics probe.
//
// It emits one JSON object per device on stdout, using the same
// cann.hardware.v1 schema as the npu-smi backend. It never initializes the
// NPU, never allocates device memory and never touches a running service.
//
// Build on the target (needs the DCMI headers/lib):
//   gcc -O2 -o dcmi_probe dcmi_probe.c -I/usr/local/Ascend/driver/include -L/usr/local/Ascend/driver/lib64/driver -ldcmi -Wl,-rpath,/usr/local/Ascend/driver/lib64/driver
//
// Basic mode is the default. Extended mode is enabled with --extended or
// DCMI_PROBE_EXTENDED=1. Extended mode adds scalar metrics that are useful for
// diagnosing idle/thermal/frequency behaviour; it does not provide kernel-level
// profiling, FLOPs, occupancy, stall reasons or per-kernel HBM bandwidth.
#include <dcmi_interface_api.h>
#include <math.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>

static int g_extended = 0;

static long long now_ns(void) {
    struct timespec ts;
    clock_gettime(CLOCK_REALTIME, &ts);
    return (long long)ts.tv_sec * 1000000000LL + ts.tv_nsec;
}

static long long to_mb(unsigned long long value) {
    // DCMI documentation is inconsistent across releases: some report MB and
    // some treat the field as bytes. The threshold keeps both plausible.
    if (value > 10000000ULL) {
        return (long long)(value / (1024ULL * 1024ULL));
    }
    return (long long)value;
}

static const char *health_string(unsigned int health) {
    switch (health) {
        case 0: return "OK";
        case 1: return "Warning";
        case 2: return "Alarm";
        case 3: return "Critical";
        default: return "Unknown";
    }
}

static void json_escape(const char *value) {
    const unsigned char *p = (const unsigned char *)value;
    for (; p != NULL && *p != 0; ++p) {
        switch (*p) {
            case 0x22:
                putchar(0x5c); putchar(0x22); break;
            case 0x5c:
                putchar(0x5c); putchar(0x5c); break;
            case 0x08: putchar(0x5c); putchar('b'); break;
            case 0x0c: putchar(0x5c); putchar('f'); break;
            case 0x0a: putchar(0x5c); putchar('n'); break;
            case 0x0d: putchar(0x5c); putchar('r'); break;
            case 0x09: putchar(0x5c); putchar('t'); break;
            default:
                if (*p < 0x20) {
                    putchar(0x5c); putchar('u'); printf("%04x", (unsigned int)*p);
                } else {
                    putchar(*p);
                }
        }
    }
}

typedef struct {
    int first;
} json_writer;

static void jw_sep(json_writer *w) {
    if (!w->first) {
        putchar(',');
    }
    w->first = 0;
}

static void jw_str(json_writer *w, const char *key, const char *value) {
    if (value == NULL || value[0] == 0) {
        return;
    }
    jw_sep(w);
    printf("\"%s\":\"", key);
    json_escape(value);
    putchar('"');
}

static void jw_int(json_writer *w, const char *key, long long value) {
    jw_sep(w);
    printf("\"%s\":%lld", key, value);
}

static void jw_num(json_writer *w, const char *key, double value) {
    if (!isfinite(value)) {
        return;
    }
    jw_sep(w);
    printf("\"%s\":%.6g", key, value);
}

static void jw_bool(json_writer *w, const char *key, int value) {
    jw_sep(w);
    printf("\"%s\":%s", key, value ? "true" : "false");
}

static void warn_query(const char *name, int device, int ret) {
    fprintf(stderr, "dcmi_probe: warning: device %d %s failed: %d\n", device, name, ret);
}

static int read_sensor_temp(int card, int device, enum dcmi_manager_sensor_id sensor_id, double *out) {
    union dcmi_sensor_info info;
    memset(&info, 0, sizeof(info));
    int ret = dcmi_get_device_sensor_info(card, device, sensor_id, &info);
    if (ret != 0) {
        return ret;
    }
    // Measured on 910B2 / driver 25.2.1 (2026-11-28): the sensor union's
    // signed iint arm matches npu-smi -t sensors for SOC/SFP/N_DIE/HBM.
    *out = (double)info.iint;
    return 0;
}

static void read_utilization(json_writer *w, int card, int device, int device_id,
                             int rate_type, const char *name, const char *key) {
    unsigned int value = 0;
    int ret = dcmi_get_device_utilization_rate(card, device, rate_type, &value);
    if (ret == 0) {
        jw_num(w, key, (double)value);
    } else {
        warn_query(name, device_id, ret);
    }
}

static void emit_device(int card, int device, int device_id) {
    json_writer w = {1};
    char name[128] = {0};
    struct dcmi_chip_info_v2 chip;
    memset(&chip, 0, sizeof(chip));
    if (dcmi_get_device_chip_info_v2(card, device, &chip) == 0) {
        const char *chip_name = (const char *)chip.npu_name;
        if (chip_name[0] == 0) {
            chip_name = (const char *)chip.chip_name;
        }
        snprintf(name, sizeof(name), "%s", chip_name);
    }

    unsigned int health = 0xffffffffU;
    int health_ret = dcmi_get_device_health(card, device, &health);
    int health_ok = health_ret == 0;
    if (!health_ok) {
        warn_query("health", device_id, health_ret);
    }
    unsigned int aicore = 0;
    int aicore_ret = dcmi_get_device_utilization_rate(card, device, DCMI_UTILIZATION_RATE_AICORE, &aicore);
    int aicore_ok = aicore_ret == 0;
    if (!aicore_ok) {
        warn_query("AICore utilization", device_id, aicore_ret);
    }
    int power = -1;
    int power_ok = dcmi_get_device_power_info(card, device, &power) == 0 && power >= 0;
    int temp = -1;
    int temp_ok = dcmi_get_device_temperature(card, device, &temp) == 0 && temp >= 0;
    struct dcmi_hbm_info hbm;
    memset(&hbm, 0, sizeof(hbm));
    int hbm_ret = dcmi_get_device_hbm_info(card, device, &hbm);
    int hbm_ok = hbm_ret == 0;

    putchar('{');
    jw_str(&w, "schema", "cann.hardware.v1");
    jw_int(&w, "ts_ns", now_ns());
    jw_int(&w, "device_id", device_id);
    jw_str(&w, "name", name);
    if (health_ok) {
        jw_str(&w, "health", health_string(health));
        jw_int(&w, "health_code", (long long)health);
    }
    if (aicore_ok) {
        jw_num(&w, "aicore_pct", (double)aicore);
    }
    if (hbm_ok) {
        long long used = to_mb(hbm.memory_usage);
        long long total = to_mb(hbm.memory_size);
        jw_int(&w, "hbm_used_mb", used);
        jw_int(&w, "hbm_total_mb", total);
        if (total >= used) {
            jw_int(&w, "hbm_free_mb", total - used);
        }
    }
    if (power_ok) {
        jw_num(&w, "power_w", (double)power / 10.0);
    }
    if (temp_ok) {
        jw_num(&w, "temp_c", (double)temp);
    }
    jw_str(&w, "source", "dcmi");

    if (g_extended) {
        unsigned int value = 0;
        int ret = 0;
        double number = 0.0;

        if (!hbm_ok) {
            warn_query("HBM info", device_id, hbm_ret);
        }
        read_utilization(&w, card, device, device_id, DCMI_UTILIZATION_RATE_VECTORCORE,
                         "AIVector utilization", "aivector_pct");
        read_utilization(&w, card, device, device_id, DCMI_UTILIZATION_RATE_AICPU,
                         "AICPU utilization", "aicpu_pct");
        read_utilization(&w, card, device, device_id, DCMI_UTILIZATION_RATE_CTRLCPU,
                         "CtrlCPU utilization", "ctrlcpu_pct");
        read_utilization(&w, card, device, device_id, DCMI_UTILIZATION_RATE_NPU,
                         "NPU utilization", "npu_pct");
        read_utilization(&w, card, device, device_id, DCMI_UTILIZATION_RATE_HBM_BANDWIDTH,
                         "HBM bandwidth utilization", "hbm_bandwidth_util_pct");

        unsigned int aicore_cur = 0;
        unsigned int aicore_max = 0;
        ret = dcmi_get_device_frequency(card, device, DCMI_FREQ_AICORE_CURRENT_, &aicore_cur);
        if (ret == 0) {
            jw_num(&w, "aicore_clock_mhz", (double)aicore_cur);
        } else {
            warn_query("AICore current frequency", device_id, ret);
        }
        ret = dcmi_get_device_frequency(card, device, DCMI_FREQ_AICORE_MAX, &aicore_max);
        if (ret == 0) {
            jw_num(&w, "aicore_clock_max_mhz", (double)aicore_max);
        } else {
            warn_query("AICore max frequency", device_id, ret);
        }
        if (aicore_cur > 0 && aicore_max > 0) {
            jw_num(&w, "aicore_clock_ratio", (double)aicore_cur / (double)aicore_max);
        }
        ret = dcmi_get_device_frequency(card, device, DCMI_FREQ_HBM, &value);
        if (ret == 0) {
            jw_num(&w, "hbm_clock_mhz", (double)value);
        } else {
            warn_query("HBM frequency", device_id, ret);
        }
        ret = dcmi_get_device_frequency(card, device, DCMI_FREQ_CTRLCPU, &value);
        if (ret == 0) {
            jw_num(&w, "ctrlcpu_clock_mhz", (double)value);
        } else {
            warn_query("CtrlCPU frequency", device_id, ret);
        }
        // VectorCore frequency is unsupported on the 910B2 / driver 25.2.1
        // used for validation (DCMI returns -8255); omit it instead of
        // inventing a value, and keep the error visible in the probe log.
        int vector_ret = dcmi_get_device_frequency(card, device, DCMI_FREQ_VECTORCORE_CURRENT, &value);
        if (vector_ret == 0) {
            jw_num(&w, "vectorcore_clock_mhz", (double)value);
        } else {
            warn_query("VectorCore frequency", device_id, vector_ret);
        }

        if (hbm_ok && hbm.temp >= 0) {
            jw_num(&w, "hbm_temp_c", (double)hbm.temp);
        }
        int sensor_ret = read_sensor_temp(card, device, DCMI_SOC_TEMP_ID, &number);
        if (sensor_ret == 0) {
            jw_num(&w, "soc_temp_c", number);
        } else {
            warn_query("SOC temperature", device_id, sensor_ret);
        }
        sensor_ret = read_sensor_temp(card, device, DCMI_N_DIE_TEMP_ID, &number);
        if (sensor_ret == 0) {
            jw_num(&w, "ndie_temp_c", number);
        } else {
            warn_query("N_DIE temperature", device_id, sensor_ret);
        }
        sensor_ret = read_sensor_temp(card, device, DCMI_FP_TEMP_ID, &number);
        if (sensor_ret == 0) {
            jw_num(&w, "sfp_temp_c", number);
        } else {
            warn_query("SFP temperature", device_id, sensor_ret);
        }

        int error_count = -1;
        unsigned int error_codes[16];
        memset(error_codes, 0, sizeof(error_codes));
        ret = dcmi_get_device_errorcode_v2(card, device, &error_count, error_codes, 16);
        if (ret == 0 && error_count >= 0) {
            jw_int(&w, "health_error_count", (long long)error_count);
        } else if (ret != 0) {
            warn_query("health error count", device_id, ret);
        }

        struct dcmi_ecc_info ecc;
        memset(&ecc, 0, sizeof(ecc));
        ret = dcmi_get_device_ecc_info(card, device, DCMI_DEVICE_TYPE_HBM, &ecc);
        if (ret == 0) {
            jw_int(&w, "ecc_single_bit_errors", (long long)ecc.single_bit_error_cnt);
            jw_int(&w, "ecc_double_bit_errors", (long long)ecc.double_bit_error_cnt);
            jw_int(&w, "ecc_single_bit_total", (long long)ecc.total_single_bit_error_cnt);
            jw_int(&w, "ecc_double_bit_total", (long long)ecc.total_double_bit_error_cnt);
            jw_int(&w, "ecc_single_bit_isolated_pages", (long long)ecc.single_bit_isolated_pages_cnt);
            jw_int(&w, "ecc_double_bit_isolated_pages", (long long)ecc.double_bit_isolated_pages_cnt);
            jw_int(&w, "ecc_single_bit_next_isolated_pages", (long long)ecc.single_bit_next_isolated_pages_cnt);
            jw_int(&w, "ecc_double_bit_next_isolated_pages", (long long)ecc.double_bit_next_isolated_pages_cnt);
            jw_bool(&w, "ecc_enabled", ecc.enable_flag != 0);
        } else {
            warn_query("ECC", device_id, ret);
        }

        unsigned int voltage = 0;
        ret = dcmi_get_device_voltage(card, device, &voltage);
        if (ret == 0) {
            // 910B2 driver 25.2.1 returns 81 for 0.81 V, so the unit is 10 mV.
            jw_num(&w, "voltage_v", (double)voltage / 100.0);
        } else {
            warn_query("voltage", device_id, ret);
        }

        struct dcmi_pcie_link_error_info pcie;
        memset(&pcie, 0, sizeof(pcie));
        unsigned int pcie_size = (unsigned int)sizeof(pcie);
        ret = dcmi_get_device_info(card, device, DCMI_MAIN_CMD_PCIE, DCMI_PCIE_SUB_CMD_PCIE_ERROR_INFO, &pcie, &pcie_size);
        if (ret == 0) {
            jw_int(&w, "pcie_tx_errors_total", (long long)pcie.tx_err_cnt);
            jw_int(&w, "pcie_rx_errors_total", (long long)pcie.rx_err_cnt);
            jw_int(&w, "pcie_lcrc_errors_total", (long long)pcie.lcrc_err_cnt);
            jw_int(&w, "pcie_ecrc_errors_total", (long long)pcie.ecrc_err_cnt);
            jw_int(&w, "pcie_retry_total", (long long)pcie.retry_cnt);
        } else {
            warn_query("PCIe error counters", device_id, ret);
        }
    }

    putchar('}');
    putchar('\n');
}

int main(int argc, char **argv) {
    for (int i = 1; i < argc; ++i) {
        if (strcmp(argv[i], "--extended") == 0) {
            g_extended = 1;
        } else if (strcmp(argv[i], "--help") == 0 || strcmp(argv[i], "-h") == 0) {
            printf("usage: dcmi_probe [--extended]\n");
            return 0;
        } else {
            fprintf(stderr, "dcmi_probe: unknown argument: %s\n", argv[i]);
            return 2;
        }
    }
    const char *extended_env = getenv("DCMI_PROBE_EXTENDED");
    if (extended_env != NULL && extended_env[0] != 0 && strcmp(extended_env, "0") != 0) {
        g_extended = 1;
    }

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
            emit_device(card, d, device_id);
        }
    }
    if (device_id == 0) {
        fprintf(stderr, "no DCMI devices found\n");
        return 5;
    }
    return 0;
}
