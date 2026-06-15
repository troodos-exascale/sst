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
	"strconv"
	"strings"

	sst "github.com/troodos-exascale/sst/internal/sst"
	mkimagepb "github.com/troodos-exascale/sst/proto/yaml/mkimage/v1"
)

// findPartitionByTag securely scopes a UUID/PARTUUID search to the children of a specific loop device,
// bypassing global blkid queries to avoid collisions with the host VM's own identical partitions.
func findPartitionByTag(ctx context.Context, log *sst.Logx, baseDev, tagKey, tagValue string) (string, error) {
	res, err := sst.ExecShell(ctx, log, "lsblk", "-J", "-o", "NAME,TYPE", baseDev)
	if err != nil {
		return "", fmt.Errorf("lsblk failed: %w", err)
	}

	type blockDev struct {
		Name     string     `json:"name"`
		Type     string     `json:"type"`
		Children []blockDev `json:"children,omitempty"`
	}
	var output struct {
		BlockDevices []blockDev `json:"blockdevices"`
	}

	if err := json.Unmarshal([]byte(res.Stdout), &output); err != nil {
		return "", fmt.Errorf("failed to parse lsblk output: %w", err)
	}

	var devs []string
	var collect func(children []blockDev)
	collect = func(children []blockDev) {
		for _, c := range children {
			if c.Type == "part" || c.Type == "loop" || c.Type == "disk" {
				devs = append(devs, "/dev/"+c.Name)
			}
			collect(c.Children)
		}
	}
	collect(output.BlockDevices)

	// Explicitly probe each child device directly to bypass udev symlink latency
	for _, dev := range devs {
		blkidRes, _ := sst.ExecShell(ctx, log, "blkid", "-o", "export", dev)
		for _, line := range strings.Split(blkidRes.Stdout, "\n") {
			parts := strings.SplitN(line, "=", 2)
			if len(parts) == 2 && strings.EqualFold(parts[0], tagKey) {
				if strings.EqualFold(parts[1], tagValue) {
					return dev, nil
				}
			}
		}
	}

	return "", fmt.Errorf("no partition found with %s=%s on %s", tagKey, tagValue, baseDev)
}

func resolvePartitionByUUID(ctx context.Context, log *sst.Logx, baseDev string, targetUUID string) (string, error) {
	return findPartitionByTag(ctx, log, baseDev, "UUID", targetUUID)
}

type ImageSB struct {
	Ctx           context.Context
	Log           *sst.Logx
	YamlObject    *mkimagepb.ImageYAML
	SstRuntimeDir string
}

// vdaName safely translates a loop device name to a Virtio block device name.
// Examples: "loop0" -> "/dev/vda", "loop0p1" -> "/dev/vda1", "loop2p5" -> "/dev/vda5"
func vdaName(devName string) string {
	devName = strings.TrimPrefix(devName, "/dev/")
	
	// "loop" has a 'p' at index 3. We strictly want the last 'p' if it denotes a partition.
	lastP := strings.LastIndex(devName, "p")
	if lastP > 3 {
		return "/dev/vda" + devName[lastP+1:]
	}
	return "/dev/vda"
}

func AnalyseImg(sb *ImageSB) error {
	rawImage := filepath.Join(sb.SstRuntimeDir, "images", sb.YamlObject.Provenance.Name, sb.YamlObject.Storage.RawImage)

	sb.Log.Info(sb.Ctx, "Attaching image to loop device for analysis", "image", rawImage)

	loopDev, err := attachLoop(sb.Ctx, sb.Log, rawImage)
	if err != nil {
		return fmt.Errorf("failed to attach loop device: %w", err)
	}
	defer func() {
		if err := detachLoop(sb.Ctx, sb.Log, loopDev); err != nil {
			sb.Log.Warn(sb.Ctx, "Failed to detach loop device", "device", loopDev, "error", err)
		}
	}()

	sb.Log.Info(sb.Ctx, "Scanning partitions with lsblk", "device", loopDev)
	res, err := sst.ExecShell(sb.Ctx, sb.Log, "lsblk", "-J", "-b", "-o", "NAME,START,SIZE,FSTYPE,PARTUUID,TYPE,MAJ:MIN,UUID", loopDev)
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
		Name     string
		Start    uint64
		Size     uint64
		PartUUID string
		FsUUID   string
		FSType   string
	}
	var candidates []candidate
	var maxStart uint64
	var findCandidates func(devs []BlockDevice)
	maxStart = 0

	findCandidates = func(devs []BlockDevice) {
		for _, d := range devs {
			// TYPE "loop" captures unpartitioned raw filesystems (like Ubuntu .img)
			if d.Type == "part" || d.Type == "disk" || d.Type == "loop" {
				// Force blkid to read filesystem metadata directly, bypassing udev symlinks!
				devPath := "/dev/" + d.Name
				blkidRes, _ := sst.ExecShell(sb.Ctx, sb.Log, "blkid", "-o", "export", devPath)

				var fstype, uuid, partuuid string
				for _, line := range strings.Split(blkidRes.Stdout, "\n") {
					parts := strings.SplitN(line, "=", 2)
					if len(parts) == 2 {
						switch parts[0] {
						case "TYPE":
							fstype = parts[1]
						case "UUID":
							uuid = parts[1]
						case "PARTUUID":
							partuuid = parts[1]
						}
					}
				}

				if fstype == "ext4" || fstype == "ext2" || fstype == "xfs" {
					pUuid := partuuid
					if pUuid == "" {
						pUuid = uuid
					}

					c := candidate{
						Name:     d.Name,
						Start:    d.Start,
						Size:     d.Size,
						PartUUID: pUuid,
						FsUUID:   uuid,
						FSType:   fstype,
					}
					if c.Start > maxStart {
						maxStart = c.Start
					}
					candidates = append(candidates, c)
					sb.Log.Info(sb.Ctx, "Found candidate partition", "name", c.Name, "fstype", c.FSType)
				}
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
	sb.YamlObject.Storage.RootfsPartUuid = rootPart.PartUUID
	sb.YamlObject.Storage.RootfsUuid = rootPart.FsUUID
	sb.YamlObject.Storage.RootfsDevice = vdaName(rootPart.Name)
	sb.YamlObject.Storage.RootfsType = rootPart.FSType
	sb.YamlObject.Storage.RootfsSize = fmt.Sprintf("%d%s", rootPart.Size, "B")

	if rootPart.Start == maxStart {
		sb.Log.Info(sb.Ctx, "Rootfs is the largest partition", "start", rootPart.Start)
		sb.YamlObject.Storage.RootfsIsLast = true
	} else {
		sb.Log.Info(sb.Ctx, "Rootfs is not the largest partition", "start", rootPart.Start)
		sb.YamlObject.Storage.RootfsIsLast = false
	}
	sb.Log.Info(sb.Ctx, "Identified Rootfs (Largest)",
		"part_uuid", sb.YamlObject.Storage.RootfsPartUuid,
		"fs_uuid", sb.YamlObject.Storage.RootfsUuid,
		"rootfs_device", sb.YamlObject.Storage.RootfsDevice,
		"fstype", sb.YamlObject.Storage.RootfsType,
		"size_bytes", rootPart.Size,
	)

	if len(candidates) > 1 {
		bootPart := candidates[len(candidates)-1]
		sb.YamlObject.Boot.BootfsPartUuid = bootPart.PartUUID
		sb.YamlObject.Boot.BootfsType = bootPart.FSType
		sb.YamlObject.Boot.BootfsDevice = vdaName(bootPart.Name)
		sb.YamlObject.Boot.BootfsUuid = bootPart.FsUUID
		sb.Log.Info(sb.Ctx, "Identified Bootfs (Smallest)",
			"part_uuid", sb.YamlObject.Boot.BootfsPartUuid,
			"fs_uuid", sb.YamlObject.Boot.BootfsUuid,
			"bootfs_device", sb.YamlObject.Boot.BootfsDevice,
			"fstype", sb.YamlObject.Boot.BootfsType,
			"size_bytes", bootPart.Size,
		)
	} else {
		sb.YamlObject.Boot.BootfsPartUuid = rootPart.PartUUID
		sb.YamlObject.Boot.BootfsType = rootPart.FSType
		sb.YamlObject.Boot.BootfsUuid = rootPart.FsUUID
		sb.YamlObject.Boot.BootfsDevice = vdaName(rootPart.Name)
		sb.Log.Info(sb.Ctx, "Only one Linux partition found; assuming /boot is part of rootfs",
			"bootfs_uuid", sb.YamlObject.Boot.BootfsPartUuid)
	}

	return nil
}

func ExtractArtifacts(sb *ImageSB) error {
	rawImage := filepath.Join(sb.SstRuntimeDir, "images", sb.YamlObject.Provenance.Name, sb.YamlObject.Storage.RawImage)
	bootfsPartUuid := sb.YamlObject.Boot.BootfsPartUuid

	if bootfsPartUuid == "" {
		return fmt.Errorf("bootfs_part_uuid is empty; run AnalyseImg first")
	}

	sb.Log.Info(sb.Ctx, "kernel.extract.start",
		"image", rawImage,
		"bootfs_part_uuid", bootfsPartUuid,
	)

	loopDev, err := attachLoop(sb.Ctx, sb.Log, rawImage)
	if err != nil {
		sb.Log.Diagnose(sb.Ctx, "LOOP_ATTACH_FAILED", "failed to attach disk image",
			"image", rawImage,
			"error", err.Error(),
		)
		return err
	}
	defer func() {
		if err := detachLoop(sb.Ctx, sb.Log, loopDev); err != nil {
			sb.Log.Warn(sb.Ctx, "Failed to detach loop device", "device", loopDev, "error", err)
		}
	}()

	partDev, err := resolveBootPartitionDevice(sb.Ctx, sb.Log, sb.YamlObject, loopDev)
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

	destDir := filepath.Join(sb.SstRuntimeDir, "images", sb.YamlObject.Provenance.Name)
	kernelName, initrdName, err := copyArtifacts(kernelPath, initrdPath, destDir)
	if err != nil {
		return err
	}

	sb.YamlObject.Boot.Kernel = kernelName
	sb.YamlObject.Boot.Initrd = initrdName

	rootUUID := sb.YamlObject.Storage.RootfsUuid
	if rootUUID != "" {
		rootLoopDev, err := resolvePartitionByUUID(sb.Ctx, sb.Log, loopDev, rootUUID)
		if err == nil && rootLoopDev != "" {
			rootMnt, err := mountPartition(sb.Ctx, sb.Log, rootLoopDev)
			if err == nil {
				defer unmountPartition(sb.Ctx, sb.Log, rootMnt)

				osInfo, err := ParseOSRelease(rootMnt)
				if err == nil {
					semanticName := GenerateSemanticName(osInfo)
					upstreamBuildID := osInfo["BUILD_ID"]
					sb.Log.Info(sb.Ctx, "parsed os-release from imported image", "semantic_name", semanticName, "upstream_build_id", upstreamBuildID)

					sb.YamlObject.Provenance.UpstreamOs = semanticName
					sb.YamlObject.Provenance.UpstreamBuildId = upstreamBuildID
				} else {
					sb.Log.Warn(sb.Ctx, "could not parse os-release from root partition", "error", err)
				}
			}
		} else {
			sb.Log.Warn(sb.Ctx, "could not resolve loop partition by UUID", "uuid", rootUUID, "error", err)
		}
	}

	return nil
}

func resolveBootPartitionDevice(ctx context.Context, log *sst.Logx, img *mkimagepb.ImageYAML, baseDev string) (string, error) {
	if img.Boot != nil && img.Boot.BootfsPartUuid != "" {
		if img.Boot.BootfsPartUuid == img.Boot.BootfsUuid {
			return baseDev, nil // Unpartitioned raw volume
		}
		return findPartitionByTag(ctx, log, baseDev, "PARTUUID", img.Boot.BootfsPartUuid)
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

func attachLoop(ctx context.Context, log *sst.Logx, image string) (string, error) {
	// -P ensures the kernel scans the partition table synchronously.
	res, err := sst.ExecShell(ctx, log, "losetup", "-fP", "--show", image)
	if err != nil {
		return "", fmt.Errorf("losetup failed: %w", err)
	}
	dev := strings.TrimSpace(res.Stdout)
	if dev == "" {
		return "", fmt.Errorf("losetup returned empty device name")
	}

	// Tell the kernel to definitively re-read the partition table. 
	// This acts as a barrier, ensuring any /dev/loopXpY nodes exist before we return.
	_, _ = sst.ExecShell(ctx, log, "blockdev", "--rereadpt", dev)

	return dev, nil
}

func detachLoop(ctx context.Context, log *sst.Logx, dev string) error {
	_, err := sst.ExecShell(ctx, log, "losetup", "-d", dev)
	return err
}

func mountPartition(ctx context.Context, log *sst.Logx, dev string) (string, error) {
	dir, err := os.MkdirTemp("", "mkimage-boot-*")
	if err != nil {
		return "", err
	}

	if _, err := sst.ExecShell(ctx, log, "mount", "-o", "ro", dev, dir); err != nil {
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
