package rtsst

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/swiss-software-tool/runtime-smart/go/internal/sst"
)

func TestOCIWorkflowWithPodman(t *testing.T) {
	// Skip this test entirely if we are on macOS
	if runtime.GOOS != "linux" {
		t.Skip("skipping OCI fetch/verify test; requires a native Linux environment")
	}

	_, found := os.LookupEnv("PATH")
	if !found {
		t.Skip("PATH not found in environment")
	}

	log := sst.LogxRoot()
	tmp := t.TempDir() 

	builder := NewBuilder(log, nil, "Dockerfile.test", tmp)
	builder.BaseImage = "alpine:latest"
	builder.Name = "test-vm" 
	
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
	
	os.RemoveAll(filepath.Join(tmp, "oci", "alpine:latest"))
}
