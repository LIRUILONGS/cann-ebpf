package stacks

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestDefaultSymbolRulesOrder(t *testing.T) {
	rules := DefaultSymbolRules()
	names := make([]string, 0, len(rules.Categories))
	for _, category := range rules.Categories {
		names = append(names, category.Name)
	}
	want := []string{"npu", "dma", "net", "sync", "vfs", "block", "sched", "sample", "other"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("category order = %v, want %v", names, want)
	}
}

func TestClassifyNamePrefixesAndModuleOverride(t *testing.T) {
	rules := DefaultSymbolRules()
	tests := []struct {
		name   string
		want   string
		module string
	}{
		{name: "devdrv_mm", want: "npu"},
		{name: "davinci_kernel", want: "npu"},
		{name: "ascend_irq", want: "npu"},
		{name: "dma_map_page", want: "dma"},
		{name: "arm_smmu_map", want: "dma"},
		{name: "tcp_sendmsg", want: "net"},
		{name: "udp_recvmsg", want: "net"},
		{name: "futex_wait", want: "sync"},
		{name: "epoll_wait", want: "sync"},
		{name: "vfs_read", want: "vfs"},
		{name: "ext4_file_write", want: "vfs"},
		{name: "blk_mq_dispatch", want: "block"},
		{name: "nvme_queue_rq", want: "block"},
		{name: "sched_switch", want: "sched"},
		{name: "timer_tick", want: "sched"},
		{name: "mystery_frame", want: "other"},
		{name: "unknown_kernel_frame", want: "other"},
		{name: "tcp_sendmsg", module: "davinci_driver", want: "npu"},
		{name: "mystery_frame", module: "hisi_npu", want: "npu"},
	}
	for _, test := range tests {
		t.Run(test.name+"/"+test.module, func(t *testing.T) {
			if got := ClassifyName(test.name, test.module, rules); got != test.want {
				t.Fatalf("ClassifyName(%q, %q) = %q, want %q", test.name, test.module, got, test.want)
			}
		})
	}

	if got := ClassifyName("devdrv_mm", "", nil); got != "npu" {
		t.Fatalf("nil rules fallback = %q, want npu", got)
	}
}

func TestClassifyNameFirstMatchingRuleWins(t *testing.T) {
	rules := &SymbolRules{Categories: []CategoryRule{
		{Name: "first", Prefixes: []string{"devdrv_"}},
		{Name: "second", Prefixes: []string{"devdrv_mm"}},
	}}
	if got := ClassifyName("devdrv_mm_alloc", "", rules); got != "first" {
		t.Fatalf("first matching category = %q, want first", got)
	}
}

func TestLoadSymbolRules(t *testing.T) {
	dir := t.TempDir()
	path := mustWriteTestFile(t, dir, "rules.yaml", `categories:
  - name: dma_first
    prefixes:
      - devdrv_
  - name: npu_later
    prefixes:
      - devdrv_x
  - name: other
    prefixes: []
`)
	rules, err := LoadSymbolRules(path)
	if err != nil {
		t.Fatalf("LoadSymbolRules: %v", err)
	}
	if got := ClassifyName("devdrv_x_alloc", "", rules); got != "dma_first" {
		t.Fatalf("loaded rule order = %q, want dma_first", got)
	}

	badPath := mustWriteTestFile(t, dir, "bad.yaml", "categories:\n  - name: npu\n    prefixes: [oops\n")
	if _, err := LoadSymbolRules(badPath); err == nil {
		t.Fatalf("expected invalid YAML error")
	}

	emptyPath := mustWriteTestFile(t, dir, "empty.yaml", "")
	if _, err := LoadSymbolRules(emptyPath); err == nil {
		t.Fatalf("expected empty categories error")
	}

	missingRules, err := LoadSymbolRules(filepath.Join(dir, "missing.yaml"))
	if err != nil {
		t.Fatalf("missing file should fall back to defaults: %v", err)
	}
	if got := ClassifyName("devdrv_alloc", "", missingRules); got != "npu" {
		t.Fatalf("missing-file fallback = %q, want npu", got)
	}
}

func TestKernelSymbolsProfileMatchesDefaultRules(t *testing.T) {
	path := filepath.Join("..", "..", "profiles", "kernel-symbols.yaml")
	rules, err := LoadSymbolRules(path)
	if err != nil {
		t.Fatalf("load profiles/kernel-symbols.yaml: %v", err)
	}
	defaults := DefaultSymbolRules()
	if len(rules.Categories) != len(defaults.Categories) {
		t.Fatalf("profile categories = %d, want %d", len(rules.Categories), len(defaults.Categories))
	}
	for index := range defaults.Categories {
		if rules.Categories[index].Name != defaults.Categories[index].Name {
			t.Fatalf("category %d = %q, want %q", index, rules.Categories[index].Name, defaults.Categories[index].Name)
		}
		if !equalStringSlices(rules.Categories[index].Prefixes, defaults.Categories[index].Prefixes) {
			t.Fatalf("profile prefixes for %s differ from defaults: %v vs %v",
				defaults.Categories[index].Name, rules.Categories[index].Prefixes, defaults.Categories[index].Prefixes)
		}
	}

	npu := rules.Categories[0]
	required := []string{
		"hdcdrv_", "hdccom_", "devmm_", "trs_", "pcivnic_",
		"dms_", "tsagent_", "vdavinci", "dbl_", "ka_",
	}
	for _, prefix := range required {
		if !containsString(npu.Prefixes, prefix) {
			t.Fatalf("npu prefixes missing kallsyms-evidenced %q: %v", prefix, npu.Prefixes)
		}
	}
}

func TestClassifyNameRealEvidenceSymbols(t *testing.T) {
	path := filepath.Join("..", "..", "profiles", "kernel-symbols.yaml")
	rules, err := LoadSymbolRules(path)
	if err != nil {
		t.Fatalf("load profiles/kernel-symbols.yaml: %v", err)
	}
	tests := []struct {
		name   string
		module string
		want   string
	}{
		{name: "drv_ascend_intf_ioctl", module: "drv_davinci_intf_host", want: "npu"},
		{name: "devdrv_dma_map_page", module: "drv_pcie_host", want: "npu"},
		{name: "devmm_svm_ioctl", module: "drv_devmm_host", want: "npu"},
		{name: "trs_ioctl", module: "ascend_trs_core", want: "npu"},
		{name: "hdcdrv_ioctl", module: "drv_pcie_hdc_host", want: "npu"},
		{name: "hdccom_alloc_mem", module: "drv_pcie_hdc_host", want: "npu"},
		{name: "pcivnic_dma_map_single", module: "drv_pcie_vnic_host", want: "npu"},
		{name: "dms_ioctl", module: "ascend_urd", want: "npu"},
		{name: "tsagent_cqe_update", module: "ts_agent", want: "npu"},
		{name: "vdavinci_do_map", module: "drv_vascend", want: "npu"},
		{name: "dbl_get_run_env", module: "dbl_runenv_config", want: "npu"},
		{name: "ka_kfree", module: "ascend_kernel_adapt", want: "npu"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ClassifyName(test.name, test.module, rules); got != test.want {
				t.Fatalf("ClassifyName(%q, %q) = %q, want %q", test.name, test.module, got, test.want)
			}
		})
	}
}

func TestClassifyNameGenericDMAStaysDMA(t *testing.T) {
	path := filepath.Join("..", "..", "profiles", "kernel-symbols.yaml")
	rules, err := LoadSymbolRules(path)
	if err != nil {
		t.Fatalf("load profiles/kernel-symbols.yaml: %v", err)
	}
	for _, name := range []string{
		"dma_map_page", "dma_map_page_attrs", "dma_unmap_page_attrs",
		"iommu_dma_map_page", "iommu_dma_unmap_page", "iommu_dma_alloc_iova",
		"arm_smmu_map", "arm_smmu_unmap",
	} {
		if got := ClassifyName(name, "", rules); got != "dma" {
			t.Fatalf("ClassifyName(%q) = %q, want dma (generic DMA/IOMMU must not be npu)", name, got)
		}
	}
}

func TestClassifyNameCaseAndModuleMatching(t *testing.T) {
	path := filepath.Join("..", "..", "profiles", "kernel-symbols.yaml")
	rules, err := LoadSymbolRules(path)
	if err != nil {
		t.Fatalf("load profiles/kernel-symbols.yaml: %v", err)
	}
	if got := ClassifyName("DEVDRV_DMA_MAP", "", rules); got != "other" {
		t.Fatalf("uppercase symbol = %q, want other (symbol prefixes are case-sensitive)", got)
	}
	for _, module := range []string{"devdrv_custom", "DAVINCI_DRIVER", "HISI_NPU"} {
		if got := ClassifyName("custom_symbol", module, rules); got != "npu" {
			t.Fatalf("ClassifyName(custom_symbol, %q) = %q, want npu (module matching is case-insensitive)", module, got)
		}
	}
	if got := ClassifyName("custom_symbol", "drv_pcie_host", rules); got != "other" {
		t.Fatalf("module without npu marker = %q, want other", got)
	}
}

func TestClassifyNameUnknownAndEmpty(t *testing.T) {
	rules := DefaultSymbolRules()
	for _, name := range []string{"", "[unknown]", "unknown", "totally_unknown_symbol"} {
		if got := ClassifyName(name, "", rules); got != "other" {
			t.Fatalf("ClassifyName(%q) = %q, want other", name, got)
		}
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// equalStringSlices compares order exactly while treating a nil slice and an
// empty YAML sequence as equivalent.
func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for index := range a {
		if a[index] != b[index] {
			return false
		}
	}
	return true
}
