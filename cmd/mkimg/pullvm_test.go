package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/swiss-software-tool/sst/internal/rtsst"
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

// createDummyArtifacts generates the kernel/initrd files.
func createDummyArtifacts(t *testing.T, dir string) (string, string) {
	shBin, err := exec.LookPath("sh")
	if err != nil {
		shBin = "/bin/sh"
	}

	rawBin := filepath.Join(dir, "raw.bin")
	sh(t, dir, "cat %s > %s", shBin, rawBin)
	sh(t, dir, "dd if=/dev/urandom >> %s bs=1M count=5 status=none", rawBin)

	vmlinuz := filepath.Join(dir, "vmlinuz")
	sh(t, dir, "gzip -c %s > %s", rawBin, vmlinuz)

	initrd := filepath.Join(dir, "initrd.img")
	sh(t, dir, "dd if=/dev/zero of=%s bs=1M count=5 status=none", initrd)

	return vmlinuz, initrd
}

func TestScenario1_SeparateBootPartition(t *testing.T) {
	requireRoot(t)
	tmpDir := t.TempDir()

	upstreamPath := filepath.Join(tmpDir, "upstream-case1.qcow2")
	rawPath := filepath.Join(tmpDir, "temp-raw.img")

	sh(t, tmpDir, "truncate -s 100M %s", rawPath)
	sh(t, tmpDir, "sgdisk -n 1:0:+10M -t 1:ef00 %s", rawPath)
	sh(t, tmpDir, "sgdisk -n 2:0:+20M -t 2:8300 %s", rawPath)
	sh(t, tmpDir, "sgdisk -n 3:0:0    -t 3:8300 %s", rawPath)

	loopOut, err := exec.Command("losetup", "-f", "--show", "-P", rawPath).CombinedOutput()
	require.NoError(t, err)
	loopDev := string(loopOut[:len(loopOut)-1])
	defer sh(t, tmpDir, "losetup -d %s", loopDev)

	sh(t, tmpDir, "mkfs.vfat %sp1", loopDev)
	sh(t, tmpDir, "mkfs.ext4 -q %sp2", loopDev)
	sh(t, tmpDir, "mkfs.ext4 -q %sp3", loopDev)

	mountDir := filepath.Join(tmpDir, "mnt-p2")
	os.Mkdir(mountDir, 0755)
	sh(t, tmpDir, "mount %sp2 %s", loopDev, mountDir)

	vmlinuzSrc, initrdSrc := createDummyArtifacts(t, tmpDir)
	sh(t, tmpDir, "cp %s %s/vmlinuz", vmlinuzSrc, mountDir)
	sh(t, tmpDir, "cp %s %s/initrd", initrdSrc, mountDir)

	sh(t, tmpDir, "umount %s", mountDir)
	sh(t, tmpDir, "qemu-img convert -f raw -O qcow2 %s %s", rawPath, upstreamPath)

	app := &pullVMApp{
		upstreamArg: upstreamPath,
		methods:     defaultPullVMOps,
	}

	oldFlagRoot := flagRoot
	flagRoot = tmpDir
	flagForce = true
	defer func() {
		flagRoot = oldFlagRoot
		flagForce = false
	}()

	app.methods.DownloadImage = func(a *pullVMApp) error {
		base := filepath.Base(a.upstreamArg)
		a.srcImage = filepath.Join(a.imageDir, base)
		return rtsst.CopySparse(a.upstreamArg, a.srcImage)
	}

	err = runPullVMSteps(app)
	require.NoError(t, err)

	manifestPath := filepath.Join(tmpDir, "images", "upstream-case1", "image.yaml")
	assert.FileExists(t, manifestPath)
}
