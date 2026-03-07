package bpmpkg

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
)

// Verify validates that a package is well-formed (.deb or .rpm).
// Returns (true, nil) if valid, (false, err) otherwise.
func Verify(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()

	buf := make([]byte, 8)
	n, err := io.ReadFull(f, buf)
	if err != nil {
		return false, fmt.Errorf("read header: %w", err)
	}
	if n < 8 {
		return false, fmt.Errorf("short read: %d bytes", n)
	}

	// ar(5) magic for .deb
	if string(buf) == "!<arch>\n" {
		return true, nil
	}

	// rpm magic
	if buf[0] == 0xed && buf[1] == 0xab && buf[2] == 0xee && buf[3] == 0xdb {
		return true, nil
	}

	return false, fmt.Errorf("unknown package magic: %x", buf[:4])
}

// ComparePackages ensures two package files are byte-for-byte identical.
func ComparePackages(pathA, pathB string) error {
	hA, err := fileSHA256(pathA)
	if err != nil {
		return err
	}
	hB, err := fileSHA256(pathB)
	if err != nil {
		return err
	}
	if hA != hB {
		return fmt.Errorf("mismatch: %s vs %s", hA, hB)
	}
	fmt.Println("✅ deterministic match")
	return nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}
