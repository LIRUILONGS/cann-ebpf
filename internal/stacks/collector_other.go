//go:build !linux

package stacks

import (
	"context"
	"errors"
	"fmt"
	"runtime"
)

// ErrUnsupported reports that kernel stack sampling requires Linux.
var ErrUnsupported = errors.New("stacks: kernel stack sampling is not supported on this platform")

// Collect fails explicitly on non-Linux builds instead of pretending that an
// empty profile means the target had no kernel activity.
func Collect(context.Context, Options) (*CollectResult, error) {
	return nil, fmt.Errorf("%w (current GOOS=%s)", ErrUnsupported, runtime.GOOS)
}
