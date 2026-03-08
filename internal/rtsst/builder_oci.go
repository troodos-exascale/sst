package rtsst

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/google/go-containerregistry/pkg/crane"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/swiss-software-tool/sst/internal/sst"
)

// FetchAndExtractBase resolves the FROM image and extracts it directly to the rootfs.
func (b *BuildApp) FetchAndExtractBase(ctx context.Context) error {
	// 1. Resolve path
	b.RootDir = filepath.Join(b.RuntimeDir, "build", b.Name, "rootfs")
	b.Log.Info(ctx, "resolved paths", "rootfs", b.RootDir)

	// 2. Guarantee a clean slate
	_ = os.RemoveAll(b.RootDir)
	if err := os.MkdirAll(b.RootDir, 0755); err != nil {
		return fmt.Errorf("failed to create root directory: %w", err)
	}

	// 3. Bypass fetching for scratch builds
	if b.BaseImage == "scratch" {
		b.Log.Info(ctx, "base image is 'scratch', skipping OCI fetch, leaving empty rootfs")
		return nil
	}

	b.Log.Info(ctx, "fetching OCI base image via crane", "image", b.BaseImage, "arch", runtime.GOARCH)

	// 4. Pull the image metadata and layers natively (forcing the host architecture)
	platform := &v1.Platform{
		Architecture: runtime.GOARCH,
		OS:           "linux",
	}

	img, err := crane.Pull(b.BaseImage, crane.WithPlatform(platform))
	if err != nil {
		b.Log.Diagnose(ctx, "OCI_PULL_FAILED", "failed to pull OCI image", "image", b.BaseImage, "error", err.Error())
		return fmt.Errorf("crane pull failed for %s: %w", b.BaseImage, err)
	}

	// 5. Export the flattened filesystem to a temporary tarball
	tmpTar := filepath.Join(b.RuntimeDir, "build", b.Name, "base_flat.tar")
	f, err := os.Create(tmpTar)
	if err != nil {
		return fmt.Errorf("failed to create temp tarball: %w", err)
	}

	b.Log.Info(ctx, "exporting flattened filesystem", "tarball", tmpTar)
	if err := crane.Export(img, f); err != nil {
		f.Close()
		_ = os.Remove(tmpTar)
		b.Log.Diagnose(ctx, "OCI_EXPORT_FAILED", "failed to export image to tarball", "image", b.BaseImage, "error", err.Error())
		return fmt.Errorf("crane export failed: %w", err)
	}
	f.Close()

	// Ensure cleanup of the intermediate tarball
	defer os.Remove(tmpTar)

	// 6. Extract the flattened tarball to the rootfs using the system tar
	b.Log.Info(ctx, "extracting filesystem to rootfs", "rootfs", b.RootDir)
	if _, err := sst.ExecShell(ctx, b.Log, "tar", "-C", b.RootDir, "-xf", tmpTar); err != nil {
		b.Log.Diagnose(ctx, "OCI_EXTRACT_FAILED", "failed to extract tarball to rootfs", "target_dir", b.RootDir, "error", err.Error())
		return fmt.Errorf("failed to untar base image: %w", err)
	}

	b.Log.Info(ctx, "successfully unpacked base image", "image", b.BaseImage)
	return nil
}