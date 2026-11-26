//go:build !linux || !arm64

package probes

import (
	"context"
	"time"
)

type pythonBatchLayer struct{}

func startPythonBatchLayer(context.Context, Options) (*pythonBatchLayer, []string) {
	return nil, []string{"python batch probe requires linux/arm64"}
}

func (layer *pythonBatchLayer) Collect(time.Duration) ([]PythonBatchCapture, []string) {
	return nil, nil
}
