package probes

import (
	"fmt"

	"cann-ebpf/internal/config"
)

// resolveHardwareMode merges the CLI mode with profile.hardware.enabled.
// "auto" follows the profile; "on"/"off" force the coarse npu-smi layer.
func resolveHardwareMode(profile *config.Document, mode string) (bool, error) {
	switch mode {
	case "", "auto":
		if profile == nil {
			return false, nil
		}
		return anyEnabled(profile.Spec.Hardware.Enabled), nil
	case "on":
		return true, nil
	case "off":
		return false, nil
	default:
		return false, fmt.Errorf("unknown hardware mode %q (want auto|on|off)", mode)
	}
}

func anyEnabled(value any) bool {
	switch v := value.(type) {
	case bool:
		return v
	case string:
		return v != "" && v != "false" && v != "off" && v != "no"
	default:
		return false
	}
}

func hardwareIntervalSeconds(profile *config.Document) int {
	if profile == nil {
		return 10
	}
	if profile.Spec.Hardware.IntervalS > 0 {
		return profile.Spec.Hardware.IntervalS
	}
	return 10
}
