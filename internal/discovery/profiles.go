package discovery

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"cann-ebpf/internal/config"
)

type LoadedProfile struct {
	Name string
	Path string
	Doc  *config.Document
}

func LoadProfile(path string) (*LoadedProfile, []string, error) {
	doc, warnings, err := config.LoadEffective(path)
	if err != nil {
		return nil, warnings, err
	}
	return &LoadedProfile{Name: doc.Metadata.Name, Path: path, Doc: doc}, warnings, nil
}

func LoadProfileByName(dir, name string) (*LoadedProfile, []string, error) {
	if strings.ContainsAny(name, `/\\`) || strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".yml") {
		return LoadProfile(name)
	}
	return LoadProfile(filepath.Join(dir, name+".yaml"))
}

func LoadAllProfiles(dir string) ([]*LoadedProfile, []string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, err
	}
	var paths []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".yml") {
			paths = append(paths, filepath.Join(dir, name))
		}
	}
	sort.Strings(paths)
	var out []*LoadedProfile
	var warnings []string
	for _, p := range paths {
		prof, w, err := LoadProfile(p)
		warnings = append(warnings, w...)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %v", p, err))
			continue
		}
		out = append(out, prof)
	}
	if len(out) == 0 {
		return nil, warnings, fmt.Errorf("no profiles loaded from %s", dir)
	}
	return out, warnings, nil
}

func CandidatesFor(profiles []*LoadedProfile, p *Process) []Candidate {
	out := make([]Candidate, 0, len(profiles))
	for _, prof := range profiles {
		out = append(out, Candidate{
			Profile: prof.Doc,
			Path:    prof.Path,
			Match:   EvaluateMatch(prof.Doc.Spec.Match, p),
		})
	}
	return out
}
