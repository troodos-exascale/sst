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

func TestBuildRPMDeterministic(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "pkg")
	if err := os.Mkdir(root, 0755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(root, "hello.sh"), []byte("#!/bin/sh\necho Hello world\n"), 0755)

	opts := &Options{
		InputType:   "dir",
		OutputType:  "rpm",
		Name:        "demo",
		Version:     "1.0",
		Arch:        "arm64",
		Maintainer:  "Test <test@sst>",
		Description: "demo rpm package",
		License:     "MIT",
		Chdir:       root,
	}

	out1, err := BuildRPM(opts)
	if err != nil {
		t.Fatalf("first build failed: %v", err)
	}
	stat1, _ := os.Stat(out1)

	out2, err := BuildRPM(opts)
	if err != nil {
		t.Fatalf("second build failed: %v", err)
	}
	stat2, _ := os.Stat(out2)

	if stat1.Size() != stat2.Size() {
		t.Fatalf("size mismatch: %d != %d", stat1.Size(), stat2.Size())
	}
}
