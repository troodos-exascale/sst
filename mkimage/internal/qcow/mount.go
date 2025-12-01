package qcow

import (
	"fmt"
	"mkimage/internal/util"
	"os"
	"os/exec"
	"strings"
)

// FindRootPartition returns /dev/nbd0pX or the raw device,
// but now also reports *why* none were found.
func FindRootPartition(nbd string) (string, error) {

	foundParts := []string{}
	for i := 1; i < 16; i++ {
		part := fmt.Sprintf("%sp%d", nbd, i)
		if _, err := os.Stat(part); err == nil {
			foundParts = append(foundParts, part)
		}
	}

	if len(foundParts) > 0 {
		return foundParts[0], nil
	}

	// no partitions — check if raw nbd exists
	if _, err := os.Stat(nbd); err == nil {
		return nbd, nil
	}

	return "", fmt.Errorf(
		"no usable partitions under %s: checked %s. "+
			"Is qemu-nbd running? Was the qcow2 successfully attached?",
		nbd, strings.Join(foundParts, ","),
	)
}

// MountPartition mounts the block device read-only and returns the mountpoint.
// Now returns full diagnostics.
func MountPartition(dev string) (string, error) {

	// verify device exists
	if _, err := os.Stat(dev); err != nil {
		return "", fmt.Errorf("mount failed: device %s does not exist", dev)
	}

	// ensure ext4 is loaded
	util.Run("modprobe", "ext4")

	dir, err := os.MkdirTemp("", "mkimage-mnt-*")
	if err != nil {
		return "", fmt.Errorf("mount failed: cannot create mountpoint: %v", err)
	}

	//
	// IMPORTANT FIX:
	// Ext4 does not accept uid= / gid= options. They cause "wrong fs type" failures.
	//
	mountOpts := "ro"

	// Try automatic filesystem detection
	autoCmd := exec.Command("mount", "-o", mountOpts, dev, dir)
	if _, err := autoCmd.CombinedOutput(); err == nil {
		return dir, nil
	}

	// Explicit ext4 fallback
	ext4Cmd := exec.Command("mount", "-t", "ext4", "-o", mountOpts, dev, dir)
	if out, err := ext4Cmd.CombinedOutput(); err == nil {
		return dir, nil
	} else {
		return "", fmt.Errorf("mount failed on %s: %s", dev, string(out))
	}
}

// Unmount unmounts the given directory.
func Unmount(dir string) error {
	cmd := exec.Command("umount", dir)
	return cmd.Run()
}
