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
)

var activeManifest *mkimagepb.ImageYAML

// RegisterHandlers binds rtsst-specific Go functions to the sst.Exec registry.
func (b *BuildApp) RegisterHandlers() {
	sst.RegisterSysHandler("sys.provision_dataset", func(ctx context.Context, args []string) error {
		return b.execProvisionDataset(ctx, strings.Join(args, " "))
	})
	sst.RegisterSysHandler("sys.analyse_image", b.sysAnalyseImage)
	sst.RegisterSysHandler("sys.extract_boot_artifacts", b.sysExtractBootArtifacts)
	sst.RegisterSysHandler("sys.emit_image_yaml", b.sysEmitImageYaml)
	sst.RegisterSysHandler("sys.export_capsule", b.sysExportCapsule)
	sst.RegisterSysHandler("sys.export_runtime", func(ctx context.Context, args []string) error {
		return b.ExportRuntime(ctx)
	})
	sst.RegisterSysHandler("sys.finalize_instance", func(ctx context.Context, args []string) error {
		return b.FinalizeInstance(ctx)
	})
	sst.RegisterSysHandler("sys.finalize_vm", func(ctx context.Context, args []string) error {
		return b.FinalizeVM(ctx)
	})
}

func parseSysArgsArray(args []string) map[string]string {
	res := make(map[string]string)
	for _, p := range args {
		if strings.HasPrefix(p, "--") {
			kv := strings.SplitN(strings.TrimPrefix(p, "--"), "=", 2)
			if len(kv) == 2 {
				// Strip quotes from the key-value string so Go handles literal paths correctly
				res[kv[0]] = strings.Trim(kv[1], `"'`)
			} else {
				res[kv[0]] = "true"
			}
		}
	}
	return res
}

func (b *BuildApp) initActiveManifest() {
	if activeManifest == nil {
		activeManifest = &mkimagepb.ImageYAML{
			ApiVersion: "sst.io/v1",
			Kind:       "Image",
			Provenance: &mkimagepb.Provenance{
				Name:    b.Name,
				BuildId: time.Now().UTC().Format("20060102-150405"),
			},
			Storage: &mkimagepb.StoragePlan{},
			Boot:    &mkimagepb.BootStructure{},
		}
	}
}

func (b *BuildApp) sysAnalyseImage(ctx context.Context, args []string) error {
	b.initActiveManifest()
	parsed := parseSysArgsArray(args)
	diskPath := parsed["disk"]
	if diskPath == "" {
		return fmt.Errorf("sys.analyse_image requires --disk")
	}

	activeManifest.Storage.RawImage = filepath.Base(diskPath)

	sb := &ImageSB{
		Ctx:           ctx,
		Log:           b.Log,
		YamlObject:    activeManifest,
		SstRuntimeDir: b.RuntimeDir,
		DiskPath:      diskPath,
	}

	if err := AnalyseImg(sb); err != nil {
		b.Log.Warn(ctx, "sys.analyse_image returned non-standard layout", "error", err)
	}
	return nil
}

func (b *BuildApp) sysExtractBootArtifacts(ctx context.Context, args []string) error {
	b.initActiveManifest()
	parsed := parseSysArgsArray(args)
	outdir := parsed["outdir"]
	diskPath := parsed["disk"]
	if outdir == "" || diskPath == "" {
		return fmt.Errorf("sys.extract_boot_artifacts requires --outdir and --disk")
	}

	if activeManifest.Boot != nil && activeManifest.Boot.BootfsPartUuid != "" {
		sb := &ImageSB{
			Ctx:           ctx,
			Log:           b.Log,
			YamlObject:    activeManifest,
			SstRuntimeDir: b.RuntimeDir,
			DiskPath:      diskPath,
			OutDir:        outdir,
		}

		if err := ExtractArtifacts(sb); err != nil {
			b.Log.Warn(ctx, "sys.extract_boot_artifacts failed", "error", err)
		}
	}
	return nil
}

func (b *BuildApp) sysEmitImageYaml(ctx context.Context, args []string) error {
	b.initActiveManifest()
	parsed := parseSysArgsArray(args)
	outdir := parsed["outdir"]
	if outdir == "" {
		return fmt.Errorf("sys.emit_image_yaml requires --outdir")
	}

	yo := &sst.YamlObject{
		Path:   filepath.Join(outdir, "image.yaml"),
		Schema: "sst.yaml.mkimage.v1.ImageYAML",
		Kind:   "Image",
		Msg:    activeManifest,
	}
	if diags := yo.Save(ctx, b.Log); diags != nil && diags.HasAny() {
		return diags
	}
	return nil
}

func (b *BuildApp) sysExportCapsule(ctx context.Context, args []string) error {
	b.initActiveManifest()
	parsed := parseSysArgsArray(args)

	capsuleSrc := parsed["dir"]
	if capsuleSrc == "" {
		capsuleSrc = filepath.Join(filepath.Dir(b.RootDir), "capsule")
	}
	capsuleDst := filepath.Join(b.RuntimeDir, "images", b.Name)

	if err := os.MkdirAll(filepath.Dir(capsuleDst), 0755); err != nil {
		return err
	}

	// Always clear the destination first to prevent "directory not empty" collisions on retry
	_ = os.RemoveAll(capsuleDst)

	if err := os.Rename(capsuleSrc, capsuleDst); err != nil {
		// Go's os.Rename fails across filesystem boundaries (e.g. ZFS to macOS VirtioFS).
		// Fallback to a recursive deep copy and manual deletion.
		if strings.Contains(err.Error(), "cross-device link") || strings.Contains(err.Error(), "EXDEV") {
			b.Log.Info(ctx, "cross-device boundary detected, using deep copy fallback")
			if _, cpErr := sst.ExecShell(ctx, b.Log, "cp", "-a", capsuleSrc, capsuleDst); cpErr != nil {
				return fmt.Errorf("failed cross-device capsule copy: %w", cpErr)
			}
			_ = os.RemoveAll(capsuleSrc)
		} else {
			return fmt.Errorf("failed to move capsule: %w", err)
		}
	}

	UpdateCatalog(ctx, b.Log, "images", b.Name, capsuleDst, activeManifest.Provenance.BuildId)
	return nil
}
