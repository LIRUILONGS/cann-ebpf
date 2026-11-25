package discovery

import (
	"fmt"
	"strings"

	"cann-ebpf/internal/config"
)

type MatchResult struct {
	Matched      bool     `json:"matched"`
	Excluded     bool     `json:"excluded"`
	MatchedRules []string `json:"matched_rules"`
	Confidence   float64  `json:"confidence"`
}

func EvaluateMatch(m *config.Match, p *Process) MatchResult {
	if p == nil {
		return MatchResult{}
	}
	if m == nil {
		return MatchResult{Matched: true, Confidence: 0.5, MatchedRules: []string{"no-match-rule"}}
	}

	for _, expr := range m.Exclude {
		if matchExpr(expr, p) {
			return MatchResult{Excluded: true, MatchedRules: []string{"exclude: " + expr.String()}}
		}
	}

	var rules []string
	anyOK := len(m.Any) == 0
	for _, expr := range m.Any {
		if matchExpr(expr, p) {
			anyOK = true
			rules = append(rules, "any: "+expr.String())
		}
	}
	allOK := true
	for _, expr := range m.All {
		if !matchExpr(expr, p) {
			allOK = false
			break
		}
		rules = append(rules, "all: "+expr.String())
	}
	if len(m.Any) == 0 && len(m.All) == 0 {
		return MatchResult{}
	}
	matched := anyOK && allOK
	confidence := 0.0
	if matched {
		confidence = 1.0
		if m.Priority == 0 {
			confidence = 0.5
		}
	}
	return MatchResult{Matched: matched, MatchedRules: rules, Confidence: confidence}
}

func matchExpr(expr config.MatchExpr, p *Process) bool {
	if expr.Empty() {
		return false
	}
	if expr.Comm != "" && p.Comm != expr.Comm {
		return false
	}
	if expr.CmdlineContains != "" && !strings.Contains(p.Cmdline, expr.CmdlineContains) {
		return false
	}
	if expr.MapsContains != "" && !p.HasMap(expr.MapsContains) {
		return false
	}
	if expr.ContainerName != "" && strings.TrimPrefix(p.ContainerName, "/") != strings.TrimPrefix(expr.ContainerName, "/") {
		return false
	}
	if expr.ContainerImageContains != "" && !strings.Contains(p.ContainerImage, expr.ContainerImageContains) {
		return false
	}
	if expr.ListenPort != 0 && !p.ListensOn(expr.ListenPort) {
		return false
	}
	return true
}

type Candidate struct {
	Profile *config.Document `json:"profile"`
	Path    string           `json:"path"`
	Match   MatchResult      `json:"match"`
}

func SelectBest(cands []Candidate) (*Candidate, error) {
	var best *Candidate
	for i := range cands {
		c := &cands[i]
		if !c.Match.Matched || c.Match.Excluded {
			continue
		}
		if best == nil {
			best = c
			continue
		}
		bp := best.Profile.Spec.Match
		cp := c.Profile.Spec.Match
		bpri, cpri := 0, 0
		if bp != nil {
			bpri = bp.Priority
		}
		if cp != nil {
			cpri = cp.Priority
		}
		switch {
		case cpri > bpri:
			best = c
		case cpri == bpri:
			return nil, fmt.Errorf("profile match tie: %s and %s both priority %d", best.Profile.Metadata.Name, c.Profile.Metadata.Name, cpri)
		}
	}
	if best == nil {
		return nil, fmt.Errorf("no matching profile")
	}
	return best, nil
}
