package config

import (
	"fmt"
	"strings"
)

type Document struct {
	APIVersion string   `yaml:"apiVersion" json:"apiVersion"`
	Kind       string   `yaml:"kind" json:"kind"`
	Metadata   Metadata `yaml:"metadata" json:"metadata"`
	Spec       Spec     `yaml:"spec" json:"spec"`
}

type Metadata struct {
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
}

type Spec struct {
	Extends    string      `yaml:"extends,omitempty" json:"extends,omitempty"`
	Match      *Match      `yaml:"match,omitempty" json:"match,omitempty"`
	Notes      []string    `yaml:"notes,omitempty" json:"notes,omitempty"`
	Target     Target      `yaml:"target" json:"target"`
	Preflight  Preflight   `yaml:"preflight" json:"preflight"`
	Cann       Cann        `yaml:"cann" json:"cann"`
	Probes     Probes      `yaml:"probes" json:"probes"`
	Hardware   Hardware    `yaml:"hardware" json:"hardware"`
	Context    Context     `yaml:"context" json:"context"`
	Mspti      Mspti       `yaml:"mspti" json:"mspti"`
	Classify   Classify    `yaml:"classify" json:"classify"`
	Output     Output      `yaml:"output" json:"output"`
	Maps       Maps        `yaml:"maps" json:"maps"`
	SelfMetric SelfMetrics `yaml:"self_metrics" json:"self_metrics"`
	Safety     Safety      `yaml:"safety" json:"safety"`
}

type Match struct {
	Priority int         `yaml:"priority" json:"priority"`
	Any      []MatchExpr `yaml:"any,omitempty" json:"any,omitempty"`
	All      []MatchExpr `yaml:"all,omitempty" json:"all,omitempty"`
	Exclude  []MatchExpr `yaml:"exclude,omitempty" json:"exclude,omitempty"`
}

type MatchExpr struct {
	Comm                   string `yaml:"comm,omitempty" json:"comm,omitempty"`
	CmdlineContains        string `yaml:"cmdline_contains,omitempty" json:"cmdline_contains,omitempty"`
	MapsContains           string `yaml:"maps_contains,omitempty" json:"maps_contains,omitempty"`
	ContainerName          string `yaml:"container_name,omitempty" json:"container_name,omitempty"`
	ContainerImageContains string `yaml:"container_image_contains,omitempty" json:"container_image_contains,omitempty"`
	ListenPort             int    `yaml:"listen_port,omitempty" json:"listen_port,omitempty"`
}

func (m MatchExpr) Empty() bool {
	return m.Comm == "" && m.CmdlineContains == "" && m.MapsContains == "" &&
		m.ContainerName == "" && m.ContainerImageContains == "" && m.ListenPort == 0
}

func (m MatchExpr) String() string {
	var parts []string
	if m.Comm != "" {
		parts = append(parts, "comm="+m.Comm)
	}
	if m.CmdlineContains != "" {
		parts = append(parts, "cmdline~"+m.CmdlineContains)
	}
	if m.MapsContains != "" {
		parts = append(parts, "maps~"+m.MapsContains)
	}
	if m.ContainerName != "" {
		parts = append(parts, "container="+m.ContainerName)
	}
	if m.ContainerImageContains != "" {
		parts = append(parts, "image~"+m.ContainerImageContains)
	}
	if m.ListenPort != 0 {
		parts = append(parts, fmt.Sprintf("port=%d", m.ListenPort))
	}
	return strings.Join(parts, " AND ")
}

type Target struct {
	PID               int    `yaml:"pid" json:"pid"`
	Selector          string `yaml:"selector" json:"selector"`
	MaxProcesses      int    `yaml:"max_processes" json:"max_processes"`
	ProcessTree       bool   `yaml:"process_tree" json:"process_tree"`
	IncludeChildren   bool   `yaml:"include_children" json:"include_children"`
	DiscoveryWindowMS int    `yaml:"discovery_window_ms" json:"discovery_window_ms"`
	ProcRoot          string `yaml:"proc_root" json:"proc_root"`
}

type Preflight struct {
	RequireBTF             bool `yaml:"require_btf" json:"require_btf"`
	RequireUprobeEvents    bool `yaml:"require_uprobe_events" json:"require_uprobe_events"`
	RequireBPFSyscall      bool `yaml:"require_bpf_syscall" json:"require_bpf_syscall"`
	AllowMissingSymbols    bool `yaml:"allow_missing_symbols" json:"allow_missing_symbols"`
	CheckPerfEventParanoid bool `yaml:"check_perf_event_paranoid" json:"check_perf_event_paranoid"`
}

type Cann struct {
	Version         any             `yaml:"version" json:"version"`
	LibraryGlobs    []string        `yaml:"library_globs" json:"library_globs"`
	SymbolPolicy    string          `yaml:"symbol_policy" json:"symbol_policy"`
	LaunchLayer     string          `yaml:"launch_layer" json:"launch_layer"`
	SymbolOverrides SymbolOverrides `yaml:"symbol_overrides" json:"symbol_overrides"`
}

type SymbolOverrides struct {
	Sync   []string `yaml:"sync,omitempty" json:"sync,omitempty"`
	Event  []string `yaml:"event,omitempty" json:"event,omitempty"`
	Handle []string `yaml:"handle,omitempty" json:"handle,omitempty"`
	Memory []string `yaml:"memory,omitempty" json:"memory,omitempty"`
	Submit []string `yaml:"submit,omitempty" json:"submit,omitempty"`
	Hccl   []string `yaml:"hccl,omitempty" json:"hccl,omitempty"`
}

func (s SymbolOverrides) All() []string {
	seen := make(map[string]struct{})
	var out []string
	for _, list := range [][]string{s.Sync, s.Event, s.Handle, s.Memory, s.Submit, s.Hccl} {
		for _, name := range list {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			if _, ok := seen[name]; ok {
				continue
			}
			seen[name] = struct{}{}
			out = append(out, name)
		}
	}
	return out
}

func (c Cann) VersionString() string {
	if c.Version == nil {
		return "auto"
	}
	return fmt.Sprint(c.Version)
}

type Probes struct {
	Sync   Probe `yaml:"sync" json:"sync"`
	Event  Probe `yaml:"event" json:"event"`
	Memory Probe `yaml:"memory" json:"memory"`
	Handle Probe `yaml:"handle" json:"handle"`
	Submit Probe `yaml:"submit" json:"submit"`
	Hccl   Probe `yaml:"hccl" json:"hccl"`
	Sched  Probe `yaml:"sched" json:"sched"`
	Futex  Probe `yaml:"futex" json:"futex"`
	Ioctl  Probe `yaml:"ioctl" json:"ioctl"`
}

type Probe struct {
	Enabled       any    `yaml:"enabled" json:"enabled"`
	Template      string `yaml:"template,omitempty" json:"template,omitempty"`
	Mode          string `yaml:"mode,omitempty" json:"mode,omitempty"`
	SampleRate    int    `yaml:"sample_rate,omitempty" json:"sample_rate,omitempty"`
	MinDurationUS int    `yaml:"min_duration_us,omitempty" json:"min_duration_us,omitempty"`
}

func (p Probe) IsEnabled() bool {
	switch v := p.Enabled.(type) {
	case bool:
		return v
	case string:
		return v != "" && v != "false" && v != "off" && v != "no"
	default:
		return false
	}
}

type Hardware struct {
	Enabled        any      `yaml:"enabled" json:"enabled"`
	Source         string   `yaml:"source" json:"source"`
	IntervalS      int      `yaml:"interval_s" json:"interval_s"`
	Metrics        []string `yaml:"metrics" json:"metrics"`
	NpuSmiFallback bool     `yaml:"npu_smi_fallback" json:"npu_smi_fallback"`
}

type Context struct {
	Mode             string  `yaml:"mode" json:"mode"`
	Batch            any     `yaml:"batch" json:"batch"`
	AttributionLevel string  `yaml:"attribution_level" json:"attribution_level"`
	Plugin           *Plugin `yaml:"plugin,omitempty" json:"plugin,omitempty"`
	Notes            string  `yaml:"notes,omitempty" json:"notes,omitempty"`
}

type Plugin struct {
	Transport string   `yaml:"transport" json:"transport"`
	Socket    string   `yaml:"socket" json:"socket"`
	Events    []string `yaml:"events" json:"events"`
	Fallback  string   `yaml:"fallback" json:"fallback"`
}

type Mspti struct {
	Mode         string   `yaml:"mode" json:"mode"`
	Library      string   `yaml:"library" json:"library"`
	LibraryGlobs []string `yaml:"library_globs" json:"library_globs"`
	Headers      string   `yaml:"headers" json:"headers"`
	Activity     []string `yaml:"activity" json:"activity"`
}

type Classify struct {
	SyncSlowUS       int     `yaml:"sync_slow_us" json:"sync_slow_us"`
	SubmitSlowUS     int     `yaml:"submit_slow_us" json:"submit_slow_us"`
	OffCPURatio      float64 `yaml:"offcpu_ratio" json:"offcpu_ratio"`
	FutexRatio       float64 `yaml:"futex_ratio" json:"futex_ratio"`
	DeviceOverlap    float64 `yaml:"device_overlap_ratio" json:"device_overlap_ratio"`
	DeviceIdle       float64 `yaml:"device_idle_ratio" json:"device_idle_ratio"`
	HBMFreePct       float64 `yaml:"hbm_free_pct" json:"hbm_free_pct"`
	StuckThresholdMS int     `yaml:"stuck_threshold_ms" json:"stuck_threshold_ms"`
}

type Output struct {
	Dir           string `yaml:"dir" json:"dir"`
	JSONL         string `yaml:"jsonl" json:"jsonl"`
	Report        string `yaml:"report" json:"report"`
	Manifest      string `yaml:"manifest" json:"manifest"`
	RingbufMB     int    `yaml:"ringbuf_mb" json:"ringbuf_mb"`
	BatchSize     int    `yaml:"batch_size" json:"batch_size"`
	Format        string `yaml:"format" json:"format"`
	RotateMB      int    `yaml:"rotate_mb" json:"rotate_mb"`
	RetentionDays int    `yaml:"retention_days" json:"retention_days"`
	MinFreeGB     int    `yaml:"min_free_gb" json:"min_free_gb"`
}

type Maps struct {
	TargetPIDsMax int `yaml:"target_pids_max" json:"target_pids_max"`
	InflightMax   int `yaml:"inflight_max" json:"inflight_max"`
	HandleMax     int `yaml:"handle_max" json:"handle_max"`
	AggMax        int `yaml:"agg_max" json:"agg_max"`
}

type SelfMetrics struct {
	Enabled   bool     `yaml:"enabled" json:"enabled"`
	IntervalS int      `yaml:"interval_s" json:"interval_s"`
	Listen    string   `yaml:"listen" json:"listen"`
	Metrics   []string `yaml:"metrics" json:"metrics"`
}

type Safety struct {
	MaxOverheadPct       float64 `yaml:"max_overhead_pct" json:"max_overhead_pct"`
	MaxRingbufLossPct    float64 `yaml:"max_ringbuf_loss_pct" json:"max_ringbuf_loss_pct"`
	MaxRSSMB             int     `yaml:"max_rss_mb" json:"max_rss_mb"`
	MaxCPUPct            float64 `yaml:"max_cpu_pct" json:"max_cpu_pct"`
	MaxDurationS         int     `yaml:"max_duration_s" json:"max_duration_s"`
	AutoDetachOnOverhead bool    `yaml:"auto_detach_on_overhead" json:"auto_detach_on_overhead"`
	DryRun               bool    `yaml:"dry_run" json:"dry_run"`
}
