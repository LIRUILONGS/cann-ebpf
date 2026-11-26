package hardware

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// CommandRunner lets tests replace the npu-smi invocation.
type CommandRunner func(ctx context.Context, name string, args ...string) ([]byte, error)

// Result summarises one hardware sampling session.
type Result struct {
	Enabled   bool     `json:"enabled"`
	Available bool     `json:"available"`
	Source    string   `json:"source,omitempty"`
	Samples   int      `json:"samples"`
	Devices   int      `json:"devices"`
	IntervalS float64  `json:"interval_s,omitempty"`
	Path      string   `json:"path,omitempty"`
	Warnings  []string `json:"warnings,omitempty"`
}

// Collector samples npu-smi on an interval and writes JSONL. Sampling failures
// are warnings: hardware metrics are an optional coarse layer and must never
// break host span collection.
type Collector struct {
	mu      sync.Mutex
	opts    collectorOptions
	file    *os.File
	writer  *bufio.Writer
	samples int
	devices int
	lastErr string
	cancel  context.CancelFunc
	done    chan struct{}
	stopped bool
}

type collectorOptions struct {
	interval time.Duration
	path     string
	runner   CommandRunner
	now      func() time.Time
}

// Start samples immediately and then on the configured interval. It returns a
// collector even when npu-smi is unavailable; callers report Result.Available.
func Start(ctx context.Context, outputPath string, interval time.Duration) (*Collector, []string) {
	if interval <= 0 {
		interval = 10 * time.Second
	}
	runCtx, cancel := context.WithCancel(ctx)
	c := &Collector{
		opts: collectorOptions{
			interval: interval, path: outputPath, runner: execRunner,
			now: time.Now,
		},
		cancel: cancel,
		done:   make(chan struct{}),
	}
	var warnings []string
	if _, err := exec.LookPath("npu-smi"); err != nil {
		warnings = append(warnings, "hardware disabled: npu-smi not found in PATH")
		cancel()
		close(c.done)
		c.stopped = true
		return c, warnings
	}
	if outputPath != "" {
		if err := os.MkdirAll(filepath.Dir(outputPath), 0o700); err != nil {
			warnings = append(warnings, "hardware output directory: "+err.Error())
		} else {
			file, err := os.OpenFile(outputPath, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
			if err != nil {
				warnings = append(warnings, "hardware output file: "+err.Error())
			} else {
				c.file = file
				c.writer = bufio.NewWriter(file)
			}
		}
	}
	c.sampleOnce(runCtx)
	go c.loop(runCtx)
	return c, warnings
}

func execRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	return cmd.Output()
}

func (c *Collector) loop(ctx context.Context) {
	defer close(c.done)
	ticker := time.NewTicker(c.opts.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.sampleOnce(ctx)
		}
	}
}

func (c *Collector) sampleOnce(ctx context.Context) {
	sampleCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	raw, err := c.opts.runner(sampleCtx, "npu-smi", "info")
	if err != nil {
		c.mu.Lock()
		if c.lastErr != err.Error() {
			c.lastErr = err.Error()
		}
		c.mu.Unlock()
		return
	}
	samples, err := ParseNPUSmiInfo(raw)
	if err != nil {
		c.mu.Lock()
		c.lastErr = err.Error()
		c.mu.Unlock()
		return
	}
	now := c.opts.now().UnixNano()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.devices = len(samples)
	for i := range samples {
		samples[i].TsNS = now
		if c.writer != nil {
			data, marshalErr := json.Marshal(samples[i])
			if marshalErr == nil {
				c.writer.Write(data)
				c.writer.WriteByte('\n')
				c.writer.Flush()
			}
		}
	}
	c.samples++
}

// Stop stops sampling, flushes and closes the output file.
func (c *Collector) Stop() Result {
	c.mu.Lock()
	if c.stopped {
		result := c.resultLocked()
		c.mu.Unlock()
		return result
	}
	c.stopped = true
	c.mu.Unlock()

	c.cancel()
	<-c.done

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.writer != nil {
		c.writer.Flush()
	}
	if c.file != nil {
		c.file.Sync()
		c.file.Close()
		c.file = nil
	}
	return c.resultLocked()
}

func (c *Collector) resultLocked() Result {
	result := Result{
		Enabled: true, Samples: c.samples, Devices: c.devices,
		IntervalS: c.opts.interval.Seconds(), Path: c.opts.path,
	}
	if c.samples > 0 {
		result.Available = true
		result.Source = "npu-smi"
	}
	if c.lastErr != "" {
		result.Warnings = append(result.Warnings, "hardware sampling: "+c.lastErr)
	}
	return result
}

// FormatSampleLine is exported for tests that build fixtures.
func FormatSampleLine(s Sample) (string, error) {
	data, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
