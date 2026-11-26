package device

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

const PIDMapSchemaV1 = "cann.pidmap.v1"

// PIDMapArtifact is captured on the host while the target process is alive.
// Unlike CollectorMeta, it records the real host pid/tid for every namespace-
// local id used by msPTI, so evidence stays joinable after the process exits.
type PIDMapArtifact struct {
	Schema      string      `json:"schema"`
	ContainerID string      `json:"container_id,omitempty"`
	HostPIDs    []int       `json:"host_pids"`
	PIDMap      map[int]int `json:"pid_map"`
	TIDMap      map[int]int `json:"tid_map"`
}

// ResolveContainerPIDMap builds local->host pid and tid maps for a Docker
// container by scanning /proc/<hostpid>/status NSpid lines. It is intended for
// the host side of a containerized msPTI collector: msPTI reports the pid/tid
// inside the container, while eBPF host spans record the outermost pid/tid.
func ResolveContainerPIDMap(containerID string) (map[int]int, map[int]int, error) {
	if runtime.GOOS != "linux" {
		return nil, nil, nil
	}
	containerID = strings.TrimSpace(containerID)
	if containerID == "" {
		return nil, nil, nil
	}
	pidMap := map[int]int{}
	tidMap := map[int]int{}
	entries, err := filepath.Glob("/proc/[0-9]*")
	if err != nil {
		return nil, nil, fmt.Errorf("glob /proc: %w", err)
	}
	for _, procDir := range entries {
		hostPID, err := strconv.Atoi(filepath.Base(procDir))
		if err != nil {
			continue
		}
		cgroup, err := os.ReadFile(filepath.Join(procDir, "cgroup"))
		if err != nil || !strings.Contains(string(cgroup), containerID) {
			continue
		}
		if host, local, ok := readNSpid(filepath.Join(procDir, "status")); ok && local > 0 {
			pidMap[local] = host
		}
		taskStatuses, _ := filepath.Glob(filepath.Join(procDir, "task", "[0-9]*", "status"))
		for _, statusPath := range taskStatuses {
			if host, local, ok := readNSpid(statusPath); ok && local > 0 {
				tidMap[local] = host
			}
		}
		_ = hostPID
	}
	if len(pidMap) == 0 {
		return nil, nil, fmt.Errorf("container %s not found in /proc cgroups", containerID)
	}
	return pidMap, tidMap, nil
}

// TranslateIDs returns a copy of spans with API pid/tid translated through the
// supplied maps. KERNEL/MEMCPY/HCCL records do not carry process ids.
func TranslateIDs(spans []Span, pidMap, tidMap map[int]int) []Span {
	out := make([]Span, len(spans))
	copy(out, spans)
	for i := range out {
		if out[i].Kind != KindAPI {
			continue
		}
		if host, ok := pidMap[out[i].PID]; ok {
			out[i].PID = host
		}
		if host, ok := tidMap[out[i].TID]; ok {
			out[i].TID = host
		}
	}
	return out
}

func readNSpid(statusPath string) (int, int, bool) {
	data, err := os.ReadFile(statusPath)
	if err != nil {
		return 0, 0, false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "NSpid:") {
			continue
		}
		fields := strings.Fields(strings.TrimPrefix(line, "NSpid:"))
		if len(fields) == 0 {
			return 0, 0, false
		}
		host, err1 := strconv.Atoi(fields[0])
		local, err2 := strconv.Atoi(fields[len(fields)-1])
		if err1 != nil || err2 != nil {
			return 0, 0, false
		}
		return host, local, true
	}
	return 0, 0, false
}

// CapturePIDMaps reads /proc from the host for the given host pids. It is
// read-only and returns an error when no namespace-local id can be resolved.
func CapturePIDMaps(hostPIDs []int) (*PIDMapArtifact, error) {
	artifact := &PIDMapArtifact{
		Schema:   PIDMapSchemaV1,
		HostPIDs: append([]int(nil), hostPIDs...),
		PIDMap:   map[int]int{},
		TIDMap:   map[int]int{},
	}
	for _, hostPID := range hostPIDs {
		if hostPID <= 0 {
			continue
		}
		procDir := filepath.Join("/proc", strconv.Itoa(hostPID))
		if artifact.ContainerID == "" {
			if data, err := os.ReadFile(filepath.Join(procDir, "cgroup")); err == nil {
				artifact.ContainerID = containerIDFromCgroup(string(data))
			}
		}
		if host, local, ok := readNSpid(filepath.Join(procDir, "status")); ok {
			artifact.PIDMap[local] = host
		}
		taskStatuses, _ := filepath.Glob(filepath.Join(procDir, "task", "[0-9]*", "status"))
		for _, statusPath := range taskStatuses {
			if host, local, ok := readNSpid(statusPath); ok {
				artifact.TIDMap[local] = host
			}
		}
	}
	if len(artifact.PIDMap) == 0 {
		return nil, fmt.Errorf("no /proc pid mapping captured for host pids %v", hostPIDs)
	}
	return artifact, nil
}

func containerIDFromCgroup(cgroup string) string {
	if idx := strings.Index(cgroup, "/docker/"); idx >= 0 {
		rest := cgroup[idx+len("/docker/"):]
		for _, sep := range []byte{'/', '\n', '\r'} {
			if end := strings.IndexByte(rest, sep); end >= 0 {
				rest = rest[:end]
			}
		}
		return strings.TrimSpace(rest)
	}
	if idx := strings.Index(cgroup, "docker-"); idx >= 0 {
		rest := cgroup[idx+len("docker-"):]
		if end := strings.Index(rest, ".scope"); end >= 0 {
			return rest[:end]
		}
	}
	return ""
}

func WritePIDMap(path string, artifact *PIDMapArtifact) error {
	data, err := json.MarshalIndent(artifact, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

func ReadPIDMap(path string) (*PIDMapArtifact, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var artifact PIDMapArtifact
	if err := json.Unmarshal(data, &artifact); err != nil {
		return nil, err
	}
	if artifact.Schema != "" && artifact.Schema != PIDMapSchemaV1 {
		return nil, fmt.Errorf("pidmap schema %q is not %s", artifact.Schema, PIDMapSchemaV1)
	}
	return &artifact, nil
}
