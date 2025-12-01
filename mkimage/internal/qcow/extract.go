package qcow

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"mkimage/internal/kernel"
	"mkimage/internal/util"
)

// FindAndExtractBootArtifacts iterates over partitions on the NBD device,
// looks for kernel and initrd, and extracts them to the destination.
func FindAndExtractBootArtifacts(nbdDev, destDir string) (string, string, error) {
	fmt.Printf("listing partitions on %s...\n", nbdDev)
	
	// Get potential partitions (p1..p31)
	partitions := listDevicePartitions(nbdDev)
	if len(partitions) == 0 {
		// Fallback for non-partitioned images
		partitions = []string{nbdDev}
	}

	for _, dev := range partitions {
		fmt.Printf("mounting %s...\n", dev)
		mnt, err := MountPartition(dev)
		if err != nil {
			// This is expected for swap, unknown FS, etc.
			fmt.Printf("  -> skipped (mount failed: %v)\n", err)
			continue 
		}

		// Search in this partition
		k, i, found := scanPartitionForArtifacts(mnt)
		if found {
			fmt.Println("found boot artifacts:")
			fmt.Printf("  kernel: %s\n", k)
			fmt.Printf("  initrd: %s\n", i)

			// Copy files out
			finalKernel, finalInitrd, err := copyArtifacts(k, i, destDir)
			
			// Unmount immediately after copying
			_ = Unmount(mnt)
			
			if err != nil {
				return "", "", err
			}
			return finalKernel, finalInitrd, nil
		}

		fmt.Printf("  -> nothing found in %s\n", dev)
		_ = Unmount(mnt)
		fmt.Printf("next partition...\n")
	}

	return "", "", fmt.Errorf("could not find vmlinu* and init* in any partition")
}

func scanPartitionForArtifacts(mnt string) (string, string, bool) {
	// Places to look: / and /boot
	searchPaths := []string{
		mnt,                      // Root of partition
		filepath.Join(mnt, "boot"), // /boot subdirectory
	}

	var kernelPath, initrdPath string

	for _, dir := range searchPaths {
		fmt.Printf("looking for regular file vmlinu* in %s\n", dir)
		
		files, err := os.ReadDir(dir)
		if err != nil {
			continue
		}

		for _, f := range files {
			// Resolve symlinks to ensure we have a real file and real size
			info, err := f.Info()
			if err != nil {
				continue
			}
			
			realPath := filepath.Join(dir, f.Name())

			// If symlink, resolve it
			if info.Mode()&os.ModeSymlink != 0 {
				resolved, err := filepath.EvalSymlinks(realPath)
				if err == nil {
					info, err = os.Stat(resolved)
					if err != nil { continue }
					realPath = resolved
				}
			}

			if !info.Mode().IsRegular() {
				continue
			}
			
			// Heuristics based on user input
			name := strings.ToLower(f.Name())
			
			// Kernel: starts with vmlinu, size > 5MB (avoid tiny shims)
			if strings.HasPrefix(name, "vmlinu") && info.Size() > 5*1024*1024 {
				if kernelPath == "" || info.Size() > 0 { // pick first valid
					kernelPath = realPath
				}
			}

			// Initrd: starts with init, size > 1MB
			if strings.HasPrefix(name, "init") && info.Size() > 1024*1024 {
				if initrdPath == "" {
					initrdPath = realPath
				}
			}
		}

		if kernelPath != "" && initrdPath != "" {
			return kernelPath, initrdPath, true
		}
	}

	return "", "", false
}

func copyArtifacts(srcKernel, srcInitrd, destDir string) (string, string, error) {
	dstKernel := filepath.Join(destDir, "kernel")
	dstInitrd := filepath.Join(destDir, "initrd")

	// Copy Kernel
	if err := util.CopyFile(srcKernel, dstKernel); err != nil {
		return "", "", fmt.Errorf("failed to copy kernel: %w", err)
	}

	// Check compression
	fmt.Printf("checking kernel compression...\n")
	out, _ := util.Run("file", dstKernel)
	lower := strings.ToLower(out)

	if strings.Contains(lower, "gzip compressed data") {
		fmt.Printf("decompressing kernel...\n")
		rawPath := filepath.Join(destDir, "kernel.raw")
		if err := kernel.ExtractRawKernel(dstKernel, rawPath); err != nil {
			return "", "", fmt.Errorf("failed to decompress kernel: %w", err)
		}
		// Move raw to kernel, remove compressed
		_ = os.Remove(dstKernel)
		_ = os.Rename(rawPath, dstKernel)
	} else {
		fmt.Printf("kernel already decompressed or unknown format\n")
	}

	// Copy Initrd
	if err := util.CopyFile(srcInitrd, dstInitrd); err != nil {
		return "", "", fmt.Errorf("failed to copy initrd: %w", err)
	}

	return "kernel", "initrd", nil
}
