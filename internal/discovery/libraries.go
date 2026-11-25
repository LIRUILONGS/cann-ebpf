package discovery

import (
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type LibraryFile struct {
	Path     string    `json:"path"`
	RealPath string    `json:"real_path"`
	Inode    uint64    `json:"inode"`
	Size     int64     `json:"size"`
	ModTime  time.Time `json:"mod_time"`
	Note     string    `json:"note,omitempty"`
}

var libraryMarkers = []string{
	"libascendcl.so",
	"libruntime.so",
	"libhccl.so",
	"libmspti.so",
	"libtorch_npu.so",
}

func FindLibraries(p *Process, globs []string) []LibraryFile {
	seen := map[string]struct{}{}
	var paths []string
	for _, mp := range p.MapPaths() {
		for _, marker := range libraryMarkers {
			if strings.Contains(mp, marker) {
				paths = append(paths, mp)
				break
			}
		}
	}
	if len(paths) == 0 {
		root := p.Root
		if root == "" {
			root = "/"
		}
		for _, pattern := range globs {
			for _, path := range globLibrary(root, pattern) {
				paths = append(paths, path)
			}
		}
	}
	var out []LibraryFile
	for _, path := range paths {
		real, note := resolveLibraryPath(p, path)
		if _, ok := seen[real]; ok {
			continue
		}
		seen[real] = struct{}{}
		info, err := os.Stat(real)
		if err != nil {
			if note != "" {
				note += "; "
			}
			note += err.Error()
			out = append(out, LibraryFile{Path: path, RealPath: real, Note: note})
			continue
		}
		out = append(out, LibraryFile{
			Path:     path,
			RealPath: real,
			Inode:    inodeOf(info),
			Size:     info.Size(),
			ModTime:  info.ModTime(),
			Note:     note,
		})
	}
	return out
}

// resolveLibraryPath resolves a path recorded in /proc/<pid>/maps against the
// target process root. Container maps contain container-absolute paths, which
// are not necessarily valid in the host mount namespace.
func resolveLibraryPath(p *Process, path string) (string, string) {
	original := strings.TrimSuffix(path, " (deleted)")
	var notes []string
	if original != path {
		notes = append(notes, "map entry marked deleted")
	}
	rel := strings.TrimPrefix(original, "/")
	var candidates []string
	if p != nil {
		if p.PID > 0 {
			candidates = append(candidates, filepath.Join("/proc", strconv.Itoa(p.PID), "root", rel))
		}
		if p.Root != "" && p.Root != "/" {
			candidates = append(candidates, filepath.Join(p.Root, rel))
		}
	}
	candidates = append(candidates, original)
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		info, err := os.Stat(candidate)
		if err != nil || info.IsDir() {
			continue
		}
		real := candidate
		if r, err := filepath.EvalSymlinks(candidate); err == nil {
			real = r
		}
		if _, err := os.Stat(real); err != nil {
			continue
		}
		if candidate != original {
			notes = append(notes, "resolved in process root")
		}
		return real, strings.Join(notes, "; ")
	}
	return original, strings.Join(notes, "; ")
}

func globLibrary(root, pattern string) []string {
	rel := strings.TrimPrefix(pattern, "/")
	if !strings.Contains(rel, "**") {
		matches, _ := filepath.Glob(filepath.Join(root, rel))
		return matches
	}
	idx := strings.Index(rel, "**")
	prefix := strings.TrimSuffix(rel[:idx], "/")
	suffix := strings.TrimPrefix(rel[idx+2:], "/")
	base := filepath.Join(root, prefix)
	var out []string
	_ = filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		relPath, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		relPath = filepath.ToSlash(relPath)
		if suffix == "" || strings.HasSuffix(relPath, filepath.ToSlash(suffix)) {
			out = append(out, path)
		}
		return nil
	})
	return out
}
