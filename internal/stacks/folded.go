package stacks

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
)

var foldedNameReplacer = strings.NewReplacer(
	"\r\n", " ",
	"\n", " ",
	"\r", " ",
	";", ":",
)

// WriteFolded emits collapsed stacks in flame-graph folded format. Stacks that
// render to the same line are aggregated, and lines are sorted for deterministic
// output and diffs.
func WriteFolded(w io.Writer, profile *Profile) error {
	if profile == nil {
		return fmt.Errorf("write folded: nil profile")
	}

	counts := make(map[string]uint64, len(profile.Stacks))
	for stackIndex, stack := range profile.Stacks {
		if len(stack.Frames) == 0 {
			return fmt.Errorf("write folded: stack %d has no frames", stackIndex)
		}
		for frameIndex, frame := range stack.Frames {
			if foldedFrameName(frame.Name) == "" {
				return fmt.Errorf("write folded: stack %d frame %d has an empty name", stackIndex, frameIndex)
			}
		}
		stackName := foldedStackString(stack.Frames)
		counts[stackName] += stack.Count
	}

	lines := make([]string, 0, len(counts))
	for stackName, count := range counts {
		lines = append(lines, stackName+" "+strconv.FormatUint(count, 10))
	}
	sort.Strings(lines)

	buffered := bufio.NewWriter(w)
	for _, line := range lines {
		if _, err := fmt.Fprintln(buffered, line); err != nil {
			return err
		}
	}
	return buffered.Flush()
}

// ReadFolded loads a folded stack file. Each frame is reconstructed without a
// category because the folded format does not carry classification metadata.
func ReadFolded(path string) (*Profile, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open folded %s: %w", path, err)
	}
	defer file.Close()
	return readFolded(file, path)
}

func readFolded(reader io.Reader, source string) (*Profile, error) {
	profile := &Profile{Stacks: make([]ResolvedStack, 0)}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 1024*1024)

	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := scanner.Text()
		if line == "" {
			return nil, fmt.Errorf("%s:%d: empty folded line", source, lineNumber)
		}

		separator := strings.LastIndexByte(line, ' ')
		if separator <= 0 || separator == len(line)-1 {
			return nil, fmt.Errorf("%s:%d: missing stack/count separator", source, lineNumber)
		}
		stackName := line[:separator]
		countText := line[separator+1:]
		count, err := strconv.ParseUint(countText, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("%s:%d: invalid count %q: %w", source, lineNumber, countText, err)
		}

		frameNames := strings.Split(stackName, ";")
		frames := make([]Frame, 0, len(frameNames))
		for _, frameName := range frameNames {
			if frameName == "" {
				return nil, fmt.Errorf("%s:%d: empty frame name", source, lineNumber)
			}
			frames = append(frames, Frame{
				Name:    frameName,
				Unknown: frameName == "[unknown]",
			})
		}
		profile.Stacks = append(profile.Stacks, ResolvedStack{Frames: frames, Count: count})
		profile.Total += count
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("%s:%d: scan folded: %w", source, lineNumber+1, err)
	}
	return profile, nil
}

func foldedStackString(frames []Frame) string {
	names := make([]string, 0, len(frames))
	for _, frame := range frames {
		names = append(names, foldedFrameName(frame.Name))
	}
	return strings.Join(names, ";")
}

func foldedFrameName(name string) string {
	return foldedNameReplacer.Replace(name)
}
