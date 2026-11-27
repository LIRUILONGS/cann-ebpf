package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
)

// cmdPIDs prints host PIDs for a known target profile. It does not attach,
// sample, or start a report; it only helps build explicit --pid arguments.
func cmdPIDs(args []string) int {
	fs := flag.NewFlagSet("pids", flag.ContinueOnError)
	profile := fs.String("profile", "vllm-ascend", "vllm-ascend, orinfer, or auto")
	jsonOut := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	patterns := map[string]string{
		"vllm-ascend": "vllm serve|VLLM::Worker_TP|VLLM::EngineCore",
		"orinfer":     "/opt/orinfer/bin/orinfer serve",
	}
	var listed []int
	var selected string

	pick := func(name string) []int {
		out, err := exec.Command("pgrep", "-f", patterns[name]).Output()
		if err != nil {
			return nil
		}
		seen := map[int]struct{}{}
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			pid, err := strconv.Atoi(strings.TrimSpace(line))
			if err == nil && pid > 0 {
				seen[pid] = struct{}{}
			}
		}
		pids := make([]int, 0, len(seen))
		for pid := range seen {
			pids = append(pids, pid)
		}
		sort.Ints(pids)
		return pids
	}

	switch *profile {
	case "vllm-ascend", "orinfer":
		selected = *profile
		listed = pick(selected)
	case "auto":
		if pids := pick("orinfer"); len(pids) > 0 {
			selected, listed = "orinfer", pids
		} else if pids := pick("vllm-ascend"); len(pids) > 0 {
			selected, listed = "vllm-ascend", pids
		}
	default:
		fmt.Fprintln(os.Stderr, "pids: --profile must be vllm-ascend, orinfer, or auto")
		return 2
	}

	if len(listed) == 0 {
		fmt.Fprintf(os.Stderr, "pids: no process matched profile %s\n", *profile)
		fmt.Fprintln(os.Stderr, "  vLLM:    pgrep -af 'vllm serve|VLLM::Worker_TP|VLLM::EngineCore'")
		fmt.Fprintln(os.Stderr, "  Orinfer: pgrep -af '/opt/orinfer/bin/orinfer serve'")
		return 4
	}

	if *jsonOut {
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
			"profile": selected,
			"pids":    listed,
		})
		return 0
	}
	parts := make([]string, 0, len(listed)*2)
	for _, pid := range listed {
		parts = append(parts, "--pid", strconv.Itoa(pid))
	}
	fmt.Println(strings.Join(parts, " "))
	return 0
}
