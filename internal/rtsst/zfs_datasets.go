package rtsst

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type DatasetManager struct {
	Zpool           string 
	BuildID         string 
	BasePoint       string 
	RuntimeRootName string 
}

func NewDatasetManager(runtimeDir, buildID, runtimeRootName string) *DatasetManager {
	zpool := os.Getenv("SST_ZPOOL")
	return &DatasetManager{
		Zpool:           zpool,
		BuildID:         buildID,
		BasePoint:       runtimeDir,
		RuntimeRootName: runtimeRootName,
	}
}

func (d *DatasetManager) datasetPath(subPath string) string {
	if d.Zpool == "" {
		return ""
	}
	if subPath == "" {
		return fmt.Sprintf("%s/builds/%s", d.Zpool, d.BuildID)
	}
	return fmt.Sprintf("%s/builds/%s/%s", d.Zpool, d.BuildID, subPath)
}

func (d *DatasetManager) MountPath(subPath string) string {
	return filepath.Join(d.BasePoint, "builds", d.BuildID, subPath)
}

func (d *DatasetManager) create(subPath string) error {
	physicalPath := d.MountPath(subPath)

	if dsPath := d.datasetPath(subPath); dsPath != "" {
		cmd := exec.Command("zfs", "create", "-p", dsPath)
		_ = cmd.Run()
	}

	return os.MkdirAll(physicalPath, 0755)
}

func (d *DatasetManager) InitBuildEnvironment() error {
	if err := d.create(""); err != nil {
		return err
	}

	caches := []string{"cache/apt", "cache/apk", "cache/get", "cache/from", "cache/dnf"}
	for _, cache := range caches {
		if err := d.create(cache); err != nil {
			return err
		}
	}

	if err := d.create("as-roots"); err != nil {
		return err
	}
	if err := d.create(d.RuntimeRootName); err != nil {
		return err
	}

	return nil
}

func (d *DatasetManager) CreateAsRoot(stageName string) (string, error) {
	subPath := fmt.Sprintf("as-roots/%s", stageName)
	if err := d.create(subPath); err != nil {
		return "", fmt.Errorf("failed to create AS dataset for %s: %w", stageName, err)
	}
	return d.MountPath(subPath), nil
}

func (d *DatasetManager) RuntimeRoot() string {
	return d.MountPath(d.RuntimeRootName)
}

func (d *DatasetManager) CacheDir(cacheName string) string {
	return d.MountPath(fmt.Sprintf("cache/%s", cacheName))
}

func (d *DatasetManager) Destroy() error {
	mountPath := d.MountPath("")
	_ = exec.Command("umount", "-R", mountPath).Run()

	if dsPath := d.datasetPath(""); dsPath != "" {
		cmd := exec.Command("zfs", "destroy", "-r", dsPath)
		_ = cmd.Run() 
	}

	return os.RemoveAll(mountPath)
}

func (d *DatasetManager) ClearCache() error {
	caches := []string{"cache/apt", "cache/apk", "cache/get", "cache/from", "cache/dnf"}
	for _, cache := range caches {
		if dsPath := d.datasetPath(cache); dsPath != "" {
			_ = exec.Command("zfs", "destroy", "-r", dsPath).Run()
		}
		_ = os.RemoveAll(d.MountPath(cache))
		_ = d.create(cache)
	}
	return nil
}

func (d *DatasetManager) PromoteTo(subPath, destCategory, destName string) error {
	srcPath := d.MountPath(subPath)
	dstPath := filepath.Join(d.BasePoint, destCategory, destName)

	if d.Zpool != "" {
		srcDS := d.datasetPath(subPath)
		dstDS := fmt.Sprintf("%s/%s/%s", d.Zpool, destCategory, destName)

		_ = exec.Command("zfs", "create", "-p", filepath.Dir(dstDS)).Run()
		if err := exec.Command("zfs", "rename", srcDS, dstDS).Run(); err != nil {
			return fmt.Errorf("zfs rename failed: %w", err)
		}
		_ = exec.Command("zfs", "set", "mountpoint="+dstPath, dstDS).Run()
		return nil
	}

	os.MkdirAll(filepath.Dir(dstPath), 0755)
	return os.Rename(srcPath, dstPath)
}

func (d *DatasetManager) SnapshotDataset(subPath, tag string) error {
	if d.Zpool == "" {
		return nil
	}
	dsPath := d.datasetPath(subPath)
	cmd := exec.Command("zfs", "snapshot", fmt.Sprintf("%s@%s", dsPath, tag))
	if out, err := cmd.CombinedOutput(); err != nil {
		if !strings.Contains(string(out), "already exists") {
			return fmt.Errorf("zfs snapshot failed: %w, output: %s", err, string(out))
		}
	}
	return nil
}

func (d *DatasetManager) RollbackDataset(subPath, tag string) error {
	if d.Zpool == "" {
		return nil
	}
	dsPath := d.datasetPath(subPath)
	cmd := exec.Command("zfs", "rollback", "-r", "-f", fmt.Sprintf("%s@%s", dsPath, tag))
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("zfs rollback failed: %w, output: %s", err, string(out))
	}
	return nil
}

func (d *DatasetManager) CheckSnapshot(subPath, tag string) bool {
	if d.Zpool == "" {
		// FIX: Returning false ensures engine proceeds to Action when ZFS is not active
		return false 
	}
	dsPath := d.datasetPath(subPath)
	cmd := exec.Command("zfs", "list", "-t", "snapshot", fmt.Sprintf("%s@%s", dsPath, tag))
	return cmd.Run() == nil
}
