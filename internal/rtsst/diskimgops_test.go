package rtsst

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/troodos-exascale/sst/internal/sst"
	mkimagepb "github.com/troodos-exascale/sst/proto/yaml/mkimage/v1"
)

func TestImageSB_Paths(t *testing.T) {
	sb := &ImageSB{
		Ctx:        context.Background(),
		Log:        sst.LogxRoot(),
		YamlObject: &mkimagepb.ImageYAML{
			Provenance: &mkimagepb.Provenance{Name: "test-img"},
			Storage:    &mkimagepb.StoragePlan{RawImage: "test.raw"},
		},
		SstRuntimeDir: "/default/catalog",
	}

	// Test default fallbacks (simulate post-build VM runtime lookup)
	assert.Equal(t, "/default/catalog/images/test-img/test.raw", sb.getRawImagePath())
	assert.Equal(t, "/default/catalog/images/test-img", sb.getOutDir())

	// Test explicit overrides (simulate in-flight ZFS build dataset)
	sb.DiskPath = "/tmp/builds/capsule/disk.raw"
	sb.OutDir = "/tmp/builds/capsule"

	assert.Equal(t, "/tmp/builds/capsule/disk.raw", sb.getRawImagePath())
	assert.Equal(t, "/tmp/builds/capsule", sb.getOutDir())
}
