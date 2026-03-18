package rtsst

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/swiss-software-tool/sst/internal/sst"
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

	builder := NewBuilder(log, nil, tmpfile.Name(), "/tmp/dummy-runtimes")

	err = builder.Parse(context.Background())
	assert.NoError(t, err)

	err = builder.PreProcess(context.Background())
	assert.NoError(t, err)

	assert.Equal(t, "debian:bookworm-slim", builder.BaseImage)
	assert.Equal(t, "base-boot-debian", builder.Name)

	foundRun := false
	for _, inst := range builder.Instructions {
		// FIX: We just check if ANY run instruction has our target string,
		// because from-oci.dockerfile injects its own RUN instructions now.
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

	builder := NewBuilder(log, nil, tmpfile.Name(), t.TempDir())

	err = builder.Parse(context.Background())
	assert.NoError(t, err)

	err = builder.PreProcess(context.Background())
	assert.NoError(t, err)

	for _, inst := range builder.Instructions {
		if inst.Type == "RUN" {
			continue
		}
		err := builder.ExecuteSingleInstruction(context.Background(), inst)
		assert.NoError(t, err)
	}

	assert.Equal(t, "/opt/app", builder.Workdir)
	assert.Equal(t, "appuser:appgroup", builder.User)
	assert.Equal(t, `["/bin/server", "--port", "8080"]`, builder.Cmd)
	assert.Equal(t, `["/tini", "--"]`, builder.EntryPoint)

	assert.Equal(t, "1.0", builder.Labels["version"])
	assert.Contains(t, builder.ExposedPorts, "8080/tcp")
	assert.Contains(t, builder.ExposedPorts, "9090/udp")
	assert.Contains(t, builder.Volumes, "/data")
	assert.Contains(t, builder.Volumes, "/config")

	foundAdd := false
	for _, inst := range builder.Instructions {
		// FIX: Updated assertion to match the actual -sSL flag emitted by desugarAdd
		if inst.Type == "RUN" && strings.Contains(inst.Args, "curl -sSL 'https://example.com/binary'") {
			foundAdd = true
			break
		}
	}
	assert.True(t, foundAdd, "ADD instruction should be desugared into a RUN curl command")
}
