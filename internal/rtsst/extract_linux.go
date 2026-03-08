//go:build linux

package rtsst

import (
	"bytes"
	"compress/bzip2"
	"compress/gzip"
	"debug/elf"
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/klauspost/compress/zstd"
	"github.com/pierrec/lz4/v4"
	"github.com/ulikunitz/xz"
	"github.com/ulikunitz/xz/lzma"
)

const me = "extract-vmlinux-go"

// ExtractRawKernel extracts an uncompressed vmlinux from inPath and writes it
// to outPath. It uses native Go decompression libraries and debug/elf to
// validate the kernel image, falling back to shell only for legacy LZO.
func ExtractRawKernel(inPath, outPath string) error {
	st, err := os.Stat(inPath)
	if err != nil {
		return fmt.Errorf("stat kernel %s: %w", inPath, err)
	}
	if st.Size() == 0 {
		return fmt.Errorf("%s: input kernel image is empty", me)
	}

	data, err := os.ReadFile(inPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", inPath, err)
	}

	// 1. Define pure-Go decompression strategies matching the original script
	type decompressor struct {
		name    string
		magic   []byte
		decoder func(io.Reader) (io.Reader, error)
	}

	strategies := []decompressor{
		{"gzip", []byte{0x1F, 0x8B, 0x08}, func(r io.Reader) (io.Reader, error) { return gzip.NewReader(r) }},
		{"unxz", []byte{0xFD, 0x37, 0x7A, 0x58, 0x5A, 0x00}, func(r io.Reader) (io.Reader, error) { return xz.NewReader(r) }},
		{"bunzip2", []byte("BZh"), func(r io.Reader) (io.Reader, error) { return bzip2.NewReader(r), nil }},
		{"unlzma", []byte{0x5D, 0x00, 0x00, 0x00}, func(r io.Reader) (io.Reader, error) { return lzma.NewReader(r) }},
		{"lz4 -d", []byte{0x02, 0x21, 0x4C, 0x18}, func(r io.Reader) (io.Reader, error) { return lz4.NewReader(r), nil }},
		{"unzstd", []byte{0x28, 0xB5, 0x2F, 0xFD}, func(r io.Reader) (io.Reader, error) { return zstd.NewReader(r) }},
	}

	for _, dec := range strategies {
		if ok, err := tryNativeDecompress(data, dec.magic, dec.name, dec.decoder, outPath); err != nil {
			return err
		} else if ok {
			return nil
		}
	}

	// 2. Legacy fallback for LZO (No standard pure-Go stream library exists for LZO)
	if ok, _ := tryLZOFallback(data, outPath); ok {
		return nil
	}

	// 3. Finally check for uncompressed images or objects natively
	if isVmlinux(inPath) {
		if err := CopyFile(inPath, outPath); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "%s: Extracted vmlinux using 'cat' from offset 0\n", me)
		return nil
	}

	return fmt.Errorf("%s: Cannot find vmlinux.", me)
}

// tryNativeDecompress searches for magic bytes, starts a Go decompressor, and peeks
// at the first 4 bytes to check for an ELF header before writing to disk.
func tryNativeDecompress(data []byte, magic []byte, name string, newDecoder func(io.Reader) (io.Reader, error), outPath string) (bool, error) {
	offset := 0
	for {
		pos := bytes.Index(data[offset:], magic)
		if pos == -1 {
			break
		}
		abs := int64(offset + pos)
		offset += pos + 1

		reader := bytes.NewReader(data[abs:])
		decReader, err := newDecoder(reader)
		if err != nil {
			continue
		}

		// Peek the first 4 bytes to see if it decompresses into an ELF file
		elfMagic := make([]byte, 4)
		if _, err := io.ReadFull(decReader, elfMagic); err != nil {
			closeIfCloser(decReader)
			continue
		}

		if !bytes.Equal(elfMagic, []byte("\x7fELF")) {
			closeIfCloser(decReader)
			continue
		}

		// We found an ELF! Stitch the peeked bytes back with the rest of the stream
		multi := io.MultiReader(bytes.NewReader(elfMagic), decReader)

		out, err := os.Create(outPath)
		if err != nil {
			closeIfCloser(decReader)
			return false, fmt.Errorf("failed to create output file: %w", err)
		}

		_, err = io.Copy(out, multi)
		out.Close()
		closeIfCloser(decReader)

		if err == nil && isVmlinux(outPath) {
			fmt.Fprintf(os.Stderr, "%s: Extracted vmlinux using '%s' from offset %d\n", me, name, abs)
			return true, nil
		}
		
		// If it wasn't a valid vmlinux, clean up the bad extraction and keep searching
		_ = os.Remove(outPath)
	}
	return false, nil
}

// tryLZOFallback handles LZO via the system binary, exactly as the original script did.
func tryLZOFallback(data []byte, outPath string) (bool, error) {
	magic := []byte{0x89, 0x4C, 0x5A}
	offset := 0
	for {
		pos := bytes.Index(data[offset:], magic)
		if pos == -1 {
			break
		}
		abs := int64(offset + pos)
		offset += pos + 1

		cmd := exec.Command("lzop", "-d")
		cmd.Stdin = bytes.NewReader(data[abs:])
		out, err := os.Create(outPath)
		if err != nil {
			continue
		}
		cmd.Stdout = out
		cmd.Stderr = io.Discard
		
		_ = cmd.Run()
		out.Close()

		if isVmlinux(outPath) {
			fmt.Fprintf(os.Stderr, "%s: Extracted vmlinux using 'lzop -d' from offset %d\n", me, abs)
			return true, nil
		}
		
		_ = os.Remove(outPath)
	}
	return false, nil
}

// isVmlinux uses Go's native ELF parser to verify the file is a valid Linux Kernel.
func isVmlinux(path string) bool {
	f, err := elf.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	
	// Uncompressed kernels are either Executable (ET_EXEC) or Shared Object (ET_DYN, for KASLR/PIE)
	// This efficiently prevents false-positives on kernel modules (.ko) which are Relocatable (ET_REL)
	return f.Type == elf.ET_EXEC || f.Type == elf.ET_DYN
}

func closeIfCloser(i interface{}) {
	if c, ok := i.(io.Closer); ok {
		_ = c.Close()
	} else if z, ok := i.(*zstd.Decoder); ok {
		// zstd.Decoder uses Close() but does not strictly implement io.ReadCloser in older versions
		z.Close()
	}
}