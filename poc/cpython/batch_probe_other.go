//go:build !linux || !arm64

package cpython

import (
	"context"
	"fmt"
)

func RunBatch(ctx context.Context, opts BatchOptions) (*BatchResult, error) {
	return nil, fmt.Errorf("vLLM batch POC requires linux/arm64")
}
