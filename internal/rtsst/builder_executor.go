package rtsst

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/swiss-software-tool/sst/internal/sst"
	mkimagepb "github.com/swiss-software-tool/sst/proto/yaml/mkimage/v1"
	runctlpb "github.com/swiss-software-tool/sst/proto/yaml/runctl/v1"
)

func (b *BuildApp) InitEnvironment(ctx context.Context) error {
	absContext, err := filepath.Abs(b.ContextDir)
	if err != nil {
		return fmt.Errorf("failed to resolve context directory: %w", err)
	}

	absDockerfilePath, err := filepath.Abs(b.Dockerfile)
	if err != nil {
		return fmt.Errorf("failed to resolve absolute path of dockerfile: %w", err)
	}

	targetDockerfilePath := filepath.Join(absContext, filepath.Base(absDockerfilePath))

	if absDockerfilePath != targetDockerfilePath {
		err = os.Link(absDockerfilePath, targetDockerfilePath)
		if err != nil {
			if os.IsExist(err) {
				srcInfo, statErr1 := os.Stat(absDockerfilePath)
				dstInfo, statErr2 := os.Stat(targetDockerfilePath)
				if statErr1 != nil || statErr2 != nil || !os.SameFile(srcInfo, dstInfo) {
					return fmt.Errorf("dockerfile name collision: %s already exists in context directory but points to a different file", filepath.Base(absDockerfilePath))
				}
			} else {
				return fmt.Errorf("failed to hardlink dockerfile into context directory (cross-device link?): %w", err)
			}
		}
		b.Log.Info(ctx, "hardlinked dockerfile into context workspace", "src", absDockerfilePath, "dst", targetDockerfilePath)
	}

	b.Dockerfile = targetDockerfilePath

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

	// USES THE DEDICATED BUILD DIR FOR ZFS EXTRACTION
	b.DatasetMgr = NewDatasetManager(b.BuildDir, b.BuildID, runtimeRootName)

	if b.Force {
		b.Log.Info(ctx, "force flag provided, annihilating existing build environment", "build_id", b.BuildID)
		_ = b.DatasetMgr.Destroy()
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

	b.StageMounts[target] = mntPath
	b.Log.Info(ctx, "provisioned AS dataset", "stage", target, "mount", mntPath)
	return nil
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
		return nil
	case "WORKDIR":
		b.Workdir = b.Substitute(cleanArgs)
		return nil
	case "USER":
		b.User = b.Substitute(cleanArgs)
		return nil
	case "CMD":
		b.Cmd = b.Substitute(cleanArgs)
		return nil
	case "ENTRYPOINT":
		b.EntryPoint = b.Substitute(cleanArgs)
		return nil
	case "LABEL":
		parts := strings.SplitN(cleanArgs, "=", 2)
		if len(parts) == 2 {
			b.Labels[strings.Trim(parts[0], `"`)] = strings.Trim(parts[1], `"`)
		}
		return nil
	case "EXPOSE":
		for _, port := range strings.Fields(cleanArgs) {
			if !strings.Contains(port, "/") {
				port += "/tcp"
			}
			b.ExposedPorts[port] = struct{}{}
		}
		return nil
	case "VOLUME":
		clean := strings.TrimPrefix(strings.TrimSuffix(cleanArgs, "]"), "[")
		for _, vol := range strings.Split(clean, ",") {
			b.Volumes[strings.Trim(strings.TrimSpace(vol), `"`)] = struct{}{}
		}
		return nil
	case "FROM", "NAME", "VMOUT", "EMIT", "COPY", "ADD":
		return nil
	case "RUN":
		if !b.shouldExecute(inst.Conditions) {
			b.Log.Info(ctx, "skipping instruction (condition mismatch)", "conditions", inst.Conditions, "emits", b.Emits)
			return nil
		}

		if strings.HasPrefix(inst.Args, "sys.provision_dataset") {
			return b.execProvisionDataset(ctx, inst.Args)
		}

		resolvedLoc := b.Substitute(inst.Location)

		targetDir := b.RootDir
		activeRootfs := b.RootDir

		if resolvedLoc == "base" {
			targetDir = filepath.Dir(b.RootDir)
		} else if resolvedLoc == "boot" {
			targetDir = filepath.Join(filepath.Dir(b.RootDir), "boot")
		} else if stagePath, ok := b.StageMounts[resolvedLoc]; ok {
			targetDir = stagePath
			activeRootfs = stagePath
		} else if resolvedLoc != "root" && resolvedLoc != "host" && resolvedLoc != "chroot" && resolvedLoc != "" {
			targetDir = filepath.Join(b.DatasetMgr.mountPath(""), resolvedLoc)
			os.MkdirAll(targetDir, 0755)
		}

		b.Log.Info(ctx, "evaluating RUN directive", "context", inst.Context, "dir", targetDir)

		if inst.Context == "host" {
			return b.execHostRun(ctx, args, targetDir, activeRootfs)
		}
		return b.execChrootRun(ctx, args, targetDir, activeRootfs)
	default:
		return fmt.Errorf("unknown instruction type: %s", inst.Type)
	}
}

func (b *BuildApp) buildEnvironmentMap(activeRootfs string) []string {
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

	envMap["SST_CACHE_APT"] = b.DatasetMgr.CacheDir("apt")
	envMap["SST_CACHE_APK"] = b.DatasetMgr.CacheDir("apk")
	envMap["SST_CACHE_DNF"] = b.DatasetMgr.CacheDir("dnf")
	envMap["SST_CACHE_GET"] = b.DatasetMgr.CacheDir("get")
	envMap["SST_CACHE_FROM"] = b.DatasetMgr.CacheDir("from")

	for k, v := range b.Env {
		envMap[k] = v
	}
	for k, v := range b.Args {
		envMap[k] = v
	}
	for k, v := range b.BuildArgs {
		envMap[k] = v
	}

	var envs []string
	for k, v := range envMap {
		envs = append(envs, k+"="+v)
	}
	return envs
}

func (b *BuildApp) execHostRun(ctx context.Context, cmdStr string, targetDir string, activeRootfs string) error {
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return err
	}

	env := b.buildEnvironmentMap(activeRootfs)

	req := sst.ExecRequest{
		Cmd:  "/bin/sh",
		Args: []string{"-c", cmdStr},
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

func (b *BuildApp) execChrootRun(ctx context.Context, cmdStr string, targetDir string, activeRootfs string) error {
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
	for name, guestPath := range cachePaths {
		hostDir := b.DatasetMgr.CacheDir(name)
		os.MkdirAll(hostDir, 0755)

		target := filepath.Join(targetDir, guestPath)
		os.MkdirAll(target, 0755)

		if _, err := sst.ExecShell(ctx, b.Log, "mount", "--bind", hostDir, target); err == nil {
			activeCacheMounts = append(activeCacheMounts, target)
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

	workdir := b.Workdir
	if workdir == "" {
		workdir = "/"
	}
	user := b.User
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

	env := b.buildEnvironmentMap(activeRootfs)
	env = append(env,
		"APK_CACHE_DIR=/var/cache/apk",
		"APT_CACHE_DIR=/var/cache/apt/archives",
		"DNF_CACHE_DIR=/var/cache/dnf",
		"YUM_CACHE_DIR=/var/cache/dnf",
	)

	req := sst.ExecRequest{
		Cmd:  "chroot",
		Args: []string{targetDir, "/bin/sh", "-c", chrootCmd},
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
		b.Log.Info(ctx, "parsed os-release from rootfs", "semantic_name", semanticName, "upstream_build_id", upstreamBuildID)
	} else {
		b.Log.Warn(ctx, "could not parse os-release from rootfs; falling back to short name", "error", err)
	}

	originalName := b.Name
	longName := originalName
	if semanticName != "" {
		longName = fmt.Sprintf("%s-%s", semanticName, originalName)
	}

	originalOutputDir := filepath.Join(b.RuntimeDir, "images", originalName)
	finalOutputDir := filepath.Join(b.RuntimeDir, "images", longName)

	// 1. Move the directory and inner files FIRST, before we evaluate them
	if originalOutputDir != finalOutputDir {
		if err := os.Rename(originalOutputDir, finalOutputDir); err != nil && !os.IsNotExist(err) {
			b.Log.Warn(ctx, "failed to rename output directory to semantic name", "error", err)
		}
		
		_ = os.Rename(filepath.Join(finalOutputDir, originalName+".raw"), filepath.Join(finalOutputDir, longName+".raw"))
		_ = os.Rename(filepath.Join(finalOutputDir, originalName+".qcow2"), filepath.Join(finalOutputDir, longName+".qcow2"))
		
		b.Name = longName // Update the builder's name so everything downstream matches
	}

	b.ImageOutput = finalOutputDir
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

	// Dynamic Branching: Did the templates emit a block device?
	rawDiskPath := filepath.Join(b.ImageOutput, b.Name+".raw")
	if _, err := os.Stat(rawDiskPath); err == nil {
		// It's a Hardware VM (Layer 3/4)
		if err := AnalyseImg(sb); err != nil {
			b.Log.Warn(ctx, "AnalyseImg failed", "error", err)
		}

		if sb.YamlObject.Boot != nil && sb.YamlObject.Boot.BootfsDevice != "" {
			_ = ExtractArtifacts(sb)
		}

		QueuePurification(b.RuntimeDir, []string{rawDiskPath})
	} else {
		// It's a Container! (Layer 1/2). Promote the underlying ZFS dataset physically.
		b.Log.Info(ctx, "skipping disk analysis (no block device generated, resolving as container)")
		sb.YamlObject.Storage.RawImage = "" 

		rootfsDest := filepath.Join(b.ImageOutput, "rootfs")
		if err := b.DatasetMgr.PromoteTo(b.DatasetMgr.RuntimeRootName, "images", b.Name+"/rootfs"); err != nil {
			b.Log.Warn(ctx, "failed to promote ZFS dataset natively", "error", err)
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
			_ = os.WriteFile(b.Dockerfile, []byte(strings.Join(newLines, "\n")), 0644)
		}
		UpdateCatalog(ctx, b.Log, "images", b.Name, b.ImageOutput, buildID)
	}

	return nil
}


func (b *BuildApp) FinalizeInstance(ctx context.Context) error {
	instName := b.BuildArgs["INST_NAME"]
	if instName == "" {
		instName = b.Args["INST_NAME"]
	}
	if instName == "" {
		instName = b.Name
	}

	baseImage := b.BuildArgs["BASE_IMAGE"]
	if baseImage == "" {
		baseImage = b.Args["BASE_IMAGE"]
	}
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
