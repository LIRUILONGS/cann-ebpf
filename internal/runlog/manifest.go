package runlog

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const ManifestSchemaV1 = "cann.manifest.v1"

const (
	ManifestStatusRunning  = "running"
	ManifestStatusComplete = "complete"
	ManifestStatusError    = "error"
)

var (
	ErrManifestExists = errors.New("runlog: manifest already exists")
	ErrManifestClosed = errors.New("runlog: manifest store closed")
)

// LibraryRef identifies the exact shared object mapped by the target process.
// Device+Inode+MTime are included so an inode reused after a library upgrade
// cannot be mistaken for the original evidence target.
type LibraryRef struct {
	Path   string     `json:"path"`
	Device uint64     `json:"device"`
	Inode  uint64     `json:"inode"`
	Size   int64      `json:"size,omitempty"`
	MTime  *time.Time `json:"mtime,omitempty"`
}

// SegmentRef describes one JSONL file after it has been durably closed.
type SegmentRef struct {
	Path   string `json:"path"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

// Manifest is the run-level evidence index. EventCount is advanced only after
// bytes have been flushed and fsynced, so a crash cannot make the manifest
// claim data that is not durable. DroppedEvents is reported separately.
type Manifest struct {
	Schema        string       `json:"schema"`
	RunID         string       `json:"run_id"`
	ToolVersion   string       `json:"tool_version"`
	Status        string       `json:"status"`
	Profile       string       `json:"profile"`
	Host          string       `json:"host,omitempty"`
	Kernel        string       `json:"kernel,omitempty"`
	CannVersion   string       `json:"cann_version,omitempty"`
	TargetPIDs    []int        `json:"target_pids"`
	Libraries     []LibraryRef `json:"libraries"`
	StartedAt     time.Time    `json:"started_at"`
	EndedAt       *time.Time   `json:"ended_at,omitempty"`
	EventCount    uint64       `json:"event_count"`
	DroppedEvents uint64       `json:"dropped_events"`
	Warnings      []string     `json:"warnings"`
	Operator      string       `json:"operator,omitempty"`
	Command       string       `json:"command,omitempty"`
	Segments      []SegmentRef `json:"segments"`
	Error         string       `json:"error,omitempty"`
}

// ManifestStore serializes manifest updates and replaces the file atomically.
// A manifest is written immediately at construction, before any span is
// accepted, and every update is durable before returning.
type ManifestStore struct {
	mu     sync.Mutex
	path   string
	value  Manifest
	closed bool
}

func NewManifestStore(path string, manifest Manifest) (*ManifestStore, error) {
	if path == "" {
		return nil, fmt.Errorf("manifest path is empty")
	}
	if _, err := os.Lstat(path); err == nil {
		return nil, fmt.Errorf("%w: %s", ErrManifestExists, path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("stat manifest: %w", err)
	}
	normalizeManifest(&manifest)
	if err := validateManifest(manifest); err != nil {
		return nil, err
	}
	if err := writeJSONAtomic(path, manifest); err != nil {
		return nil, fmt.Errorf("write manifest: %w", err)
	}
	return &ManifestStore{path: filepath.Clean(path), value: manifest}, nil
}

func (s *ManifestStore) Snapshot() Manifest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneManifest(s.value)
}

// Update applies fn to a copy, validates it, durably writes it, and only then
// publishes the new value. A failed write leaves the previous manifest intact.
func (s *ManifestStore) Update(fn func(*Manifest)) error {
	if fn == nil {
		return fmt.Errorf("manifest update function is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrManifestClosed
	}
	next := cloneManifest(s.value)
	fn(&next)
	normalizeManifest(&next)
	if err := validateManifest(next); err != nil {
		return err
	}
	if err := writeJSONAtomic(s.path, next); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}
	s.value = next
	return nil
}

func (s *ManifestStore) Finish(endedAt time.Time, runErr error) error {
	if endedAt.IsZero() {
		endedAt = time.Now()
	}
	err := s.Update(func(manifest *Manifest) {
		manifest.EndedAt = timePointer(endedAt)
		if runErr != nil {
			manifest.Status = ManifestStatusError
			manifest.Error = runErr.Error()
		} else {
			manifest.Status = ManifestStatusComplete
			manifest.Error = ""
		}
	})
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	return nil
}

func normalizeManifest(manifest *Manifest) {
	if manifest.Schema == "" {
		manifest.Schema = ManifestSchemaV1
	}
	if manifest.Status == "" {
		manifest.Status = ManifestStatusRunning
	}
	if manifest.StartedAt.IsZero() {
		manifest.StartedAt = time.Now()
	}
	if manifest.TargetPIDs == nil {
		manifest.TargetPIDs = []int{}
	}
	if manifest.Libraries == nil {
		manifest.Libraries = []LibraryRef{}
	}
	if manifest.Warnings == nil {
		manifest.Warnings = []string{}
	}
	if manifest.Segments == nil {
		manifest.Segments = []SegmentRef{}
	}
}

func validateManifest(manifest Manifest) error {
	if manifest.Schema != ManifestSchemaV1 {
		return fmt.Errorf("manifest schema: got %q, want %q", manifest.Schema, ManifestSchemaV1)
	}
	if manifest.RunID == "" {
		return fmt.Errorf("manifest run_id is empty")
	}
	if manifest.ToolVersion == "" {
		return fmt.Errorf("manifest tool_version is empty")
	}
	if manifest.Profile == "" {
		return fmt.Errorf("manifest profile is empty")
	}
	if len(manifest.TargetPIDs) == 0 {
		return fmt.Errorf("manifest target_pids is empty")
	}
	for _, pid := range manifest.TargetPIDs {
		if pid <= 0 {
			return fmt.Errorf("manifest target pid must be positive: %d", pid)
		}
	}
	if manifest.StartedAt.IsZero() {
		return fmt.Errorf("manifest started_at is empty")
	}
	switch manifest.Status {
	case ManifestStatusRunning:
	case ManifestStatusComplete, ManifestStatusError:
		if manifest.EndedAt == nil {
			return fmt.Errorf("manifest status %q requires ended_at", manifest.Status)
		}
		if manifest.Status == ManifestStatusError && manifest.Error == "" {
			return fmt.Errorf("manifest status %q requires error text", manifest.Status)
		}
	default:
		return fmt.Errorf("manifest status %q is invalid", manifest.Status)
	}
	for i, segment := range manifest.Segments {
		if segment.Path == "" {
			return fmt.Errorf("manifest segments[%d].path is empty", i)
		}
		if segment.Bytes < 0 {
			return fmt.Errorf("manifest segments[%d].bytes is negative", i)
		}
	}
	return nil
}

func cloneManifest(manifest Manifest) Manifest {
	clone := manifest
	clone.TargetPIDs = append([]int(nil), manifest.TargetPIDs...)
	if manifest.Libraries != nil {
		clone.Libraries = make([]LibraryRef, len(manifest.Libraries))
		for i, library := range manifest.Libraries {
			clone.Libraries[i] = library
			clone.Libraries[i].MTime = copyTimePointer(library.MTime)
		}
	}
	clone.Warnings = append([]string(nil), manifest.Warnings...)
	clone.Segments = append([]SegmentRef(nil), manifest.Segments...)
	clone.EndedAt = copyTimePointer(manifest.EndedAt)
	if clone.TargetPIDs == nil {
		clone.TargetPIDs = []int{}
	}
	if clone.Warnings == nil {
		clone.Warnings = []string{}
	}
	if clone.Segments == nil {
		clone.Segments = []SegmentRef{}
	}
	return clone
}

func copyTimePointer(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func timePointer(value time.Time) *time.Time {
	return &value
}

func ensureDir(path string) error {
	if path == "" || path == "." {
		return nil
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("create directory %s: %w", path, err)
	}
	return nil
}

func writeJSONAtomic(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return writeFileAtomic(path, data, 0o600)
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	if path == "" {
		return fmt.Errorf("output path is empty")
	}
	dir := filepath.Dir(path)
	if err := ensureDir(dir); err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)

	if err := temp.Chmod(mode); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		return err
	}
	return nil
}
