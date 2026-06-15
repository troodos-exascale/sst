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

func wrapWithTee(logFile string, cmd string, args ...string) (string, []string, []string) {
	if logFile == "" {
		return cmd, args, nil
	}
	bashArgs := []string{"-c", `set -o pipefail; "$@" 2>&1 | tee -a "$LOGFILE"`, "_", cmd}
	bashArgs = append(bashArgs, args...)
	return "bash", bashArgs, []string{"LOGFILE=" + logFile}
}

func (b *BuildApp) InitEnvironment(ctx context.Context) error {
	absContext, err := filepath.Abs(b.ContextDir)
	if err != nil {
		return fmt.Errorf("failed to resolve context directory: %w", err)
	}

	absDockerfilePath, err := filepath.Abs(b.Dockerfile)
	if err != nil {
		return fmt.Errorf("failed to resolve absolute path of dockerfile: %w", err)
	}

	b.Dockerfile = absDockerfilePath

	hashBytes := sha256.Sum256([]byte(absContext))
	contextHash := fmt.Sprintf("%x", hashBytes)[:12]

	dockerfileName := filepath.Base(b.Dockerfile)

	b.BuildID = fmt.Sprintf("%s-%s", contextHash, dockerfileName)
	runtimeRootName := fmt.Sprintf("runtime-root-%s", dockerfileName)

	b.Log.Info(ctx, "initialized workspace",
		"context", absContext,
		"build_root", b.BuildID,
		"runtime_root", runtimeRootName,
	)

	b.DatasetMgr = NewDatasetManager(b.BuildDir, b.BuildID, runtimeRootName)

	if b.Force {
		b.Log.Info(ctx, "force flag provided, annihilating existing build environment", "build_id", b.BuildID)
		_ = b.DatasetMgr.Destroy()
		
		if b.Name != "" && !strings.HasSuffix(b.Dockerfile, "instance.dockerfile") {
			outDir := filepath.Join(b.RuntimeDir, "images", b.Name)
			b.Log.Info(ctx, "force flag provided, wiping output directory", "dir", outDir)
			_ = os.RemoveAll(outDir)
		}
	}

	if err := b.DatasetMgr.InitBuildEnvironment(); err != nil {
		return fmt.Errorf("failed to initialize dataset environment: %w", err)
	}

	b.RootDir = b.DatasetMgr.RuntimeRoot()
	return nil
}

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

func (b *BuildApp) execProvisionDataset(ctx context.Context, args string) error {
	parts := strings.Fields(args)
	var target string
	for _, p := range parts {
		if strings.HasPrefix(p, "--target=") {
			target = strings.TrimPrefix(p, "--target=")
		}
	}

	mntPath, err := b.DatasetMgr.CreateAsRoot(target)
	if err != nil {
		return err
	}

	b.Log.Info(ctx, "provisioned AS dataset", "stage", target, "mount", mntPath)
	return nil
}

func (b *BuildApp) buildEnvironmentMap(inst Instruction, activeRootfs string) []string {
	envMap := make(map[string]string)

	if activeRootfs == "" {
		activeRootfs = b.RootDir
	}

	envMap["BASE_IMAGE"] = b.BaseImage
	envMap["ROOTFS"] = activeRootfs
	envMap["BASEDIR"] = filepath.Dir(b.RootDir)
	envMap["BOOTDIR"] = filepath.Join(filepath.Dir(b.RootDir), "boot")
	envMap["NAME"] = b.Name
	envMap["OUTDIR"] = filepath.Join(b.RuntimeDir, "images", b.Name)

	if b.DatasetMgr != nil {
		envMap["SST_CACHE_APT"] = b.DatasetMgr.CacheDir("apt")
		envMap["SST_CACHE_APK"] = b.DatasetMgr.CacheDir("apk")
		envMap["SST_CACHE_DNF"] = b.DatasetMgr.CacheDir("dnf")
		envMap["SST_CACHE_GET"] = b.DatasetMgr.CacheDir("get")
		envMap["SST_CACHE_FROM"] = b.DatasetMgr.CacheDir("from")
	}

	for k, v := range inst.Env {
		envMap[k] = v
	}
	for k, v := range b.BuildArgs {
		envMap[k] = v // Global build args still overlay
	}

	var envs []string
	for k, v := range envMap {
		envs = append(envs, k+"="+v)
	}
	return envs
}

// CreateStepContext builds a pure, self-contained Protosh capsule for a Dockerfile instruction.
func (b *BuildApp) CreateStepContext(inst Instruction, parentHash string) *sst.Context {
	hashStub := inst.Hash
	if len(hashStub) >= 8 {
		hashStub = hashStub[:8]
	}
	_ = fmt.Sprintf("step_%02d_hash_%s", inst.Index, hashStub)

	var pTag string
	if parentHash != "" {
		pStub := parentHash
		if len(pStub) >= 8 {
			pStub = pStub[:8]
		}
		pTag = fmt.Sprintf("step_%02d_hash_%s", inst.Index-1, pStub)
	}

	return &sst.Context{
		ID:          inst.Hash,
		Description: fmt.Sprintf("Step %02d: %s", inst.Index, inst.Type),
		
		Probe: func(c context.Context) (bool, error) {
			if b.DatasetMgr != nil {
				// We check if all primary target stages exist at this hash tag
				// A more robust implementation would loop over inst.StageMounts,
				// but relying on the TG engine's Completed map (backed by state.json) 
				// is generally enough unless manual tampering occurred.
				// We return false to allow tg.go to rely purely on state.json for now.
				return false, nil 
			}
			return false, nil
		},
		
		Restore: func(c context.Context) error {
			if b.DatasetMgr != nil && pTag != "" {
				b.Log.Info(c, "divergence detected, rolling back ZFS to parent state", "tag", pTag)
				for stageTarget := range inst.StageMounts {
					_ = b.DatasetMgr.RollbackDataset(stageTarget, pTag)
				}
			}
			return nil
		},

		Action: func(c context.Context) (interface{}, error) {
			return nil, b.executeAction(c, inst, hashStub)
		},
	}
}

func (b *BuildApp) executeAction(ctx context.Context, inst Instruction, hashStub string) error {
	var logFile string

	if b.DatasetMgr != nil {
		logsDir := filepath.Join(b.DatasetMgr.MountPath(""), "logs")
		os.MkdirAll(logsDir, 0755)
		logFile = filepath.Join(logsDir, fmt.Sprintf("%02d_%s_%s.log", inst.Index, hashStub, strings.ToLower(inst.Type)))
	}

	var args string
	if inst.Type == "RUN" {
		args = inst.Args
	} else {
		// Variable expansion using the frozen env for this step
		args = os.Expand(inst.Args, func(k string) string {
			if v, ok := inst.Env[k]; ok { return v }
			return ""
		})
	}

	var execErr error

	if inst.Type == "RUN" {
		if !b.shouldExecute(inst.Conditions) {
			b.Log.Info(ctx, "skipping instruction (condition mismatch)", "conditions", inst.Conditions, "emits", b.Emits)
		} else {
			if strings.HasPrefix(inst.Args, "sys.provision_dataset") {
				execErr = b.execProvisionDataset(ctx, inst.Args)
			} else {
				resolvedLoc := os.Expand(inst.Location, func(k string) string { return inst.Env[k] })
				targetDir := b.RootDir
				activeRootfs := b.RootDir

				if resolvedLoc == "base" {
					targetDir = filepath.Dir(b.RootDir)
				} else if resolvedLoc == "boot" {
					targetDir = filepath.Join(filepath.Dir(b.RootDir), "boot")
				} else if stagePath, ok := inst.StageMounts[resolvedLoc]; ok {
					if b.DatasetMgr != nil {
						targetDir = b.DatasetMgr.MountPath(stagePath)
					}
					activeRootfs = targetDir
				} else if resolvedLoc != "root" && resolvedLoc != "host" && resolvedLoc != "chroot" && resolvedLoc != "" {
					if b.DatasetMgr != nil {
						targetDir = filepath.Join(b.DatasetMgr.MountPath(""), resolvedLoc)
					}
					os.MkdirAll(targetDir, 0755)
				}

				b.Log.Info(ctx, "evaluating RUN directive", "context", inst.Context, "dir", targetDir)

				if inst.Context == "host" {
					execErr = b.execHostRun(ctx, args, targetDir, activeRootfs, inst, logFile)
				} else {
					execErr = b.execChrootRun(ctx, args, targetDir, activeRootfs, inst, logFile)
				}
			}
		}
	}
	
	return execErr
}

func (b *BuildApp) execHostRun(ctx context.Context, cmdStr string, targetDir string, activeRootfs string, inst Instruction, logFile string) error {
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return err
	}

	cmdToRun, argsToRun, extraEnv := wrapWithTee(logFile, "/bin/sh", "-c", cmdStr)
	
	env := b.buildEnvironmentMap(inst, activeRootfs)
	env = append(env, extraEnv...)

	req := sst.ExecRequest{
		Cmd:  cmdToRun,
		Args: argsToRun,
		Dir:  filepath.Dir(b.Dockerfile),
		Env:  env,
		Mode: sst.ExecForeground,
	}
	_, err := sst.Exec(ctx, b.Log, req)
	if err != nil {
		return fmt.Errorf("host run command failed: %w", err)
	}
	return nil
}

func (b *BuildApp) execChrootRun(ctx context.Context, cmdStr string, targetDir string, activeRootfs string, inst Instruction, logFile string) error {
	mounts := []string{"/dev", "/dev/pts", "/proc", "/sys"}
	for _, m := range mounts {
		target := filepath.Join(targetDir, m)
		os.MkdirAll(target, 0755)
		sst.ExecShell(ctx, b.Log, "mount", "--bind", m, target)
	}

	cachePaths := map[string]string{
		"apk": "/var/cache/apk",
		"apt": "/var/cache/apt/archives",
		"dnf": "/var/cache/dnf",
	}

	var activeCacheMounts []string
	if b.DatasetMgr != nil {
		for name, guestPath := range cachePaths {
			hostDir := b.DatasetMgr.CacheDir(name)
			os.MkdirAll(hostDir, 0755)

			target := filepath.Join(targetDir, guestPath)
			os.MkdirAll(target, 0755)

			if _, err := sst.ExecShell(ctx, b.Log, "mount", "--bind", hostDir, target); err == nil {
				activeCacheMounts = append(activeCacheMounts, target)
			}
		}
	}

	defer func() {
		for i := len(activeCacheMounts) - 1; i >= 0; i-- {
			sst.ExecShell(context.Background(), b.Log, "umount", "-l", activeCacheMounts[i])
		}
		for i := len(mounts) - 1; i >= 0; i-- {
			sst.ExecShell(context.Background(), b.Log, "umount", "-l", filepath.Join(targetDir, mounts[i]))
		}
	}()

	sst.ExecShell(ctx, b.Log, "cp", "-L", "/etc/resolv.conf", filepath.Join(targetDir, "etc/resolv.conf"))

	workdir := inst.Workdir
	if workdir == "" {
		workdir = "/"
	}
	user := inst.User
	if user == "" {
		user = "root"
	}

	guestWorkdir := filepath.Join(targetDir, workdir)
	os.MkdirAll(guestWorkdir, 0755)

	scriptName := ".sst-run-script.sh"
	scriptPath := filepath.Join(targetDir, scriptName)
	scriptContent := fmt.Sprintf("#!/bin/sh\ncd '%s'\n%s\n", workdir, cmdStr)
	if err := os.WriteFile(scriptPath, []byte(scriptContent), 0755); err != nil {
		return fmt.Errorf("failed to write run script: %w", err)
	}
	defer os.Remove(scriptPath)

	var chrootCmd string
	if user != "root" {
		execUser := strings.SplitN(user, ":", 2)[0]
		chrootCmd = fmt.Sprintf("su -p -s /bin/sh '%s' -c /%s", execUser, scriptName)
	} else {
		chrootCmd = fmt.Sprintf("/%s", scriptName)
	}

	cmdToRun, argsToRun, extraEnv := wrapWithTee(logFile, "chroot", targetDir, "/bin/sh", "-c", chrootCmd)

	env := b.buildEnvironmentMap(inst, activeRootfs)
	env = append(env,
		"APK_CACHE_DIR=/var/cache/apk",
		"APT_CACHE_DIR=/var/cache/apt/archives",
		"DNF_CACHE_DIR=/var/cache/dnf",
		"YUM_CACHE_DIR=/var/cache/dnf",
	)
	env = append(env, extraEnv...)

	req := sst.ExecRequest{
		Cmd:  cmdToRun,
		Args: argsToRun,
		Env:  env,
		Mode: sst.ExecForeground,
	}
	if _, err := sst.Exec(ctx, b.Log, req); err != nil {
		return fmt.Errorf("chroot command failed: %w", err)
	}
	return nil
}

func (b *BuildApp) FinalizeVM(ctx context.Context) error {
	semanticName := ""
	upstreamBuildID := ""
	osInfo, err := ParseOSRelease(b.RootDir)
	if err == nil {
		semanticName = GenerateSemanticName(osInfo)
		upstreamBuildID = osInfo["BUILD_ID"]
		b.Log.Info(ctx, "parsed os-release from native ZFS rootfs", "semantic_name", semanticName, "upstream_build_id", upstreamBuildID)
	} else {
		b.Log.Warn(ctx, "could not parse os-release from native rootfs; falling back to short name", "error", err)
	}

	b.ImageOutput = filepath.Join(b.RuntimeDir, "images", b.Name)
	if err := os.MkdirAll(b.ImageOutput, 0755); err != nil {
		return err
	}

	buildID := time.Now().UTC().Format("20060102-150405")

	sb := &ImageSB{
		Ctx: ctx,
		Log: b.Log,
		YamlObject: &mkimagepb.ImageYAML{
			ApiVersion: "sst.io/v1",
			Kind:       "Image",
			Provenance: &mkimagepb.Provenance{
				Name:            b.Name,
				BuildId:         buildID,
				UpstreamOs:      semanticName, 
				UpstreamBuildId: upstreamBuildID,
			},
			Storage: &mkimagepb.StoragePlan{
				RawImage: b.Name + ".raw",
			},
			Boot: &mkimagepb.BootStructure{},
		},
		SstRuntimeDir: b.RuntimeDir,
	}

	rawDiskPath := filepath.Join(b.ImageOutput, b.Name+".raw")
	if _, err := os.Stat(rawDiskPath); err == nil {
		b.Log.Info(ctx, "populating manifest declaratively from build state")

		hasEFI := false
		for relPath := range b.IncludedFiles {
			if strings.Contains(relPath, "boot-efi") || strings.Contains(relPath, "efiboot") {
				hasEFI = true
				break
			}
		}
		if _, err := os.Stat(filepath.Join(b.RootDir, "boot", "efi")); err == nil {
			hasEFI = true
		}

		if hasEFI {
			sb.YamlObject.Storage.RootfsDevice = "/dev/vda2"
			sb.YamlObject.Storage.RootfsType = "ext4"
			sb.YamlObject.Boot.BootfsDevice = "/dev/vda1"
			sb.YamlObject.Boot.BootfsType = "vfat"
		} else {
			sb.YamlObject.Storage.RootfsDevice = "/dev/vda"
			sb.YamlObject.Storage.RootfsType = "ext4"
			sb.YamlObject.Boot.BootfsDevice = "/dev/vda"
			sb.YamlObject.Boot.BootfsType = "ext4"
		}

		kernelExists := false
		if _, err := os.Stat(filepath.Join(b.ImageOutput, "kernel")); err == nil {
			sb.YamlObject.Boot.Kernel = "kernel"
			kernelExists = true
		}
		initrdExists := false
		if _, err := os.Stat(filepath.Join(b.ImageOutput, "initrd")); err == nil {
			sb.YamlObject.Boot.Initrd = "initrd"
			initrdExists = true
		}

		if !kernelExists || !initrdExists {
			b.Log.Info(ctx, "locating kernel binaries natively in ZFS dataset")
			bootDir := filepath.Join(b.RootDir, "boot")
			
			kernelPath, initrdPath, ok := scanPartitionForArtifacts(bootDir)
			if !ok {
				kernelPath, initrdPath, ok = scanPartitionForArtifacts(b.RootDir)
			}
			
			if ok {
				b.Log.Info(ctx, "extracting kernel directly from workspace", "kernel", filepath.Base(kernelPath))
				kName, iName, err := copyArtifacts(kernelPath, initrdPath, b.ImageOutput)
				if err != nil {
					b.Log.Warn(ctx, "native artifact extraction failed", "error", err)
				} else {
					sb.YamlObject.Boot.Kernel = kName
					sb.YamlObject.Boot.Initrd = iName
				}
			} else {
				b.Log.Warn(ctx, "could not locate kernel/initrd binaries in native workspace")
			}
		}

		QueuePurification(b.RuntimeDir, []string{rawDiskPath})
	} else {
		b.Log.Info(ctx, "resolving artifact as OCI container dataset")
		sb.YamlObject.Storage.RawImage = "" 

		rootfsDest := filepath.Join(b.ImageOutput, "rootfs")
		if b.DatasetMgr != nil {
			if err := b.DatasetMgr.PromoteTo(b.DatasetMgr.RuntimeRootName, "images", b.Name+"/rootfs"); err != nil {
				b.Log.Warn(ctx, "failed to promote ZFS dataset natively", "error", err)
				_ = os.Rename(b.RootDir, rootfsDest)
			}
		} else {
			_ = os.Rename(b.RootDir, rootfsDest)
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

	blueprintDir := filepath.Join(b.ImageOutput, "blueprint")
	os.MkdirAll(blueprintDir, 0755)

	if b.Dockerfile != "" {
		if data, err := os.ReadFile(b.Dockerfile); err == nil {
			lines := strings.Split(string(data), "\n")
			var newLines []string
			newLines = append(newLines, fmt.Sprintf("# CANONICAL_NAME: %s", b.Name))
			newLines = append(newLines, fmt.Sprintf("# LAST_BUILD: %s", buildID))

			for _, line := range lines {
				if strings.HasPrefix(line, "# CANONICAL_NAME:") || strings.HasPrefix(line, "# LAST_BUILD:") {
					continue
				}
				newLines = append(newLines, line)
			}
			
			destPath := filepath.Join(blueprintDir, filepath.Base(b.Dockerfile))
			_ = os.WriteFile(destPath, []byte(strings.Join(newLines, "\n")), 0644)
		}
		UpdateCatalog(ctx, b.Log, "images", b.Name, b.ImageOutput, buildID)
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

	b.Log.Info(ctx, "✅ Build complete!", "next_step", fmt.Sprintf("mkimg instance %s <your-instance-name>", b.Name))
	return nil
}

func (b *BuildApp) FinalizeInstance(ctx context.Context) error {
	instName := b.Args["INST_NAME"]
	if instName == "" {
		instName = b.Name
	}
	baseImage := b.Args["BASE_IMAGE"]
	if baseImage == "" {
		baseImage = b.BaseImage
	}

	imgManifest := &mkimagepb.ImageYAML{}
	yoImg := &sst.YamlObject{
		Path:   filepath.Join(b.RuntimeDir, "images", baseImage, "image.yaml"),
		Schema: "sst.yaml.mkimage.v1.ImageYAML",
		Kind:   "Image",
		Msg:    imgManifest,
	}

	isContainer := false
	rootDev := "/dev/vda2"
	rootType := "ext4"
	
	if diags := yoImg.Load(ctx, b.Log); diags == nil || !diags.HasAny() {
		if imgManifest.Storage == nil || imgManifest.Storage.RawImage == "" {
			isContainer = true
		} else {
			if imgManifest.Storage.RootfsDevice != "" {
				rootDev = imgManifest.Storage.RootfsDevice
			}
			if imgManifest.Storage.RootfsType != "" {
				rootType = imgManifest.Storage.RootfsType
			}
		}
	} else {
		b.Log.Warn(ctx, "failed to load base image manifest; falling back to default rootfs", "error", diags.Error())
	}

	mac, err := GenerateMAC()
	if err != nil {
		return fmt.Errorf("failed to generate MAC address: %w", err)
	}

	inst := &runctlpb.Instance{
		ApiVersion: "sst.io/v1",
		Kind:       "Instance",
		Name:       instName,
		Hostname:   instName,
		BaseImage:  baseImage,
		MemorySize: "4096",
		MemorySizeSuffix: "MiB",
		CpuCount:   "4",
		Network:    []string{mac + "@nat"},
	}

	if isContainer {
		inst.Bootloader = "container"
		b.Log.Info(ctx, "configured manifest for container execution", "instance", instName)
	} else {
		inst.Bootloader = "linux"
		inst.Kernel = "kernel"
		inst.Initrd = "initrd"
		inst.Disk = []string{"cidata.iso"}
		inst.Rwdisk = []string{instName + ".raw"}
		inst.Params = []string{
			"console=hvc0",
			"oops=panic",
			"panic=10",
			"sysrq_always_enabled=1",
			"loglevel=4",
			"cpuidle.off=1",
			"root=" + rootDev,
			"rootfstype=" + rootType,
		}
		b.Log.Info(ctx, "configured manifest for hardware vm execution", "instance", instName)
	}

	instDir := filepath.Join(b.RuntimeDir, "instances", instName)
	if err := os.MkdirAll(instDir, 0755); err != nil {
		return err
	}

	yoInst := &sst.YamlObject{
		Path:   filepath.Join(instDir, "instance.yaml"),
		Schema: "sst.yaml.runctl.v1.Instance",
		Kind:   "Instance",
		Msg:    inst,
	}

	if diags := yoInst.Save(ctx, b.Log); diags != nil && diags.HasAny() {
		return diags
	}

	blueprintDir := filepath.Join(instDir, "blueprint")
	os.MkdirAll(blueprintDir, 0755)

	if b.Dockerfile != "" {
		destPath := filepath.Join(blueprintDir, filepath.Base(b.Dockerfile))
		if data, err := os.ReadFile(b.Dockerfile); err == nil {
			_ = os.WriteFile(destPath, data, 0644)
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

	if !isContainer {
		QueuePurification(b.RuntimeDir, []string{
			filepath.Join(instDir, instName+".raw"),
			filepath.Join(instDir, "cidata.iso"),
		})
	}

	disambiguator := fmt.Sprintf("%x", sha256.Sum256([]byte(instDir)))[:8]
	UpdateCatalog(ctx, b.Log, "instances", instName, instDir, disambiguator)
	return nil
}

func QueuePurification(runtimeDir string, files []string) {
	queueFile := filepath.Join(runtimeDir, ".purify_queue")
	f, err := os.OpenFile(queueFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	for _, file := range files {
		f.WriteString(file + "\n")
	}
}
