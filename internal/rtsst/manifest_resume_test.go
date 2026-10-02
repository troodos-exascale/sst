package rtsst

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/troodos-exascale/sst/internal/sst"
)

func TestManifestResumption(t *testing.T) {
	tmpDir := t.TempDir()
	statePath := filepath.Join(tmpDir, "state.json")

	log := sst.LogxRoot()
	ctx := context.Background()

	// ---------------------------------------------------------
	// Run 1: Initialization and Memory Mutation
	// ---------------------------------------------------------
	r1 := &sst.Runner{
		Log:       log,
		StatePath: statePath,
		State:     make(map[string]interface{}),
		Completed: make(map[string]bool),
	}

	b1 := NewBuilder(log, nil, "dummy.dockerfile", tmpDir, tmpDir, tmpDir)
	b1.Name = "test-image"
	
	// Registers b1.ActiveManifest into the Runner's persistence lifecycle
	b1.RegisterHandlers(r1) 

	// Simulate a task executing `sys.analyse_image`
	step1 := &sst.Context{
		ID:          "step1-hash",
		Description: "Mutate Manifest",
		Action: func(c context.Context) (interface{}, error) {
			b1.ActiveManifest.Storage.RawImage = "disk-modified.raw"
			b1.ActiveManifest.Provenance.UpstreamOs = "ubuntu-24.04-test"
			return nil, nil
		},
	}

	// Execution triggers completion and atomic serialization to state.json
	err := r1.Run(ctx, step1)
	assert.NoError(t, err)

	_, err = os.Stat(statePath)
	assert.NoError(t, err, "state.json should have been written to disk")

	// ---------------------------------------------------------
	// Run 2: Simulating Process Restart / Time-Travel --cont
	// ---------------------------------------------------------
	r2 := &sst.Runner{
		Log:       log,
		StatePath: statePath,
		State:     make(map[string]interface{}),
		Completed: make(map[string]bool),
	}

	// Create a completely new memory environment
	b2 := NewBuilder(log, nil, "dummy.dockerfile", tmpDir, tmpDir, tmpDir)
	b2.Name = "test-image"
	
	// Register the NEW pointer to the NEW runner BEFORE loading state
	b2.RegisterHandlers(r2) 

	// Read state.json, which uses the registry to dynamically unmarshal
	// the JSON directly into the typed mkimagepb.ImageYAML structure.
	err = r2.LoadState()
	assert.NoError(t, err)

	// Assert the Protobuf struct was rehydrated seamlessly!
	assert.Equal(t, "disk-modified.raw", b2.ActiveManifest.Storage.RawImage, "RawImage string should be restored cleanly")
	assert.Equal(t, "ubuntu-24.04-test", b2.ActiveManifest.Provenance.UpstreamOs, "UpstreamOs string should be restored cleanly")
}
