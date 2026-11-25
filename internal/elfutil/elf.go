package elfutil

import (
	"debug/elf"
	"fmt"
	"sort"
	"strings"
)

type Symbol struct {
	Wanted  string `json:"wanted"`
	Name    string `json:"name"`
	Found   bool   `json:"found"`
	Value   uint64 `json:"value"`
	Offset  uint64 `json:"offset"`
	Size    uint64 `json:"size"`
	Bind    string `json:"bind"`
	Type    string `json:"type"`
	Section string `json:"section"`
	Error   string `json:"error,omitempty"`
}

func Resolve(path string, wanted []string) ([]Symbol, []string, error) {
	f, err := elf.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()

	syms, err := f.DynamicSymbols()
	if err != nil || len(syms) == 0 {
		syms, err = f.Symbols()
	}
	if err != nil {
		return nil, nil, fmt.Errorf("read symbols: %w", err)
	}

	byName := map[string][]elf.Symbol{}
	for _, s := range syms {
		// Dynamic symbol tables also contain imports. An undefined symbol is
		// not an exported target we can uprobe in this library.
		if s.Section == elf.SHN_UNDEF {
			continue
		}
		base, _ := splitVersion(s.Name)
		byName[base] = append(byName[base], s)
	}

	var out []Symbol
	var warnings []string
	for _, want := range wanted {
		base, version := splitVersion(want)
		cands := byName[base]
		if len(cands) == 0 {
			out = append(out, Symbol{Wanted: want, Name: base, Error: "symbol not found"})
			warnings = append(warnings, fmt.Sprintf("%s: %s not found", path, want))
			continue
		}
		sort.SliceStable(cands, func(i, j int) bool {
			return symbolRank(cands[i]) > symbolRank(cands[j])
		})
		chosen := cands[0]
		offset, offsetErr := vaddrToOffset(f, chosen.Value)
		secName := ""
		if int(chosen.Section) < len(f.Sections) {
			secName = f.Sections[chosen.Section].Name
		}
		sym := Symbol{
			Wanted:  want,
			Name:    chosen.Name,
			Found:   true,
			Value:   chosen.Value,
			Offset:  offset,
			Size:    chosen.Size,
			Bind:    fmt.Sprint(elf.ST_BIND(chosen.Info)),
			Type:    fmt.Sprint(elf.ST_TYPE(chosen.Info)),
			Section: secName,
		}
		if offsetErr != nil {
			sym.Error = offsetErr.Error()
			warnings = append(warnings, fmt.Sprintf("%s: %s: %v", path, want, offsetErr))
		}
		if version != "" && !strings.Contains(chosen.Name, "@") {
			warnings = append(warnings, fmt.Sprintf("%s: %s requested version %s, but debug/elf did not expose versioned name", path, want, version))
		}
		out = append(out, sym)
	}
	return out, warnings, nil
}

func splitVersion(name string) (base, version string) {
	if i := strings.IndexByte(name, '@'); i >= 0 {
		return name[:i], name[i+1:]
	}
	return name, ""
}

func symbolRank(s elf.Symbol) int {
	rank := 0
	if s.Section != elf.SHN_UNDEF {
		rank += 10
	}
	if s.Value != 0 {
		rank += 5
	}
	switch elf.ST_BIND(s.Info) {
	case elf.STB_GLOBAL:
		rank += 3
	case elf.STB_WEAK:
		rank++
	}
	switch elf.ST_TYPE(s.Info) {
	case elf.STT_FUNC:
		rank += 2
	case elf.STT_OBJECT:
		rank++
	}
	return rank
}

func vaddrToOffset(f *elf.File, vaddr uint64) (uint64, error) {
	for _, p := range f.Progs {
		if p.Type != elf.PT_LOAD {
			continue
		}
		if vaddr >= p.Vaddr && vaddr < p.Vaddr+p.Filesz {
			return p.Off + (vaddr - p.Vaddr), nil
		}
	}
	for _, s := range f.Sections {
		if s.Type == elf.SHT_NOBITS || s.Size == 0 {
			continue
		}
		if vaddr >= s.Addr && vaddr < s.Addr+s.Size {
			return s.Offset + (vaddr - s.Addr), nil
		}
	}
	return 0, fmt.Errorf("vaddr 0x%x not covered by PT_LOAD or section", vaddr)
}
