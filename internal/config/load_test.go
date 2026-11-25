package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadOrinferProfile(t *testing.T) {
	doc, warnings, err := LoadEffective(filepath.Join("..", "..", "profiles", "orinfer.yaml"))
	if err != nil {
		t.Fatalf("LoadEffective: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	if doc.Metadata.Name != "orinfer" {
		t.Fatalf("name = %q", doc.Metadata.Name)
	}
	if got := doc.Spec.Cann.VersionString(); got != "9.2.0" {
		t.Fatalf("cann version = %q", got)
	}
	if doc.Spec.Context.Mode != "tid" {
		t.Fatalf("context mode = %q", doc.Spec.Context.Mode)
	}
	if doc.Spec.Mspti.Mode != "exclusive" {
		t.Fatalf("mspti mode = %q", doc.Spec.Mspti.Mode)
	}
	if doc.Spec.Probes.Event.Mode != "aggregate" {
		t.Fatalf("event mode = %q", doc.Spec.Probes.Event.Mode)
	}
	if doc.Spec.Output.Dir != "/var/lib/cann-ebpf/runs/{run_id}" {
		t.Fatalf("output dir = %q", doc.Spec.Output.Dir)
	}
	if doc.Spec.Target.Selector != "first_match" {
		t.Fatalf("selector = %q", doc.Spec.Target.Selector)
	}
	if doc.Spec.Hardware.Source != "auto" || doc.Spec.Hardware.IntervalS != 10 {
		t.Fatalf("hardware = %+v", doc.Spec.Hardware)
	}
	if n := len(doc.Spec.Cann.SymbolOverrides.All()); n < 25 {
		t.Fatalf("symbols = %d, want >= 25", n)
	}
}

func TestLoadVLLMProfile(t *testing.T) {
	doc, _, err := LoadEffective(filepath.Join("..", "..", "profiles", "vllm-ascend.yaml"))
	if err != nil {
		t.Fatalf("LoadEffective: %v", err)
	}
	if doc.Metadata.Name != "vllm-ascend" {
		t.Fatalf("name = %q", doc.Metadata.Name)
	}
	if doc.Spec.Context.Mode != "plugin" {
		t.Fatalf("context mode = %q", doc.Spec.Context.Mode)
	}
	if doc.Spec.Target.Selector != "all_matching" {
		t.Fatalf("selector = %q", doc.Spec.Target.Selector)
	}
	if doc.Spec.Mspti.Mode != "shared" {
		t.Fatalf("mspti mode = %q", doc.Spec.Mspti.Mode)
	}
	var hasLaunch bool
	for _, name := range doc.Spec.Cann.SymbolOverrides.All() {
		if name == "aclrtLaunchKernel" {
			hasLaunch = true
		}
	}
	if !hasLaunch {
		t.Fatal("aclrtLaunchKernel missing from vllm profile")
	}
}

func TestUnknownFieldRejected(t *testing.T) {
	path := writeTemp(t, `
apiVersion: cann-ebpf/v1
kind: Config
metadata:
  name: bad
spec:
  foo: bar
`)
	if _, _, err := LoadEffective(path); err == nil {
		t.Fatal("expected unknown field error")
	}
}

func TestUnquotedOffRemainsString(t *testing.T) {
	path := writeTemp(t, `
apiVersion: cann-ebpf/v1
kind: Config
metadata:
  name: bad-off
spec:
  mspti:
    mode: off
`)
	doc, _, err := LoadEffective(path)
	if err != nil {
		t.Fatalf("LoadEffective: %v", err)
	}
	if doc.Spec.Mspti.Mode != "off" {
		t.Fatalf("mspti mode = %q, want off", doc.Spec.Mspti.Mode)
	}
}

func TestMergeMapsReplacesLists(t *testing.T) {
	base := map[string]any{
		"a": map[string]any{"x": 1, "list": []any{"a"}},
		"b": "base",
	}
	override := map[string]any{
		"a": map[string]any{"y": 2, "list": []any{"b", "c"}},
		"c": "override",
	}
	got := mergeMaps(base, override)
	a := got["a"].(map[string]any)
	if a["x"] != 1 || a["y"] != 2 {
		t.Fatalf("map merge failed: %#v", a)
	}
	if list := a["list"].([]any); len(list) != 2 || list[0] != "b" {
		t.Fatalf("list should be replaced, got %#v", list)
	}
	if got["b"] != "base" || got["c"] != "override" {
		t.Fatalf("scalar merge failed: %#v", got)
	}
}

func TestExtendsCycleRejected(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.yaml")
	b := filepath.Join(dir, "b.yaml")
	writeFile(t, a, `
apiVersion: cann-ebpf/v1
kind: Profile
metadata:
  name: a
spec:
  extends: b.yaml
`)
	writeFile(t, b, `
apiVersion: cann-ebpf/v1
kind: Profile
metadata:
  name: b
spec:
  extends: a.yaml
`)
	if _, _, err := LoadEffective(a); err == nil {
		t.Fatal("expected cycle error")
	}
}

func writeTemp(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	writeFile(t, path, body)
	return path
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(strings.TrimSpace(body)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}
