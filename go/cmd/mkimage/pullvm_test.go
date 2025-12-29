package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Ensure we are root
func requireRoot(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("skipping test; requires root privileges")
	}
}

// Helper to run shell commands easily
func sh(t *testing.T, dir string, format string, args ...interface{}) {
	cmdStr := fmt.Sprintf(format, args...)
	cmd := exec.Command("sh", "-c", cmdStr)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("command failed: %s\nOutput:\n%s", cmdStr, string(out))
	}
}

// go/cmd/mkimage/pullvm_test.go

// createDummyArtifacts generates the kernel/initrd files.
// We use the system's 'sh' binary as the base for the kernel so it has a valid ELF header,
// satisfying the 'readelf' check in the extraction logic.
// We then append random data to satisfy the file size heuristic (>1MB).
func createDummyArtifacts(t *testing.T, dir string) (string, string) {
	// 1. Find a real binary to act as our "vmlinux" base (for valid ELF header)
	shBin, err := exec.LookPath("sh")
	if err != nil {
		// Fallback if 'sh' isn't in PATH, though unlikely on Linux
		shBin = "/bin/sh"
	}

	rawBin := filepath.Join(dir, "raw.bin")

	// 2. Create the raw kernel file: [Valid ELF Header] + [5MB Random Data]
	// Using 'cat' ensures we get the binary content.
	sh(t, dir, "cat %s > %s", shBin, rawBin)
	// Append random data (incompressible) to ensure size > 1MB even after gzip
	sh(t, dir, "dd if=/dev/urandom >> %s bs=1M count=5 status=none", rawBin)

	// 3. Gzip it to create vmlinuz (this adds the gzip signature detected by the extractor)
	vmlinuz := filepath.Join(dir, "vmlinuz")
	sh(t, dir, "gzip -c %s > %s", rawBin, vmlinuz)

	// 4. Create 5MB initrd (zeros are fine here; initrd isn't content-checked by us, just size-checked)
	initrd := filepath.Join(dir, "initrd.img")
	sh(t, dir, "dd if=/dev/zero of=%s bs=1M count=5 status=none", initrd)

	return vmlinuz, initrd
}

// TestScenario1_SeparateBootPartition tests the case where /boot is a separate partition.
// Heuristic: Smallest ext4 is Boot, Largest ext4 is Root.
func TestScenario1_SeparateBootPartition(t *testing.T) {
	requireRoot(t)
	tmpDir := t.TempDir()

	// 1. Prepare Upstream Image
	upstreamName := "upstream-case1.qcow2"
	upstreamPath := filepath.Join(tmpDir, upstreamName)
	rawPath := filepath.Join(tmpDir, "temp-raw.img") // Intermediate for building

	// Create 100MB raw file
	sh(t, tmpDir, "truncate -s 100M %s", rawPath)

	// Create GPT Partition Table
	// Part 1: VFAT (10M) - Type EF00
	// Part 2: EXT4 Small (20M) - Type 8300 - Target BOOT partition
	// Part 3: EXT4 Large (Remaining ~70M) - Type 8300 - Target ROOT partition
	sh(t, tmpDir, "sgdisk -n 1:0:+10M -t 1:ef00 %s", rawPath)
	sh(t, tmpDir, "sgdisk -n 2:0:+20M -t 2:8300 %s", rawPath)
	sh(t, tmpDir, "sgdisk -n 3:0:0    -t 3:8300 %s", rawPath)

	// Setup Loopback for formatting
	// -P scans partitions
	loopOut, err := exec.Command("losetup", "-f", "--show", "-P", rawPath).CombinedOutput()
	require.NoError(t, err)
	loopDev := string(loopOut[:len(loopOut)-1]) // trim newline
	defer sh(t, tmpDir, "losetup -d %s", loopDev)

	// Format partitions
	sh(t, tmpDir, "mkfs.vfat %sp1", loopDev)
	sh(t, tmpDir, "mkfs.ext4 -q %sp2", loopDev) // Boot
	sh(t, tmpDir, "mkfs.ext4 -q %sp3", loopDev) // Root

	// Mount Boot Partition (p2) and populate
	mountDir := filepath.Join(tmpDir, "mnt-p2")
	os.Mkdir(mountDir, 0755)
	sh(t, tmpDir, "mount %sp2 %s", loopDev, mountDir)

	// Artifacts go in ROOT of boot partition
	vmlinuzSrc, initrdSrc := createDummyArtifacts(t, tmpDir)
	sh(t, tmpDir, "cp %s %s/vmlinuz", vmlinuzSrc, mountDir)
	sh(t, tmpDir, "cp %s %s/initrd", initrdSrc, mountDir)

	sh(t, tmpDir, "umount %s", mountDir)

	// Convert raw to qcow2 to simulate upstream
	sh(t, tmpDir, "qemu-img convert -f raw -O qcow2 %s %s", rawPath, upstreamPath)

	// 2. Setup pullVM App
	// We override DownloadImage to skip network, but run everything else real.
	app := &pullVMApp{
		upstreamArg: upstreamPath, // acts as the "url"
		methods:     defaultPullVMOps,
	}

	// Override setup to force our tempdir as the runtime dir
	// We can't easily inject flags into main.go vars from here without side effects,
	// so we set the flags global vars manually.
	oldFlagRoot := flagRoot
	flagRoot = tmpDir
	flagForce = true // Overwrite if needed
	flagVerbose = true
	defer func() {
		flagRoot = oldFlagRoot
		flagForce = false
		flagVerbose = false
	}()

	// Inject a "local copy" mock for download, because our upstreamArg is a file path
	app.methods.DownloadImage = func(a *pullVMApp) error {
		// Just copy upstreamPath -> app.srcImage
		base := filepath.Base(a.upstreamArg)
		a.srcImage = filepath.Join(a.imageDir, base)
		return rtsst.CopySparse(a.upstreamArg, a.srcImage)
	}

	// 3. Run
	err = runPullVMSteps(app)
	require.NoError(t, err)

	// 4. Verify Results
	// Check YAML
	manifestPath := filepath.Join(tmpDir, "images", "upstream-case1", "image.yaml")
	assert.FileExists(t, manifestPath)

	// Check extracted kernels
	destDir := filepath.Join(tmpDir, "images", "upstream-case1")
	assert.FileExists(t, filepath.Join(destDir, "vmlinuz"))
	assert.FileExists(t, filepath.Join(destDir, "initrd"))
	assert.FileExists(t, filepath.Join(destDir, "kernel")) // symlink
	assert.FileExists(t, filepath.Join(destDir, "initrd")) // symlink

	// Verify Heuristics in State Bag
	// Root should be Part 3 (Larger), Boot should be Part 2 (Smaller)
	// We can't check specific UUIDs easily without parsing `lsblk` ourselves,
	// but we can assume success if the extraction worked, since extraction depends on correct boot UUID.
	assert.NotEmpty(t, app.sb.YamlObject.RootfsPartUuid)
	assert.NotEmpty(t, app.sb.YamlObject.BootfsPartUuid)
	assert.NotEqual(t, app.sb.YamlObject.RootfsPartUuid, app.sb.YamlObject.BootfsPartUuid, "Root and Boot UUIDs should differ for separate partitions")
}

// TestScenario2_UnifiedPartitionBackupGPT tests /boot directory on single partition + Backup GPT usage.
func TestScenario2_UnifiedPartitionBackupGPT(t *testing.T) {
	requireRoot(t)
	tmpDir := t.TempDir()

	// 1. Prepare Upstream Image
	upstreamName := "upstream-case2.qcow2"
	upstreamPath := filepath.Join(tmpDir, upstreamName)
	rawPath := filepath.Join(tmpDir, "temp-raw-2.img")

	sh(t, tmpDir, "truncate -s 100M %s", rawPath)

	// Create GPT: 1 VFAT, 1 EXT4 (Unified)
	sh(t, tmpDir, "sgdisk -n 1:0:+10M -t 1:ef00 %s", rawPath)
	sh(t, tmpDir, "sgdisk -n 2:0:0    -t 2:8300 %s", rawPath)

	// --- SETUP USING QEMU-NBD ---
	sh(t, tmpDir, "modprobe nbd max_part=16")

	setupDev := "/dev/nbd1"
	exec.Command("qemu-nbd", "-d", setupDev).Run()

	sh(t, tmpDir, "qemu-nbd -f raw -c %s %s", setupDev, rawPath)

	// Use standard blockdev tool
	sh(t, tmpDir, "blockdev --rereadpt %s", setupDev)
	sh(t, tmpDir, "udevadm settle")

	defer exec.Command("qemu-nbd", "-d", setupDev).Run()

	// Format
	sh(t, tmpDir, "mkfs.vfat %sp1", setupDev)
	sh(t, tmpDir, "mkfs.ext4 -q %sp2", setupDev)

	// Mount Unified Partition (p2)
	mountDir := filepath.Join(tmpDir, "mnt-p2")
	os.Mkdir(mountDir, 0755)
	sh(t, tmpDir, "mount %sp2 %s", setupDev, mountDir)

	os.Mkdir(filepath.Join(mountDir, "boot"), 0755)
	vmlinuzSrc, initrdSrc := createDummyArtifacts(t, tmpDir)
	sh(t, tmpDir, "cp %s %s/boot/vmlinuz", vmlinuzSrc, mountDir)
	sh(t, tmpDir, "cp %s %s/boot/initrd", initrdSrc, mountDir)

	sh(t, tmpDir, "umount %s", mountDir)

	// Detach setup device explicitly
	sh(t, tmpDir, "qemu-nbd -d %s", setupDev)

	// CORRUPTION: Wipe ONLY Primary GPT Header (LBA 1).
	// We MUST preserve LBA 0 (PMBR).
	// We MUST preserve LBA 2-33 (Partition Entries) if we want to be nice,
	// though they technically shouldn't be read if Header is bad.
	// Wiping just the header (512 bytes) forces checksum failure -> fallback to Backup.
	sh(t, tmpDir, "dd if=/dev/zero of=%s bs=512 seek=1 count=1 conv=notrunc", rawPath)

	// Convert to qcow2
	sh(t, tmpDir, "qemu-img convert -f raw -O qcow2 %s %s", rawPath, upstreamPath)

	// 2. Setup App
	app := &pullVMApp{
		upstreamArg: upstreamPath,
		methods:     defaultPullVMOps,
	}

	oldFlagRoot := flagRoot
	flagRoot = tmpDir
	flagForce = true
	flagVerbose = true
	defer func() {
		flagRoot = oldFlagRoot
		flagForce = false
		flagVerbose = false
	}()

	app.methods.DownloadImage = func(a *pullVMApp) error {
		base := filepath.Base(a.upstreamArg)
		a.srcImage = filepath.Join(a.imageDir, base)
		return copyFile(a.upstreamArg, a.srcImage)
	}

	// 3. Run
	err := runPullVMSteps(app)
	require.NoError(t, err)

	// 4. Verify Results
	destDir := filepath.Join(tmpDir, "images", "upstream-case2")
	assert.FileExists(t, filepath.Join(destDir, "vmlinuz"))
	assert.FileExists(t, filepath.Join(destDir, "initrd"))

	assert.NotEmpty(t, app.sb.YamlObject.RootfsPartUuid)
	assert.Equal(t, app.sb.YamlObject.RootfsPartUuid, app.sb.YamlObject.BootfsPartUuid)
}
