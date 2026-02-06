package rtsst

import (
	"archive/tar"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/swiss-software-tool/runtime-smart/go/internal/sst"
)

func TestUnpackOCI_Flattening(t *testing.T) {
	tmp := t.TempDir()
	log := sst.LogxRoot()
	
	// Setup Mock OCI Layout structure
	ociDir := filepath.Join(tmp, "oci-mock")
	blobsDir := filepath.Join(ociDir, "blobs", "sha256")
	require.NoError(t, os.MkdirAll(blobsDir, 0755))

	// Helper to create a tar blob
	createLayer := func(name string, files map[string]string) string {
		path := filepath.Join(blobsDir, name)
		f, err := os.Create(path)
		require.NoError(t, err)
		tw := tar.NewWriter(f)
		for name, content := range files {
			hdr := &tar.Header{
				Name: name,
				Mode: 0644,
				Size: int64(len(content)),
			}
			require.NoError(t, tw.WriteHeader(hdr))
			_, err := tw.Write([]byte(content))
			require.NoError(t, err)
		}
		require.NoError(t, tw.Close())
		require.NoError(t, f.Close())
		return name
	}

	// Create two layers: Layer 2 should overwrite Layer 1
	l1 := createLayer("layer1", map[string]string{"etc/config": "original", "usr/bin/app": "v1"})
	l2 := createLayer("layer2", map[string]string{"etc/config": "overwritten"})

	// Create Manifest
	manifest := struct {
		Layers []struct{ Digest string `json:"digest"` } `json:"layers"`
	}{
		Layers: []struct{ Digest string `json:"digest"` }{
			{Digest: "sha256:" + l1},
			{Digest: "sha256:" + l2},
		},
	}
	mBuf, err := json.Marshal(manifest)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(blobsDir, "manifest"), mBuf, 0644))

	// Create Index
	index := struct {
		Manifests []struct{ Digest string `json:"digest"` } `json:"manifests"`
	}{
		Manifests: []struct{ Digest string `json:"digest"` }{
			{Digest: "sha256:manifest"},
		},
	}
	iBuf, err := json.Marshal(index)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(ociDir, "index.json"), iBuf, 0644))

	// Initialize Builder
	builder := &BuildApp{
		Log:        log,
		OCIStaging: ociDir,
		RootDir:    filepath.Join(tmp, "rootfs"),
	}

	// Run Unpack
	err = builder.UnpackOCI(context.Background())
	assert.NoError(t, err)

	// Verify Flattening Result
	// 1. etc/config must be "overwritten"
	cfg, err := os.ReadFile(filepath.Join(builder.RootDir, "etc/config"))
	assert.NoError(t, err)
	assert.Equal(t, "overwritten", string(cfg))

	// 2. usr/bin/app must still exist from layer 1
	app, err := os.ReadFile(filepath.Join(builder.RootDir, "usr/bin/app"))
	assert.NoError(t, err)
	assert.Equal(t, "v1", string(app))
}
