package qcow

import (
	"fmt"
	"time"

	"mkimage/internal/util"
)

// Attach connects a QCOW2 file to /dev/nbd0 using the minimal sequence
// that matches the user's manual working commands.
func Attach(path string) (string, error) {

	// Ensure stale device is detached
	util.Run("qemu-nbd", "-d", "/dev/nbd0")

	// Load NBD module with partition support
	util.Run("modprobe", "nbd", "max_part=16")

	// Connect qcow2 to nbd0
	out, err := util.Run("qemu-nbd", "--connect=/dev/nbd0", path)
	if err != nil {
		return "", fmt.Errorf("qemu-nbd attach failed: %s", out)
	}

	// Give udev time to create /dev/nbd0p1, p2…
	time.Sleep(1 * time.Second)

	// Ask udev to settle (create nodes)
	util.Run("udevadm", "settle")

	return "/dev/nbd0", nil
}

func Detach(dev string) error {
	util.Run("qemu-nbd", "-d", dev)
	return nil
}
