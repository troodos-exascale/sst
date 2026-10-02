package rtsst

import (
	"context"
	"crypto/sha256"
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
	sst.RegisterSysHandler("sys.analyse_image", b.sysAnalyseImage)
	sst.RegisterSysHandler("sys.extract_boot_artifacts", b.sysExtractBootArtifacts)
	sst.RegisterSysHandler("sys.export_capsule", b.sysExportCapsule)
	
	// New Modular Export / Finalization Handlers
	sst.RegisterSysHandler("sys.build_image_state", b.sysBuildImageState)
	sst.RegisterSysHandler("sys.build_runtime_state", b.sysBuildRuntimeState)
	sst.RegisterSysHandler("sys.emit_image_yaml", b.sysEmitImageYaml)
	sst.RegisterSysHandler("sys.emit_instance_yaml", b.sysEmitInstanceYaml)
	sst.RegisterSysHandler("sys.copy_blueprints", b.sysCopyBlueprints)
	sst.RegisterSysHandler("sys.update_catalog", b.sysUpdateCatalog)
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
				Name:    b.Name,
				BuildId: time.Now().UTC().Format("20060102-150405"),
			},
			Storage: &mkimagepb.StoragePlan{},
			Boot:    &mkimagepb.BootStructure{},
		}
	} else if b.ActiveManifest.Provenance == nil {
		b.ActiveManifest.Provenance = &mkimagepb.Provenance{
			Name:    b.Name,
			BuildId: time.Now().UTC().Format("20060102-150405"),
		}
	}
}

func (b *BuildApp) initActiveInstance() {
	if b.ActiveInstance == nil {
		b.ActiveInstance = &runctlpb.Instance{
			ApiVersion: "sst.io/v1",
			Kind:       "Instance",
			Name:       b.Name,
		}
	} else if b.ActiveInstance.Name == "" {
		b.ActiveInstance.Name = b.Name
	}
}

func (b *BuildApp) sysAnalyseImage(ctx context.Context, args []string) error {
	b.initActiveManifest()
	parsed := parseSysArgsArray(args)
	diskPath := parsed["disk"]
	if diskPath == "" {
		return fmt.Errorf("sys.analyse_image requires --disk")
	}

	b.ActiveManifest.Storage.RawImage = filepath.Base(diskPath)

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

	if b.ActiveManifest.Boot != nil && b.ActiveManifest.Boot.BootfsPartUuid != "" {
		sb := &ImageSB{
			Ctx:           ctx,
			Log:           b.Log,
			YamlObject:    b.ActiveManifest,
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
	_ = os.RemoveAll(capsuleDst)

	if err := os.Rename(capsuleSrc, capsuleDst); err != nil {
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

	UpdateCatalog(ctx, b.Log, "images", b.Name, capsuleDst, b.ActiveManifest.Provenance.BuildId)
	return nil
}

func (b *BuildApp) sysBuildImageState(ctx context.Context, args []string) error {
	parsed := parseSysArgsArray(args)
	outdir := parsed["outdir"]
	if outdir == "" {
		return fmt.Errorf("sys.build_image_state requires --outdir")
	}
	return b.BuildImageState(ctx, outdir)
}

func (b *BuildApp) sysBuildRuntimeState(ctx context.Context, args []string) error {
	parsed := parseSysArgsArray(args)
	outdir := parsed["outdir"]
	kind := parsed["kind"]
	if outdir == "" || kind == "" {
		return fmt.Errorf("sys.build_runtime_state requires --outdir and --kind (Runtime or Instance)")
	}
	return b.BuildRuntimeState(ctx, outdir, kind)
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
		Msg:    b.ActiveManifest,
	}
	if diags := yo.Save(ctx, b.Log); diags != nil && diags.HasAny() {
		return diags
	}
	return nil
}

func (b *BuildApp) sysEmitInstanceYaml(ctx context.Context, args []string) error {
	b.initActiveInstance()
	parsed := parseSysArgsArray(args)
	outdir := parsed["outdir"]
	if outdir == "" {
		return fmt.Errorf("sys.emit_instance_yaml requires --outdir")
	}

	filename := "runtime.yaml"
	if b.ActiveInstance.Kind == "Instance" {
		filename = "instance.yaml"
	}

	yo := &sst.YamlObject{
		Path:   filepath.Join(outdir, filename),
		Schema: "sst.yaml.runctl.v1.Instance",
		Kind:   b.ActiveInstance.Kind,
		Msg:    b.ActiveInstance,
	}
	if diags := yo.Save(ctx, b.Log); diags != nil && diags.HasAny() {
		return diags
	}
	return nil
}

func (b *BuildApp) sysCopyBlueprints(ctx context.Context, args []string) error {
	parsed := parseSysArgsArray(args)
	outdir := parsed["outdir"]
	if outdir == "" {
		return fmt.Errorf("sys.copy_blueprints requires --outdir")
	}
	return b.CopyBlueprints(ctx, outdir)
}

func (b *BuildApp) sysUpdateCatalog(ctx context.Context, args []string) error {
	parsed := parseSysArgsArray(args)
	kind := parsed["kind"]
	outdir := parsed["outdir"]

	if kind == "" || outdir == "" {
		return fmt.Errorf("sys.update_catalog requires --kind and --outdir")
	}

	name := b.Name
	if kind == "runtimes" {
		lastEnv := b.getLastEnv()
		runtimeName := lastEnv["RUNTIME_NAME"]
		if runtimeName == "" { runtimeName = b.Name }
		baseImage := lastEnv["BASE_IMAGE"]
		if baseImage == "" { baseImage = b.BaseImage }
		name = filepath.Join(baseImage, runtimeName)
	}

	disambiguator := fmt.Sprintf("%x", sha256.Sum256([]byte(outdir)))[:8]
	if b.ActiveManifest != nil && b.ActiveManifest.Provenance != nil && b.ActiveManifest.Provenance.BuildId != "" {
		disambiguator = b.ActiveManifest.Provenance.BuildId
	}

	return UpdateCatalog(ctx, b.Log, kind, name, outdir, disambiguator)
}
