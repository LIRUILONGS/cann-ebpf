package discovery

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type MapEntry struct {
	Start  uint64 `json:"start"`
	End    uint64 `json:"end"`
	Perms  string `json:"perms"`
	Offset uint64 `json:"offset"`
	Dev    string `json:"dev"`
	Inode  uint64 `json:"inode"`
	Path   string `json:"path"`
}

type Process struct {
	PID            int               `json:"pid"`
	PPID           int               `json:"ppid"`
	Comm           string            `json:"comm"`
	Cmdline        string            `json:"cmdline"`
	Exe            string            `json:"exe"`
	Root           string            `json:"root"`
	Cgroup         string            `json:"cgroup"`
	StartTime      uint64            `json:"start_time"`
	BootID         string            `json:"boot_id"`
	Maps           []MapEntry        `json:"maps"`
	Namespaces     map[string]string `json:"namespaces"`
	ContainerID    string            `json:"container_id,omitempty"`
	ContainerName  string            `json:"container_name,omitempty"`
	ContainerImage string            `json:"container_image,omitempty"`

	listens map[int]bool
}

func FindProcesses(procRoot string) ([]*Process, error) {
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return nil, err
	}
	var out []*Process
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 {
			continue
		}
		p, err := ReadProcess(procRoot, pid)
		if err != nil {
			continue
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PID < out[j].PID })
	return out, nil
}

func ReadProcess(procRoot string, pid int) (*Process, error) {
	base := filepath.Join(procRoot, strconv.Itoa(pid))
	if _, err := os.Stat(base); err != nil {
		return nil, err
	}
	p := &Process{PID: pid, Namespaces: map[string]string{}}
	stat, err := os.ReadFile(filepath.Join(base, "stat"))
	if err != nil {
		return nil, err
	}
	comm, ppid, start, err := parseStat(string(stat))
	if err != nil {
		return nil, err
	}
	p.Comm, p.PPID, p.StartTime = comm, ppid, start
	p.Cmdline = readCmdline(filepath.Join(base, "cmdline"))
	p.Exe = readlink(filepath.Join(base, "exe"))
	p.Root = readlink(filepath.Join(base, "root"))
	p.Cgroup = readFileString(filepath.Join(base, "cgroup"))
	p.BootID = readFileString(filepath.Join(procRoot, "sys/kernel/random/boot_id"))
	p.Maps = readMaps(filepath.Join(base, "maps"))
	p.Namespaces = readNamespaces(filepath.Join(base, "ns"))
	p.ContainerID = containerIDFromCgroup(p.Cgroup)
	if p.ContainerID != "" {
		p.ContainerName, p.ContainerImage = dockerMeta(p.ContainerID)
	}
	return p, nil
}

func parseStat(s string) (comm string, ppid int, startTime uint64, err error) {
	open := strings.IndexByte(s, '(')
	close := strings.LastIndexByte(s, ')')
	if open < 0 || close <= open {
		return "", 0, 0, fmt.Errorf("malformed stat")
	}
	comm = s[open+1 : close]
	fields := strings.Fields(s[close+1:])
	if len(fields) < 20 {
		return "", 0, 0, fmt.Errorf("malformed stat fields")
	}
	ppid, err = strconv.Atoi(fields[1])
	if err != nil {
		return "", 0, 0, err
	}
	startTime, err = strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return "", 0, 0, err
	}
	return comm, ppid, startTime, nil
}

func readCmdline(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(strings.ReplaceAll(string(b), "\x00", " "))
}

func readlink(path string) string {
	s, err := os.Readlink(path)
	if err != nil {
		return ""
	}
	return s
}

func readFileString(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func readMaps(path string) []MapEntry {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []MapEntry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 6 {
			continue
		}
		var e MapEntry
		if _, err := fmt.Sscanf(fields[0], "%x-%x", &e.Start, &e.End); err != nil {
			continue
		}
		e.Perms = fields[1]
		e.Offset, _ = strconv.ParseUint(fields[2], 16, 64)
		e.Dev = fields[3]
		e.Inode, _ = strconv.ParseUint(fields[4], 10, 64)
		e.Path = strings.Join(fields[5:], " ")
		if e.Path == "" || e.Path == "[anonymous]" {
			continue
		}
		out = append(out, e)
	}
	return out
}

func readNamespaces(path string) map[string]string {
	out := map[string]string{}
	entries, err := os.ReadDir(path)
	if err != nil {
		return out
	}
	for _, e := range entries {
		out[e.Name()] = readlink(filepath.Join(path, e.Name()))
	}
	return out
}

var containerIDRe = regexp.MustCompile(`(?:docker|containerd|cri-containerd)[-/]([0-9a-f]{12,64})`)

func containerIDFromCgroup(cgroup string) string {
	m := containerIDRe.FindStringSubmatch(cgroup)
	if len(m) == 2 {
		return m[1]
	}
	return ""
}

func dockerMeta(id string) (name, image string) {
	b, err := os.ReadFile(filepath.Join("/var/lib/docker/containers", id, "config.v2.json"))
	if err != nil {
		return "", ""
	}
	var meta struct {
		Name   string
		Image  string
		Config struct {
			Image string
		}
	}
	if json.Unmarshal(b, &meta) != nil {
		return "", ""
	}
	name = strings.TrimPrefix(meta.Name, "/")
	image = meta.Config.Image
	if image == "" {
		image = meta.Image
	}
	return name, image
}

func (p *Process) HasMap(substr string) bool {
	for _, m := range p.Maps {
		if strings.Contains(m.Path, substr) {
			return true
		}
	}
	return false
}

func (p *Process) MapPaths() []string {
	seen := map[string]struct{}{}
	var out []string
	for _, m := range p.Maps {
		if _, ok := seen[m.Path]; ok {
			continue
		}
		seen[m.Path] = struct{}{}
		out = append(out, m.Path)
	}
	return out
}

func (p *Process) ListensOn(port int) bool {
	if p.listens == nil {
		p.listens = loadListenPorts(p.PID)
	}
	return p.listens[port]
}

func loadListenPorts(pid int) map[int]bool {
	out := map[int]bool{}
	sockets := map[uint64]int{}
	for _, name := range []string{"tcp", "tcp6"} {
		for inode, port := range parseNetTCP(filepath.Join("/proc", strconv.Itoa(pid), "net", name)) {
			sockets[inode] = port
		}
	}
	fdDir := filepath.Join("/proc", strconv.Itoa(pid), "fd")
	entries, err := os.ReadDir(fdDir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		link := readlink(filepath.Join(fdDir, e.Name()))
		if !strings.HasPrefix(link, "socket:[") {
			continue
		}
		inodeStr := strings.TrimSuffix(strings.TrimPrefix(link, "socket:["), "]")
		inode, err := strconv.ParseUint(inodeStr, 10, 64)
		if err != nil {
			continue
		}
		if port, ok := sockets[inode]; ok {
			out[port] = true
		}
	}
	return out
}

func parseNetTCP(path string) map[uint64]int {
	out := map[uint64]int{}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	first := true
	for sc.Scan() {
		if first {
			first = false
			continue
		}
		fields := strings.Fields(sc.Text())
		if len(fields) < 10 || fields[3] != "0A" {
			continue
		}
		colon := strings.LastIndexByte(fields[1], ':')
		if colon < 0 {
			continue
		}
		port64, err := strconv.ParseUint(fields[1][colon+1:], 16, 16)
		if err != nil {
			continue
		}
		inode, err := strconv.ParseUint(fields[9], 10, 64)
		if err != nil {
			continue
		}
		out[inode] = int(port64)
	}
	return out
}
