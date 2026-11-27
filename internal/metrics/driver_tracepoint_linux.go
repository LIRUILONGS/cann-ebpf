//go:build linux

package metrics

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// tracepointField is one field parsed from tracefs's event format file.
type tracepointField struct {
	offset int
	size   int
}

// checkDriverTracepointLayout validates the syscall tracepoint payload layout
// before attaching a program whose C context struct has fixed offsets. The
// Linux 5.10 arm64 generic syscall tracepoint has args[] at offset 16. If
// tracefs is not readable, the check is skipped; the attach itself remains the
// authority for whether the tracepoint exists.
func checkDriverTracepointLayout(group, event string) error {
	fields, err := readTracepointFields(group, event)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return validateDriverTracepointFields(event, fields)
}

func validateDriverTracepointFields(event string, fields map[string]tracepointField) error {
	if args, ok := fields["args"]; ok {
		if args.offset != 16 || args.size < 48 {
			return fmt.Errorf("syscalls/%s args offset=%d size=%d, expected offset=16 size>=48", event, args.offset, args.size)
		}
		return nil
	}
	// Some kernels expose named syscall fields instead of the generic args[]
	// array. Check only the fields used by driver_metrics.bpf.c.
	switch event {
	case "sys_enter_ioctl":
		fd, okFD := fields["fd"]
		cmd, okCMD := fields["cmd"]
		if !okFD || !okCMD || fd.offset != 16 || cmd.offset != 24 {
			return fmt.Errorf("syscalls/%s named fields do not match fd@16 cmd@24", event)
		}
	case "sys_enter_mmap":
		addr, okAddr := fields["addr"]
		length, okLen := fields["len"]
		fd, okFD := fields["fd"]
		if !okAddr || !okLen || !okFD || addr.offset != 16 || length.offset != 24 || fd.offset != 48 {
			return fmt.Errorf("syscalls/%s named fields do not match addr@16 len@24 fd@48", event)
		}
	}
	return nil
}

func readTracepointFields(group, event string) (map[string]tracepointField, error) {
	var lastErr error
	for _, root := range []string{"/sys/kernel/tracing", "/sys/kernel/debug/tracing"} {
		path := filepath.Join(root, "events", group, event, "format")
		data, err := os.ReadFile(path)
		if err == nil {
			return parseTracepointFields(data)
		}
		if !os.IsNotExist(err) {
			lastErr = fmt.Errorf("read %s: %w", path, err)
		}
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, os.ErrNotExist
}

func parseTracepointFields(data []byte) (map[string]tracepointField, error) {
	fields := make(map[string]tracepointField)
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "field:") {
			continue
		}
		parts := strings.Split(line, ";")
		decl := strings.TrimSpace(strings.TrimPrefix(parts[0], "field:"))
		words := strings.Fields(decl)
		if len(words) == 0 {
			continue
		}
		name := words[len(words)-1]
		if index := strings.IndexByte(name, '['); index >= 0 {
			name = name[:index]
		}
		field := tracepointField{offset: -1, size: -1}
		for _, part := range parts[1:] {
			part = strings.TrimSpace(part)
			switch {
			case strings.HasPrefix(part, "offset:"):
				field.offset = parseFormatInt(strings.TrimPrefix(part, "offset:"))
			case strings.HasPrefix(part, "size:"):
				field.size = parseFormatInt(strings.TrimPrefix(part, "size:"))
			}
		}
		if field.offset < 0 || field.size <= 0 {
			return nil, fmt.Errorf("malformed tracepoint field %q", line)
		}
		fields[name] = field
	}
	if len(fields) == 0 {
		return nil, fmt.Errorf("tracepoint format has no field entries")
	}
	return fields, nil
}

func parseFormatInt(value string) int {
	value = strings.TrimSpace(value)
	if index := strings.IndexByte(value, ' '); index >= 0 {
		value = value[:index]
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return -1
	}
	return parsed
}
