//go:build linux && arm64

package probes

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"cann-ebpf/poc/cpython"
)

type pythonBatchLayer struct {
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	results chan pythonBatchOutcome
}

type pythonBatchOutcome struct {
	capture  PythonBatchCapture
	warnings []string
}

func startPythonBatchLayer(ctx context.Context, opts Options) (*pythonBatchLayer, []string) {
	if !opts.PythonBatch.Enabled {
		return nil, nil
	}
	offsets, err := loadPythonBatchOffsets(opts.PythonBatch.OffsetsPath)
	if err != nil {
		return nil, []string{"python batch disabled: " + err.Error()}
	}
	maxEvents := opts.PythonBatch.MaxEvents
	if maxEvents <= 0 {
		maxEvents = 20000
	}
	requireType := strings.TrimSpace(opts.PythonBatch.RequireType)
	if requireType == "" {
		requireType = "SchedulerOutput"
	}

	type candidate struct {
		pid int
		lib string
	}
	var candidates []candidate
	for _, pid := range opts.PIDs {
		lib, libErr := cpython.FindLibPython(pid, opts.PythonBatch.LibPath)
		if libErr != nil {
			continue
		}
		candidates = append(candidates, candidate{pid: pid, lib: lib})
	}
	if len(candidates) == 0 {
		return nil, []string{"python batch: no target mapped libpython3.12.so; nothing attached"}
	}

	batchCtx, cancel := context.WithCancel(ctx)
	layer := &pythonBatchLayer{
		cancel:  cancel,
		results: make(chan pythonBatchOutcome, len(candidates)),
	}
	for _, target := range candidates {
		target := target
		layer.wg.Add(1)
		go func() {
			defer layer.wg.Done()
			outcome := pythonBatchOutcome{}
			result, runErr := cpython.RunBatch(batchCtx, cpython.BatchOptions{
				PID:       target.pid,
				Duration:  opts.Duration,
				LibPath:   target.lib,
				MaxEvents: maxEvents,
				Offsets:   offsets,
			})
			if runErr != nil {
				outcome.warnings = []string{fmt.Sprintf("python batch pid %d: %v", target.pid, runErr)}
				layer.results <- outcome
				return
			}
			report := cpython.BuildBatchReport(cpython.BatchReportInput{
				PID:         result.PID,
				Lib:         result.LibPath,
				Duration:    opts.Duration,
				Events:      result.Events,
				OpenBatches: result.OpenBatches,
				Stats:       result.Stats,
				Warnings:    result.Warnings,
				Offsets:     offsets,
				RequireType: requireType,
			})
			reportJSON, marshalErr := json.MarshalIndent(report, "", "  ")
			if marshalErr != nil {
				outcome.warnings = append(outcome.warnings, fmt.Sprintf("python batch pid %d: marshal report: %v", target.pid, marshalErr))
			} else {
				reportJSON = append(reportJSON, '\n')
			}
			spans, spanWarnings := cpython.StandardSpans(result.PID, result.LibPath, report)
			outcome.capture = PythonBatchCapture{
				PID:        result.PID,
				Lib:        result.LibPath,
				Batches:    report.BatchCount,
				Requests:   report.RequestCount,
				Status:     report.Status,
				ReportJSON: reportJSON,
				Spans:      spans,
			}
			outcome.warnings = append(outcome.warnings, result.Warnings...)
			outcome.warnings = append(outcome.warnings, report.Warnings...)
			outcome.warnings = append(outcome.warnings, spanWarnings...)
			layer.results <- outcome
		}()
	}
	return layer, nil
}

func loadPythonBatchOffsets(path string) (cpython.OffsetBundle, error) {
	if strings.TrimSpace(path) == "" {
		return cpython.LoadEmbeddedOffsets()
	}
	return cpython.LoadOffsetsFile(path)
}

func (layer *pythonBatchLayer) Collect(timeout time.Duration) ([]PythonBatchCapture, []string) {
	if layer == nil {
		return nil, nil
	}
	defer layer.cancel()
	done := make(chan struct{})
	go func() {
		layer.wg.Wait()
		close(layer.results)
		close(done)
	}()
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	select {
	case <-done:
	case <-time.After(timeout):
		layer.cancel()
		<-done
	}
	var captures []PythonBatchCapture
	var warnings []string
	for outcome := range layer.results {
		if outcome.capture.PID != 0 {
			captures = append(captures, outcome.capture)
		}
		warnings = append(warnings, outcome.warnings...)
	}
	return captures, warnings
}
