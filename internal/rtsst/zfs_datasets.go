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
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/troodos-exascale/sst/internal/sst"
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

// PromoteCapsule takes an assembled capsule directory from the build area
// and atomically moves it into the permanent catalog (images or instances).
// If ZFS is active, the artefacts/ subdirectory is natively 'zfs rename'd.
func (d *DatasetManager) PromoteCapsule(target, srcCapsuleDir, kind string) error {
	destCapsuleDir := filepath.Join(d.BasePoint, kind, target)
	_ = os.MkdirAll(filepath.Dir(destCapsuleDir), 0755)

	// Clean out any previous incomplete or overwritten state
	_ = os.RemoveAll(destCapsuleDir)

	if d.Zpool != "" {
		// e.g. <pool>/builds/<id>/capsules/ubuntu-efi-artifacts
		srcDS := fmt.Sprintf("%s/capsules/%s-artifacts", d.datasetPath(""), target)
		// We nest it structurally in ZFS as <pool>/catalog/<kind>/<target>-artifacts
		dstDS := fmt.Sprintf("%s/catalog/%s/%s-artifacts", d.Zpool, kind, target)

		// Create parent dataset structure if needed
		_ = exec.Command("zfs", "create", "-p", filepath.Dir(dstDS)).Run()

		// 1. Move the plain text directory (image.yaml, blueprint/) FIRST
		if err := os.Rename(srcCapsuleDir, destCapsuleDir); err != nil {
			// Fallback if renaming across mount boundaries
			_, cpErr := sst.ExecShell(context.Background(), nil, "cp", "-a", srcCapsuleDir, destCapsuleDir)
			if cpErr != nil {
				return fmt.Errorf("failed to copy capsule directory to catalog: %w", cpErr)
			}
		}

		// 2. Atomically rename the artifacts dataset to rest under the new mountpoint
		if err := exec.Command("zfs", "rename", "-f", srcDS, dstDS).Run(); err == nil {
			newMount := filepath.Join(destCapsuleDir, fmt.Sprintf("capsule-%s-artifacts", target))
			_ = exec.Command("zfs", "set", "mountpoint="+newMount, dstDS).Run()
		} else {
			return fmt.Errorf("zfs rename of artifacts failed: %w", err)
		}
		return nil
	}

	// No ZFS: simply rename the directory structure directly
	return os.Rename(srcCapsuleDir, destCapsuleDir)
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
		// Returning false ensures engine proceeds to Action when ZFS is not active
		return false 
	}
	dsPath := d.datasetPath(subPath)
	cmd := exec.Command("zfs", "list", "-t", "snapshot", fmt.Sprintf("%s@%s", dsPath, tag))
	return cmd.Run() == nil
}
