package cpython

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
)

//go:embed offsets/cpython-3.12.13-aarch64.json
var embeddedCPythonOffsets []byte

//go:embed offsets/vllm-0.23.0rc1-aarch64.json
var embeddedVLLMOffsets []byte

// CPythonOffsets is the pinned CPython object layout used by batch_probe.bpf.c.
// The BPF object is compiled with the same values. Changing any value requires
// a new BPF object and a new POC run.
type CPythonOffsets struct {
	Schema                    string   `json:"schema"`
	Python                    string   `json:"python"`
	Arch                      string   `json:"arch"`
	Source                    string   `json:"source"`
	PyASCIIObjectSize         int      `json:"py_ascii_object_size"`
	PyDictMaKeysOffset        int      `json:"py_dict_ma_keys_offset"`
	PyDictMaValuesOffset      int      `json:"py_dict_ma_values_offset"`
	PyDictKeysDKKindOffset    int      `json:"py_dict_keys_dk_kind_offset"`
	PyDictKeysDKNEntries      int      `json:"py_dict_keys_dk_nentries_offset"`
	PyDictKeysDKIndicesOffset int      `json:"py_dict_keys_dk_indices_offset"`
	PyDictUnicodeEntrySize    int      `json:"py_dict_unicode_entry_size"`
	PyTypeTPNameOffset        int      `json:"py_type_tp_name_offset"`
	TargetAttr                string   `json:"target_attr"`
	TargetAttrLen             int      `json:"target_attr_len"`
	Notes                     []string `json:"notes"`
}

// VLLMOffsets is a Python-level profile, not a C struct offset table.
// SchedulerOutput is a dataclass, so the POC identifies its batch dict by
// pointer identity and confirms the owner object by type name.
type VLLMOffsets struct {
	Schema                string   `json:"schema"`
	VLLM                  string   `json:"vllm"`
	VLLMAscend            string   `json:"vllm_ascend"`
	Arch                  string   `json:"arch"`
	SchedulerOutputClass  string   `json:"scheduler_output_class"`
	SchedulerOutputModule string   `json:"scheduler_output_module"`
	TargetAttr            string   `json:"target_attr"`
	TargetAttrLen         int      `json:"target_attr_len"`
	Boundary              string   `json:"boundary"`
	WorkerEntry           string   `json:"worker_entry"`
	Source                string   `json:"source"`
	Notes                 []string `json:"notes"`
}

type OffsetBundle struct {
	CPython CPythonOffsets `json:"cpython"`
	VLLM    VLLMOffsets    `json:"vllm"`
}

func LoadEmbeddedOffsets() (OffsetBundle, error) {
	var cpython CPythonOffsets
	if err := json.Unmarshal(embeddedCPythonOffsets, &cpython); err != nil {
		return OffsetBundle{}, fmt.Errorf("parse embedded CPython offsets: %w", err)
	}
	var vllm VLLMOffsets
	if err := json.Unmarshal(embeddedVLLMOffsets, &vllm); err != nil {
		return OffsetBundle{}, fmt.Errorf("parse embedded vLLM offsets: %w", err)
	}
	bundle := OffsetBundle{CPython: cpython, VLLM: vllm}
	if err := bundle.Validate(); err != nil {
		return OffsetBundle{}, err
	}
	return bundle, nil
}

func LoadOffsetsFile(path string) (OffsetBundle, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return OffsetBundle{}, fmt.Errorf("read offsets file: %w", err)
	}
	var bundle OffsetBundle
	if err := json.Unmarshal(data, &bundle); err != nil {
		return OffsetBundle{}, fmt.Errorf("parse offsets file: %w", err)
	}
	if err := bundle.Validate(); err != nil {
		return OffsetBundle{}, err
	}
	return bundle, nil
}

func (b OffsetBundle) Validate() error {
	if b.CPython.Python != "3.12.13" {
		return fmt.Errorf("CPython offset profile is for %q, want 3.12.13", b.CPython.Python)
	}
	if b.CPython.Arch != "aarch64" {
		return fmt.Errorf("CPython offset profile arch is %q, want aarch64", b.CPython.Arch)
	}
	if b.CPython.PyASCIIObjectSize != 40 {
		return fmt.Errorf("unexpected PyASCIIObject size %d", b.CPython.PyASCIIObjectSize)
	}
	if b.CPython.PyDictMaKeysOffset != 32 || b.CPython.PyDictMaValuesOffset != 40 {
		return fmt.Errorf("unexpected PyDictObject offsets")
	}
	if b.CPython.PyDictKeysDKKindOffset != 10 ||
		b.CPython.PyDictKeysDKNEntries != 24 ||
		b.CPython.PyDictKeysDKIndicesOffset != 32 {
		return fmt.Errorf("unexpected PyDictKeysObject offsets")
	}
	if b.CPython.PyDictUnicodeEntrySize != 16 {
		return fmt.Errorf("unexpected PyDictUnicodeEntry size %d", b.CPython.PyDictUnicodeEntrySize)
	}
	if b.CPython.PyTypeTPNameOffset != 24 {
		return fmt.Errorf("unexpected PyTypeObject.tp_name offset %d", b.CPython.PyTypeTPNameOffset)
	}
	if b.CPython.TargetAttr != "num_scheduled_tokens" || b.CPython.TargetAttrLen != 20 {
		return fmt.Errorf("unexpected CPython target attribute profile")
	}
	if b.VLLM.TargetAttr != "num_scheduled_tokens" || b.VLLM.TargetAttrLen != 20 {
		return fmt.Errorf("unexpected vLLM target attribute profile")
	}
	if b.VLLM.Boundary != "scheduler_output_dict_identity" {
		return fmt.Errorf("unexpected vLLM boundary %q", b.VLLM.Boundary)
	}
	return nil
}

func (b OffsetBundle) Versions() Versions {
	return Versions{
		Python:     b.CPython.Python,
		VLLM:       b.VLLM.VLLM,
		VLLMAscend: b.VLLM.VLLMAscend,
	}
}
