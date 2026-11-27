package metrics

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ProcSource reads process-level counters and gauges from procfs. It never
// guesses a PID: the caller must pass explicit --pid values.
type ProcSource struct {
	PIDs     []int
	Root     string
	Every    time.Duration
	Deadline time.Duration
}

// NewProcSource creates a procfs source. Root defaults to /proc.
func NewProcSource(pids []int, root string, interval, timeout time.Duration) *ProcSource {
	if root == "" {
		root = "/proc"
	}
	if interval <= 0 {
		interval = time.Second
	}
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	return &ProcSource{PIDs: append([]int(nil), pids...), Root: root, Every: interval, Deadline: timeout}
}

func (s *ProcSource) Name() string            { return "proc" }
func (s *ProcSource) Interval() time.Duration { return s.Every }
func (s *ProcSource) Timeout() time.Duration  { return s.Deadline }

func (s *ProcSource) Collect(ctx context.Context) ([]Point, []string, error) {
	var points []Point
	var warnings []string
	for _, pid := range s.PIDs {
		if err := ctx.Err(); err != nil {
			return points, warnings, err
		}
		if pid <= 0 {
			warnings = append(warnings, fmt.Sprintf("proc: invalid pid %d", pid))
			continue
		}
		pidPoints, pidWarnings := collectProcPID(s.Root, pid)
		points = append(points, pidPoints...)
		warnings = append(warnings, pidWarnings...)
	}
	return points, warnings, nil
}

func collectProcPID(root string, pid int) ([]Point, []string) {
	dir := filepath.Join(root, strconv.Itoa(pid))
	var points []Point
	var warnings []string
	labels := map[string]string{"pid": strconv.Itoa(pid)}

	stat, err := readProcStat(filepath.Join(dir, "stat"))
	if err != nil {
		return nil, []string{fmt.Sprintf("proc pid %d: %v", pid, err)}
	}
	if stat.comm != "" {
		labels["comm"] = stat.comm
	}
	userSeconds := float64(stat.utimeTicks) / 100.0
	systemSeconds := float64(stat.stimeTicks) / 100.0
	points = append(points,
		withProcIdentity(Counter("proc_cpu_user_seconds", "seconds", userSeconds, labels)),
		withProcIdentity(Counter("proc_cpu_system_seconds", "seconds", systemSeconds, labels)),
	)
	if stat.threads > 0 {
		points = append(points, withProcIdentity(Gauge("proc_threads", "count", float64(stat.threads), labels)))
	}

	status, statusErr := readProcStatus(filepath.Join(dir, "status"))
	if statusErr != nil {
		warnings = append(warnings, fmt.Sprintf("proc pid %d: %v", pid, statusErr))
	}
	if status.rssBytes != nil {
		points = append(points, withProcIdentity(Gauge("proc_rss_bytes", "bytes", float64(*status.rssBytes), labels)))
	}
	if status.threads != nil && stat.threads <= 0 {
		points = append(points, withProcIdentity(Gauge("proc_threads", "count", float64(*status.threads), labels)))
	}

	if sched, err := readProcSchedstat(filepath.Join(dir, "schedstat")); err == nil {
		points = append(points,
			withProcIdentity(Counter("proc_sched_run_delay_seconds", "seconds", float64(sched.waitingNS)/1e9, labels)),
			withProcIdentity(Counter("proc_sched_running_seconds", "seconds", float64(sched.runningNS)/1e9, labels)),
			withProcIdentity(Counter("proc_sched_timeslices", "count", float64(sched.timeslices), labels)),
		)
	} else if !os.IsNotExist(err) {
		warnings = append(warnings, fmt.Sprintf("proc pid %d: %v", pid, err))
	}
	if ioStats, err := readProcIO(filepath.Join(dir, "io")); err == nil {
		points = append(points,
			withProcIdentity(Counter("proc_io_read_bytes", "bytes", float64(ioStats.readBytes), labels)),
			withProcIdentity(Counter("proc_io_write_bytes", "bytes", float64(ioStats.writeBytes), labels)),
		)
	} else if !os.IsNotExist(err) {
		warnings = append(warnings, fmt.Sprintf("proc pid %d: %v", pid, err))
	}
	return points, warnings
}

func withProcIdentity(p Point) Point {
	p.Source = "proc"
	p.Device = "host"
	return p
}

type procStat struct {
	comm       string
	utimeTicks int64
	stimeTicks int64
	threads    int64
}

func readProcStat(path string) (procStat, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return procStat{}, err
	}
	text := string(data)
	open := strings.IndexByte(text, '(')
	close := strings.LastIndexByte(text, ')')
	if open < 0 || close <= open {
		return procStat{}, fmt.Errorf("malformed stat: missing comm")
	}
	stat := procStat{comm: text[open+1 : close]}
	fields := strings.Fields(text[close+1:])
	// fields[0] is field 3 (state); utime/stime are fields 14/15 and
	// num_threads is field 20.
	if len(fields) < 18 {
		return procStat{}, fmt.Errorf("malformed stat: %d fields", len(fields))
	}
	if stat.utimeTicks, err = strconv.ParseInt(fields[11], 10, 64); err != nil {
		return procStat{}, fmt.Errorf("stat utime: %w", err)
	}
	if stat.stimeTicks, err = strconv.ParseInt(fields[12], 10, 64); err != nil {
		return procStat{}, fmt.Errorf("stat stime: %w", err)
	}
	if stat.threads, err = strconv.ParseInt(fields[17], 10, 64); err != nil {
		return procStat{}, fmt.Errorf("stat threads: %w", err)
	}
	return stat, nil
}

type procStatus struct {
	rssBytes *int64
	threads  *int64
}

func readProcStatus(path string) (procStatus, error) {
	file, err := os.Open(path)
	if err != nil {
		return procStatus{}, err
	}
	defer file.Close()
	var out procStatus
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		fields := strings.Fields(value)
		if len(fields) == 0 {
			continue
		}
		switch strings.TrimSpace(key) {
		case "VmRSS":
			if len(fields) >= 2 && strings.EqualFold(fields[1], "kB") {
				if kb, err := strconv.ParseInt(fields[0], 10, 64); err == nil {
					bytes := kb * 1024
					out.rssBytes = &bytes
				}
			}
		case "Threads":
			if threads, err := strconv.ParseInt(fields[0], 10, 64); err == nil {
				out.threads = &threads
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return procStatus{}, err
	}
	return out, nil
}

type procSchedstat struct {
	runningNS  int64
	waitingNS  int64
	timeslices int64
}

func readProcSchedstat(path string) (procSchedstat, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return procSchedstat{}, err
	}
	fields := strings.Fields(string(data))
	if len(fields) < 3 {
		return procSchedstat{}, fmt.Errorf("malformed schedstat")
	}
	var out procSchedstat
	if out.runningNS, err = strconv.ParseInt(fields[0], 10, 64); err != nil {
		return out, err
	}
	if out.waitingNS, err = strconv.ParseInt(fields[1], 10, 64); err != nil {
		return out, err
	}
	if out.timeslices, err = strconv.ParseInt(fields[2], 10, 64); err != nil {
		return out, err
	}
	return out, nil
}

type procIO struct {
	readBytes  int64
	writeBytes int64
}

func readProcIO(path string) (procIO, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return procIO{}, err
	}
	var out procIO
	for _, line := range strings.Split(string(data), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		number, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if err != nil {
			continue
		}
		switch strings.TrimSpace(key) {
		case "read_bytes":
			out.readBytes = number
		case "write_bytes":
			out.writeBytes = number
		}
	}
	return out, nil
}
