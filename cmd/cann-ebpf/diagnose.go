package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"cann-ebpf/internal/diagnose"
)

// cmdDiagnose is the read-only P0 entry point: it answers "what happened in
// this window" from an existing run and never attaches probes or rewrites
// evidence.
func cmdDiagnose(args []string) int {
	fs := flag.NewFlagSet("diagnose", flag.ContinueOnError)
	input := fs.String("input", "", "run directory or spans.jsonl path")
	runID := fs.String("run", "", "alias for --input (run directory)")
	startNS := fs.Int64("start-ns", 0, "window start in monotonic ns (same domain as span start_ns)")
	endNS := fs.Int64("end-ns", 0, "window end in monotonic ns")
	last := fs.Duration("last", 0, "window of the last duration before the newest span")
	pidList := fs.String("pid", "", "comma-separated PIDs")
	tidList := fs.String("tid", "", "comma-separated TIDs")
	batchID := fs.String("batch-id", "", "only spans with this batch_id")
	requestID := fs.String("request-id", "", "only spans whose request_ids contain this ID")
	traceID := fs.String("trace-id", "", "only spans with this trace_id")
	topN := fs.Int("top", 10, "maximum rows per ranking")
	slowThreshold := fs.Duration("slow-threshold", time.Millisecond, "minimum duration for the evidence list")
	offcpuRatio := fs.Float64("offcpu-ratio", 0.5, "off-CPU/wall threshold for host_lock_contention")
	futexRatio := fs.Float64("futex-ratio", 0.3, "futex/wall threshold for host_lock_contention")
	hbmFreePct := fs.Float64("hbm-free-pct", 5, "HBM free percentage threshold for hbm_pressure")
	deviceTolerance := fs.Duration("device-tolerance", 100*time.Microsecond, "max host/device API start skew for the M3 join")
	jsonOut := fs.Bool("json", false, "json output")
	outPath := fs.String("out", "", "write the report to this file instead of stdout")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	path := strings.TrimSpace(*input)
	if path == "" {
		path = strings.TrimSpace(*runID)
	}
	if path == "" {
		fmt.Fprintln(os.Stderr, "diagnose: --input (run directory or spans.jsonl) is required")
		return 2
	}
	if *startNS < 0 || *endNS < 0 {
		fmt.Fprintln(os.Stderr, "diagnose: --start-ns/--end-ns must not be negative")
		return 2
	}
	pids, err := parsePIDList(*pidList)
	if err != nil {
		fmt.Fprintf(os.Stderr, "diagnose: --pid: %v\n", err)
		return 2
	}
	tids, err := parsePIDList(*tidList)
	if err != nil {
		fmt.Fprintf(os.Stderr, "diagnose: --tid: %v\n", err)
		return 2
	}

	in, err := diagnose.Load(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "diagnose: %v\n", err)
		return 4
	}
	report, err := diagnose.Build(in, diagnose.Options{
		StartNS: *startNS, EndNS: *endNS, Last: *last,
		PIDs: pids, TIDs: tids, BatchID: *batchID, RequestID: *requestID, TraceID: *traceID,
		TopN: *topN, SlowThresholdNS: int64(*slowThreshold),
		OffCPURatioThreshold: *offcpuRatio, FutexRatioThreshold: *futexRatio,
		HBMFreePctThreshold:   *hbmFreePct,
		DeviceTimeToleranceNS: int64(*deviceTolerance),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "diagnose: %v\n", err)
		return 3
	}

	var output []byte
	if *jsonOut {
		data, marshalErr := json.MarshalIndent(report, "", "  ")
		if marshalErr != nil {
			fmt.Fprintf(os.Stderr, "diagnose: marshal json: %v\n", marshalErr)
			return 3
		}
		output = append(data, '\n')
	} else {
		output = []byte(report.Markdown())
	}
	if strings.TrimSpace(*outPath) == "" {
		os.Stdout.Write(output)
		return 0
	}
	if err := os.WriteFile(*outPath, output, 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "diagnose: write %s: %v\n", *outPath, err)
		return 4
	}
	fmt.Printf("diagnose report=%s status=%s matched_spans=%d verdicts=%d\n",
		*outPath, report.Status, report.MatchedSpans, len(report.Verdicts))
	return 0
}
