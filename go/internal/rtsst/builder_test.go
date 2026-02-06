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
VMOUT: base-boot-debian`

	tmpfile, err := os.CreateTemp("", "Dockerfile.*")
	assert.NoError(t, err)
	defer os.Remove(tmpfile.Name())
	
	_, err = tmpfile.Write([]byte(content))
	assert.NoError(t, err)
	tmpfile.Close()

	log := sst.LogxRoot()
	builder := NewBuilder(log, nil, tmpfile.Name())
	err = builder.Parse(context.Background())

	assert.NoError(t, err)
	assert.Equal(t, "debian:bookworm-slim", builder.BaseImage)
	assert.Equal(t, "base-boot-debian", builder.VMOUT)
	
	foundRun := false
	for _, inst := range builder.Instructions {
		if inst.Type == "RUN" {
			assert.Contains(t, inst.Args, "apt-get update")
			foundRun = true
		}
	}
	assert.True(t, foundRun)
}
