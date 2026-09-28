package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestE2E_Mkimg_RuntimeExport(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("Skipping mkimg E2E test; requires root to invoke isolated build environments")
	}

	tmpDir := t.TempDir()
	binPath := filepath.Join(tmpDir, "mkimg")

	t.Log("Compiling mkimg binary for E2E test...")
	if err := exec.Command("go", "build", "-o", binPath, ".").Run(); err != nil {
		t.Fatalf("failed to build mkimg: %v", err)
	}

	// 1. Scaffold an isolated catalog environment
	isolatedRuntimesDir := filepath.Join(tmpDir, ".runtimes")
	imageName := "dummy-base-os"
	runtimeName := "db-node-01"

	// Create a dummy base image so the builder has something to pivot FROM
	targetRuntimeDir := filepath.Join(isolatedRuntimesDir, "runtimes", imageName, runtimeName)
	if err := os.MkdirAll(targetRuntimeDir, 0755); err != nil {
		t.Fatalf("failed to scaffold target directory: %v", err)
	}

	// 2. Define the runtime.dockerfile directly in the target directory
	dockerfileContent := `
FROM ` + imageName + `
NAME: ` + runtimeName + `
# Minimal shell validation to prove [chroot] AST executes
RUN [chroot]: echo "hello from test runtime build" > /etc/sst_test_marker
EMIT: runtime:ext4:20G
`
	dockerfilePath := filepath.Join(targetRuntimeDir, "runtime.dockerfile")
	if err := os.WriteFile(dockerfilePath, []byte(dockerfileContent), 0644); err != nil {
		t.Fatal(err)
	}

	// 3. Execute the compiled binary natively
	t.Logf("Running: mkimg runtime %s %s", imageName, runtimeName)
	cmd := exec.Command(binPath, "--dir", isolatedRuntimesDir, "runtime", imageName, runtimeName)

	// Disable interactive terminal expectations
	cmd.Env = append(os.Environ(), "SST_LOG_FORMAT=text")

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		t.Fatalf("mkimg runtime execution failed: %v\nStdout: %s\nStderr: %s", err, stdout.String(), stderr.String())
	}

	// 4. Verify the Data Capsule (The Output Boundary)
	t.Log("Validating the exported Data Capsule...")

	// Verify the physical sparse disk exported
	diskPath := filepath.Join(targetRuntimeDir, "disk.raw")
	if _, err := os.Stat(diskPath); os.IsNotExist(err) {
		t.Errorf("FATAL: sparse disk.raw was not exported to %s", diskPath)
	}

	// Verify the YAML descriptor hydrated
	yamlPath := filepath.Join(targetRuntimeDir, "runtime.yaml")
	yamlData, err := os.ReadFile(yamlPath)
	if err != nil {
		t.Fatalf("FATAL: runtime.yaml descriptor was not written: %v", err)
	}

	yamlStr := string(yamlData)
	if !strings.Contains(yamlStr, "kind: Runtime") {
		t.Errorf("runtime.yaml does not contain 'kind: Runtime'")
	}
	if !strings.Contains(yamlStr, "name: "+runtimeName) {
		t.Errorf("runtime.yaml does not contain correct name property")
	}
	if !strings.Contains(yamlStr, "disk.raw") {
		t.Errorf("runtime.yaml does not register disk.raw in rwdisk array")
	}

	t.Log("✅ E2E Data Capsule exported successfully")
}
