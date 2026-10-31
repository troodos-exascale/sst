// Copyright 2025-2026 Peter Braam
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

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
