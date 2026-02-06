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
	if os.Geteuid() != 0 {
		t.Skip("skipping test; requires root privileges for mounting")
	}

	log := sst.LogxRoot()
	tmp := t.TempDir()
	
	builder := &BuildApp{
		Log:     log,
		VMOUT:   "test-finalize",
		RootDir: filepath.Join(tmp, "rootfs"),
	}
	os.MkdirAll(filepath.Join(builder.RootDir, "etc"), 0755)
	os.WriteFile(filepath.Join(builder.RootDir, "etc/hostname"), []byte("test-vm"), 0644)

	err := builder.FinalizeVM(context.Background())
	assert.NoError(t, err)

	// Verify output
	outputDir := filepath.Join(Home(), ".runtimes", "images", "test-finalize")
	assert.FileExists(t, filepath.Join(outputDir, "test-finalize.raw"))
	assert.FileExists(t, filepath.Join(outputDir, "image.yaml"))
	
	os.RemoveAll(outputDir)
}
