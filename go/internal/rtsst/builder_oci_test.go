package rtsst

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/swiss-software-tool/runtime-smart/go/internal/sst"
)

func TestOCIWorkflowWithPodman(t *testing.T) {
	_, found := os.LookupEnv("PATH")
	if !found {
		t.Skip("PATH not found in environment")
	}

	log := sst.LogxRoot()
	tmp := t.TempDir() // Use an isolated temp dir for tests

	// FIX: Added the 4th parameter (RuntimeDir) to NewBuilder
	builder := NewBuilder(log, nil, "Dockerfile.test", tmp)
	builder.BaseImage = "alpine:latest"
	builder.Name = "test-vm" // FIX: Replaced VMOUT
	
	err := builder.ResolveOCIPaths()
	require.NoError(t, err)

	ctx := context.Background()
	
	// Step 1: Fetch via Skopeo
	err = builder.FetchOCI(ctx)
	if err != nil {
		t.Skipf("skopeo fetch failed (is skopeo installed?): %v", err)
	}

	// Step 2: Verify with Podman
	err = builder.VerifyWithPodman(ctx)
	assert.NoError(t, err)
	
	// Cleanup staging area after test (no longer strictly necessary since tmp is destroyed, but good practice)
	os.RemoveAll(filepath.Join(tmp, "oci", "alpine:latest"))
}
