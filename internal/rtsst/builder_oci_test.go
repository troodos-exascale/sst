package rtsst

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/swiss-software-tool/sst/internal/sst"
)

func TestOCIWorkflowWithCrun(t *testing.T) {
	// Skip on non-Linux environments
	if runtime.GOOS != "linux" {
		t.Skip("skipping OCI fetch/verify test; requires a native Linux environment")
	}

	// Ensure crun is installed
	if _, err := exec.LookPath("crun"); err != nil {
		t.Skip("skipping test; crun not found in PATH")
	}

	log := sst.LogxRoot()
	tmp := t.TempDir()

	builder := NewBuilder(log, nil, "Dockerfile.test", tmp)
	builder.BaseImage = "alpine:latest"
	builder.Name = "test-vm"

	ctx := context.Background()

	// Step 1: Fetch and extract natively via crane
	err := builder.FetchAndExtractBase(ctx)
	require.NoError(t, err, "FetchAndExtractBase should succeed")

	// Step 2: Verify the extracted rootfs using crun.
	// We use the parent directory of b.RootDir as our OCI bundle directory.
	bundleDir := filepath.Dir(builder.RootDir)

	// Generate a default OCI config.json
	cmd := exec.Command("crun", "spec")
	cmd.Dir = bundleDir
	require.NoError(t, cmd.Run(), "failed to generate crun spec")

	// Modify config.json to run a specific command non-interactively
	specPath := filepath.Join(bundleDir, "config.json")
	specBytes, err := os.ReadFile(specPath)
	require.NoError(t, err)

	var spec map[string]interface{}
	require.NoError(t, json.Unmarshal(specBytes, &spec))

	// Override the default process execution
	process := spec["process"].(map[string]interface{})
	process["terminal"] = false
	process["args"] = []string{"/bin/cat", "/etc/os-release"}

	specBytes, _ = json.MarshalIndent(spec, "", "  ")
	require.NoError(t, os.WriteFile(specPath, specBytes, 0644))

	// Step 3: Run crun against our newly extracted rootfs
	runCmd := exec.Command("crun", "run", "sst-test-container")
	runCmd.Dir = bundleDir
	out, err := runCmd.CombinedOutput()

	require.NoError(t, err, "crun execution failed: %s", string(out))
	assert.Contains(t, string(out), "Alpine Linux", "crun did not return expected output from rootfs")
}
