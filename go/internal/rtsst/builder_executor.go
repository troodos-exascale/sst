package rtsst

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/swiss-software-tool/runtime-smart/go/internal/sst"
	mkimagepb "github.com/swiss-software-tool/runtime-smart/go/proto/yaml/mkimage/v1"
)

func (b *BuildApp) ExecuteSingleInstruction(ctx context.Context, inst Instruction) error {
	args := b.Substitute(inst.Args)
	
	cleanArgs := args
	if inst.Type != "RUN" {
		cleanArgs = strings.SplitN(args, " #", 2)[0]
		cleanArgs = strings.TrimSpace(cleanArgs)
	}

	switch inst.Type {
	case "ARG":
		parts := strings.SplitN(cleanArgs, "=", 2)
		if len(parts) == 2 {
			b.Args[parts[0]] = parts[1]
		} else if val, ok := b.BuildArgs[parts[0]]; ok {
			b.Args[parts[0]] = val
		}
		b.Log.Info(ctx, "processed ARG", "key", parts[0])
		return nil
	case "ENV":
		parts := strings.SplitN(cleanArgs, "=", 2)
		if len(parts) == 2 {
			b.Env[parts[0]] = parts[1]
		} else {
			parts = strings.SplitN(cleanArgs, " ", 2)
			if len(parts) == 2 {
				b.Env[parts[0]] = parts[1]
			}
		}
		b.Log.Info(ctx, "processed ENV", "key", parts[0])
		return nil
	case "FROM":
		b.Args = make(map[string]string)
		for k, v := range b.BuildArgs {
			b.Args[k] = v
		}
		return nil
	case "NAME", "VMOUT", "EMIT":
		return nil
	case "EXPOSE", "CMD", "LABEL", "VOLUME", "USER", "WORKDIR":
		b.Log.Info(ctx, "ignoring instruction (unnecessary for VM building)", "type", inst.Type, "args", cleanArgs)
		return nil
	case "COPY":
		return b.execCopy(ctx, cleanArgs)
	case "RUN":
		return b.execRun(ctx, args)
	default:
		return fmt.Errorf("unknown instruction type: %s", inst.Type)
	}
}

func (b *BuildApp) execCopy(ctx context.Context, args string) error {
	parts := strings.Fields(args)
	if len(parts) < 2 {
		return fmt.Errorf("invalid COPY instruction: %s", args)
	}

	dst := parts[len(parts)-1]
	srcs := parts[:len(parts)-1]

	dstPath := filepath.Join(b.RootDir, strings.TrimPrefix(dst, "/"))

	if err := os.MkdirAll(filepath.Dir(dstPath), 0755); err != nil {
		return err
	}

	for _, src := range srcs {
		b.Log.Info(ctx, "executing COPY", "src", src, "dst", dstPath)
		
		target := dstPath
		if strings.HasSuffix(dst, "/") || len(srcs) > 1 {
			os.MkdirAll(dstPath, 0755)
			target = filepath.Join(dstPath, filepath.Base(src))
		}
		
		if _, err := sst.ExecShell(ctx, b.Log, "cp", "-a", src, target); err != nil {
			return fmt.Errorf("COPY failed: %w", err)
		}
	}
	return nil
}

func (b *BuildApp) execRun(ctx context.Context, cmdStr string) error {
	b.Log.Info(ctx, "executing RUN in chroot", "command", cmdStr)
	
	mounts := []string{"/dev", "/dev/pts", "/proc", "/sys"}
	for _, m := range mounts {
		target := filepath.Join(b.RootDir, m)
		os.MkdirAll(target, 0755)
		sst.ExecShell(ctx, b.Log, "mount", "--bind", m, target)
	}
	
	defer func() {
		for i := len(mounts)-1; i >= 0; i-- {
			target := filepath.Join(b.RootDir, mounts[i])
			sst.ExecShell(context.Background(), b.Log, "umount", "-l", target)
		}
	}()

	sst.ExecShell(ctx, b.Log, "cp", "-L", "/etc/resolv.conf", filepath.Join(b.RootDir, "etc/resolv.conf"))

	var envs []string
	for k, v := range b.Env {
		envs = append(envs, k+"="+v)
	}

	req := sst.ExecRequest{
		Cmd:  "chroot",
		Args: []string{b.RootDir, "/bin/sh", "-c", cmdStr},
		Env:  envs,
		Mode: sst.ExecCaptured,
	}
	_, err := sst.Exec(ctx, b.Log, req)
	if err != nil {
		return fmt.Errorf("chroot command failed: %w", err)
	}
	return nil
}

func (b *BuildApp) FinalizeVM(ctx context.Context) error {
	b.ImageOutput = filepath.Join(b.RuntimeDir, "images", b.Name)
	if err := os.MkdirAll(b.ImageOutput, 0755); err != nil {
		return err
	}

	rawImagePath := filepath.Join(b.ImageOutput, b.Name+".raw")
	b.Log.Info(ctx, "creating deterministic ext4 foundation", "path", rawImagePath)

	if err := CreateSparse(rawImagePath, 4*1024*1024*1024); err != nil {
		return err
	}

	fixedUUID := "12345678-1234-1234-1234-123456789abc"
	req := sst.ExecRequest{
		Cmd: "mke2fs",
		Args: []string{
			"-t", "ext4", // FIX: Changed from ext2 to ext4 so AnalyseImg detects it
			"-d", b.RootDir,
			"-U", fixedUUID,
			"-E", "hash_seed=" + fixedUUID,
			"-F",
			rawImagePath,
		},
		Env:  []string{"SOURCE_DATE_EPOCH=0"},
		Mode: sst.ExecCaptured,
	}

	if _, err := sst.Exec(ctx, b.Log, req); err != nil {
		return fmt.Errorf("deterministic mke2fs failed: %w", err)
	}

	sb := &ImageSB{
		Ctx: ctx,
		Log: b.Log, 
		YamlObject: &mkimagepb.ImageYAML{
			Name:     b.Name,
			RawImage: b.Name + ".raw",
		},
		SstRuntimeDir: b.RuntimeDir,
	}
	
	_ = AnalyseImg(sb)
	if sb.YamlObject.BootfsDevice != "" {
		_ = ExtractKernelAndInitrdFromBootPartition(sb)
	}

	for _, emit := range b.Emits {
		b.Log.Info(ctx, "processing EMIT directive", "format", emit)

		switch emit {
		case "raw":
		case "qcow2":
			qcowPath := filepath.Join(b.ImageOutput, b.Name+".qcow2")
			if _, err := sst.ExecShell(ctx, b.Log, "qemu-img", "convert", "-f", "raw", "-O", "qcow2", rawImagePath, qcowPath); err != nil {
				return fmt.Errorf("failed to convert to qcow2: %w", err)
			}
		case "oci":
			ociPath := filepath.Join(b.ImageOutput, b.Name+".tar")
			
			tmpDir, err := os.MkdirTemp("", "sst-oci-*")
			if err != nil {
				return fmt.Errorf("failed to create oci temp dir: %w", err)
			}
			defer os.RemoveAll(tmpDir)

			layerTar := filepath.Join(tmpDir, "layer.tar")
			tarReq := sst.ExecRequest{
				Cmd: "tar",
				Args: []string{
					"--sort=name",
					"--mtime=@0",
					"--owner=0",
					"--group=0",
					"--numeric-owner",
					"-cf", layerTar,
					"-C", b.RootDir,
					".",
				},
				Mode: sst.ExecCaptured,
			}
			res, err := sst.Exec(ctx, b.Log, tarReq)
			if err != nil {
				if res != nil && res.ExitCode == 1 {
					b.Log.Warn(ctx, "tar returned exit code 1 (warnings), treating as success", "stderr", res.Stderr)
				} else {
					return fmt.Errorf("failed to create layer tarball: %w", err)
				}
			}

			layerHash, err := SHA256File(layerTar)
			if err != nil {
				return fmt.Errorf("failed to hash layer.tar: %w", err)
			}

			configContent := fmt.Sprintf(`{
  "architecture": "%s",
  "os": "linux",
  "config": {
    "Env": ["PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"]
  },
  "rootfs": {
    "type": "layers",
    "diff_ids": ["sha256:%s"]
  },
  "history": [{"created": "1970-01-01T00:00:00Z", "created_by": "sst mkimage"}]
}`, runtime.GOARCH, layerHash)

			h := sha256.New()
			h.Write([]byte(configContent))
			configHash := hex.EncodeToString(h.Sum(nil))
			configFile := configHash + ".json"

			if err := os.WriteFile(filepath.Join(tmpDir, configFile), []byte(configContent), 0644); err != nil {
				return err
			}

			manifestContent := fmt.Sprintf(`[
  {
    "Config": "%s",
    "RepoTags": ["%s:latest"],
    "Layers": ["layer.tar"]
  }
]`, configFile, b.Name)

			if err := os.WriteFile(filepath.Join(tmpDir, "manifest.json"), []byte(manifestContent), 0644); err != nil {
				return err
			}

			tarReq2 := sst.ExecRequest{
				Cmd: "tar",
				Args: []string{
					"--sort=name",
					"--mtime=@0",
					"--owner=0",
					"--group=0",
					"--numeric-owner",
					"-cf", ociPath,
					"-C", tmpDir,
					"manifest.json",
					configFile,
					"layer.tar",
				},
				Mode: sst.ExecCaptured,
			}
			if _, err := sst.Exec(ctx, b.Log, tarReq2); err != nil {
				return fmt.Errorf("failed to create final docker archive: %w", err)
			}

		default:
			b.Log.Warn(ctx, "unsupported EMIT format", "format", emit)
		}
	}
	
	yo := &sst.YamlObject{
		Path:   filepath.Join(b.ImageOutput, "image.yaml"),
		Schema: "sst.yaml.mkimage.v1.ImageYAML",
		Kind:   "Image",
		Msg:    sb.YamlObject,
	}
	
	if diags := yo.Save(ctx, b.Log); diags != nil && diags.HasAny() {
		return diags
	}
	return nil
}
