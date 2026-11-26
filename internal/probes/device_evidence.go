package probes

import (
	"fmt"
	"os"
	"path/filepath"

	"cann-ebpf/internal/device"
)

// DeviceEvidence records what the host side imported from an external msPTI
// collector. The collector runs inside the target container and writes
// device.jsonl (+ .meta.json); attach copies them into the run directory,
// captures a host-side pidmap while the targets are alive, and seals every file
// as a manifest artifact.
type DeviceEvidence struct {
	Imported   bool     `json:"imported"`
	DevicePath string   `json:"device_path,omitempty"`
	MetaPath   string   `json:"meta_path,omitempty"`
	PIDMapPath string   `json:"pidmap_path,omitempty"`
	Warnings   []string `json:"warnings,omitempty"`
}

// importDeviceEvidence copies collector output into the run directory without
// ever overwriting existing evidence. Missing files are warnings, not fatal:
// the host span capture must still succeed.
func importDeviceEvidence(outputDir, deviceJSONL string, pids []int) DeviceEvidence {
	evidence := DeviceEvidence{}
	if deviceJSONL == "" {
		return evidence
	}
	if outputDir == "" {
		evidence.Warnings = append(evidence.Warnings, "device evidence import skipped: no output directory")
		return evidence
	}
	source := filepath.Clean(deviceJSONL)
	if _, err := os.Stat(source); err != nil {
		evidence.Warnings = append(evidence.Warnings,
			fmt.Sprintf("device evidence: %s: %v", source, err))
		return evidence
	}

	deviceDest := filepath.Join(outputDir, "device.jsonl")
	if _, err := os.Stat(deviceDest); err == nil {
		evidence.Warnings = append(evidence.Warnings,
			"device evidence: refusing to overwrite existing "+deviceDest)
	} else if err := copyFileAtomic(source, deviceDest); err != nil {
		evidence.Warnings = append(evidence.Warnings, "device evidence copy: "+err.Error())
	} else {
		evidence.Imported = true
		evidence.DevicePath = deviceDest
	}

	metaSource := source + ".meta.json"
	metaDest := filepath.Join(outputDir, "device.jsonl.meta.json")
	if _, err := os.Stat(metaSource); err == nil {
		if _, err := os.Stat(metaDest); err == nil {
			evidence.Warnings = append(evidence.Warnings,
				"device evidence: refusing to overwrite existing "+metaDest)
		} else if err := copyFileAtomic(metaSource, metaDest); err != nil {
			evidence.Warnings = append(evidence.Warnings, "device meta copy: "+err.Error())
		} else {
			evidence.MetaPath = metaDest
		}
	} else {
		evidence.Warnings = append(evidence.Warnings,
			"device evidence: "+metaSource+" missing; collector meta not sealed")
	}

	pidMapPath := filepath.Join(outputDir, "pidmap.json")
	if _, err := os.Stat(pidMapPath); err == nil {
		evidence.Warnings = append(evidence.Warnings,
			"device evidence: refusing to overwrite existing "+pidMapPath)
		evidence.PIDMapPath = pidMapPath
		return evidence
	}
	artifact, err := device.CapturePIDMaps(pids)
	if err != nil {
		evidence.Warnings = append(evidence.Warnings, "device pidmap: "+err.Error())
		return evidence
	}
	if err := device.WritePIDMap(pidMapPath, artifact); err != nil {
		evidence.Warnings = append(evidence.Warnings, "device pidmap write: "+err.Error())
		return evidence
	}
	evidence.PIDMapPath = pidMapPath
	return evidence
}

func copyFileAtomic(source, dest string) error {
	data, err := os.ReadFile(source)
	if err != nil {
		return fmt.Errorf("read %s: %w", source, err)
	}
	dir := filepath.Dir(dest)
	temp, err := os.CreateTemp(dir, "."+filepath.Base(dest)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp in %s: %w", dir, err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, dest)
}
