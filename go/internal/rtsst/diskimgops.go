package rtsst

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	sst "github.com/swiss-software-tool/runtime-smart/go/internal/sst"
	mkimagepb "github.com/swiss-software-tool/runtime-smart/go/proto/yaml/mkimage/v1"
)

// ImageSB is the state bag for image operations.
// It is defined here so it can be passed to rtsst functions.
type ImageSB struct {
	Ctx           context.Context
	Log           sst.Logx
	YamlObject    *mkimagepb.ImageYAML
	SstRuntimeDir string
}

// AnalyseImg connects the raw image via NBD, parses partition data using lsblk...
func AnalyseImg(sb *ImageSB) error {
	rawImage := filepath.Join(sb.SstRuntimeDir, "images", sb.YamlObject.Name, sb.YamlObject.RawImage)

	sb.Log.Info(sb.Ctx, "Attaching image to NBD for analysis", "image", rawImage)

	nbdDev, err := attachNBD(sb.Ctx, &sb.Log, rawImage)
	if err != nil {
		return fmt.Errorf("failed to attach nbd device: %w", err)
	}
	// Ensure we detach even if analysis fails
	defer func() {
		if err := detachNBD(sb.Ctx, &sb.Log, nbdDev); err != nil {
			sb.Log.Warn(sb.Ctx, "Failed to detach NBD device", "device", nbdDev, "error", err)
		}
	}()

	sb.Log.Info(sb.Ctx, "Scanning partitions with lsblk", "device", nbdDev)
	res, err := sst.ExecShell(sb.Ctx, &sb.Log, "lsblk", "-J", "-b", "-o", "NAME,SIZE,FSTYPE,PARTUUID,TYPE,MAJ:MIN,UUID", nbdDev)
	if err != nil {
		return fmt.Errorf("lsblk execution failed: %w", err)
	}

	// --- DEBUG INSTRUMENTATION START ---
	sb.Log.Info(sb.Ctx, "lsblk raw output", "json", res.Stdout)
	// --- DEBUG INSTRUMENTATION END ---

	// 3. Parse JSON Output
	type BlockDevice struct {
		Name       string        `json:"name"`
		Size       uint64        `json:"size"`
		FSType     string        `json:"fstype"`
		PartUUID   string        `json:"partuuid"`
		UUID       string        `json:"uuid"`
		MajorMinor string        `json:"maj:min"`
		Type       string        `json:"type"`
		Children   []BlockDevice `json:"children,omitempty"`
	}

	var output struct {
		BlockDevices []BlockDevice `json:"blockdevices"`
	}

	if err := json.Unmarshal([]byte(res.Stdout), &output); err != nil {
		return fmt.Errorf("failed to parse lsblk json: %w", err)
	}

	// 4. Collect Candidates
	type candidate struct {
		Name      string
		Size      uint64
		PartUUID  string
		FsUUID    string
		PartMinor string
		FSType    string
	}
	var candidates []candidate

	var findCandidates func(devs []BlockDevice)
    findCandidates = func(devs []BlockDevice) {
        for _, d := range devs {
            // ... logging ...

            // 1. Check if THIS device is a candidate
            if d.Type == "part" && (d.FSType == "ext4" || d.FSType == "xfs") {
                // Safely parse MajorMinor only when we know we need it
                parts := strings.Split(d.MajorMinor, ":")
                if len(parts) < 2 {
                    sb.Log.Warn(sb.Ctx, "Skipping partition with invalid maj:min", "name", d.Name)
                    continue
                }
                minor := parts[1]

                c := candidate{
                    Name:      d.Name,
                    Size:      d.Size,
                    PartUUID:  d.PartUUID,
                    FsUUID:    d.UUID,
                    PartMinor: minor,
                    FSType:    d.FSType,
                }
                candidates = append(candidates, c)
                sb.Log.Info(sb.Ctx, "Found candidate partition",
                    // ... logging args ...
                )
            }

            // 2. RECURSE UNCONDITIONALLY
            // We must look at children regardless of whether the parent
            // was a candidate (e.g. we must look inside 'disk' to find 'part')
            if len(d.Children) > 0 {
                findCandidates(d.Children)
            }
        }
    }
	
	findCandidates(output.BlockDevices)

	if len(candidates) == 0 {
		// Log the full structure for debugging context before failing
		return fmt.Errorf("heuristic failed: no ext4 or xfs partitions found in %s", rawImage)
	}

	// 5. Apply Heuristics
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Size > candidates[j].Size
	})

	rootPart := candidates[0]
	sb.YamlObject.RootfsPartUuid = rootPart.PartUUID
	sb.YamlObject.RootfsUuid = rootPart.FsUUID
	sb.YamlObject.RootfsDevice = "/dev/vda" + rootPart.PartMinor
	sb.YamlObject.RootfsType = rootPart.FSType

	sb.Log.Info(sb.Ctx, "Identified Rootfs (Largest)",
		"part_uuid", sb.YamlObject.RootfsPartUuid,
		"fs_uuid", sb.YamlObject.RootfsUuid,
		"rootfs_device", "/dev/vda"+sb.YamlObject.RootfsDevice,
		"fstype", sb.YamlObject.RootfsType,
		"size_bytes", rootPart.Size,
	)

	if len(candidates) > 1 {
		bootPart := candidates[len(candidates)-1]
		sb.YamlObject.BootfsPartUuid = bootPart.PartUUID
		sb.YamlObject.BootfsType = bootPart.FSType
		sb.YamlObject.BootfsDevice = "/dev/vda" + bootPart.PartMinor
		sb.YamlObject.BootfsUuid = bootPart.FsUUID
		sb.Log.Info(sb.Ctx, "Identified Bootfs (Smallest)",
			"part_uuid", sb.YamlObject.BootfsPartUuid,
			"fs_uuid", sb.YamlObject.BootfsUuid,
			"bootfs_device", sb.YamlObject.BootfsDevice,
			"fstype", sb.YamlObject.BootfsType,
			"size_bytes", bootPart.Size,
		)
	} else {
		// FIX: Explicitly map Bootfs to Rootfs when single partition
		sb.YamlObject.BootfsPartUuid = rootPart.PartUUID
		sb.YamlObject.BootfsType = rootPart.FSType
		sb.YamlObject.BootfsUuid = rootPart.FsUUID
		sb.YamlObject.BootfsDevice = "/dev/vda" + rootPart.PartMinor
		sb.Log.Info(sb.Ctx, "Only one Linux partition found; assuming /boot is part of rootfs",
			"bootfs_uuid", sb.YamlObject.BootfsPartUuid)
	}

	return nil
}

// ExtractKernelAndInitrdFromBootPartition extracts boot artifacts.
func ExtractKernelAndInitrdFromBootPartition(sb *ImageSB) error {
	// Reconstruct the full path to the raw image file
	rawImage := filepath.Join(sb.SstRuntimeDir, "images", sb.YamlObject.Name, sb.YamlObject.RawImage)
	bootfsPartUuid := sb.YamlObject.BootfsPartUuid

	if bootfsPartUuid == "" {
		return fmt.Errorf("bootfs_part_uuid is empty; run AnalyseImg first")
	}

	sb.Log.Info(sb.Ctx, "kernel.extract.start",
		"image", rawImage,
		"bootfs_part_uuid", bootfsPartUuid,
	)

	// 1. Attach NBD
	nbdDev, err := attachNBD(sb.Ctx, &sb.Log, rawImage)
	if err != nil {
		sb.Log.Diagnose(sb.Ctx, "NBD_ATTACH_FAILED", "failed to attach disk image",
			"image", rawImage,
			"error", err.Error(),
		)
		return err
	}
	// Always detach NBD at end of function
	defer func() {
		if err := detachNBD(sb.Ctx, &sb.Log, nbdDev); err != nil {
			sb.Log.Warn(sb.Ctx, "Failed to detach NBD device", "device", nbdDev, "error", err)
		}
	}()

	// 2. Resolve Partition Device
	partDev, err := resolveBootPartitionDevice(sb.YamlObject)
	if err != nil {
		return err
	}

	if _, err := os.Stat(partDev); err != nil {
		return fmt.Errorf("boot partition device not found: %s", partDev)
	}

	// 3. Mount
	mnt, err := mountPartition(sb.Ctx, &sb.Log, partDev)
	if err != nil {
		return err
	}
	defer func() { _ = unmountPartition(sb.Ctx, &sb.Log, mnt) }()

	// 4. Scan & Copy
	kernelPath, initrdPath, ok := scanPartitionForArtifacts(mnt)
	if !ok {
		return fmt.Errorf("kernel/initrd not found on boot partition %s", partDev)
	}

	// Destination: ~/.runtimes/images/<name>/
	destDir := filepath.Join(sb.SstRuntimeDir, "images", sb.YamlObject.Name)
	kernelName, initrdName, err := copyArtifacts(kernelPath, initrdPath, destDir)
	if err != nil {
		return err
	}

	sb.Log.Info(sb.Ctx, "kernel.extract.done",
		"kernel", kernelName,
		"initrd", initrdName,
	)

	// Update the SB object
	sb.YamlObject.Kernel = kernelName
	sb.YamlObject.Initrd = initrdName

	return nil
}

// resolveBootPartitionDevice determines the device path.
// It prioritizes the PartUUID found during analysis.
func resolveBootPartitionDevice(img *mkimagepb.ImageYAML) (string, error) {
	if img.BootfsPartUuid != "" {
		// udev usually creates these links; attachNBD calls `udevadm settle` to ensure they exist.
		return "/dev/disk/by-partuuid/" + img.BootfsPartUuid, nil
	}
	return "", fmt.Errorf("bootfs info missing")
}

// ============================================================================
// Low-level helpers (updated to accept *sst.Logx)
// ============================================================================
func attachNBD(ctx context.Context, log *sst.Logx, image string) (string, error) {
	// Dereference *log for ExecShell compatibility
	_, _ = sst.ExecShell(ctx, log, "modprobe", "nbd", "max_part=16")
	_, _ = sst.ExecShell(ctx, log, "qemu-nbd", "-d", "/dev/nbd0")

	// Connect to /dev/nbd0
	if _, err := sst.ExecShell(ctx, log,
		"qemu-nbd", "-f", "raw", "--connect=/dev/nbd0", image); err != nil {
		return "", err
	}

	// Force kernel to re-read partition table.
	// We use blockdev because it is standard in util-linux.
	_, _ = sst.ExecShell(ctx, log, "blockdev", "--rereadpt", "/dev/nbd0")

	// POLLING LOOP: Wait up to 5s for partitions to appear.
	// This solves the race condition where lsblk runs before
	// the kernel/udev has populated the partition nodes.
	dev := "/dev/nbd0"
	partitionDetected := false

	for i := 0; i < 20; i++ {
		// Check for p1 (standard) or p15 (common on cloud images like Ubuntu)
		if _, err := os.Stat(dev + "p1"); err == nil {
			partitionDetected = true
			break
		}
		if _, err := os.Stat(dev + "p15"); err == nil {
			partitionDetected = true
			break
		}

		// Fallback: Check sysfs (sometimes updates faster than /dev)
		if _, err := os.Stat("/sys/block/nbd0/nbd0p1"); err == nil {
			partitionDetected = true
			break
		}

		// Wait 250ms
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(250 * time.Millisecond):
			// retry
		}
	}

	if !partitionDetected {
		// Warn but don't error; let the caller fail at lsblk if it really is empty.
		log.Warn(ctx, "attachNBD: timed out waiting for partitions to appear", "device", dev)
	}

	// Final settle to ensure nodes are stable for mount
	_, _ = sst.ExecShell(ctx, log, "udevadm", "settle")
	return dev, nil
}

func detachNBD(ctx context.Context, log *sst.Logx, dev string) error {
	_, err := sst.ExecShell(ctx, log, "qemu-nbd", "-d", dev)
	return err
}

func mountPartition(ctx context.Context, log *sst.Logx, dev string) (string, error) {
	dir, err := os.MkdirTemp("", "mkimage-boot-*")
	if err != nil {
		return "", err
	}

	if _, err := sst.ExecShell(ctx, log,
		"mount", "-o", "ro", dev, dir); err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}

	return dir, nil
}

func unmountPartition(ctx context.Context, log *sst.Logx, dir string) error {
	_, err := sst.ExecShell(ctx, log, "umount", dir)
	_ = os.RemoveAll(dir)
	return err
}

// go/internal/rtsst/diskimgops.go

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

			name := strings.ToLower(f.Name()) // case-insensitive check

			// KERNEL HEURISTICS
			// 1. vmlinu* (standard Linux)
			// 2. image (ARM64 standard)
			// 3. kernel (generic)
			isKernelName := strings.HasPrefix(name, "vmlinu") ||
				strings.HasPrefix(name, "image") ||
				strings.HasPrefix(name, "kernel")

			if isKernelName && info.Size() > 1024*1024 {
				kernelPath = real
			}

			// INITRD HEURISTICS
			// 1. initrd*
			// 2. initramfs*
			isInitrdName := strings.HasPrefix(name, "initrd") ||
				strings.HasPrefix(name, "initramfs")

			if isInitrdName && info.Size() > 1024*1024 {
				initrdPath = real
			}
		}

		// Stop if we found both in this directory
		if kernelPath != "" && initrdPath != "" {
			return kernelPath, initrdPath, true
		}
	}
	return "", "", false
}
func copyArtifacts(srcKernel, srcInitrd, destDir string) (string, string, error) {
	kernelName := filepath.Base(srcKernel)
	initrdName := filepath.Base(srcInitrd)

	fullKernel := filepath.Join(destDir, kernelName)
	fullInitrd := filepath.Join(destDir, initrdName)

	if err := ExtractRawKernel(srcKernel, fullKernel); err != nil {
		return "", "", fmt.Errorf("failed to extract kernel: %w", err)
	}
	if err := CopyFile(srcInitrd, fullInitrd); err != nil {
		return "", "", fmt.Errorf("failed to copy initrd: %w", err)
	}

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

	return kernelName, initrdName, nil
}

// IsQcow2 checks if the file at path is a QCOW2 image by reading the first 4 magic bytes.
// This replaces unreliable file extension checks.
func IsQcow2(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()

	// The QCOW2 magic string is "QFI\xfb"
	// Reference: https://gitlab.com/qemu-project/qemu/-/blob/master/docs/interop/qcow2.txt
	magic := []byte{'Q', 'F', 'I', 0xfb}
	buf := make([]byte, 4)

	if _, err := io.ReadFull(f, buf); err != nil {
		// If file is smaller than 4 bytes, it's definitely not QCOW2
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return false, nil
		}
		return false, err
	}

	return bytes.Equal(buf, magic), nil
}
