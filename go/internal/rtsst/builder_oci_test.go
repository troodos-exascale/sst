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
	// Fix: os.LookupEnv returns (string, bool). Check the boolean 'found'.
	_, found := os.LookupEnv("PATH")
	if !found {
		t.Skip("PATH not found in environment")
	}

	log := sst.LogxRoot()
	// Use 'alpine' as a tiny test container
	builder := NewBuilder(log, nil, "Dockerfile.test")
	builder.BaseImage = "alpine:latest"
	builder.VMOUT = "test-vm"
	
	err := builder.ResolveOCIPaths()
	require.NoError(t, err)

	ctx := context.Background()
	
	// Step 1: Fetch via Skopeo
	// Note: This may still skip if skopeo is not installed on the host
	err = builder.FetchOCI(ctx)
	if err != nil {
		t.Skipf("skopeo fetch failed (is skopeo installed?): %v", err)
	}

	// Step 2: Verify with Podman
	err = builder.VerifyWithPodman(ctx)
	assert.NoError(t, err)
	
	// Cleanup staging area after test
	home, _ := os.UserHomeDir()
	os.RemoveAll(filepath.Join(home, ".runtimes", "oci", "alpine:latest"))
}
