package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCloneCacheInvalidationOnFileMutation(t *testing.T) {
	tmpDir := t.TempDir()
	dummySource := filepath.Join(tmpDir, "test.qcow2")
	
	// Create initial image payload
	if err := os.WriteFile(dummySource, []byte("v1_payload"), 0644); err != nil {
		t.Fatal(err)
	}

	getMetaHash := func() string {
		info, _ := os.Stat(dummySource)
		return fmt.Sprintf("%d-%d", info.Size(), info.ModTime().UnixNano())
	}

	hash1 := getMetaHash()
	time.Sleep(50 * time.Millisecond) // Ensure monotonic clock progression
	
	// Simulate user overwriting the image with a new build (same path)
	if err := os.WriteFile(dummySource, []byte("v2_larger_payload"), 0644); err != nil {
		t.Fatal(err)
	}
	hash2 := getMetaHash()

	if hash1 == hash2 {
		t.Fatalf("File mutation did not change meta hash. Cache will falsely hit on next build!")
	}
}
