package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"cann-ebpf/schema"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"
)

const schemaURL = "https://cann-ebpf.local/schema/config.schema.json"

var (
	schemaOnce sync.Once
	schemaDoc  *jsonschema.Schema
	schemaErr  error
)

func compileSchema() (*jsonschema.Schema, error) {
	schemaOnce.Do(func() {
		var doc any
		if err := json.Unmarshal(schema.Bytes, &doc); err != nil {
			schemaErr = fmt.Errorf("parse embedded schema: %w", err)
			return
		}
		compiler := jsonschema.NewCompiler()
		if err := compiler.AddResource(schemaURL, doc); err != nil {
			schemaErr = fmt.Errorf("add schema resource: %w", err)
			return
		}
		schemaDoc, schemaErr = compiler.Compile(schemaURL)
		if schemaErr != nil {
			schemaErr = fmt.Errorf("compile schema: %w", schemaErr)
		}
	})
	return schemaDoc, schemaErr
}

func validateRaw(doc map[string]any) error {
	sch, err := compileSchema()
	if err != nil {
		return err
	}
	if err := sch.Validate(doc); err != nil {
		return fmt.Errorf("schema validation: %w", err)
	}
	return nil
}

func readYAML(path string) (map[string]any, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var raw map[string]any
	dec := yaml.NewDecoder(bytes.NewReader(b))
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode yaml: %w", err)
	}
	normalized, ok := normalize(raw).(map[string]any)
	if !ok {
		return nil, fmt.Errorf("top-level yaml is not a map")
	}
	return normalized, nil
}

func normalize(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			out[k] = normalize(val)
		}
		return out
	case map[any]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			out[fmt.Sprint(k)] = normalize(val)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = normalize(val)
		}
		return out
	default:
		return v
	}
}

func mergeMaps(base, override map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(override))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range override {
		if bv, ok := out[k].(map[string]any); ok {
			if ov, ok := v.(map[string]any); ok {
				out[k] = mergeMaps(bv, ov)
				continue
			}
		}
		out[k] = v
	}
	return out
}

func getString(m map[string]any, path ...string) string {
	var cur any = m
	for _, p := range path {
		mm, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		cur = mm[p]
	}
	if s, ok := cur.(string); ok {
		return s
	}
	return ""
}

func loadMerged(path string, depth int, stack map[string]bool) (map[string]any, []string, error) {
	if depth > 4 {
		return nil, nil, fmt.Errorf("extends depth exceeds 4 at %s", path)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, nil, err
	}
	if stack[abs] {
		return nil, nil, fmt.Errorf("extends cycle at %s", abs)
	}
	stack[abs] = true
	defer delete(stack, abs)

	raw, err := readYAML(abs)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", abs, err)
	}
	if err := validateRaw(raw); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", abs, err)
	}

	var warnings []string
	if extends := getString(raw, "spec", "extends"); extends != "" {
		basePath := filepath.Join(filepath.Dir(abs), extends)
		base, w, err := loadMerged(basePath, depth+1, stack)
		if err != nil {
			return nil, nil, err
		}
		warnings = append(warnings, w...)
		raw = mergeMaps(base, raw)
	}
	return raw, warnings, nil
}

func decodeStrict(raw map[string]any) (*Document, error) {
	b, err := yaml.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var doc Document
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("strict decode: %w", err)
	}
	return &doc, nil
}

func LoadEffective(path string) (*Document, []string, error) {
	raw, warnings, err := loadMerged(path, 0, map[string]bool{})
	if err != nil {
		return nil, nil, err
	}
	if err := validateRaw(raw); err != nil {
		return nil, warnings, err
	}
	doc, err := decodeStrict(raw)
	if err != nil {
		return nil, warnings, err
	}
	doc.ApplyDefaults()
	return doc, warnings, nil
}

func (d *Document) ApplyDefaults() {
	if d.APIVersion == "" {
		d.APIVersion = "cann-ebpf/v1"
	}
	if d.Spec.Target.Selector == "" {
		d.Spec.Target.Selector = "first_match"
	}
	if d.Spec.Target.MaxProcesses == 0 {
		d.Spec.Target.MaxProcesses = 64
	}
	if d.Spec.Target.ProcRoot == "" {
		d.Spec.Target.ProcRoot = "/proc/{pid}/root"
	}
	if d.Spec.Cann.SymbolPolicy == "" {
		d.Spec.Cann.SymbolPolicy = "fail_soft"
	}
	if d.Spec.Cann.LaunchLayer == "" {
		d.Spec.Cann.LaunchLayer = "auto"
	}
	if d.Spec.Context.Mode == "" {
		d.Spec.Context.Mode = "tid"
	}
	if d.Spec.Context.AttributionLevel == "" {
		d.Spec.Context.AttributionLevel = "auto"
	}
	if d.Spec.Mspti.Mode == "" {
		d.Spec.Mspti.Mode = "off"
	}
	if d.Spec.Output.Dir == "" {
		d.Spec.Output.Dir = "/var/lib/cann-ebpf/runs/{run_id}"
	}
	if d.Spec.Output.JSONL == "" {
		d.Spec.Output.JSONL = "spans.jsonl"
	}
	if d.Spec.Output.Manifest == "" {
		d.Spec.Output.Manifest = "manifest.json"
	}
	if d.Spec.Output.Report == "" {
		d.Spec.Output.Report = "summary.md"
	}
	if d.Spec.Output.RingbufMB == 0 {
		d.Spec.Output.RingbufMB = 8
	}
	if d.Spec.Output.BatchSize == 0 {
		d.Spec.Output.BatchSize = 128
	}
	if d.Spec.Output.RotateMB == 0 {
		d.Spec.Output.RotateMB = 256
	}
	if d.Spec.Output.RetentionDays == 0 {
		d.Spec.Output.RetentionDays = 7
	}
	if d.Spec.Output.MinFreeGB == 0 {
		d.Spec.Output.MinFreeGB = 5
	}
	if d.Spec.Safety.MaxOverheadPct == 0 {
		d.Spec.Safety.MaxOverheadPct = 3
	}
	if d.Spec.Safety.MaxRingbufLossPct == 0 {
		d.Spec.Safety.MaxRingbufLossPct = 0.1
	}
	if d.Spec.Safety.MaxRSSMB == 0 {
		d.Spec.Safety.MaxRSSMB = 512
	}
	if d.Spec.Safety.MaxCPUPct == 0 {
		d.Spec.Safety.MaxCPUPct = 20
	}
	if d.Spec.Safety.MaxDurationS == 0 {
		d.Spec.Safety.MaxDurationS = 300
	}
	if d.Spec.Hardware.IntervalS == 0 {
		d.Spec.Hardware.IntervalS = 10
	}
	if d.Spec.Hardware.Source == "" {
		d.Spec.Hardware.Source = "auto"
	}
}

func SplitSymbolName(name string) (base, version string) {
	if i := strings.IndexByte(name, '@'); i >= 0 {
		return name[:i], name[i+1:]
	}
	return name, ""
}
