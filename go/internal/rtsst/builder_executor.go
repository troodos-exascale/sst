package rtsst

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/swiss-software-tool/runtime-smart/go/internal/sst"
	mkimagepb "github.com/swiss-software-tool/runtime-smart/go/proto/yaml/mkimage/v1"
)

// ExecuteSingleInstruction processes one COPY or RUN command from the parser
func (b *BuildApp) ExecuteSingleInstruction(ctx context.Context, inst Instruction) error {
	switch inst.Type {
	case "COPY":
		return b.execCopy(ctx, inst.Args)
	case "RUN":
		return b.execRun(ctx, inst.Args)
	default:
		return fmt.Errorf("unknown instruction type: %s", inst.Type)
	}
}

func (b *BuildApp) execCopy(ctx context.Context, args string) error {
	parts := strings.Fields(args)
	if len(parts) < 2 {
		return fmt.Errorf("invalid COPY instruction: %s", args)
	}
	src := parts[0]
	dst := filepath.Join(b.RootDir, parts[1])
	
	b.Log.Info(ctx, "executing COPY", "src", src, "dst", dst)
	_, err := sst.ExecShell(ctx, b.Log, "cp", "-a", src, dst)
	return err
}

func (b *BuildApp) execRun(ctx context.Context, cmdStr string) error {
	b.Log.Info(ctx, "executing RUN in chroot", "command", cmdStr)
	// Execute command inside the flattened rootfs using host chroot
	_, err := sst.ExecShell(ctx, b.Log, "chroot", b.RootDir, "/bin/sh", "-c", cmdStr)
	if err != nil {
		return fmt.Errorf("chroot command failed: %w", err)
	}
	return nil
}

// FinalizeVM converts the rootfs to a bootable ext2 image and saves the manifest
func (b *BuildApp) FinalizeVM(ctx context.Context) error {
	b.ImageOutput = filepath.Join(Home(), ".runtimes", "images", b.VMOUT)
	if err := os.MkdirAll(b.ImageOutput, 0755); err != nil {
		return err
	}

	rawImagePath := filepath.Join(b.ImageOutput, b.VMOUT+".raw")
	b.Log.Info(ctx, "creating bootable VM image", "path", rawImagePath)

	// Create 4G sparse image
	if err := CreateSparse(rawImagePath, 4*1024*1024*1024); err != nil {
		return err
	}

	// Format as ext2 for simplicity and reproducibility
	_, err := sst.ExecShell(ctx, b.Log, "mkfs.ext2", "-F", rawImagePath)
	if err != nil {
		return err
	}

	// Mount and populate
	tmpMnt, err := os.MkdirTemp("", "sst-mnt-*")
	if err != nil { return err }
	defer os.RemoveAll(tmpMnt)

	_, err = sst.ExecShell(ctx, b.Log, "mount", "-o", "loop", rawImagePath, tmpMnt)
	if err != nil { return err }
	
	_, _ = sst.ExecShell(ctx, b.Log, "cp", "-a", b.RootDir+"/.", tmpMnt)
	_, _ = sst.ExecShell(ctx, b.Log, "umount", tmpMnt)

	// Build the ImageYAML metadata
	sb := &ImageSB{
		Ctx: ctx,
		Log: *b.Log,
		YamlObject: &mkimagepb.ImageYAML{
			Name:     b.VMOUT,
			RawImage: b.VMOUT + ".raw",
		},
		SstRuntimeDir: filepath.Join(Home(), ".runtimes"),
	}
	
	// Perform heuristic analysis to find kernel/initrd
	_ = AnalyseImg(sb)
	
	yo := &sst.YamlObject{
		Path:   filepath.Join(b.ImageOutput, "image.yaml"),
		Schema: "sst.yaml.mkimage.v1.ImageYAML",
		Kind:   "Image",
		Msg:    sb.YamlObject,
	}
	return yo.Save(ctx, b.Log)
}
