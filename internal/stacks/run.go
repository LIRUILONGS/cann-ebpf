package stacks

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

type stagedRunFile struct {
	tempPath  string
	finalPath string
}

// WriteRun writes one immutable evidence bundle. Existing folded, meta or raw
// files are never replaced: a second write is refused so an investigation
// cannot silently lose its original symbols or addresses.
func WriteRun(dir string, meta Meta, samples []Sample, profile *Profile, raw bool) error {
	if profile == nil {
		return fmt.Errorf("write run: nil profile")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create run directory %s: %w", dir, err)
	}

	targets := []string{
		filepath.Join(dir, FoldedFileName),
		filepath.Join(dir, MetaFileName),
		filepath.Join(dir, RawFileName),
	}
	for _, target := range targets {
		if _, err := os.Lstat(target); err == nil {
			return fmt.Errorf("refusing to overwrite existing evidence %s", target)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("inspect run target %s: %w", target, err)
		}
	}

	metaData, err := marshalRunMeta(meta)
	if err != nil {
		return err
	}

	staged := make([]stagedRunFile, 0, 3)
	cleanup := func() {
		for _, file := range staged {
			_ = os.Remove(file.tempPath)
		}
	}

	metaFile, err := stageRunFile(dir, targets[1], func(writer io.Writer) error {
		_, err := writer.Write(metaData)
		return err
	})
	if err != nil {
		cleanup()
		return err
	}
	staged = append(staged, metaFile)

	foldedFile, err := stageRunFile(dir, targets[0], func(writer io.Writer) error {
		return WriteFolded(writer, profile)
	})
	if err != nil {
		cleanup()
		return err
	}
	staged = append(staged, foldedFile)

	if raw {
		rawFile, err := stageRunFile(dir, targets[2], func(writer io.Writer) error {
			buffered := bufio.NewWriter(writer)
			encoder := json.NewEncoder(buffered)
			for _, sample := range samples {
				if err := encoder.Encode(sample); err != nil {
					return err
				}
			}
			return buffered.Flush()
		})
		if err != nil {
			cleanup()
			return err
		}
		staged = append(staged, rawFile)
	}

	for index, file := range staged {
		if err := os.Rename(file.tempPath, file.finalPath); err != nil {
			for _, remaining := range staged[index:] {
				_ = os.Remove(remaining.tempPath)
			}
			return fmt.Errorf("commit run file %s: %w", file.finalPath, err)
		}
	}
	return nil
}

func marshalRunMeta(meta Meta) ([]byte, error) {
	if meta.Schema == "" {
		meta.Schema = SchemaV1
	}
	if meta.Schema != SchemaV1 {
		return nil, fmt.Errorf("write run: unsupported meta schema %q", meta.Schema)
	}
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal run meta: %w", err)
	}
	data = append(data, '\n')
	return data, nil
}

func stageRunFile(dir, finalPath string, write func(io.Writer) error) (stagedRunFile, error) {
	file, err := os.CreateTemp(dir, "."+filepath.Base(finalPath)+".tmp-*")
	if err != nil {
		return stagedRunFile{}, fmt.Errorf("create temporary run file for %s: %w", finalPath, err)
	}
	tempPath := file.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tempPath)
		}
	}()

	if err := file.Chmod(0o644); err != nil {
		_ = file.Close()
		return stagedRunFile{}, fmt.Errorf("chmod temporary run file %s: %w", tempPath, err)
	}
	buffered := bufio.NewWriter(file)
	if err := write(buffered); err != nil {
		_ = file.Close()
		return stagedRunFile{}, fmt.Errorf("write temporary run file %s: %w", tempPath, err)
	}
	if err := buffered.Flush(); err != nil {
		_ = file.Close()
		return stagedRunFile{}, fmt.Errorf("flush temporary run file %s: %w", tempPath, err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return stagedRunFile{}, fmt.Errorf("sync temporary run file %s: %w", tempPath, err)
	}
	if err := file.Close(); err != nil {
		return stagedRunFile{}, fmt.Errorf("close temporary run file %s: %w", tempPath, err)
	}
	committed = true
	return stagedRunFile{tempPath: tempPath, finalPath: finalPath}, nil
}

// LoadRun reads the immutable metadata and folded profile from a run directory
// or directly from its stacks.folded path. A missing folded file yields a Run
// with a nil Profile, while missing or malformed metadata is always an error.
func LoadRun(path string) (*Run, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("load run: empty path")
	}

	cleaned := filepath.Clean(path)
	dir := cleaned
	foldedPath := filepath.Join(cleaned, FoldedFileName)
	if filepath.Base(cleaned) == FoldedFileName {
		dir = filepath.Dir(cleaned)
		foldedPath = cleaned
	}

	metaPath := filepath.Join(dir, MetaFileName)
	metaData, err := os.ReadFile(metaPath)
	if err != nil {
		return nil, fmt.Errorf("read run meta %s: %w", metaPath, err)
	}
	var meta Meta
	if err := json.Unmarshal(metaData, &meta); err != nil {
		return nil, fmt.Errorf("parse run meta %s: %w", metaPath, err)
	}
	if meta.Schema != SchemaV1 {
		return nil, fmt.Errorf("parse run meta %s: unsupported schema %q", metaPath, meta.Schema)
	}

	profile, err := ReadFolded(foldedPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return &Run{Dir: dir, Meta: meta, Profile: nil}, nil
		}
		return nil, err
	}
	return &Run{Dir: dir, Meta: meta, Profile: profile}, nil
}
