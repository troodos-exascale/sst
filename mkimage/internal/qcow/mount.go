package qcow

import (
	"fmt"
	"mkimage/internal/util"
	"os"
	"os/exec"
	"os/user"
	"strings"
)

// listDevicePartitions returns all visible partition device nodes for nbd,
// e.g. /dev/nbd0p1…/dev/nbd0p31.
func listDevicePartitions(nbd string) []string {
	var parts []string
	for i := 1; i < 32; i++ {
		part := fmt.Sprintf("%sp%d", nbd, i)
		if _, err := os.Stat(part); err == nil {
			parts = append(parts, part)
		}
	}
	return parts
}

// MountPartition mounts the given device read-only and returns the mountpoint.
// It also makes sure the temporary mount directory is cleaned up on Unmount.
func MountPartition(dev string) (string, error) {

	// verify device node
	if _, err := os.Stat(dev); err != nil {
		return "", fmt.Errorf(
			"mount failed: device %s does not exist. qemu-nbd may not have created partitions", dev)
	}

	// ensure ext4 module is present (best effort)
	if out, err := util.Run("modprobe", "ext4"); err != nil && !strings.Contains(out, "not found") {
		// if ext4 really fails, report it; otherwise ignore missing modprobe
		// on non-modular kernels
		return "", fmt.Errorf(
			"mount failed: could not load ext4 kernel module: %v (output: %s)",
			err, out)
	}

	_, err := user.Current()
	if err != nil {
		return "", fmt.Errorf("mount failed: cannot determine current user: %v", err)
	}

	dir, err := os.MkdirTemp("", "mkimage-mnt-*")
	if err != nil {
		return "", fmt.Errorf("mount failed: cannot create temp mount directory: %v", err)
	}

	// ext4: keep options simple
	mountOpts := "ro"

	// Attempt auto fs-detection mount first
	autoCmd := exec.Command("mount", "-o", mountOpts, dev, dir)
	if out, err := autoCmd.CombinedOutput(); err == nil {
		// we will clean dir in Unmount
		return dir, nil
	} else {
		// If this is just an unsupported filesystem (BIOS/ESP/etc), we want
		// the caller to be able to ignore it.
		_ = os.RemoveAll(dir)
		return "", fmt.Errorf("mount failed on %s: %s", dev, strings.TrimSpace(string(out)))
	}
}

// Unmount unmounts the given directory and removes the mountpoint.
func Unmount(dir string) error {
	if err := exec.Command("umount", dir).Run(); err != nil {
		return err
	}
	// remove mount directory to avoid cruft in /tmp
	return os.RemoveAll(dir)
}
