// Copyright 2025-2026 Peter Braam
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

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
