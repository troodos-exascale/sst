package rtsst

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDatasetManager_TopologyAndTeardown(t *testing.T) {
	// Use a temp directory for the physical mountpoint
	tmpDir := t.TempDir()

	// Create a manager WITHOUT a Zpool defined (tests the multi-platform physical fallback)
	mgr := &DatasetManager{
		Zpool:     "",
		BuildID:   "test-heist",
		BasePoint: tmpDir,
	}

	// 1. Test Initialization
	err := mgr.InitBuildEnvironment()
	assert.NoError(t, err, "InitBuildEnvironment should succeed")

	// Verify the permanent caches were created
	for _, cache := range []string{"apt", "apk", "get", "from"} {
		cachePath := mgr.CacheDir(cache)
		info, err := os.Stat(cachePath)
		assert.NoError(t, err, "Cache directory %s should exist", cache)
		assert.True(t, info.IsDir(), "Cache path should be a directory")
	}

	// Verify the target root exists
	runtimeRoot := mgr.RuntimeRoot()
	info, err := os.Stat(runtimeRoot)
	assert.NoError(t, err, "Runtime root should exist")
	assert.True(t, info.IsDir())

	// 2. Test AS Stage Provisioning
	stagePath, err := mgr.CreateAsRoot("ubuntu-massive")
	assert.NoError(t, err, "CreateAsRoot should succeed")

	expectedStagePath := filepath.Join(tmpDir, "builds", "build-test-heist", "as-roots", "ubuntu-massive")
	assert.Equal(t, expectedStagePath, stagePath, "Stage path should match expected layout")

	info, err = os.Stat(stagePath)
	assert.NoError(t, err, "Stage directory should exist on disk")
	assert.True(t, info.IsDir())

	// 3. Test Nuclear Destruction (--force)
	err = mgr.Destroy()
	assert.NoError(t, err, "Destroy should succeed")

	// Verify the ENTIRE build-<ino> directory is gone
	buildRoot := mgr.mountPath("")
	_, err = os.Stat(buildRoot)
	assert.True(t, os.IsNotExist(err), "The entire build directory should be annihilated")
}
