package rtsst

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"strings"
	"time"
)

// listDevicePartitions returns e.g. /dev/nbd0p1…/dev/nbd0p31.
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

// MountPartition mounts a device read-only and returns the mountpoint.
// It also makes sure a temporary mount directory is cleaned upon Unmount.
func MountPartition(dev string) (string, error) {

	// verify device node
	if _, err := os.Stat(dev); err != nil {
		return "", fmt.Errorf(
			"mount failed: device %s does not exist. qemu-nbd may not have created partitions", dev)
	}

	// ensure ext4 module is present (best effort)
	if out, err := Run("modprobe", "ext4"); err != nil && !strings.Contains(out, "not found") {
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
	// Try a clean unmount
	cmd := exec.Command("umount", dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("umount %s failed: %v (output: %s)", dir, err, strings.TrimSpace(string(out)))
	}

	// Remove the temporary directory
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("failed to remove mount dir %s: %w", dir, err)
	}
	fmt.Printf("umount succeeded %s\n", dir)
	return nil
}

// Attach connects a QCOW2 file to /dev/nbd0 using the minimal sequence
func Attach(path string) (string, error) {

	// Ensure stale device is detached
	Run("qemu-nbd", "-d", "/dev/nbd0")

	// Load NBD module with partition support
	Run("modprobe", "nbd", "max_part=16")

	// Connect qcow2 to nbd0
	out, err := Run("qemu-nbd", "--connect=/dev/nbd0", path)
	if err != nil {
		return "", fmt.Errorf("qemu-nbd attach failed: %s", out)
	}

	// Give udev time to create /dev/nbd0p1, p2…
	time.Sleep(1 * time.Second)

	// Ask udev to settle (create nodes)
	Run("udevadm", "settle")

	return "/dev/nbd0", nil
}

func Detach(dev string) error {
	Run("qemu-nbd", "-d", dev)
	return nil
}
