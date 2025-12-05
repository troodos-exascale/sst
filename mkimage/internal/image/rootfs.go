package image

import (
	"encoding/json"
	"fmt"

	"mkimage/internal/util"
)

// Structures to parse `lsblk -J -b -o NAME,TYPE,SIZE` output.

type lsblkOutput struct {
	Blockdevices []lsblkDevice `json:"blockdevices"`
}

type lsblkDevice struct {
	Name     string        `json:"name"`
	Type     string        `json:"type"`
	Size     json.Number   `json:"size"`
	Children []lsblkDevice `json:"children,omitempty"`
}

// FindRootPartition selects the *largest* partition on nbdDev.
// Heuristic: among all children with type == "part", pick the one
// with the maximum Size. If none exist, fall back to nbdDev itself.
//
// Example call: FindRootPartition("/dev/nbd0") → "/dev/nbd0p3".
func FindRootPartition(nbdDev string) (string, error) {
	out, err := util.Run("lsblk", "-J", "-b", "-o", "NAME,TYPE,SIZE", nbdDev)
	if err != nil {
		return "", fmt.Errorf("lsblk failed for %s: %v\noutput:\n%s", nbdDev, err, out)
	}

	var parsed lsblkOutput
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		return "", fmt.Errorf("parse lsblk JSON for %s: %w\nraw:\n%s", nbdDev, err, out)
	}
	if len(parsed.Blockdevices) == 0 {
		// no information; safest fallback is the device itself
		return nbdDev, nil
	}

	var bestName string
	var bestSize int64

	var walk func(dev lsblkDevice)
	walk = func(dev lsblkDevice) {
		if dev.Type == "part" {
			if sz, err := dev.Size.Int64(); err == nil {
				if sz > bestSize {
					bestSize = sz
					bestName = dev.Name
				}
			}
		}
		for _, c := range dev.Children {
			walk(c)
		}
	}

	for _, d := range parsed.Blockdevices {
		walk(d)
	}

	if bestName == "" {
		// no partitions; use the raw device
		return nbdDev, nil
	}

	// lsblk "name" is usually bare ("nbd0p3"), not "/dev/nbd0p3".
	if bestName[0] == '/' {
		return bestName, nil
	}
	return "/dev/" + bestName, nil
}
