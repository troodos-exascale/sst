package rtsst

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/swiss-software-tool/runtime-smart/go/internal/sst"
	mkimagepb "github.com/swiss-software-tool/runtime-smart/go/proto/yaml/mkimage/v1"
)

// shouldExecute evaluates if the instruction's conditions match the active EMIT targets.
func (b *BuildApp) shouldExecute(conditions []string) bool {
	for _, cond := range conditions {
		if cond == "all" {
			return true
		}
		for _, emit := range b.Emits {
			if emit == cond {
				return true
			}
		}
	}
	return false
}

func (b *BuildApp) ExecuteSingleInstruction(ctx context.Context, inst Instruction) error {
	var args string

	if inst.Type == "RUN" {
		args = inst.Args
	} else {
		args = b.Substitute(inst.Args)
	}
	
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
	case "WORKDIR":
		b.Workdir = b.Substitute(cleanArgs)
		b.Log.Info(ctx, "processed WORKDIR", "dir", b.Workdir)
		return nil
	case "USER":
		b.User = b.Substitute(cleanArgs)
		b.Log.Info(ctx, "processed USER", "user", b.User)
		return nil
	case "CMD":
		b.Cmd = b.Substitute(cleanArgs)
		b.Log.Info(ctx, "processed CMD", "cmd", b.Cmd)
		return nil
	case "ENTRYPOINT":
		b.EntryPoint = b.Substitute(cleanArgs)
		b.Log.Info(ctx, "processed ENTRYPOINT", "entrypoint", b.EntryPoint)
		return nil
	case "LABEL":
		parts := strings.SplitN(cleanArgs, "=", 2)
		if len(parts) == 2 {
			// Strip quotes if they exist
			b.Labels[strings.Trim(parts[0], `"`)] = strings.Trim(parts[1], `"`)
		}
		b.Log.Info(ctx, "processed LABEL", "label", parts[0])
		return nil
	case "EXPOSE":
		for _, port := range strings.Fields(cleanArgs) {
			// OCI expects "port/tcp" or "port/udp". Default to tcp.
			if !strings.Contains(port, "/") { port += "/tcp" }
			b.ExposedPorts[port] = struct{}{}
		}
		b.Log.Info(ctx, "processed EXPOSE", "ports", cleanArgs)
		return nil
	case "VOLUME":
		// Handle both `VOLUME ["/data"]` and `VOLUME /data`
		clean := strings.TrimPrefix(strings.TrimSuffix(cleanArgs, "]"), "[")
		for _, vol := range strings.Split(clean, ",") {
			// FIX: Trim spaces first, then trim quotes!
			b.Volumes[strings.Trim(strings.TrimSpace(vol), `"`)] = struct{}{}
		}
		b.Log.Info(ctx, "processed VOLUME", "volumes", cleanArgs)
		return nil
	case "FROM", "NAME", "VMOUT", "EMIT":
		return nil // Handled in PreProcess
	case "COPY", "ADD":
		// COPY and ADD are natively desugared into RUN [root, host] by the parser.
		return nil
	case "RUN":
		if !b.shouldExecute(inst.Conditions) {
			b.Log.Info(ctx, "skipping instruction (condition mismatch)", "conditions", inst.Conditions, "emits", b.Emits)
			return nil
		}

		// Resolve Location
		targetDir := b.RootDir // default "root"
		if inst.Location == "base" {
			targetDir = filepath.Dir(b.RootDir)
		} else if inst.Location == "boot" {
			targetDir = filepath.Join(filepath.Dir(b.RootDir), "boot")
		}

		if inst.Context == "host" {
			return b.execHostRun(ctx, args, targetDir)
		}
		return b.execChrootRun(ctx, args, targetDir)
	default:
		return fmt.Errorf("unknown instruction type: %s", inst.Type)
	}
}

// execHostRun executes directly on the builder host OS without chroot.
func (b *BuildApp) execHostRun(ctx context.Context, cmdStr string, targetDir string) error {
	b.Log.Info(ctx, "executing HOST_RUN", "dir", targetDir)

	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return err
	}

	var envs []string
	for k, v := range b.Args { envs = append(envs, k+"="+v) }
	for k, v := range b.Env { envs = append(envs, k+"="+v) }
	
	// Inject IR specific environment variables
	envs = append(envs, "ROOTFS="+b.RootDir)
	envs = append(envs, "BASEDIR="+filepath.Dir(b.RootDir))
	envs = append(envs, "BOOTDIR="+filepath.Join(filepath.Dir(b.RootDir), "boot"))
	envs = append(envs, "NAME="+b.Name)
	envs = append(envs, "OUTDIR="+filepath.Join(b.RuntimeDir, "images", b.Name))

	req := sst.ExecRequest{
		Cmd:  "/bin/sh",
		Args: []string{"-c", cmdStr},
		Dir:  filepath.Dir(b.Dockerfile), 
		Env:  envs,
		Mode: sst.ExecCaptured,
	}
	_, err := sst.Exec(ctx, b.Log, req)
	if err != nil {
		return fmt.Errorf("host run command failed: %w", err)
	}
	return nil
}

// execChrootRun executes commands inside the guest filesystem using chroot, respecting WORKDIR and USER.
func (b *BuildApp) execChrootRun(ctx context.Context, cmdStr string, targetDir string) error {
	b.Log.Info(ctx, "executing RUN in chroot", "dir", targetDir)
	
	mounts := []string{"/dev", "/dev/pts", "/proc", "/sys"}
	for _, m := range mounts {
		target := filepath.Join(targetDir, m)
		os.MkdirAll(target, 0755)
		sst.ExecShell(ctx, b.Log, "mount", "--bind", m, target)
	}

	cachePaths := map[string]string{
		"apk": "/etc/apk/cache",
		"apt": "/var/cache/apt/archives",
		"dnf": "/var/cache/dnf",
	}
	
	var activeCacheMounts []string
	for name, guestPath := range cachePaths {
		hostDir := filepath.Join(b.RuntimeDir, "cache", "pkg", name)
		os.MkdirAll(hostDir, 0755)
		
		target := filepath.Join(targetDir, guestPath)
		os.MkdirAll(target, 0755)
		
		if _, err := sst.ExecShell(ctx, b.Log, "mount", "--bind", hostDir, target); err == nil {
			activeCacheMounts = append(activeCacheMounts, target)
		}
	}
	
	defer func() {
		for i := len(activeCacheMounts)-1; i >= 0; i-- {
			sst.ExecShell(context.Background(), b.Log, "umount", "-l", activeCacheMounts[i])
		}
		for i := len(mounts)-1; i >= 0; i-- {
			target := filepath.Join(targetDir, mounts[i])
			sst.ExecShell(context.Background(), b.Log, "umount", "-l", target)
		}
	}()

	sst.ExecShell(ctx, b.Log, "cp", "-L", "/etc/resolv.conf", filepath.Join(targetDir, "etc/resolv.conf"))

	var envs []string
	for k, v := range b.Args { envs = append(envs, k+"="+v) }
	for k, v := range b.Env { envs = append(envs, k+"="+v) }

	// --- Resolve Context ---
	workdir := b.Workdir
	if workdir == "" { workdir = "/" }
	
	user := b.User
	if user == "" { user = "root" }

	// Ensure workdir exists in the guest before entering
	guestWorkdir := filepath.Join(targetDir, workdir)
	os.MkdirAll(guestWorkdir, 0755)

	// Write the script to avoid all quoting/escaping shell nightmares
	scriptName := ".sst-run-script.sh"
	scriptPath := filepath.Join(targetDir, scriptName)
	
	scriptContent := fmt.Sprintf("#!/bin/sh\ncd '%s'\n%s\n", workdir, cmdStr)
	if err := os.WriteFile(scriptPath, []byte(scriptContent), 0755); err != nil {
		return fmt.Errorf("failed to write run script: %w", err)
	}
	defer os.Remove(scriptPath)

	// --- Impersonate User ---
	var chrootCmd string
	if user != "root" {
		// Strip group if present (su compatibility)
		execUser := strings.SplitN(user, ":", 2)[0]
		// -p preserves the environment variables we inject via chroot
		chrootCmd = fmt.Sprintf("su -p -s /bin/sh '%s' -c /%s", execUser, scriptName)
	} else {
		chrootCmd = fmt.Sprintf("/%s", scriptName)
	}

	req := sst.ExecRequest{
		Cmd:  "chroot",
		Args: []string{targetDir, "/bin/sh", "-c", chrootCmd},
		Env:  envs,
		Mode: sst.ExecCaptured,
	}
	_, err := sst.Exec(ctx, b.Log, req)
	if err != nil {
		return fmt.Errorf("chroot command failed: %w", err)
	}
	return nil
}

// FinalizeVM generates the VM disk images and optionally the OCI format.
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
			"-t", "ext4", 
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

			// Helper to handle Docker's JSON array syntax vs shell syntax
			parseArgs := func(input string) []string {
				input = strings.TrimSpace(input)
				if input == "" { return nil }
				if strings.HasPrefix(input, "[") && strings.HasSuffix(input, "]") {
					var parsed []string
					if err := json.Unmarshal([]byte(input), &parsed); err == nil {
						return parsed
					}
				}
				return []string{"/bin/sh", "-c", input}
			}

			// Generate a strictly compliant OCI Image Configuration
			type OCIConfig struct {
				Env          []string               `json:"Env"`
				Cmd          []string               `json:"Cmd,omitempty"`
				Entrypoint   []string               `json:"Entrypoint,omitempty"`
				WorkingDir   string                 `json:"WorkingDir,omitempty"`
				User         string                 `json:"User,omitempty"`
				Labels       map[string]string      `json:"Labels,omitempty"`       
				ExposedPorts map[string]struct{}    `json:"ExposedPorts,omitempty"` 
				Volumes      map[string]struct{}    `json:"Volumes,omitempty"`      
			}

			type OCIImageManifest struct {
				Architecture string    `json:"architecture"`
				OS           string    `json:"os"`
				Config       OCIConfig `json:"config"`
				RootFS       struct {
					Type    string   `json:"type"`
					DiffIDs []string `json:"diff_ids"`
				} `json:"rootfs"`
				History []map[string]string `json:"history"`
			}

			manifestObj := OCIImageManifest{
				Architecture: runtime.GOARCH,
				OS:           "linux",
				RootFS: struct {
					Type    string   `json:"type"`
					DiffIDs []string `json:"diff_ids"`
				}{
					Type:    "layers",
					DiffIDs: []string{"sha256:" + layerHash},
				},
				History: []map[string]string{{"created": "1970-01-01T00:00:00Z", "created_by": "sst mkimage"}},
			}

			// Inject our parsed Dockerfile state
			manifestObj.Config.Env = []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"}
			if b.User != "" && b.User != "root" { manifestObj.Config.User = b.User }
			if b.Workdir != "" && b.Workdir != "/" { manifestObj.Config.WorkingDir = b.Workdir }
			if b.Cmd != "" { manifestObj.Config.Cmd = parseArgs(b.Cmd) }
			if b.EntryPoint != "" { manifestObj.Config.Entrypoint = parseArgs(b.EntryPoint) }
			
			if len(b.Labels) > 0 { manifestObj.Config.Labels = b.Labels }
			if len(b.ExposedPorts) > 0 { manifestObj.Config.ExposedPorts = b.ExposedPorts }
			if len(b.Volumes) > 0 { manifestObj.Config.Volumes = b.Volumes }

			configBytes, _ := json.Marshal(manifestObj)
			configContent := string(configBytes)

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
