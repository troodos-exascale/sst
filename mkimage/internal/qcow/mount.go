package qcow

import (
	"fmt"
	"mkimage/internal/util"
	"os"
	"os/exec"
	"os/user"
)

// FindRootPartition returns either /dev/nbd0pX or /dev/nbd0,
// depending on whether the qcow2 has partitions or is partitionless.
func FindRootPartition(nbd string) (string, error) {

	// Try partitions first
	for i := 1; i < 16; i++ {
		part := fmt.Sprintf("%sp%d", nbd, i)
		if _, err := os.Stat(part); err == nil {
			return part, nil
		}
	}

	// Try flat filesystem at /dev/nbd0
	if _, err := os.Stat(nbd); err == nil {
		return nbd, nil
	}

	return "", fmt.Errorf("no usable partition found for %s", nbd)
}

// MountPartition mounts the given device read-only and returns the mountpoint.
// Uses sudo
// MountPartition mounts the block device read-only and makes the contents
// readable to the current (non-root) user.
func MountPartition(dev string) (string, error) {

	// Load ext4 if needed
	util.Run("modprobe", "ext4")

	// Determine current user UID/GID for mount permissions
	u, err := user.Current()
	if err != nil {
		return "", err
	}

	dir, err := os.MkdirTemp("", "mkimage-mnt-*")
	if err != nil {
		return "", err
	}

	// Make filesystem readable by this user
	mountOpts := fmt.Sprintf("ro,uid=%s,gid=%s", u.Uid, u.Gid)

	// Attempt auto fs-detection mount
	cmd := exec.Command("sudo", "mount", "-o", mountOpts, dev, dir)
	if err := cmd.Run(); err == nil {
		return dir, nil
	}

	// Explicit ext4 fallback
	cmd = exec.Command("sudo", "mount", "-t", "ext4", "-o", mountOpts, dev, dir)
	if err := cmd.Run(); err == nil {
		return dir, nil
	}

	return "", fmt.Errorf("mount failed on %s (try installing ext4 tools)", dev)
}

// Unmount unmounts the given directory.
func Unmount(dir string) error {
	return exec.Command("umount", dir).Run()
}
