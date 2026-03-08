//go:build !linux

package rtsst

import "fmt"

// ExtractRawKernel is a stub for non-Linux systems.
// The mkimg CLI enforces a runtime check preventing this from being reached.
func ExtractRawKernel(inPath, outPath string) error {
	return fmt.Errorf("kernel extraction is only supported on Linux")
}