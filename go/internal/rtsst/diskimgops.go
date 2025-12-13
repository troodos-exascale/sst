package rtsst

import (
	"fmt"
	"os"
	"encoding/json"
	"path/filepath"
	"strings"
	"github.com/rekby/gpt"
)

func analyseDiskImg(path string) (err error, rootfs int, rootid string, bootpt int) {
	var biggest uint64
	var smallest uint64

	rootfsPartNo := -1
	bootPartNo := -1
	rootfsPartUUID := ""

	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err), 0, "", 0
	}
	defer f.Close()

	table, err := gpt.ReadTable(f, 512)
	if err != nil {
		return fmt.Errorf("read GPT %s: %w", path, err), 0, "", 0
	}

	fmt.Println("[init] GPT partitions:")
	for i, p := range table.Partitions {
		if p.IsEmpty() {
			continue
		}

		// GPT entry numbers are 1-based and can be sparse (p1, p15, …)
		partNo := i + 1

		// LBA range is inclusive
		size := p.LastLBA - p.FirstLBA + 1

		pt := strings.ToLower(p.Type.String())

		fmt.Printf("  %d: name=%q type=%s uuid=%s firstLBA=%d lastLBA=%d sizeLBA=%d\n",
			partNo,
			p.Name(),
			p.Type.String(),
			p.Id.String(),
			p.FirstLBA,
			p.LastLBA,
			size,
		)

		// Ignore EFI partitions here; this function is selecting Linux filesystem partitions.
		// (Your v1 spec refuses EFI boot.)
		if strings.Contains(pt, "efi") {
			continue
		}

		if size > biggest {
			biggest = size
			rootfsPartNo = partNo
			rootfsPartUUID = p.Id.String()
		}

		if smallest == 0 || size < smallest {
			smallest = size
			bootPartNo = partNo
		}
	}

	if rootfsPartNo < 0 {
		return fmt.Errorf("cannot find non-EFI partitions on %s", path), 0, "", 0
	}

	// If there’s only one non-EFI partition, bootPartNo may equal rootfsPartNo; that's OK.
	fmt.Printf("Root part: %d, uuid: %s, boot part: %d\n", rootfsPartNo, rootfsPartUUID, bootPartNo)
	return nil, rootfsPartNo, rootfsPartUUID, bootPartNo
}

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
	out, err := Run("lsblk", "-J", "-b", "-o", "NAME,TYPE,SIZE", nbdDev)
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


// FindAndExtractBootArtifacts scans partitions on the NBD device and
// extracts kernel + initrd into destDir/cloud.
func FindAndExtractBootArtifacts(nbdDev, destDir string) (string, string, error) {
	fmt.Printf("listing partitions on %s...\n", nbdDev)

	// Ensure we never stay inside a mountpoint accidentally
	origCwd, _ := os.Getwd()
	defer os.Chdir(origCwd)
	_ = os.Chdir("/") // guarantees no mount is kept busy by current process

	partitions := listDevicePartitions(nbdDev)
	if len(partitions) == 0 {
		partitions = []string{nbdDev}
	}

	for _, dev := range partitions {
		fmt.Printf("mounting %s...\n", dev)
		mnt, err := MountPartition(dev)
		if err != nil {
			fmt.Printf("  -> skipped (mount failed: %v)\n", err)
			continue
		}

		k, i, found := scanPartitionForArtifacts(mnt)
		if found {
			fmt.Println("found boot artifacts:")
			fmt.Printf("  kernel: %s\n", k)
			fmt.Printf("  initrd: %s\n", i)

			// Copy while the filesystem is still mounted
			finalKernel, finalInitrd, err := copyArtifacts(k, i, destDir)

			// Now unmount, and fail hard if that doesn’t work
			if errUmount := Unmount(mnt); errUmount != nil {
				return "", "", fmt.Errorf("failed to unmount %s: %w", mnt, errUmount)
			}
			if err != nil {
				return "", "", err
			}
			return finalKernel, finalInitrd, nil
		}

		// Nothing found here; unmount before trying the next partition
		if err := Unmount(mnt); err != nil {
			return "", "", fmt.Errorf("failed to unmount %s: %w", mnt, err)
		}
		fmt.Printf("  -> nothing found in %s\n", dev)
		fmt.Printf("next partition...\n")
	}

	return "", "", fmt.Errorf("could not find vmlinuz/initrd in any partition")
}

// Same heuristics as before: find vmlinu* and init*
func scanPartitionForArtifacts(mnt string) (string, string, bool) {
	search := []string{
		mnt,
		filepath.Join(mnt, "boot"),
	}

	var kernelPath, initrdPath string

	for _, dir := range search {
		files, err := os.ReadDir(dir)
		if err != nil {
			continue
		}

		for _, f := range files {
			real := filepath.Join(dir, f.Name())
			info, err := f.Info()
			if err != nil {
				continue
			}

			// resolve symlinks
			if info.Mode()&os.ModeSymlink != 0 {
				s, err := filepath.EvalSymlinks(real)
				if err == nil {
					real = s
					info, _ = os.Stat(real)
				}
			}
			if info == nil || !info.Mode().IsRegular() {
				continue
			}

			name := strings.ToLower(f.Name())

			if strings.HasPrefix(name, "vmlinu") && info.Size() > 5*1024*1024 {
				kernelPath = real
			}
			if strings.HasPrefix(name, "init") && info.Size() > 1024*1024 {
				initrdPath = real
			}
		}

		if kernelPath != "" && initrdPath != "" {
			return kernelPath, initrdPath, true
		}
	}
	return "", "", false
}

// Improved version: preserve full filenames + create symlinks kernel/initrd.
func copyArtifacts(srcKernel, srcInitrd, destDir string) (string, string, error) {

	// full filenames from the source partition
	kernelName := filepath.Base(srcKernel)
	initrdName := filepath.Base(srcInitrd)

	fullKernel := filepath.Join(destDir, kernelName)
	fullInitrd := filepath.Join(destDir, initrdName)

	fmt.Printf("extracting vmlinux from %s…\n", kernelName)

	// 1. extract vmlinux (strict extract-vmlinux behavior)
	if err := ExtractRawKernel(srcKernel, fullKernel); err != nil {
		return "", "", fmt.Errorf("failed to extract kernel: %w", err)
	}

	// 2. copy initrd exactly as-is
	if err := CopyFile(srcInitrd, fullInitrd); err != nil {
		return "", "", fmt.Errorf("failed to copy initrd: %w", err)
	}

	// 3. create stable symlinks
	kernelLink := filepath.Join(destDir, "kernel")
	initrdLink := filepath.Join(destDir, "initrd")

	_ = os.Remove(kernelLink)
	_ = os.Remove(initrdLink)

	if err := os.Symlink(kernelName, kernelLink); err != nil {
		return "", "", fmt.Errorf("failed to symlink kernel: %w", err)
	}

	if err := os.Symlink(initrdName, initrdLink); err != nil {
		return "", "", fmt.Errorf("failed to symlink initrd: %w", err)
	}

	// return full names for metadata or debugging
	return kernelName, initrdName, nil
}
