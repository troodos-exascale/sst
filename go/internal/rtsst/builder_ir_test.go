package rtsst

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/swiss-software-tool/runtime-smart/go/internal/sst"
)

func TestIRParserAndDesugar(t *testing.T) {
	content := `FROM scratch
NAME: my-ir-test

# Legacy desugaring test
COPY ./my-app /opt/my-app

# Standard RUN fallback
RUN: echo "standard"

# Fully qualified IR tests
RUN [host]: echo "host only"
RUN [base, host, qcow2]: echo "build disk"
RUN [boot, chroot, aws, ami]: echo "install aws agents"
`

	tmpfile, err := os.CreateTemp("", "Dockerfile.ir.*")
	assert.NoError(t, err)
	defer os.Remove(tmpfile.Name())

	_, err = tmpfile.Write([]byte(content))
	assert.NoError(t, err)
	tmpfile.Close()

	log := sst.LogxRoot()
	builder := NewBuilder(log, nil, tmpfile.Name(), "/tmp")

	err = builder.Parse(context.Background())
	assert.NoError(t, err)

	// To avoid brittle array index counting, we find instructions by their unique content
	var copyInst, stdRunInst, hostRunInst, buildDiskInst, awsInst *Instruction

	for i := range builder.Instructions {
		inst := &builder.Instructions[i]
		if strings.Contains(inst.Args, "cp -a ./my-app") {
			copyInst = inst
		} else if strings.Contains(inst.Args, "standard") {
			stdRunInst = inst
		} else if strings.Contains(inst.Args, "host only") {
			hostRunInst = inst
		} else if strings.Contains(inst.Args, "build disk") {
			buildDiskInst = inst
		} else if strings.Contains(inst.Args, "install aws agents") {
			awsInst = inst
		}
	}

	assert.NotNil(t, copyInst, "Failed to find desugared COPY instruction")
	assert.Equal(t, "RUN", copyInst.Type)
	assert.Equal(t, "root", copyInst.Location)
	assert.Equal(t, "host", copyInst.Context)
	assert.Contains(t, copyInst.Args, "cp -a ./my-app \"$DST\"")

	assert.NotNil(t, stdRunInst, "Failed to find standard RUN instruction")
	assert.Equal(t, "RUN", stdRunInst.Type)
	assert.Equal(t, "root", stdRunInst.Location)
	assert.Equal(t, "chroot", stdRunInst.Context)
	assert.Equal(t, []string{"all"}, stdRunInst.Conditions)

	assert.NotNil(t, hostRunInst, "Failed to find RUN [host] instruction")
	assert.Equal(t, "RUN", hostRunInst.Type)
	assert.Equal(t, "root", hostRunInst.Location)
	assert.Equal(t, "host", hostRunInst.Context)
	assert.Equal(t, []string{"all"}, hostRunInst.Conditions)

	assert.NotNil(t, buildDiskInst, "Failed to find RUN [base, host, qcow2] instruction")
	assert.Equal(t, "RUN", buildDiskInst.Type)
	assert.Equal(t, "base", buildDiskInst.Location)
	assert.Equal(t, "host", buildDiskInst.Context)
	assert.Equal(t, []string{"qcow2"}, buildDiskInst.Conditions)

	assert.NotNil(t, awsInst, "Failed to find RUN [boot, chroot, aws, ami] instruction")
	assert.Equal(t, "RUN", awsInst.Type)
	assert.Equal(t, "boot", awsInst.Location)
	assert.Equal(t, "chroot", awsInst.Context)
	assert.ElementsMatch(t, []string{"aws", "ami"}, awsInst.Conditions)
}

func TestIRExecutionConditionMatching(t *testing.T) {
	builder := &BuildApp{
		Emits: []string{"oci", "qcow2"},
	}

	assert.True(t, builder.shouldExecute([]string{"all"}))
	assert.True(t, builder.shouldExecute([]string{"oci"}))
	assert.True(t, builder.shouldExecute([]string{"aws", "qcow2"}))
	assert.False(t, builder.shouldExecute([]string{"ami"}))
}

func TestIRParserInclude(t *testing.T) {
	tmpDir := t.TempDir()

	// FIX: Place the mock include exactly where the parser expects it (next to the main file)
	tmplPath := filepath.Join(tmpDir, "boot-efi.dockerfile")
	os.WriteFile(tmplPath, []byte("RUN [base, host, qcow2]: echo \"format disk\"\n"), 0644)

	localPath := filepath.Join(tmpDir, "local-setup.dockerfile")
	os.WriteFile(localPath, []byte("RUN: echo \"local setup\"\n"), 0644)

	mainPath := filepath.Join(tmpDir, "main.dockerfile")
	mainContent := `FROM scratch
NAME: include-test
INCLUDE boot-efi
INCLUDE ./local-setup.dockerfile
RUN: echo "main"`
	os.WriteFile(mainPath, []byte(mainContent), 0644)

	log := sst.LogxRoot()
	builder := NewBuilder(log, nil, mainPath, tmpDir)

	err := builder.Parse(context.Background())
	assert.NoError(t, err)

	assert.Equal(t, 5, len(builder.Instructions))
	
	assert.Equal(t, "FROM", builder.Instructions[0].Type)
	assert.Equal(t, "NAME", builder.Instructions[1].Type)
	
	assert.Equal(t, "RUN", builder.Instructions[2].Type)
	assert.Contains(t, builder.Instructions[2].Args, "format disk")
	
	assert.Equal(t, "RUN", builder.Instructions[3].Type)
	assert.Contains(t, builder.Instructions[3].Args, "local setup")
	
	assert.Equal(t, "RUN", builder.Instructions[4].Type)
	assert.Contains(t, builder.Instructions[4].Args, "main")
}
