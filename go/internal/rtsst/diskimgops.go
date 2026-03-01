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
	"strconv"

	sst "github.com/swiss-software-tool/runtime-smart/go/internal/sst"
	mkimagepb "github.com/swiss-software-tool/runtime-smart/go/proto/yaml/mkimage/v1"
)

// ImageSB is the state bag for image operations.
type ImageSB struct {
	Ctx           context.Context
	Log           *sst.Logx
	YamlObject    *mkimagepb.ImageYAML
	SstRuntimeDir string
}

// AnalyseImg connects the raw image via NBD, parses partition data using lsblk...
func AnalyseImg(sb *ImageSB) error {
	rawImage := filepath.Join(sb.SstRuntimeDir, "images", sb.YamlObject.Name, sb.YamlObject.RawImage)

	sb.Log.Info(sb.Ctx, "Attaching image to NBD for analysis", "image", rawImage)

	nbdDev, err := attachNBD(sb.Ctx, sb.Log, rawImage)
	if err != nil {
		return fmt.Errorf("failed to attach nbd device: %w", err)
	}
	// Ensure we detach even if analysis fails
	defer func() {
		if err := detachNBD(sb.Ctx, sb.Log, nbdDev); err != nil {
			sb.Log.Warn(sb.Ctx, "Failed to detach NBD device", "device", nbdDev, "error", err)
		}
	}()

	sb.Log.Info(sb.Ctx, "Scanning partitions with lsblk", "device", nbdDev)
	res, err := sst.ExecShell(sb.Ctx, sb.Log, "lsblk", "-J", "-b", "-o", "NAME,START,SIZE,FSTYPE,PARTUUID,TYPE,MAJ:MIN,UUID", nbdDev)
	if err != nil {
		return fmt.Errorf("lsblk execution failed: %w", err)
	}

	sb.Log.Info(sb.Ctx, "lsblk raw output", "json", res.Stdout)

	type BlockDevice struct {
		Name       string        `json:"name"`
		Start      uint64        `json:"start"`	
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

	type candidate struct {
		Name      string
		Start     uint64
		Size      uint64
		PartUUID  string
		FsUUID    string
		PartMinor string
		FSType    string
	}
	var candidates []candidate
    var maxStart uint64
	var findCandidates func(devs []BlockDevice)
	maxStart = 0

    findCandidates = func(devs []BlockDevice) {
        for _, d := range devs {
			// FIX: Accept both "part" (partitioned) and "disk" (partitionless) devices
            if (d.Type == "part" || d.Type == "disk") && (d.FSType == "ext4" || d.FSType == "ext2" || d.FSType == "xfs") {
                parts := strings.Split(d.MajorMinor, ":")
                if len(parts) < 2 {
                    sb.Log.Warn(sb.Ctx, "Skipping partition with invalid maj:min", "name", d.Name)
                    continue
                }
                minor := parts[1]
				
				// For partitionless disks (like our builder outputs), there is no partition UUID.
				// We fall back to the filesystem UUID so kernel extraction has a stable identifier to mount.
				pUuid := d.PartUUID
				if pUuid == "" {
					pUuid = d.UUID
				}

                c := candidate{
                    Name:      d.Name,
                    Start:     d.Start,
                    Size:      d.Size,
                    PartUUID:  pUuid,
                    FsUUID:    d.UUID,
                    PartMinor: minor,
                    FSType:    d.FSType,
                }
				if c.Start > maxStart {
					maxStart = c.Start
				}
                candidates = append(candidates, c)
                sb.Log.Info(sb.Ctx, "Found candidate partition", "name", c.Name, "fstype", c.FSType)
            }
            if len(d.Children) > 0 {
                findCandidates(d.Children)
            }
        }
    }
	
	findCandidates(output.BlockDevices)

	if len(candidates) == 0 {
		return fmt.Errorf("heuristic failed: no ext4 or xfs partitions found in %s", rawImage)
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Size > candidates[j].Size
	})

	rootPart := candidates[0]
	sb.YamlObject.RootfsPartUuid = rootPart.PartUUID
	sb.YamlObject.RootfsUuid = rootPart.FsUUID
	sb.YamlObject.RootfsDevice = "/dev/vda" + rootPart.PartMinor
	sb.YamlObject.RootfsType = rootPart.FSType
	sb.YamlObject.RootfsSize = fmt.Sprintf("%d%s", rootPart.Size, "B")
	if rootPart.Start == maxStart {
		sb.Log.Info(sb.Ctx, "Rootfs is the largest partition", "start", rootPart.Start)
		sb.YamlObject.RootfsIsLast = true
	} else {
		sb.Log.Info(sb.Ctx, "Rootfs is not the largest partition", "start", rootPart.Start)
		sb.YamlObject.RootfsIsLast = false
	}
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
		sb.YamlObject.BootfsPartUuid = rootPart.PartUUID
		sb.YamlObject.BootfsType = rootPart.FSType
		sb.YamlObject.BootfsUuid = rootPart.FsUUID
		sb.YamlObject.BootfsDevice = "/dev/vda" + rootPart.PartMinor
		sb.Log.Info(sb.Ctx, "Only one Linux partition found; assuming /boot is part of rootfs",
			"bootfs_uuid", sb.YamlObject.BootfsPartUuid)
	}

	return nil
}

func ExtractKernelAndInitrdFromBootPartition(sb *ImageSB) error {
	rawImage := filepath.Join(sb.SstRuntimeDir, "images", sb.YamlObject.Name, sb.YamlObject.RawImage)
	bootfsPartUuid := sb.YamlObject.BootfsPartUuid

	if bootfsPartUuid == "" {
		return fmt.Errorf("bootfs_part_uuid is empty; run AnalyseImg first")
	}

	sb.Log.Info(sb.Ctx, "kernel.extract.start",
		"image", rawImage,
		"bootfs_part_uuid", bootfsPartUuid,
	)

	nbdDev, err := attachNBD(sb.Ctx, sb.Log, rawImage)
	if err != nil {
		sb.Log.Diagnose(sb.Ctx, "NBD_ATTACH_FAILED", "failed to attach disk image",
			"image", rawImage,
			"error", err.Error(),
		)
		return err
	}
	defer func() {
		if err := detachNBD(sb.Ctx, sb.Log, nbdDev); err != nil {
			sb.Log.Warn(sb.Ctx, "Failed to detach NBD device", "device", nbdDev, "error", err)
		}
	}()

	partDev, err := resolveBootPartitionDevice(sb.YamlObject, nbdDev)
	if err != nil {
		return err
	}

	if _, err := os.Stat(partDev); err != nil {
		return fmt.Errorf("boot partition device not found: %s", partDev)
	}

	mnt, err := mountPartition(sb.Ctx, sb.Log, partDev)
	if err != nil {
		return err
	}
	defer func() { _ = unmountPartition(sb.Ctx, sb.Log, mnt) }()

	kernelPath, initrdPath, ok := scanPartitionForArtifacts(mnt)
	if !ok {
		return fmt.Errorf("kernel/initrd not found on boot partition %s", partDev)
	}

	destDir := filepath.Join(sb.SstRuntimeDir, "images", sb.YamlObject.Name)
	kernelName, initrdName, err := copyArtifacts(kernelPath, initrdPath, destDir)
	if err != nil {
		return err
	}

	sb.Log.Info(sb.Ctx, "kernel.extract.done",
		"kernel", kernelName,
		"initrd", initrdName,
	)

	sb.YamlObject.Kernel = kernelName
	sb.YamlObject.Initrd = initrdName

	return nil
}

// resolveBootPartitionDevice attempts to resolve the partition device.
// For partitionless block devices, it returns the raw NBD device itself.
func resolveBootPartitionDevice(img *mkimagepb.ImageYAML, nbdDev string) (string, error) {
	if img.BootfsPartUuid != "" {
		// If it's a partitionless disk, the BootfsPartUuid was set to the filesystem UUID.
		// In an NBD context, udev rules for UUID mapping are notoriously flaky and slow. 
		// Since we only have one partition right now anyway, if it matches the fs_uuid, just return the raw dev.
		if img.BootfsPartUuid == img.BootfsUuid {
			return nbdDev, nil
		}
		return "/dev/disk/by-partuuid/" + img.BootfsPartUuid, nil
	}
	return "", fmt.Errorf("bootfs info missing")
}

func ParseMemorySizeMiB(sizeStr, suffix string) (uint64, error) {
	if sizeStr == "" {
		return 0, fmt.Errorf("memory size is empty")
	}

	size, err := strconv.ParseUint(sizeStr, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid memory size %q: %w", sizeStr, err)
	}

	switch strings.ToLower(suffix) {
	case "", "mib":
		return size, nil
	case "mb":
		return size * 1000 / 1024, nil
	case "gib":
		return size * 1024, nil
	case "gb":
		return size * 1000 * 1000 / 1024 / 1024, nil
	case "kib":
		return size / 1024, nil
	case "kb":
		return size * 1000 / 1024 / 1024, nil
	default:
		return 0, fmt.Errorf("unknown memory size suffix %q", suffix)
	}
}

func attachNBD(ctx context.Context, log *sst.Logx, image string) (string, error) {
	_, _ = sst.ExecShell(ctx, log, "modprobe", "nbd", "max_part=16")
	_, _ = sst.ExecShell(ctx, log, "qemu-nbd", "-d", "/dev/nbd0")

	if _, err := sst.ExecShell(ctx, log,
		"qemu-nbd", "-f", "raw", "--connect=/dev/nbd0", image); err != nil {
		return "", err
	}

	_, _ = sst.ExecShell(ctx, log, "blockdev", "--rereadpt", "/dev/nbd0")

	dev := "/dev/nbd0"
	partitionDetected := false

	for i := 0; i < 20; i++ {
		// Also accept the raw device itself if it has a filesystem (partitionless)
		if out, err := sst.ExecShell(ctx, log, "blkid", dev); err == nil && len(out.Stdout) > 0 {
			partitionDetected = true
			break
		}
		if _, err := os.Stat(dev + "p1"); err == nil {
			partitionDetected = true
			break
		}
		if _, err := os.Stat(dev + "p15"); err == nil {
			partitionDetected = true
			break
		}
		if _, err := os.Stat("/sys/block/nbd0/nbd0p1"); err == nil {
			partitionDetected = true
			break
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}

	if !partitionDetected {
		log.Warn(ctx, "attachNBD: timed out waiting for partitions to appear", "device", dev)
	}

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

			isKernelName := strings.HasPrefix(name, "vmlinu") ||
				strings.HasPrefix(name, "image") ||
				strings.HasPrefix(name, "kernel")

			if isKernelName && info.Size() > 1024*1024 {
				kernelPath = real
			}

			isInitrdName := strings.HasPrefix(name, "initrd") ||
				strings.HasPrefix(name, "initramfs")

			if isInitrdName && info.Size() > 1024*1024 {
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

func IsQcow2(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()

	magic := []byte{'Q', 'F', 'I', 0xfb}
	buf := make([]byte, 4)

	if _, err := io.ReadFull(f, buf); err != nil {
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return false, nil
		}
		return false, err
	}

	return bytes.Equal(buf, magic), nil
}
