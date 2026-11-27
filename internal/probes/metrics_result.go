package probes

import (
	"strings"

	"cann-ebpf/internal/metrics"
)

func compactMetricsResult(run metrics.Result, runErr error, sources []metrics.Source) MetricsResult {
	result := MetricsResult{
		Enabled: true, Status: run.Status, Points: run.Points,
		MetricsPath: run.MetricsPath, MetaPath: run.MetaPath,
		Bytes: run.Bytes, SHA256: run.SHA256,
		Warnings: append([]string(nil), run.Warnings...),
	}
	if run.Meta.Schema != "" || len(run.Meta.Sources) > 0 || len(run.Meta.Series) > 0 {
		result.Sources = append([]string(nil), run.Meta.Sources...)
		result.Series = len(run.Meta.Series)
	}
	if len(result.Sources) == 0 {
		seen := map[string]struct{}{}
		for _, source := range sources {
			if source == nil {
				continue
			}
			name := strings.TrimSpace(source.Name())
			if name == "" {
				continue
			}
			if _, ok := seen[name]; ok {
				continue
			}
			seen[name] = struct{}{}
			result.Sources = append(result.Sources, name)
		}
	}
	if runErr != nil {
		result.Error = runErr.Error()
	}
	return result
}
