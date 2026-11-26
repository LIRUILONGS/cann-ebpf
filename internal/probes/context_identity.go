package probes

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"cann-ebpf/internal/contextfeed"
)

type processCredential struct {
	UID uint32
	GID uint32
}

// contextSocketOptions derives the target UIDs from procfs. The plugin runs
// inside the vLLM container and therefore cannot be assumed to share root's
// UID. The socket owner is chosen from a non-root target when one exists.
func contextSocketOptions(procRoot string, pids []int) (contextfeed.StartOptions, []processCredential) {
	var options contextfeed.StartOptions
	seenUID := map[uint32]struct{}{}
	var credentials []processCredential
	for _, pid := range pids {
		credential, err := readProcessCredential(filepath.Join(procRoot, strconv.Itoa(pid), "status"))
		if err != nil {
			continue
		}
		if _, ok := seenUID[credential.UID]; !ok {
			seenUID[credential.UID] = struct{}{}
			options.AllowedUIDs = append(options.AllowedUIDs, credential.UID)
		}
		credentials = append(credentials, credential)
	}
	for _, credential := range credentials {
		if credential.UID == 0 {
			continue
		}
		options.OwnerUID = credential.UID
		options.OwnerGID = credential.GID
		options.Chown = true
		break
	}
	if !options.Chown && len(credentials) > 0 {
		options.OwnerUID = credentials[0].UID
		options.OwnerGID = credentials[0].GID
		options.Chown = true
	}
	return options, credentials
}

func readProcessCredential(path string) (processCredential, error) {
	var credential processCredential
	data, err := os.ReadFile(path)
	if err != nil {
		return credential, err
	}
	var uidSet, gidSet bool
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		switch fields[0] {
		case "Uid:":
			value, parseErr := strconv.ParseUint(fields[1], 10, 32)
			if parseErr != nil {
				return credential, fmt.Errorf("parse Uid: %w", parseErr)
			}
			credential.UID = uint32(value)
			uidSet = true
		case "Gid:":
			value, parseErr := strconv.ParseUint(fields[1], 10, 32)
			if parseErr != nil {
				return credential, fmt.Errorf("parse Gid: %w", parseErr)
			}
			credential.GID = uint32(value)
			gidSet = true
		}
	}
	if !uidSet || !gidSet {
		return credential, fmt.Errorf("missing Uid or Gid in %s", path)
	}
	return credential, nil
}
