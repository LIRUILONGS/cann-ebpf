package stacks

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// DefaultSymbolRules returns the built-in kernel symbol categories. Rules are
// ordered: the first matching prefix wins.
func DefaultSymbolRules() *SymbolRules {
	return &SymbolRules{Categories: []CategoryRule{
		{
			Name: "npu",
			Prefixes: []string{
				"devdrv_", "drv_", "davinci", "hisi_", "svm_", "ascend",
				"aicore", "aicpu", "hdc_", "hdcdrv_", "hdccom_", "devmm_",
				"trs_", "pcivnic_", "dms_", "tsagent_", "vdavinci", "dbl_", "ka_",
			},
		},
		{
			Name: "dma",
			Prefixes: []string{
				"dma_", "smmu_", "iommu_", "iomap_", "arm_smmu",
			},
		},
		{
			Name: "net",
			Prefixes: []string{
				"tcp_", "udp_", "unix_", "hccn_", "dev_queue_xmit",
				"napi_", "sk_", "sock_", "net_",
			},
		},
		{
			Name: "sync",
			Prefixes: []string{
				"futex_", "epoll_", "mutex", "rwsem", "schedule",
				"__schedule", "wait_", "complete", "wake_up", "osq_",
			},
		},
		{
			Name: "vfs",
			Prefixes: []string{
				"vfs_", "do_sys_open", "filp_", "path_", "ext4_", "xfs_",
				"overlayfs_", "fuse_",
			},
		},
		{
			Name: "block",
			Prefixes: []string{
				"blk_", "bio_", "submit_bio", "io_schedule", "nvme_",
			},
		},
		{
			Name: "sched",
			Prefixes: []string{
				"sched_", "cpu_", "idle_", "irq_", "softirq_", "timer_",
			},
		},
		{Name: "sample"},
		{Name: "other"},
	}}
}

// LoadSymbolRules loads categories from path. A missing file leaves the
// built-in rules in effect. Invalid YAML or an empty category list is an error;
// silently falling back would make report classifications unauditable.
func LoadSymbolRules(path string) (*SymbolRules, error) {
	if strings.TrimSpace(path) == "" {
		return DefaultSymbolRules(), nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return DefaultSymbolRules(), nil
		}
		return nil, fmt.Errorf("read symbol rules %s: %w", path, err)
	}

	var rules SymbolRules
	if err := yaml.Unmarshal(data, &rules); err != nil {
		return nil, fmt.Errorf("parse symbol rules %s: %w", path, err)
	}
	if len(rules.Categories) == 0 {
		return nil, fmt.Errorf("parse symbol rules %s: categories is empty", path)
	}
	for i := range rules.Categories {
		rules.Categories[i].Name = strings.TrimSpace(rules.Categories[i].Name)
		if rules.Categories[i].Name == "" {
			return nil, fmt.Errorf("parse symbol rules %s: category %d has no name", path, i)
		}
	}
	return &rules, nil
}

// ClassifyName maps a symbol and its module to a report category. Module names
// from the Ascend stack always identify NPU work, even when the symbol itself
// looks generic.
func ClassifyName(name, module string, rules *SymbolRules) string {
	lowerModule := strings.ToLower(module)
	for _, marker := range []string{"devdrv", "davinci", "hisi"} {
		if strings.Contains(lowerModule, marker) {
			return "npu"
		}
	}

	if rules == nil || len(rules.Categories) == 0 {
		rules = DefaultSymbolRules()
	}
	for _, category := range rules.Categories {
		for _, prefix := range category.Prefixes {
			if prefix != "" && strings.HasPrefix(name, prefix) {
				return category.Name
			}
		}
	}
	return "other"
}
