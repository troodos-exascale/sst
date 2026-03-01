package rtsst

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/swiss-software-tool/runtime-smart/go/internal/sst"
)

func sanitizeName(s string) string {
	return strings.ReplaceAll(s, ":", "_")
}

func (b *BuildApp) ResolveOCIPaths() error {
	safeBase := sanitizeName(b.BaseImage)
	b.OCIStaging = filepath.Join(b.RuntimeDir, "oci", safeBase)
	b.RootDir = filepath.Join(b.RuntimeDir, "build", b.Name, "rootfs") // UPDATED: use b.Name
	
	b.Log.Info(context.Background(), "resolved paths", "oci_staging", b.OCIStaging, "rootfs", b.RootDir)
	return os.MkdirAll(b.OCIStaging, 0755)
}

// ... (FetchOCI, VerifyWithPodman, UnpackOCI remain the same, they use b.OCIStaging and b.RootDir) ...
func (b *BuildApp) FetchOCI(ctx context.Context) error {
	indexFile := filepath.Join(b.OCIStaging, "index.json")
	if _, err := os.Stat(indexFile); err == nil {
		b.Log.Info(ctx, "base image already exists in cache, skipping pull", "file", indexFile)
		return nil
	}

	b.Log.Info(ctx, "fetching OCI base image", "image", b.BaseImage, "target", b.OCIStaging)
	
	_, err := sst.ExecShell(ctx, b.Log, "skopeo", "copy", 
		"--src-tls-verify=false",
		"docker://"+b.BaseImage, 
		"oci:"+b.OCIStaging)
	return err
}

func (b *BuildApp) VerifyWithPodman(ctx context.Context) error {
	b.Log.Info(ctx, "verifying image with podman", "image", b.BaseImage)
	_, err := sst.ExecShell(ctx, b.Log, "podman", "run", "--rm", "oci:"+b.OCIStaging, "ls", "/")
	return err
}

func (b *BuildApp) UnpackOCI(ctx context.Context) error {
	b.Log.Info(ctx, "unpacking and flattening OCI layers", "target", b.RootDir)
	
	if err := os.MkdirAll(b.RootDir, 0755); err != nil {
		return err
	}

	indexPath := filepath.Join(b.OCIStaging, "index.json")
	data, err := os.ReadFile(indexPath)
	if err != nil { 
		return fmt.Errorf("failed to read OCI index at %s: %w", indexPath, err) 
	}
	
	var index struct {
		Manifests []struct { Digest string `json:"digest"` } `json:"manifests"`
	}
	if err := json.Unmarshal(data, &index); err != nil || len(index.Manifests) == 0 {
		return fmt.Errorf("invalid or empty OCI index at %s", indexPath)
	}

	mDigest := strings.TrimPrefix(index.Manifests[0].Digest, "sha256:")
	mPath := filepath.Join(b.OCIStaging, "blobs", "sha256", mDigest)
	mData, err := os.ReadFile(mPath)
	if err != nil { 
		return fmt.Errorf("failed to read manifest blob %s: %w", mPath, err) 
	}

	var manifest struct {
		Layers []struct { Digest string `json:"digest"` } `json:"layers"`
	}
	if err := json.Unmarshal(mData, &manifest); err != nil { 
		return fmt.Errorf("failed to parse manifest: %w", err) 
	}

	for i, layer := range manifest.Layers {
		lDigest := strings.TrimPrefix(layer.Digest, "sha256:")
		lPath := filepath.Join(b.OCIStaging, "blobs", "sha256", lDigest)
		
		b.Log.Info(ctx, "extracting layer", "index", i, "digest", layer.Digest)
		
		_, err := sst.ExecShell(ctx, b.Log, "tar", "-C", b.RootDir, "-xf", lPath)
		if err != nil {
			b.Log.Warn(ctx, "failed to extract layer blob (it may be metadata)", "digest", layer.Digest)
		}
	}
	return nil
}
