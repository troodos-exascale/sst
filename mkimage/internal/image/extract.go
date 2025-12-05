package image

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"mkimage/internal/util"
)

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
	if err := util.CopyFile(srcInitrd, fullInitrd); err != nil {
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
