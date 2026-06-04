package rtsst

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

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
	tmpDir := t.TempDir()

	mgr := &DatasetManager{
		Zpool:           "",
		BuildID:         "abcdef123456-web.dockerfile",
		BasePoint:       tmpDir,
		RuntimeRootName: "runtime-root-web.dockerfile",
	}

	err := mgr.InitBuildEnvironment()
	assert.NoError(t, err, "InitBuildEnvironment should succeed")

	for _, cache := range []string{"apt", "apk", "get", "from"} {
		cachePath := mgr.CacheDir(cache)
		info, err := os.Stat(cachePath)
		assert.NoError(t, err, "Cache directory %s should exist", cache)
		assert.True(t, info.IsDir(), "Cache path should be a directory")
	}

	runtimeRoot := mgr.RuntimeRoot()
	info, err := os.Stat(runtimeRoot)
	assert.NoError(t, err, "Runtime root should exist")
	assert.True(t, info.IsDir())

	stagePath, err := mgr.CreateAsRoot("ubuntu-massive")
	assert.NoError(t, err, "CreateAsRoot should succeed")

	expectedStagePath := filepath.Join(tmpDir, "builds", "abcdef123456-web.dockerfile", "as-roots", "ubuntu-massive")
	assert.Equal(t, expectedStagePath, stagePath, "Stage path should match expected layout")

	info, err = os.Stat(stagePath)
	assert.NoError(t, err, "Stage directory should exist on disk")
	assert.True(t, info.IsDir())

	err = mgr.SnapshotDataset("as-roots/ubuntu-massive", "step_02")
	assert.NoError(t, err, "Snapshot on empty Zpool should silently no-op")

	err = mgr.RollbackDataset("as-roots/ubuntu-massive", "step_02")
	assert.NoError(t, err, "Rollback on empty Zpool should silently no-op")

	fmt.Println("\n=== Generated Dataset Topology ===")
	fmt.Println(filepath.Base(tmpDir) + " (Mock ~/.runtimes)")
	fmt.Print(goTree(tmpDir, ""))
	fmt.Println("==================================")

	err = mgr.Destroy()
	assert.NoError(t, err, "Destroy should succeed")

	buildRoot := mgr.MountPath("")
	_, err = os.Stat(buildRoot)
	assert.True(t, os.IsNotExist(err), "The entire build directory should be annihilated")
}

