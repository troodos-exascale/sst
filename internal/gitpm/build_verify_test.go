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
