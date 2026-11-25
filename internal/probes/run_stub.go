//go:build !linux

package probes

import "fmt"

func Run(opts Options) (*Result, error) {
	if err := opts.normalize(); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("eBPF backend is only available on linux")
}
