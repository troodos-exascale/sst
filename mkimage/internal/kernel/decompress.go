package kernel

import (
	"compress/gzip"
	"fmt"
	"io"
	"os"
)

// ExtractRawKernel takes a gzip-compressed Linux kernel file (like Ubuntu's
// vmlinuz-*) and writes the decompressed raw kernel image to outPath.
//
// For now we only support gzip kernels (Ubuntu ARM, some others). If the input
// is not a valid gzip stream, this will return an error.
func ExtractRawKernel(inPath, outPath string) error {
	in, err := os.Open(inPath)
	if err != nil {
		return fmt.Errorf("open compressed kernel %s: %w", inPath, err)
	}
	defer in.Close()

	gr, err := gzip.NewReader(in)
	if err != nil {
		return fmt.Errorf("gzip reader for %s: %w", inPath, err)
	}
	defer gr.Close()

	out, err := os.Create(outPath)
	if err != nil {
		return fmt.Errorf("create raw kernel %s: %w", outPath, err)
	}
	defer out.Close()

	if _, err := io.Copy(out, gr); err != nil {
		return fmt.Errorf("decompress kernel: %w", err)
	}

	return nil
}
