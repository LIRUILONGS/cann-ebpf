//go:build !linux || !arm64

package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "cpy-batch-poc: this POC requires linux/arm64")
	os.Exit(5)
}
