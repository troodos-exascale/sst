package rtsst

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/swiss-software-tool/runtime-smart/go/internal/sst"
)

func TestFinalizeVM(t *testing.T) {
	// Re-adding the root check because while image generation is rootless,
	// the subsequent AnalyseImg step (which attaches NBD) still requires root.
	if os.Geteuid() != 0 {
		t.Skip("skipping test; requires root privileges for NBD analysis")
	}

	log := sst.LogxRoot()
	tmp := t.TempDir()
	
	builder := &BuildApp{
		Log:        log,
		Name:       "test-finalize", // FIX: Replaced VMOUT
		RootDir:    filepath.Join(tmp, "rootfs"),
		RuntimeDir: filepath.Join(tmp, ".runtimes"), // FIX: explicitly set RuntimeDir
		Emits:      []string{"raw"}, // FIX: explicitly set an EMIT
	}
	os.MkdirAll(filepath.Join(builder.RootDir, "etc"), 0755)
	os.WriteFile(filepath.Join(builder.RootDir, "etc/hostname"), []byte("test-vm"), 0644)

	err := builder.FinalizeVM(context.Background())
	assert.NoError(t, err)

	// Verify output uses the mock RuntimeDir
	outputDir := filepath.Join(builder.RuntimeDir, "images", "test-finalize")
	assert.FileExists(t, filepath.Join(outputDir, "test-finalize.raw"))
	assert.FileExists(t, filepath.Join(outputDir, "image.yaml"))
}
