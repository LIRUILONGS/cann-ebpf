//go:build linux && arm64

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"cann-ebpf/poc/cpython"
)

const reportSchema = "cann.cpython.poc.v1"

type report struct {
	Schema     string        `json:"schema"`
	Status     string        `json:"status"`
	PID        int           `json:"pid"`
	Lib        string        `json:"lib"`
	Duration   string        `json:"duration"`
	Layout     string        `json:"layout"`
	Found      int           `json:"found"`
	Dicts      int           `json:"dicts"`
	Events     int           `json:"events"`
	DictKind   uint32        `json:"dict_kind,omitempty"`
	Truncated  bool          `json:"truncated"`
	RequestIDs []string      `json:"request_ids"`
	Stats      cpython.Stats `json:"stats"`
	Warnings   []string      `json:"warnings,omitempty"`
	Note       string        `json:"note"`
}

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	fs := flag.NewFlagSet("cpy-poc", flag.ContinueOnError)
	pid := fs.Int("pid", 0, "target PID (required)")
	duration := fs.Duration("duration", 10*time.Second, "capture duration")
	lib := fs.String("lib", "", "libpython3.12.so path (auto from /proc/<pid>/maps)")
	maxEvents := fs.Int("max-events", 2000, "maximum ring-buffer events to retain")
	output := fs.String("output", "", "write JSON report to this path")
	jsonOut := fs.Bool("json", false, "print JSON report")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *pid <= 0 {
		fmt.Fprintln(os.Stderr, "cpy-poc: --pid is required")
		return 2
	}
	result, err := cpython.Run(context.Background(), cpython.Options{
		PID: *pid, Duration: *duration, LibPath: *lib, MaxEvents: *maxEvents,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "cpy-poc: %v\n", err)
		return 5
	}
	rep := buildReport(result, *duration)
	if *output != "" {
		if err := writeReport(*output, rep); err != nil {
			fmt.Fprintf(os.Stderr, "cpy-poc: write %s: %v\n", *output, err)
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
	case "matched_but_unparsed":
		return 3
	default:
		return 4
	}
}

func buildReport(result *cpython.Result, duration time.Duration) report {
	rep := report{
		Schema: reportSchema, PID: result.PID, Lib: result.LibPath,
		Duration: duration.String(), Layout: "cpython-3.12",
		Events: len(result.Events), Stats: result.Stats, Warnings: result.Warnings,
		Note: "POC only: CPython 3.12 layout, combined unicode dict, compact ASCII keys, max 8 IDs per access. No prompt/output/token payload is read.",
	}
	seenIDs := map[string]struct{}{}
	seenDicts := map[uint64]struct{}{}
	for _, event := range result.Events {
		if event.Dict != 0 {
			seenDicts[event.Dict] = struct{}{}
		}
		if event.Kind != 0 && rep.DictKind == 0 {
			rep.DictKind = event.Kind
		}
		if event.Truncated != 0 {
			rep.Truncated = true
		}
		requestID := event.RequestID()
		if requestID == "" {
			continue
		}
		seenIDs[requestID] = struct{}{}
	}
	for requestID := range seenIDs {
		rep.RequestIDs = append(rep.RequestIDs, requestID)
	}
	sort.Strings(rep.RequestIDs)
	rep.Found = len(rep.RequestIDs)
	rep.Dicts = len(seenDicts)
	switch {
	case rep.Found > 0:
		rep.Status = "ok"
	case result.Stats.NameMatches > 0:
		rep.Status = "matched_but_unparsed"
	default:
		rep.Status = "no_match"
	}
	if rep.Warnings == nil {
		rep.Warnings = []string{}
	}
	if rep.RequestIDs == nil {
		rep.RequestIDs = []string{}
	}
	return rep
}

func printReport(rep report) {
	fmt.Printf("cpython-poc status=%s pid=%d lib=%s found=%d events=%d dicts=%d\n",
		rep.Status, rep.PID, rep.Lib, rep.Found, rep.Events, rep.Dicts)
	fmt.Printf("stats entries=%d name_matches=%d returns=%d parsed=%d emitted=%d skipped=%d read_errors=%d split_dict=%d non_unicode=%d empty=%d ringbuf_lost=%d\n",
		rep.Stats.Entries, rep.Stats.NameMatches, rep.Stats.Returns, rep.Stats.Parsed,
		rep.Stats.Emitted, rep.Stats.Skipped, rep.Stats.ReadErrors, rep.Stats.SplitDict,
		rep.Stats.NonUnicodeDict, rep.Stats.EmptyDict, rep.Stats.RingbufLost)
	for _, requestID := range rep.RequestIDs {
		fmt.Printf("  request_id=%s\n", requestID)
	}
	for _, warning := range rep.Warnings {
		fmt.Printf("WARN %s\n", warning)
	}
}

func writeReport(path string, rep report) error {
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
