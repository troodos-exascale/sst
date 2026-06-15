package rtsst

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/troodos-exascale/sst/internal/sst"
)

func TestDockerfileParser(t *testing.T) {
	content := `FROM debian:bookworm-slim
RUN: |
    apt-get update
    apt-get install -y linux-image-cloud-amd64
NAME: base-boot-debian`

	tmpfile, err := os.CreateTemp("", "Dockerfile.*")
	assert.NoError(t, err)
	defer os.Remove(tmpfile.Name())

	_, err = tmpfile.Write([]byte(content))
	assert.NoError(t, err)
	tmpfile.Close()

	log := sst.LogxRoot()

	builder := NewBuilder(log, nil, tmpfile.Name(), ".", "/tmp/dummy-runtimes", "/tmp/dummy-runtimes")

	err = builder.Parse(context.Background())
	assert.NoError(t, err)

	err = builder.PreProcess(context.Background())
	assert.NoError(t, err)

	assert.Equal(t, "debian:bookworm-slim", builder.BaseImage)
	assert.Equal(t, "base-boot-debian", builder.Name)

	foundRun := false
	for _, inst := range builder.Instructions {
		if inst.Type == "RUN" && strings.Contains(inst.Args, "apt-get update") {
			foundRun = true
			break
		}
	}
	assert.True(t, foundRun, "Should have found a RUN instruction containing 'apt-get update'")
}

func TestDockerfileParser_OCIFields(t *testing.T) {
	content := `FROM alpine:latest
NAME: oci-fields-test
ENV APP_DIR=/opt/app
WORKDIR $APP_DIR
USER appuser:appgroup
CMD ["/bin/server", "--port", "8080"]
ENTRYPOINT ["/tini", "--"]
LABEL version="1.0"
EXPOSE 8080 9090/udp
VOLUME ["/data", "/config"]
ADD https://example.com/binary /usr/local/bin/
`

	tmpfile, err := os.CreateTemp("", "Dockerfile.oci.*")
	assert.NoError(t, err)
	defer os.Remove(tmpfile.Name())

	_, err = tmpfile.Write([]byte(content))
	assert.NoError(t, err)
	tmpfile.Close()

	log := sst.LogxRoot()

	builder := NewBuilder(log, nil, tmpfile.Name(), ".", t.TempDir(), t.TempDir())

	err = builder.Parse(context.Background())
	assert.NoError(t, err)

	// In the two-pass architecture, state mutations happen in PreProcess.
	// We no longer manually "execute" these metadata instructions.
	err = builder.PreProcess(context.Background())
	assert.NoError(t, err)

	var lastInst Instruction
	foundAdd := false
	for _, inst := range builder.Instructions {
		if inst.Type == "RUN" && strings.Contains(inst.Args, "curl -sSL 'https://example.com/binary'") {
			lastInst = inst
			foundAdd = true
			break
		}
	}

	assert.True(t, foundAdd, "ADD instruction should be desugared into a RUN curl command")
	
	// Verify that the pre-computed snapshot attached to the final instruction is correct
	assert.Equal(t, "/opt/app", lastInst.Workdir)
	assert.Equal(t, "appuser:appgroup", lastInst.User)

	// Verify global builder properties
	assert.Equal(t, `["/bin/server", "--port", "8080"]`, builder.Cmd)
	assert.Equal(t, `["/tini", "--"]`, builder.EntryPoint)
	assert.Equal(t, "1.0", builder.Labels["version"])
	assert.Contains(t, builder.ExposedPorts, "8080/tcp")
	assert.Contains(t, builder.ExposedPorts, "9090/udp")
	assert.Contains(t, builder.Volumes, "/data")
	assert.Contains(t, builder.Volumes, "/config")
}
