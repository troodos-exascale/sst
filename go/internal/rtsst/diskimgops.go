package rtsst

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

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

// AnalyseImg connects the raw image via NBD, parses partition data using lsblk,
// and applies heuristics to identify Rootfs (largest) and Bootfs (smallest).
// It populates the PartUUID and Type fields in the provided ImageSB.YamlObject.
//
// NOTE: sb must be passed by pointer (*ImageSB) because sb.Log contains a sync.Mutex.
func AnalyseImg(sb *ImageSB) error {
	rawImage := filepath.Join(sb.SstRuntimeDir, "images", sb.YamlObject.Name, sb.YamlObject.RawImage)
	// 1. Connect Image via NBD
	sb.Log.Info(sb.Ctx, "Attaching image to NBD for analysis", "image", rawImage)

	// Pass address of sb.Log
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

	// 2. Inspect with lsblk
	// We use -J for JSON output, -b for bytes, -o to get specific columns.
	// We scan the specific NBD device to avoid noise.
	sb.Log.Info(sb.Ctx, "Scanning partitions with lsblk", "device", nbdDev)
	// Pass &sb.Log directly to ExecShell
	res, err := sst.ExecShell(sb.Ctx, &sb.Log, "lsblk", "-J", "-b", "-o", "NAME,SIZE,FSTYPE,PARTUUID,TYPE", nbdDev)
	if err != nil {
		return fmt.Errorf("lsblk execution failed: %w", err)
	}

	// 3. Parse JSON Output
	// lsblk JSON output is nested (children) for partitions.
	type BlockDevice struct {
		Name     string        `json:"name"`
		Size     uint64        `json:"size"`
		FSType   string        `json:"fstype"`
		PartUUID string        `json:"partuuid"`
		Type     string        `json:"type"`
		Children []BlockDevice `json:"children,omitempty"`
	}

	var output struct {
		BlockDevices []BlockDevice `json:"blockdevices"`
	}

	if err := json.Unmarshal([]byte(res.Stdout), &output); err != nil {
		return fmt.Errorf("failed to parse lsblk json: %w", err)
	}

	// 4. Collect Candidates
	type candidate struct {
		Name     string
		Size     uint64
		FSType   string
		PartUUID string
	}
	var candidates []candidate

	// Recursive helper to find partitions in the nested JSON
	var findCandidates func(devs []BlockDevice)
	findCandidates = func(devs []BlockDevice) {
		for _, d := range devs {
			// Check for Linux filesystems (ext4 or xfs) on partitions
			if d.Type == "part" && (d.FSType == "ext4" || d.FSType == "xfs") {
				c := candidate{
					Name:     d.Name,
					Size:     d.Size,
					FSType:   d.FSType,
					PartUUID: d.PartUUID,
				}
				candidates = append(candidates, c)
				sb.Log.Info(sb.Ctx, "Found candidate partition",
					"name", c.Name,
					"fstype", c.FSType,
					"size_bytes", c.Size,
					"uuid", c.PartUUID,
				)
			}
			// Recurse into children
			if len(d.Children) > 0 {
				findCandidates(d.Children)
			}
		}
	}

	findCandidates(output.BlockDevices)

	if len(candidates) == 0 {
		return fmt.Errorf("heuristic failed: no ext4 or xfs partitions found in %s", rawImage)
	}

	// 5. Apply Heuristics
	// Sort by size descending (Largest first)
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Size > candidates[j].Size
	})

	// Heuristic 1: Largest Linux FS is Root
	rootPart := candidates[0]
	sb.YamlObject.RootfsPartUuid = rootPart.PartUUID
	sb.YamlObject.RootfsType = rootPart.FSType

	sb.Log.Info(sb.Ctx, "Identified Rootfs (Largest)",
		"uuid", sb.YamlObject.RootfsPartUuid,
		"fstype", sb.YamlObject.RootfsType,
		"size_bytes", rootPart.Size,
	)

	// Heuristic 2: Smallest Linux FS is Boot (if valid candidates exist and > 1)
	if len(candidates) > 1 {
		// Since we sorted descending, the last element is the smallest
		bootPart := candidates[len(candidates)-1]
		sb.YamlObject.BootfsPartUuid = bootPart.PartUUID
		sb.YamlObject.BootfsType = bootPart.FSType

		sb.Log.Info(sb.Ctx, "Identified Bootfs (Smallest)",
			"uuid", sb.YamlObject.BootfsPartUuid,
			"fstype", sb.YamlObject.BootfsType,
			"size_bytes", bootPart.Size,
		)
	} else {
		sb.Log.Info(sb.Ctx, "Only one Linux partition found; assuming /boot is part of rootfs")
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

	if _, err := sst.ExecShell(ctx, log,
		"qemu-nbd", "--connect=/dev/nbd0", image); err != nil {
		return "", err
	}

	_, _ = sst.ExecShell(ctx, log, "udevadm", "settle")
	return "/dev/nbd0", nil
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
