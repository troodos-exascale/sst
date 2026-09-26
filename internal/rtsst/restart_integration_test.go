package rtsst_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/troodos-exascale/sst/internal/rtsst"
	"github.com/troodos-exascale/sst/internal/sst"
)

func setupTestZpool(t *testing.T) (string, func()) {
	if os.Geteuid() != 0 {
		t.Skip("Skipping real ZFS test; requires root")
	}
	
	poolName := "test_zpool_sst_reconciliation"
	imgFile := filepath.Join(t.TempDir(), "zpool.img")
	
	// Create a 100MB file-backed zpool
	// FIXED: sst.ExecShell returns (*sst.ExecResult, error), so we must ignore the first return value
	_, err := sst.ExecShell(context.Background(), sst.LogxRoot(), "truncate", "-s", "100M", imgFile)
	assert.NoError(t, err)

	_, err = sst.ExecShell(context.Background(), sst.LogxRoot(), "zpool", "create", poolName, imgFile)
	if err != nil {
		t.Skipf("Failed to create test zpool (is ZFS installed?): %v", err)
	}

	return poolName, func() {
		// FIXED: Ignore both return values in the cleanup function
		_, _ = sst.ExecShell(context.Background(), sst.LogxRoot(), "zpool", "destroy", "-f", poolName)
	}
}

func TestReconciliation_JSON_and_ZFS(t *testing.T) {
	poolName, cleanup := setupTestZpool(t)
	defer cleanup()

	t.Setenv("SST_ZPOOL", poolName)
	tmpDir := t.TempDir()

	// 1. Setup a dummy dataset manager
	mgr := rtsst.NewDatasetManager(tmpDir, "test-build-123", "runtime-root")
	err := mgr.InitBuildEnvironment()
	assert.NoError(t, err)

	// 2. Setup the Runner and fake instructions
	statePath := filepath.Join(mgr.MountPath(""), "state.json")
	runner := &sst.Runner{
		Log:       sst.LogxRoot(),
		StatePath: statePath,
		State:     make(map[string]interface{}),
		Completed: make(map[string]bool),
	}

	instructions := []rtsst.Instruction{
		{Index: 0, Hash: "hash00000000"},
		{Index: 1, Hash: "hash11111111"},
		{Index: 2, Hash: "hash22222222"}, // We will desync this one
	}

	// 3. Simulate a fully completed run (JSON says everything is done)
	runner.Completed["hash00000000"] = true
	runner.Completed["hash11111111"] = true
	runner.Completed["hash22222222"] = true
	assert.NoError(t, runner.SaveState())

	// 4. Create the physical ZFS snapshots for Step 0 and Step 1, but NOT Step 2
	err = mgr.SnapshotDataset(mgr.RuntimeRootName, "step_00_hash_hash1111")
	assert.NoError(t, err)
	err = mgr.SnapshotDataset(mgr.RuntimeRootName, "step_01_hash_hash2222")
	assert.NoError(t, err)
	// (Intentionally skipping Step 2's snapshot to simulate a crashed transaction / out-of-sync state)

	// 5. Run the Reconciliation Logic (This mimics the exact block we added to build.go)
	err = runner.LoadState()
	assert.NoError(t, err)

	for i, inst := range instructions {
		if runner.Completed[inst.Hash] {
			var nextHashStub string
			if i+1 < len(instructions) {
				nextHashStub = instructions[i+1].Hash[:8]
			} else {
				nextHashStub = "final"
			}
			tag := fmt.Sprintf("step_%02d_hash_%s", inst.Index, nextHashStub)
			
			if !mgr.CheckSnapshot(mgr.RuntimeRootName, tag) {
				delete(runner.Completed, inst.Hash)
			}
		}
	}

	// 6. Assertions: Mathematical Proof
	assert.True(t, runner.Completed["hash00000000"], "Step 0 should remain completed (Sync)")
	assert.True(t, runner.Completed["hash11111111"], "Step 1 should remain completed (Sync)")
	assert.False(t, runner.Completed["hash22222222"], "Step 2 should be PRUNED because ZFS snapshot is missing (Out of Sync)")
}
