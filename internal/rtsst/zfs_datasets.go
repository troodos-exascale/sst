package rtsst

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// DatasetManager enforces the strict ZFS topology for a single build instance.
type DatasetManager struct {
	Zpool     string // e.g., "zroot/runtimes"
	BuildID   string // e.g., the hash or inode of the Dockerfile
	BasePoint string // Physical mountpoint, e.g., ~/.runtimes
}

// NewDatasetManager creates a new DatasetManager.
func NewDatasetManager(runtimeDir, buildID string) *DatasetManager {
	// Detect ZFS pool if requested via env, else empty for standard directories
	zpool := os.Getenv("SST_ZPOOL")
	return &DatasetManager{
		Zpool:     zpool,
		BuildID:   buildID,
		BasePoint: runtimeDir,
	}
}

// datasetPath returns the fully qualified ZFS dataset path.
func (d *DatasetManager) datasetPath(subPath string) string {
	if d.Zpool == "" {
		return "" // No ZFS available, use physical directories only
	}
	// Avoid trailing slash if subPath is empty
	if subPath == "" {
		return fmt.Sprintf("%s/builds/build-%s", d.Zpool, d.BuildID)
	}
	return fmt.Sprintf("%s/builds/build-%s/%s", d.Zpool, d.BuildID, subPath)
}

// mountPath returns the physical host directory.
func (d *DatasetManager) mountPath(subPath string) string {
	return filepath.Join(d.BasePoint, "builds", fmt.Sprintf("build-%s", d.BuildID), subPath)
}

// create idempotently provisions the path, trying ZFS first.
func (d *DatasetManager) create(subPath string) error {
	physicalPath := d.mountPath(subPath)

	if dsPath := d.datasetPath(subPath); dsPath != "" {
		cmd := exec.Command("zfs", "create", "-p", dsPath)
		if err := cmd.Run(); err != nil {
			// Ignore if it already exists. ProcessState checks ensure we don't panic on missing zfs binary
			if !strings.Contains(err.Error(), "already exists") && cmd.ProcessState != nil && cmd.ProcessState.ExitCode() != 1 {
				// Fallthrough to standard mkdir
			}
		}
	}

	// Always ensure physical directory exists (serves as mountpoint or fallback)
	return os.MkdirAll(physicalPath, 0755)
}

// InitBuildEnvironment provisions the base dataset and the persistent caches.
func (d *DatasetManager) InitBuildEnvironment() error {
	// 1. The Base Build Root
	if err := d.create(""); err != nil {
		return err
	}

	// 2. The Cache Topologies (Persistent across resumes)
	caches := []string{"cache/apt", "cache/apk", "cache/get", "cache/from"}
	for _, cache := range caches {
		if err := d.create(cache); err != nil {
			return err
		}
	}

	// 3. The Stage & Target Topologies (Ephemeral, wiped on clean start)
	if err := d.create("as-roots"); err != nil {
		return err
	}
	if err := d.create("runtime-root"); err != nil {
		return err
	}

	return nil
}

// CreateAsRoot provisions a dedicated dataset for a named build stage.
func (d *DatasetManager) CreateAsRoot(stageName string) (string, error) {
	subPath := fmt.Sprintf("as-roots/%s", stageName)
	if err := d.create(subPath); err != nil {
		return "", fmt.Errorf("failed to create AS dataset for %s: %w", stageName, err)
	}
	return d.mountPath(subPath), nil
}

// RuntimeRoot returns the primary output root dataset.
func (d *DatasetManager) RuntimeRoot() string {
	return d.mountPath("runtime-root")
}

// CacheDir returns the path for a specific persistent cache.
func (d *DatasetManager) CacheDir(cacheName string) string {
	return d.mountPath(fmt.Sprintf("cache/%s", cacheName))
}

// Destroy completely annihilates the build dataset and its physical directory.
// It uses a recursive ZFS destroy to cleanly unmount and wipe the entire tree.
func (d *DatasetManager) Destroy() error {
	// 1. Annihilate the ZFS datasets (if ZFS is active)
	if dsPath := d.datasetPath(""); dsPath != "" {
		cmd := exec.Command("zfs", "destroy", "-r", dsPath)
		_ = cmd.Run() // We ignore the error because the dataset might not exist yet
	}

	// 2. Annihilate the physical host directory
	return os.RemoveAll(d.mountPath(""))
}
