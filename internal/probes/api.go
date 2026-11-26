package probes

import (
	"context"
	"fmt"
	"time"

	"cann-ebpf/internal/config"
	"cann-ebpf/internal/contextfeed"
	"cann-ebpf/internal/hardware"
	"cann-ebpf/internal/runlog"
)

const DefaultToolVersion = "0.6.0-m2"

type Options struct {
	Context          context.Context
	PIDs             []int
	ProcRoot         string
	Profile          *config.Document
	ProfileName      string
	Duration         time.Duration
	OutputDir        string
	ToolVersion      string
	MaxSpans         int
	MinDuration      time.Duration
	ContextSocket    string
	PythonBatch      PythonBatchOptions
	Blocking         BlockingMode
	HardwareMode     string
	HardwareInterval time.Duration
	OnCaptureStart   func()
	OnCaptureStop    func()
	DeviceJSONL      string
}

// PythonBatchOptions enables the read-only CPython/vLLM SchedulerOutput
// probe. It is opt-in because PyObject_GetAttr is a very hot Python C API.
type PythonBatchOptions struct {
	Enabled     bool
	LibPath     string
	OffsetsPath string
	MaxEvents   int
	RequireType string
}

type Attachment struct {
	PID      int
	Library  string
	Function string
	Category string
	Offset   uint64
	Entry    bool
	Exit     bool
	Error    string
}

type Result struct {
	Started              time.Time
	Ended                time.Time
	EventCount           uint64
	Spans                []runlog.Span
	JSONLPath            string
	ManifestPath         string
	SummaryPath          string
	Attachments          []Attachment
	Warnings             []string
	Stats                probeStats
	Context              contextfeed.Snapshot
	PythonBatch          []PythonBatchCapture
	Blocking             BlockingResult
	Hardware             hardware.Result
	DeviceEvidence       DeviceEvidence
	SetupDuration        time.Duration
	CaptureDuration      time.Duration
	TeardownDuration     time.Duration
	CloseLinksDuration   time.Duration
	BatchCollectDuration time.Duration
	ConsumeWaitDuration  time.Duration
	WriterCloseDuration  time.Duration
	SummaryDuration      time.Duration
}

type PythonBatchCapture struct {
	PID        int           `json:"pid"`
	Lib        string        `json:"lib"`
	Batches    int           `json:"batches"`
	Requests   int           `json:"requests"`
	Status     string        `json:"status"`
	ReportJSON []byte        `json:"-"`
	Spans      []runlog.Span `json:"-"`
	Warnings   []string      `json:"warnings,omitempty"`
}

func (o *Options) normalize() error {
	if len(o.PIDs) == 0 {
		return fmt.Errorf("at least one target pid is required")
	}
	seen := map[int]struct{}{}
	for _, pid := range o.PIDs {
		if pid <= 0 {
			return fmt.Errorf("invalid pid %d", pid)
		}
		if _, ok := seen[pid]; ok {
			return fmt.Errorf("duplicate pid %d", pid)
		}
		seen[pid] = struct{}{}
	}
	if o.Context == nil {
		o.Context = context.Background()
	}
	if o.ProcRoot == "" {
		o.ProcRoot = "/proc"
	}
	if o.ProfileName == "" {
		if o.Profile != nil && o.Profile.Metadata.Name != "" {
			o.ProfileName = o.Profile.Metadata.Name
		} else {
			o.ProfileName = "cann-generic"
		}
	}
	if o.Duration <= 0 {
		o.Duration = 10 * time.Second
	}
	if o.ToolVersion == "" {
		o.ToolVersion = DefaultToolVersion
	}
	if o.MaxSpans <= 0 {
		o.MaxSpans = 100000
	}
	return nil
}
