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

// sanitizeName replaces colons and other problematic chars for filesystem paths
func sanitizeName(s string) string {
	return strings.ReplaceAll(s, ":", "_")
}

// ResolveOCIPaths sets up the staging directories under .runtimes
func (b *BuildApp) ResolveOCIPaths() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	
	// Sanitize the base image name for the directory path
	safeBase := sanitizeName(b.BaseImage)
	b.OCIStaging = filepath.Join(home, ".runtimes", "oci", safeBase)
	b.RootDir = filepath.Join(home, ".runtimes", "build", b.VMOUT, "rootfs")
	
	b.Log.Info(context.Background(), "resolved paths", "oci_staging", b.OCIStaging, "rootfs", b.RootDir)
	return os.MkdirAll(b.OCIStaging, 0755)
}

// FetchOCI pulls the image using skopeo into a local OCI layout
func (b *BuildApp) FetchOCI(ctx context.Context) error {
	indexFile := filepath.Join(b.OCIStaging, "index.json")
	if _, err := os.Stat(indexFile); err == nil {
		b.Log.Info(ctx, "base image already exists in cache, skipping pull", "file", indexFile)
		return nil
	}

	b.Log.Info(ctx, "fetching OCI base image", "image", b.BaseImage, "target", b.OCIStaging)
	
	// We use the raw b.BaseImage for the docker:// source, but the sanitized path for oci:
	_, err := sst.ExecShell(ctx, b.Log, "skopeo", "copy", 
		"--src-tls-verify=false", // Optional: useful in some VM environments
		"docker://"+b.BaseImage, 
		"oci:"+b.OCIStaging)
	return err
}

// VerifyWithPodman runs a simple check using the OCI layout
func (b *BuildApp) VerifyWithPodman(ctx context.Context) error {
	b.Log.Info(ctx, "verifying image with podman", "image", b.BaseImage)
	// Podman also needs the oci: prefix to point to our sanitized directory
	_, err := sst.ExecShell(ctx, b.Log, "podman", "run", "--rm", "oci:"+b.OCIStaging, "ls", "/")
	return err
}

// UnpackOCI flattens OCI layers into a single filesystem directory
func (b *BuildApp) UnpackOCI(ctx context.Context) error {
	b.Log.Info(ctx, "unpacking and flattening OCI layers", "target", b.RootDir)
	
	if err := os.MkdirAll(b.RootDir, 0755); err != nil {
		return err
	}

	// 1. Load Index to find the Manifest digest
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

	// 2. Load Manifest to get layer digests in sequential order
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

	// 3. Extract layers sequentially
	for i, layer := range manifest.Layers {
		lDigest := strings.TrimPrefix(layer.Digest, "sha256:")
		lPath := filepath.Join(b.OCIStaging, "blobs", "sha256", lDigest)
		
		b.Log.Info(ctx, "extracting layer", "index", i, "digest", layer.Digest)
		
		// Use tar -xf. Sequential execution flattens the image into RootDir
		_, err := sst.ExecShell(ctx, b.Log, "tar", "-C", b.RootDir, "-xf", lPath)
		if err != nil {
			b.Log.Warn(ctx, "failed to extract layer blob (it may be metadata)", "digest", layer.Digest)
		}
	}
	return nil
}
