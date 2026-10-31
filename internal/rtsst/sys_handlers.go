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
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/troodos-exascale/sst/internal/sst"
	mkimagepb "github.com/troodos-exascale/sst/proto/yaml/mkimage/v1"
	runctlpb "github.com/troodos-exascale/sst/proto/yaml/runctl/v1"
)

func (b *BuildApp) RegisterHandlers(runner *sst.Runner) {
	b.initActiveManifest()
	b.initActiveInstance()

	if runner != nil {
		runner.RegisterState("active_manifest", b.ActiveManifest)
		runner.RegisterState("active_instance", b.ActiveInstance)
	}

	sst.RegisterSysHandler("sys.provision_dataset", func(ctx context.Context, args []string) error {
		return b.execProvisionDataset(ctx, strings.Join(args, " "))
	})
	
	// Artifact Scanning & Metadata Injection
	sst.RegisterSysHandler("sys.analyse_image", b.sysAnalyseImage)
	sst.RegisterSysHandler("sys.set_boot_asset", b.sysSetBootAsset)

	// Capsule Lifecycle Handlers
	sst.RegisterSysHandler("sys.init_capsule", b.sysInitCapsule)
	sst.RegisterSysHandler("sys.finalize_capsule", b.sysFinalizeCapsule)
}

func parseSysArgsArray(args []string) map[string]string {
	res := make(map[string]string)
	for _, p := range args {
		if strings.HasPrefix(p, "--") {
			kv := strings.SplitN(strings.TrimPrefix(p, "--"), "=", 2)
			if len(kv) == 2 {
				res[kv[0]] = strings.Trim(kv[1], `"'`)
			} else {
				res[kv[0]] = "true"
			}
		}
	}
	return res
}

func (b *BuildApp) getLastEnv() map[string]string {
	if len(b.Instructions) > 0 {
		return b.Instructions[len(b.Instructions)-1].Env
	}
	return map[string]string{}
}

func (b *BuildApp) initActiveManifest() {
	if b.ActiveManifest == nil {
		b.ActiveManifest = &mkimagepb.ImageYAML{
			ApiVersion: "sst.io/v1",
			Kind:       "Image",
			Provenance: &mkimagepb.Provenance{
				BuildId: time.Now().UTC().Format("20060102-150405"),
			},
			Storage: &mkimagepb.StoragePlan{},
			Boot:    &mkimagepb.BootStructure{},
		}
	}
}

func (b *BuildApp) initActiveInstance() {
	if b.ActiveInstance == nil {
		b.ActiveInstance = &runctlpb.Instance{
			ApiVersion: "sst.io/v1",
			Kind:       "Instance",
		}
	}
}

// sysInitCapsule runs before any emit template. It creates the capsule directory and ZFS artefact dataset.
func (b *BuildApp) sysInitCapsule(ctx context.Context, args []string) error {
	parsed := parseSysArgsArray(args)
	target := parsed["target"]
	if target == "" {
		return fmt.Errorf("sys.init_capsule requires --target")
	}

	capsuleDir := filepath.Join(b.DatasetMgr.MountPath(""), "capsules", target)
	artefactsDir := filepath.Join(capsuleDir, fmt.Sprintf("capsule-%s-artifacts", target))

	// Anti-clobbering logic: check if it already exists in the global catalog
	destKind := "images" // Assume images for initial clobber check, overwritten on finalize if runtime
	catalogDir := filepath.Join(b.RuntimeDir, destKind, target)
	if _, err := os.Stat(catalogDir); err == nil && !b.Force {
		return fmt.Errorf("capsule '%s' already exists in catalog. Use --force to overwrite", catalogDir)
	}

	os.MkdirAll(capsuleDir, 0755)

	// If ZFS is active, map the artifacts subdirectory to an isolated dataset
	if b.DatasetMgr.Zpool != "" {
		ds := fmt.Sprintf("%s/capsules/%s-artifacts", b.DatasetMgr.datasetPath(""), target)
		res, err := sst.ExecShell(ctx, b.Log, "zfs", "create", "-p", "-o", "mountpoint="+artefactsDir, ds)
		if err != nil {
			if res != nil && strings.Contains(res.Stderr, "already exists") {
				// We are likely resuming a build (--cont); dataset exists safely
				b.Log.Debug(ctx, "capsule artifacts dataset already exists, continuing")
			} else {
				errMsg := ""
				if res != nil { errMsg = res.Stderr }
				return fmt.Errorf("failed to create artifacts dataset: %w - %s", err, errMsg)
			}
		}
	} else {
		os.MkdirAll(artefactsDir, 0755)
	}

	b.Log.Info(ctx, "initialized capsule staging environment", "capsule", target)
	return nil
}

// sysAnalyseImage probes an assembled disk block and injects its partitions/UUIDs into ActiveManifest.
func (b *BuildApp) sysAnalyseImage(ctx context.Context, args []string) error {
	b.initActiveManifest()
	parsed := parseSysArgsArray(args)
	diskPath := parsed["disk"]
	prefix := parsed["prefix"] // e.g. "capsule-name-artifacts/"

	if diskPath == "" {
		return fmt.Errorf("sys.analyse_image requires --disk")
	}

	// Update the path relative to the capsule root
	b.ActiveManifest.Storage.RawImage = prefix + filepath.Base(diskPath)

	sb := &ImageSB{
		Ctx:           ctx,
		Log:           b.Log,
		YamlObject:    b.ActiveManifest,
		SstRuntimeDir: b.RuntimeDir,
		DiskPath:      diskPath,
	}

	if err := AnalyseImg(sb); err != nil {
		b.Log.Warn(ctx, "sys.analyse_image returned non-standard layout", "error", err)
	}

	// Mid-flight: Scrape OS Release info and bind it immediately
	if sb.YamlObject.Storage.RootfsUuid != "" {
		loopDev, err := attachLoop(ctx, b.Log, diskPath)
		if err == nil {
			rootLoopDev, err := resolvePartitionByUUID(ctx, b.Log, loopDev, sb.YamlObject.Storage.RootfsUuid)
			if err == nil && rootLoopDev != "" {
				rootMnt, err := mountPartition(ctx, b.Log, rootLoopDev)
				if err == nil {
					osInfo, err := ParseOSRelease(rootMnt)
					if err == nil {
						sb.YamlObject.Provenance.UpstreamOs = GenerateSemanticName(osInfo)
						sb.YamlObject.Provenance.UpstreamBuildId = osInfo["BUILD_ID"]
					}
					unmountPartition(ctx, b.Log, rootMnt)
				}
			}
			detachLoop(ctx, b.Log, loopDev)
		}
	}

	return nil
}

// sysSetBootAsset explicitly links a kernel or initrd to the ActiveManifest.
func (b *BuildApp) sysSetBootAsset(ctx context.Context, args []string) error {
	b.initActiveManifest()
	parsed := parseSysArgsArray(args)
	
	assetType := parsed["type"]
	path := parsed["path"]
	
	if assetType == "kernel" {
		b.ActiveManifest.Boot.Kernel = path
	} else if assetType == "initrd" {
		b.ActiveManifest.Boot.Initrd = path
	}
	return nil
}

// sysFinalizeCapsule serializes the YAML, copies blueprints, and atomically promotes the ZFS dataset.
func (b *BuildApp) sysFinalizeCapsule(ctx context.Context, args []string) error {
	parsed := parseSysArgsArray(args)
	target := parsed["target"]
	formats := strings.Split(parsed["formats"], "|")

	if target == "" {
		return fmt.Errorf("sys.finalize_capsule requires --target")
	}

	capsuleDir := filepath.Join(b.DatasetMgr.MountPath(""), "capsules", target)

	isRuntime := false
	for _, f := range formats {
		if f == "runtime" || f == "instance" {
			isRuntime = true
		}
	}

	// 1. Serialize YAML
	if isRuntime {
		b.initActiveInstance()
		b.ActiveInstance.Name = target
		// Instances don't have deeply nested structural paths yet, but if they did, we'd prefix them here.
		
		yo := &sst.YamlObject{
			Path:   filepath.Join(capsuleDir, "instance.yaml"),
			Schema: "sst.yaml.runctl.v1.Instance",
			Kind:   "Instance",
			Msg:    b.ActiveInstance,
		}
		if diags := yo.Save(ctx, b.Log); diags != nil && diags.HasAny() {
			return diags
		}
	} else {
		b.initActiveManifest()
		b.ActiveManifest.Provenance.Name = target
		b.ActiveManifest.Provenance.BuildId = b.BuildID
		
		yo := &sst.YamlObject{
			Path:   filepath.Join(capsuleDir, "image.yaml"),
			Schema: "sst.yaml.mkimage.v1.ImageYAML",
			Kind:   "Image",
			Msg:    b.ActiveManifest,
		}
		if diags := yo.Save(ctx, b.Log); diags != nil && diags.HasAny() {
			return diags
		}
	}

	// 2. Copy Blueprints
	blueprintDir := filepath.Join(capsuleDir, "blueprint")
	os.MkdirAll(blueprintDir, 0755)

	if b.Dockerfile != "" {
		if data, err := os.ReadFile(b.Dockerfile); err == nil {
			lines := strings.Split(string(data), "\n")
			var newLines []string
			newLines = append(newLines, fmt.Sprintf("# CANONICAL_NAME: %s", target))
			newLines = append(newLines, fmt.Sprintf("# LAST_BUILD: %s", b.BuildID))

			for _, line := range lines {
				if strings.HasPrefix(line, "# CANONICAL_NAME:") || strings.HasPrefix(line, "# LAST_BUILD:") {
					continue
				}
				newLines = append(newLines, line)
			}
			destPath := filepath.Join(blueprintDir, filepath.Base(b.Dockerfile))
			_ = os.WriteFile(destPath, []byte(strings.Join(newLines, "\n")), 0644)
		}
	}

	for relPath, absPath := range b.IncludedFiles {
		targetRel := relPath
		if !strings.HasSuffix(targetRel, ".dockerfile") {
			targetRel += ".dockerfile"
		}
		destPath := filepath.Join(blueprintDir, targetRel)
		os.MkdirAll(filepath.Dir(destPath), 0755)
		if data, err := os.ReadFile(absPath); err == nil {
			_ = os.WriteFile(destPath, data, 0644)
		}
	}

	// 3. Atomic Promotion
	destKind := "images"
	if isRuntime {
		destKind = "instances"
	}
	
	if err := b.DatasetMgr.PromoteCapsule(target, capsuleDir, destKind); err != nil {
		return fmt.Errorf("failed to promote capsule: %w", err)
	}

	b.Log.Info(ctx, "✅ Capsule Exported Successfully", "name", target, "kind", destKind)
	return nil
}
