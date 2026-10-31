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

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	sst "github.com/troodos-exascale/sst/internal/sst"
	storagepb "github.com/troodos-exascale/sst/proto/yaml/storage/v1"
)

func TestComposeStoragePlan(t *testing.T) {
	tmp := t.TempDir()

	hwPath := filepath.Join(tmp, "hardware.yaml")
	outPath := filepath.Join(tmp, "storage-layout.yaml")

	// Minimal hardware probe with two block devices
	writeFile(t, hwPath, `
api_version: sst.io/v1
kind: HardwareProbe
spec:
  block_devices:
    - name: disk0
      host_path: /dev/sda
    - name: disk1
      host_path: /dev/sdb
`)

	app := &storagePlanApp{
		nodeFile:      hwPath,
		remainderSpec: "pool0,mirror",
		filesetNames:  []string{"data"},
		outputFile:    outPath,
		methods:       newStoragePlanMethods(),
	}

	if err := app.methods.LoadProbe(app); err != nil {
		t.Fatalf("LoadProbe failed: %v", err)
	}
	if err := app.methods.BuildLayout(app); err != nil {
		t.Fatalf("BuildLayout failed: %v", err)
	}
	if err := app.methods.WriteOutput(app); err != nil {
		t.Fatalf("WriteOutput failed: %v", err)
	}

	// Load the generated storage-layout.yaml via YamlObject
	layout := &storagepb.StorageLayout{}
	yo := &sst.YamlObject{
		Path:   outPath,
		Schema: "sst.yaml.storage.v1.StorageLayout",
		Kind:   "StorageLayout",
		Msg:    layout,
	}

	if diags := yo.Load(context.Background(), sst.LogxRoot()); diags != nil {
		t.Fatalf("loading output failed: %v", diags.Error())
	}

	if len(layout.Spec.Pools) != 1 {
		t.Fatalf("expected 1 pool, got %d", len(layout.Spec.Pools))
	}
	if len(layout.Spec.Devices) != 2 {
		t.Fatalf("expected 2 devices, got %d", len(layout.Spec.Devices))
	}
	if len(layout.Spec.Filesystems) != 1 {
		t.Fatalf("expected 1 filesystem, got %d", len(layout.Spec.Filesystems))
	}

	fs := layout.Spec.Filesystems[0]
	if fs.Name != "data-data" {
		t.Fatalf("unexpected filesystem name: %s", fs.Name)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("writeFile failed: %v", err)
	}
}
