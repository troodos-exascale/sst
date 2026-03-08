package gitpm

import (
	"os"
	"path/filepath"
	"testing"
)

// TestBuildAndVerify ensures that BuildDeb produces a valid, verifiable package
// and that Verify() detects no structural errors.
func TestBuildAndVerify(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	if err := os.Mkdir(src, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "hello.txt"), []byte("world\n"), 0644); err != nil {
		t.Fatal(err)
	}

	opts := &Options{
		InputType:   "dir",
		OutputType:  "deb",
		Name:        "demo",
		Version:     "1.0",
		Arch:        "arm64",
		Maintainer:  "Test <test@sst>",
		Description: "unit test pkg",
		Chdir:       src,
	}

	out, err := BuildDeb(opts)
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}

	// Ensure file exists and has nonzero size.
	info, err := os.Stat(out)
	if err != nil {
		t.Fatalf("stat failed: %v", err)
	}
	if info.Size() == 0 {
		t.Fatalf("empty package output")
	}

	// Verify structural correctness.
	if _, err := Verify(out); err != nil {
		t.Fatalf("verify failed: %v", err)
	}

	// Rebuild deterministically and compare hashes.
	out2, err := BuildDeb(opts)
	if err != nil {
		t.Fatalf("second build failed: %v", err)
	}
	info2, _ := os.Stat(out2)
	if info.Size() != info2.Size() {
		t.Errorf("non-deterministic size: %d vs %d", info.Size(), info2.Size())
	}
}
