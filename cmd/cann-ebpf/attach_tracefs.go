package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cann-ebpf/internal/config"
	"cann-ebpf/internal/discovery"
	"cann-ebpf/internal/elfutil"
	"cann-ebpf/internal/runlog"
	"cann-ebpf/internal/tracefs"
)

func cmdAttachTracefs(args []string) int {
	fs := flag.NewFlagSet("attach-tracefs", flag.ContinueOnError)
	pid := fs.Int("pid", 0, "target pid (required)")
	profileName := fs.String("profile", "vllm-ascend", "profile name or path")
	profilesDir := fs.String("profiles-dir", "profiles", "profiles directory")
	duration := fs.Duration("duration", 10*time.Second, "capture duration")
	outputDir := fs.String("output", "", "output directory")
	only := fs.String("only", "", "comma-separated function names or categories to attach")
	dryRun := fs.Bool("dry-run", false, "resolve and list probes without attaching")
	jsonOut := fs.Bool("json", false, "json summary")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *pid <= 0 {
		fmt.Fprintln(os.Stderr, "attach-tracefs: --pid is required")
		return 2
	}
	prof, _, err := discovery.LoadProfileByName(*profilesDir, *profileName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "attach-tracefs: load profile: %v\n", err)
		return 2
	}
	p, err := discovery.ReadProcess("/proc", *pid)
	if err != nil {
		fmt.Fprintf(os.Stderr, "attach-tracefs: read pid %d: %v\n", *pid, err)
		return 4
	}
	match := discovery.EvaluateMatch(prof.Doc.Spec.Match, p)
	if !match.Matched || match.Excluded {
		fmt.Fprintf(os.Stderr, "attach-tracefs: pid %d does not match profile %s: %+v\n", *pid, prof.Name, match)
		return 3
	}

	categories := symbolCategories(prof.Doc.Spec.Cann.SymbolOverrides)
	onlySet := parseOnly(*only)
	wanted := prof.Doc.Spec.Cann.SymbolOverrides.All()
	libs := discovery.FindLibraries(p, prof.Doc.Spec.Cann.LibraryGlobs)
	seen := map[string]struct{}{}
	var specs []tracefs.SymbolSpec
	for _, lib := range libs {
		symbols, warnings, err := elfutil.Resolve(lib.RealPath, wanted)
		if err != nil {
			fmt.Fprintf(os.Stderr, "attach-tracefs: resolve %s: %v\n", lib.RealPath, err)
			continue
		}
		for _, w := range warnings {
			if *dryRun {
				fmt.Fprintf(os.Stderr, "WARN %s\n", w)
			}
		}
		for _, s := range symbols {
			if !s.Found {
				continue
			}
			category := categories[s.Wanted]
			if len(onlySet) > 0 {
				if _, ok := onlySet[s.Wanted]; !ok {
					if _, ok := onlySet[category]; !ok {
						continue
					}
				}
			}
			key := lib.RealPath + "\x00" + s.Wanted + "\x00" + fmt.Sprint(s.Offset)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			specs = append(specs, tracefs.SymbolSpec{
				Library:  lib.RealPath,
				Function: s.Wanted,
				Category: category,
				Offset:   s.Offset,
			})
		}
	}
	if len(specs) == 0 {
		fmt.Fprintln(os.Stderr, "attach-tracefs: no usable symbols")
		return 3
	}
	if *dryRun {
		for _, s := range specs {
			fmt.Printf("%s %s %s:0x%x\n", s.Category, s.Function, s.Library, s.Offset)
		}
		return 0
	}
	if *outputDir == "" {
		*outputDir = filepath.Join("runs", fmt.Sprintf("tracefs-%d-%d", *pid, time.Now().Unix()))
	}
	ctx, cancel := context.WithTimeout(context.Background(), *duration+10*time.Second)
	defer cancel()
	result, err := tracefs.Run(ctx, specs, tracefs.Options{
		PID:       *pid,
		Duration:  *duration,
		OutputDir: *outputDir,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "attach-tracefs: %v\n", err)
		return 5
	}
	runID := filepath.Base(*outputDir)
	logSpans := make([]runlog.Span, 0, len(result.Spans))
	for _, span := range result.Spans {
		logSpans = append(logSpans, runlog.Span{
			Schema:     runlog.SpanSchemaV1,
			RunID:      runID,
			Profile:    prof.Name,
			TsNS:       int64(span.TimestampNS),
			PID:        span.PID,
			TID:        span.TID,
			TGID:       span.PID,
			Function:   span.Function,
			Library:    span.Library,
			Category:   span.Category,
			DurationNS: int64(span.DurationNS),
		})
	}
	if summary, summaryErr := runlog.BuildSummary(logSpans); summaryErr == nil {
		_ = summary.WriteMarkdown(filepath.Join(*outputDir, "summary.md"))
		_ = summary.WriteJSON(filepath.Join(*outputDir, "summary.json"))
	} else {
		fmt.Fprintf(os.Stderr, "attach-tracefs: summary: %v\n", summaryErr)
	}
	if *jsonOut {
		writeJSON(map[string]any{
			"backend":  "tracefs",
			"pid":      *pid,
			"profile":  prof.Name,
			"spans":    len(result.Spans),
			"output":   *outputDir,
			"started":  result.Started,
			"ended":    result.Ended,
			"warnings": result.Warnings,
		})
	} else {
		fmt.Printf("tracefs pid=%d profile=%s spans=%d duration=%s output=%s\n", *pid, prof.Name, len(result.Spans), *duration, *outputDir)
		for _, w := range result.Warnings {
			fmt.Printf("WARN %s\n", w)
		}
	}
	return 0
}

func parseOnly(value string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if item != "" {
			out[item] = struct{}{}
		}
	}
	return out
}

func symbolCategories(s config.SymbolOverrides) map[string]string {
	out := map[string]string{}
	add := func(category string, names []string) {
		for _, name := range names {
			out[name] = category
		}
	}
	add("sync", s.Sync)
	add("event", s.Event)
	add("handle", s.Handle)
	add("memory", s.Memory)
	add("submit", s.Submit)
	add("hccl", s.Hccl)
	return out
}
