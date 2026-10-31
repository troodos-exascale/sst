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

package rtsst

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/troodos-exascale/sst/internal/sst"
)

func TestBug_CloneHashAndSkip(t *testing.T) {
	ctx := context.Background()
	log := sst.LogxRoot()
	tmpDir := t.TempDir()

	// 1. Verify Hash Bug: Different CLONE_SOURCE must produce different hashes
	dockerfile := "FROM scratch\nGET ${CLONE_SOURCE} /img\nEMIT: capsule"
	dfPath := filepath.Join(tmpDir, "clone.dockerfile")
	os.WriteFile(dfPath, []byte(dockerfile), 0644)

	b1 := NewBuilder(log, nil, dfPath, tmpDir, tmpDir, tmpDir)
	b1.BuildArgs["CLONE_SOURCE"] = "http://source-a.qcow2"
	b1.Parse(ctx)
	b1.PreProcess(ctx)

	b2 := NewBuilder(log, nil, dfPath, tmpDir, tmpDir, tmpDir)
	b2.BuildArgs["CLONE_SOURCE"] = "http://source-b.qcow2"
	b2.Parse(ctx)
	b2.PreProcess(ctx)

	var h1, h2 string
	for _, inst := range b1.Instructions {
		// The AST stores the literal parsed string. 
		// The variable substitution happens internally during the hash computation.
		if inst.Type == "ARG" && inst.Args == "GET_URL=${CLONE_SOURCE}" {
			h1 = inst.Hash
		}
	}
	for _, inst := range b2.Instructions {
		if inst.Type == "ARG" && inst.Args == "GET_URL=${CLONE_SOURCE}" {
			h2 = inst.Hash
		}
	}

	assert.NotEmpty(t, h1)
	assert.NotEqual(t, h1, h2, "Instructions with different evaluated ARGs must have different hashes")

	// 2. Verify Skip Bug: Probes must return false if ZFS is missing
	mgr := NewDatasetManager(tmpDir, "test-id", "test-root")
	mgr.Zpool = "" 
	
	exists := mgr.CheckSnapshot("test-root", "any-tag")
	assert.False(t, exists, "CheckSnapshot must return false if ZFS is not active, ensuring the Action runs")
}
