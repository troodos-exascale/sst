package rtsst

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/swiss-software-tool/runtime-smart/go/internal/sst"
)

func TestDockerfileParser(t *testing.T) {
	content := `FROM debian:bookworm-slim
RUN: |
    apt-get update
    apt-get install -y linux-image-cloud-amd64
NAME: base-boot-debian` // FIX: Use NAME instead of VMOUT

	tmpfile, err := os.CreateTemp("", "Dockerfile.*")
	assert.NoError(t, err)
	defer os.Remove(tmpfile.Name())
	
	_, err = tmpfile.Write([]byte(content))
	assert.NoError(t, err)
	tmpfile.Close()

	log := sst.LogxRoot()
	
	// FIX: Provide a dummy RuntimeDir
	builder := NewBuilder(log, nil, tmpfile.Name(), "/tmp/dummy-runtimes")
	
	err = builder.Parse(context.Background())
	assert.NoError(t, err)

	err = builder.PreProcess(context.Background())
	assert.NoError(t, err)

	assert.Equal(t, "debian:bookworm-slim", builder.BaseImage)
	assert.Equal(t, "base-boot-debian", builder.Name) // FIX: Assert against Name
	
	foundRun := false
	for _, inst := range builder.Instructions {
		if inst.Type == "RUN" {
			assert.Contains(t, inst.Args, "apt-get update")
			foundRun = true
		}
	}
	assert.True(t, foundRun)
}
