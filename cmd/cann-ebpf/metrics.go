package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"cann-ebpf/internal/export"
)

// cmdMetrics exports a completed run as Prometheus text metrics. With --listen
// it serves /metrics and re-reads the latest run on every scrape; without it,
// it prints the text (usable as a node_exporter textfile collector).
func cmdMetrics(args []string) int {
	fs := flag.NewFlagSet("metrics", flag.ContinueOnError)
	input := fs.String("input", "", "run directory or parent directory containing run directories")
	listen := fs.String("listen", "", "serve metrics on this address, e.g. 127.0.0.1:9101")
	outPath := fs.String("out", "", "write metrics text to this file instead of stdout")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if strings.TrimSpace(*input) == "" {
		fmt.Fprintln(os.Stderr, "metrics: --input is required")
		return 2
	}
	if strings.TrimSpace(*listen) != "" {
		return serveMetrics(*input, *listen)
	}
	text, run, err := export.Build(*input)
	if err != nil {
		fmt.Fprintf(os.Stderr, "metrics: %v\n", err)
		return 4
	}
	if strings.TrimSpace(*outPath) != "" {
		if err := os.WriteFile(*outPath, []byte(text), 0o600); err != nil {
			fmt.Fprintf(os.Stderr, "metrics: write %s: %v\n", *outPath, err)
			return 4
		}
		fmt.Printf("metrics run=%s out=%s bytes=%d\n", run, *outPath, len(text))
		return 0
	}
	fmt.Print(text)
	return 0
}

func serveMetrics(input, addr string) int {
	mux := http.NewServeMux()
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		text, _, err := export.Build(input)
		if err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprintf(w, "# cann-ebpf metrics error: %s\n", err)
			return
		}
		w.Write([]byte(text))
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "ok")
	})
	server := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	fmt.Fprintf(os.Stderr, "metrics listening on %s (input=%s)\n", addr, input)
	if err := server.ListenAndServe(); err != nil {
		fmt.Fprintf(os.Stderr, "metrics: %v\n", err)
		return 4
	}
	return 0
}
