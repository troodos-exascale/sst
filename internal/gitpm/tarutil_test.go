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
