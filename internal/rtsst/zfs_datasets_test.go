package rtsst

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// goTree is a pure-Go helper that mimics `tree -d` for visual test verification.
func goTree(root string, prefix string) string {
	var sb strings.Builder
	entries, err := os.ReadDir(root)
	if err != nil {
		return ""
	}

	var dirs []os.DirEntry
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, e)
		}
	}

	for i, d := range dirs {
		isLast := i == len(dirs)-1
		var branch, newPrefix string
		if isLast {
			branch = "└── "
			newPrefix = prefix + "    "
		} else {
			branch = "├── "
			newPrefix = prefix + "│   "
		}
		sb.WriteString(prefix + branch + d.Name() + "\n")
		sb.WriteString(goTree(filepath.Join(root, d.Name()), newPrefix))
	}
	return sb.String()
}

func TestDatasetManager_TopologyAndTeardown(t *testing.T) {
	// Use a temp directory for the physical mountpoint
	tmpDir := t.TempDir()

	// Create a manager WITHOUT a Zpool defined (tests the multi-platform physical fallback)
	mgr := &DatasetManager{
		Zpool:           "",
		BuildID:         "abcdef123456-web.dockerfile",
		BasePoint:       tmpDir,
		RuntimeRootName: "runtime-root-web.dockerfile",
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

	expectedStagePath := filepath.Join(tmpDir, "builds", "abcdef123456-web.dockerfile", "as-roots", "ubuntu-massive")
	assert.Equal(t, expectedStagePath, stagePath, "Stage path should match expected layout")

	info, err = os.Stat(stagePath)
	assert.NoError(t, err, "Stage directory should exist on disk")
	assert.True(t, info.IsDir())

	// 3. Test Snapshot/Rollback Stubs (Zpool == "")
	err = mgr.SnapshotDataset("as-roots/ubuntu-massive", "step_02")
	assert.NoError(t, err, "Snapshot on empty Zpool should silently no-op")

	err = mgr.RollbackDataset("as-roots/ubuntu-massive", "step_02")
	assert.NoError(t, err, "Rollback on empty Zpool should silently no-op")

	// --- VISUAL TREE OUTPUT ---
	// Prints the directory structure mimicking `tree -d`
	fmt.Println("\n=== Generated Dataset Topology ===")
	fmt.Println(filepath.Base(tmpDir) + " (Mock ~/.runtimes)")
	fmt.Print(goTree(tmpDir, ""))
	fmt.Println("==================================")

	// 4. Test Nuclear Destruction (--force)
	err = mgr.Destroy()
	assert.NoError(t, err, "Destroy should succeed")

	// Verify the ENTIRE build root directory is gone
	buildRoot := mgr.mountPath("")
	_, err = os.Stat(buildRoot)
	assert.True(t, os.IsNotExist(err), "The entire build directory should be annihilated")
}
