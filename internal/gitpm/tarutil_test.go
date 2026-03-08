package gitpm

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCreateDeterministicTar(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "demo")
	if err := os.Mkdir(sub, 0755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(sub, "a.txt"), []byte("hello\n"), 0644)
	os.WriteFile(filepath.Join(sub, "b.txt"), []byte("world\n"), 0644)

	opts := TarOptions{BaseDir: sub, Output: filepath.Join(dir, "out1.tar.gz")}
	sum1, err := CreateDeterministicTar(opts)
	if err != nil {
		t.Fatalf("first build failed: %v", err)
	}

	sum2, err := CreateDeterministicTar(opts)
	if err != nil {
		t.Fatalf("second build failed: %v", err)
	}

	if sum1 != sum2 {
		t.Fatalf("non-deterministic archive: %s != %s", sum1, sum2)
	}
}
