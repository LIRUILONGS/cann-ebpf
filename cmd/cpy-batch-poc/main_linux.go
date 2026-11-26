//go:build linux && arm64

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"cann-ebpf/poc/cpython"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	fs := flag.NewFlagSet("cpy-batch-poc", flag.ContinueOnError)
	pid := fs.Int("pid", 0, "target PID (required)")
	duration := fs.Duration("duration", 10*time.Second, "capture duration")
	lib := fs.String("lib", "", "libpython3.12.so path (auto from /proc/<pid>/maps)")
	maxEvents := fs.Int("max-events", 5000, "maximum ring-buffer events to retain")
	output := fs.String("output", "", "write JSON report to this path")
	offsetsPath := fs.String("offsets", "", "offset bundle JSON (default: embedded pinned profile)")
	requireType := fs.String("require-type", "SchedulerOutput", "required batch owner type substring; empty disables the check")
	jsonOut := fs.Bool("json", false, "print JSON report")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *pid <= 0 {
		fmt.Fprintln(os.Stderr, "cpy-batch-poc: --pid is required")
		return 2
	}
	offsets, err := loadOffsets(*offsetsPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cpy-batch-poc: %v\n", err)
		return 5
	}
	result, err := cpython.RunBatch(context.Background(), cpython.BatchOptions{
		PID:       *pid,
		Duration:  *duration,
		LibPath:   *lib,
		MaxEvents: *maxEvents,
		Offsets:   offsets,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "cpy-batch-poc: %v\n", err)
		return 5
	}
	rep := cpython.BuildBatchReport(cpython.BatchReportInput{
		PID:         result.PID,
		Lib:         result.LibPath,
		Duration:    *duration,
		Events:      result.Events,
		OpenBatches: result.OpenBatches,
		Stats:       result.Stats,
		Warnings:    result.Warnings,
		Offsets:     offsets,
		RequireType: *requireType,
	})
	if *output != "" {
		if err := writeReport(*output, rep); err != nil {
			fmt.Fprintf(os.Stderr, "cpy-batch-poc: write %s: %v\n", *output, err)
			return 5
		}
	}
	if *jsonOut {
		writeJSON(rep)
	} else {
		printReport(rep)
	}
	switch rep.Status {
	case "ok":
		return 0
	case "ok_type_mismatch", "ok_type_unconfirmed", "ok_no_ids", "matched_but_unparsed":
		return 3
	case "no_match":
		return 4
	default:
		return 4
	}
}

func loadOffsets(path string) (cpython.OffsetBundle, error) {
	if strings.TrimSpace(path) == "" {
		return cpython.LoadEmbeddedOffsets()
	}
	return cpython.LoadOffsetsFile(path)
}

func printReport(rep cpython.BatchReport) {
	fmt.Printf("vllm-batch-poc status=%s pid=%d lib=%s batches=%d requests=%d complete=%d open=%d\n",
		rep.Status, rep.PID, rep.Lib, rep.BatchCount, rep.RequestCount, rep.CompleteBatches, rep.OpenBatchCount)
	fmt.Printf("versions python=%s vllm=%s vllm_ascend=%s boundary=%s\n",
		rep.Versions.Python, rep.Versions.VLLM, rep.Versions.VLLMAscend, rep.Boundary)
	fmt.Printf("stats entries=%d name_matches=%d returns=%d parsed=%d emitted=%d read_errors=%d split_dict=%d non_unicode=%d empty=%d ringbuf_lost=%d batches_started=%d batches_closed=%d type_read_errors=%d\n",
		rep.Stats.Entries, rep.Stats.NameMatches, rep.Stats.Returns, rep.Stats.Parsed,
		rep.Stats.Emitted, rep.Stats.ReadErrors, rep.Stats.SplitDict, rep.Stats.NonUnicodeDict,
		rep.Stats.EmptyDict, rep.Stats.RingbufLost, rep.Stats.BatchesStarted,
		rep.Stats.BatchesClosed, rep.Stats.TypeReadErrors)
	fmt.Printf("types=%s type_confirmed=%t request_ids=%d\n",
		strings.Join(rep.TypeNames, ","), rep.TypeConfirmed, len(rep.RequestIDs))
	for _, batch := range rep.Batches {
		fmt.Printf("  %s dict=%#x type=%s begin_ns=%d end_ns=%d duration_ms=%.3f open=%t requests=%d ids=%s\n",
			batch.BatchID, batch.Dict, batch.TypeName, batch.BeginNS, batch.EndNS,
			float64(batch.DurationNS)/1e6, batch.Open, batch.RequestCount,
			strings.Join(batch.RequestIDs, ","))
	}
	for _, warning := range rep.Warnings {
		fmt.Printf("WARN %s\n", warning)
	}
}

func writeReport(path string, rep cpython.BatchReport) error {
	data, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o600)
}

func writeJSON(value any) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "marshal json: %v\n", err)
		return
	}
	fmt.Println(strings.TrimSpace(string(data)))
}
