package bpmpkg

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBuildDebDeterministic(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "pkg")
	if err := os.Mkdir(root, 0755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(root, "foo.txt"), []byte("bar"), 0644)

	opts := &Options{
		InputType:   "dir",
		OutputType:  "deb",
		Name:        "demo",
		Version:     "1.0",
		Arch:        "arm64",
		Maintainer:  "Test <test@sst>",
		Description: "demo package",
		License:     "CDDL",
		Chdir:       root,
	}

	out1, err := BuildDeb(opts)
	if err != nil {
		t.Fatalf("first build failed: %v", err)
	}
	stat1, _ := os.Stat(out1)

	out2, err := BuildDeb(opts)
	if err != nil {
		t.Fatalf("second build failed: %v", err)
	}
	stat2, _ := os.Stat(out2)

	if stat1.Size() != stat2.Size() {
		t.Fatalf("size mismatch: %d != %d", stat1.Size(), stat2.Size())
	}
}
