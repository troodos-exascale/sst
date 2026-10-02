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

	if b.Name != "" {
		b.BuildID = fmt.Sprintf("%s-%s-%s", contextHash, b.Name, dockerfileName)
	} else {
		b.BuildID = fmt.Sprintf("%s-%s", contextHash, dockerfileName)
	}
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
		envMap[k] = v
	}

	var envs []string
	for k, v := range envMap {
		envs = append(envs, k+"="+v)
	}
	return envs
}

func (b *BuildApp) CreateStepContext(inst Instruction, parentHash string) *sst.Context {
	hashStub := inst.Hash
	if len(hashStub) >= 8 {
		hashStub = hashStub[:8]
	}

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
				var nextHashStub string
				if inst.Index+1 < len(b.Instructions) {
					nextHashStub = b.Instructions[inst.Index+1].Hash[:8]
				} else {
					nextHashStub = "final"
				}
				tag := fmt.Sprintf("step_%02d_hash_%s", inst.Index, nextHashStub)
				exists := b.DatasetMgr.CheckSnapshot(b.DatasetMgr.RuntimeRootName, tag)
				return exists, nil
			}
			return false, nil
		},
		
		Restore: func(c context.Context) error {
			if b.DatasetMgr != nil && pTag != "" {
				b.Log.Info(c, "divergence detected, rolling back ZFS to parent state", "tag", pTag)
				for stageTarget := range inst.StageMounts {
					_ = b.DatasetMgr.RollbackDataset(stageTarget, pTag)
				}
				_ = b.DatasetMgr.RollbackDataset(b.DatasetMgr.RuntimeRootName, pTag)
			}
			return nil
		},

		Action: func(c context.Context) (interface{}, error) {
			return nil, b.executeAction(c, inst, hashStub)
		},
	}
}

func (b *BuildApp) executeAction(ctx context.Context, inst Instruction, hashStub string) error {
	b.RegisterHandlers(nil)

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
			if strings.HasPrefix(inst.Args, "sys.") {
				envMap := make(map[string]string)
				for _, kv := range b.buildEnvironmentMap(inst, b.RootDir) {
					parts := strings.SplitN(kv, "=", 2)
					if len(parts) == 2 {
						envMap[parts[0]] = parts[1]
					}
				}
				expandedCmd := os.Expand(inst.Args, func(k string) string { return envMap[k] })
				parts := strings.Fields(expandedCmd)
				
				req := sst.ExecRequest{
					Cmd:   parts[0],
					Args:  parts[1:],
					Env:   b.buildEnvironmentMap(inst, b.RootDir),
					Mode:  sst.ExecCaptured,
				}
				res, err := sst.Exec(ctx, b.Log, req)
				if logFile != "" && res != nil {
					_ = os.WriteFile(logFile, []byte(res.Stdout+"\n"+res.Stderr), 0644)
				}
				execErr = err
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

	env := b.buildEnvironmentMap(inst, activeRootfs)

	req := sst.ExecRequest{
		Cmd:   "bash",
		Args:  []string{"-c", cmdStr},
		Dir:   filepath.Dir(b.Dockerfile),
		Env:   env,
		Mode:  sst.ExecForeground,
	}
	res, err := sst.Exec(ctx, b.Log, req)
	
	if logFile != "" && res != nil {
		_ = os.WriteFile(logFile, []byte(res.Stdout+"\n"+res.Stderr), 0644)
	}
	
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

	env := b.buildEnvironmentMap(inst, activeRootfs)
	env = append(env,
		"APK_CACHE_DIR=/var/cache/apk",
		"APT_CACHE_DIR=/var/cache/apt/archives",
		"DNF_CACHE_DIR=/var/cache/dnf",
		"YUM_CACHE_DIR=/var/cache/dnf",
	)

	req := sst.ExecRequest{
		Cmd:         "chroot",
		Args:        []string{targetDir, "/bin/sh", "-c", chrootCmd},
		Env:         env,
		Mode:        sst.ExecForeground,
	}
	res, err := sst.Exec(ctx, b.Log, req)
	
	if logFile != "" && res != nil {
		_ = os.WriteFile(logFile, []byte(res.Stdout+"\n"+res.Stderr), 0644)
	}
	
	if err != nil {
		return fmt.Errorf("chroot command failed: %w", err)
	}
	return nil
}

// ---------------------------------------------------------
// Granular Finalization Builders
// ---------------------------------------------------------

func (b *BuildApp) BuildImageState(ctx context.Context, outDir string) error {
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return err
	}

	semanticName := ""
	upstreamBuildID := ""
	if osInfo, err := ParseOSRelease(b.RootDir); err == nil {
		semanticName = GenerateSemanticName(osInfo)
		upstreamBuildID = osInfo["BUILD_ID"]
	}

	buildID := time.Now().UTC().Format("20060102-150405")
	b.initActiveManifest()
	b.ActiveManifest.Provenance.Name = b.Name
	b.ActiveManifest.Provenance.BuildId = buildID
	if b.ActiveManifest.Provenance.UpstreamOs == "" { b.ActiveManifest.Provenance.UpstreamOs = semanticName }
	if b.ActiveManifest.Provenance.UpstreamBuildId == "" { b.ActiveManifest.Provenance.UpstreamBuildId = upstreamBuildID }
	if b.ActiveManifest.Storage.RawImage == "" { b.ActiveManifest.Storage.RawImage = b.Name + ".raw" }

	sb := &ImageSB{
		Ctx:           ctx,
		Log:           b.Log,
		YamlObject:    b.ActiveManifest,
		SstRuntimeDir: b.RuntimeDir,
	}

	rawDiskPath := filepath.Join(outDir, b.Name+".raw")
	kernelPath := filepath.Join(outDir, "kernel")
	initrdPath := filepath.Join(outDir, "initrd")

	_, rawErr := os.Stat(rawDiskPath)
	_, kernErr := os.Stat(kernelPath)
	_, initErr := os.Stat(initrdPath)

	if rawErr != nil && (kernErr == nil || initErr == nil) {
		b.Log.Info(ctx, "microvm detected without raw disk; generating dummy block device to satisfy vm constraints")
		_ = CreateSparse(rawDiskPath, 1024*1024)
		rawErr = nil
	}

	if rawErr == nil || kernErr == nil || initErr == nil {
		hasEFI := false
		for relPath := range b.IncludedFiles {
			if strings.Contains(relPath, "boot-efi") || strings.Contains(relPath, "efiboot") { hasEFI = true; break }
		}
		if _, err := os.Stat(filepath.Join(b.RootDir, "boot", "efi")); err == nil { hasEFI = true }

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

		if kernErr == nil { sb.YamlObject.Boot.Kernel = "kernel" }
		if initErr == nil { sb.YamlObject.Boot.Initrd = "initrd" }

		if (kernErr != nil || initErr != nil) && rawErr == nil {
			bootDir := filepath.Join(b.RootDir, "boot")
			if k, i, ok := scanPartitionForArtifacts(bootDir); ok {
				kName, iName, _ := copyArtifacts(k, i, outDir)
				sb.YamlObject.Boot.Kernel = kName
				sb.YamlObject.Boot.Initrd = iName
			} else if k, i, ok := scanPartitionForArtifacts(b.RootDir); ok {
				kName, iName, _ := copyArtifacts(k, i, outDir)
				sb.YamlObject.Boot.Kernel = kName
				sb.YamlObject.Boot.Initrd = iName
			}
		}
		if rawErr == nil { QueuePurification(b.RuntimeDir, []string{rawDiskPath}) }
	} else {
		sb.YamlObject.Storage.RawImage = ""
		rootfsDest := filepath.Join(outDir, "rootfs")
		if b.DatasetMgr != nil {
			if err := b.DatasetMgr.PromoteTo(b.DatasetMgr.RuntimeRootName, "images", b.Name+"/rootfs"); err != nil {
				_ = os.Rename(b.RootDir, rootfsDest)
			}
		} else {
			_ = os.Rename(b.RootDir, rootfsDest)
		}
	}
	return nil
}

func (b *BuildApp) BuildRuntimeState(ctx context.Context, targetDir, kind string) error {
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return err
	}

	lastEnv := b.getLastEnv()
	b.initActiveInstance()
	inst := b.ActiveInstance

	inst.ApiVersion = "sst.io/v1"
	inst.Kind = kind

	runtimeName := lastEnv["RUNTIME_NAME"]
	if runtimeName == "" { runtimeName = b.BuildArgs["INST_NAME"] }
	if runtimeName == "" { runtimeName = b.Name }

	baseImage := lastEnv["BASE_IMAGE"]
	if baseImage == "" { baseImage = b.BaseImage }

	inst.Name = runtimeName
	inst.BaseImage = baseImage

	mac := lastEnv["INST_MAC"]
	if mac == "" { mac, _ = GenerateMAC() }

	if inst.Hostname == "" { inst.Hostname = lastEnv["INST_HOSTNAME"] }
	if inst.Hostname == "" { inst.Hostname = runtimeName }
	if inst.DomainName == "" { inst.DomainName = lastEnv["INST_DOMAIN"] }
	if inst.CpuCount == "" { inst.CpuCount = lastEnv["INST_CPUS"] }
	if inst.CpuCount == "" { inst.CpuCount = "2" }
	if inst.MemorySize == "" { inst.MemorySize = lastEnv["INST_MEM"] }
	if inst.MemorySize == "" { inst.MemorySize = "2048"; inst.MemorySizeSuffix = "MiB" }

	if kind == "Runtime" {
		duid := lastEnv["INST_DUID"]
		if duid == "" { duid = "00:01:00:01:" + mac }
		if len(inst.Networks) == 0 {
			net := &runctlpb.EthInterface{
				Name:        "eth0",
				MacAddress:  mac,
				Duid:        duid,
				Ipv4Cidr:    lastEnv["INST_IPV4"],
				Ipv4Gateway: lastEnv["INST_GATEWAY"],
				Fabric:      "nat",
			}
			inst.Networks = []*runctlpb.EthInterface{net}
		}
		if len(inst.Params) == 0 {
			inst.Params = []string{"console=hvc0", "root=/dev/vda rw"}
		}
	} else {
		if len(inst.Network) == 0 && len(inst.Networks) == 0 {
			inst.Network = []string{mac + "@nat"}
		}
		if len(inst.Disk) == 0 { inst.Disk = []string{"cidata.iso"} }
		
		rootDev := "/dev/vda2"
		rootType := "ext4"
		imgManifest := &mkimagepb.ImageYAML{}
		yoImg := &sst.YamlObject{
			Path:   filepath.Join(b.RuntimeDir, "images", baseImage, "image.yaml"),
			Schema: "sst.yaml.mkimage.v1.ImageYAML",
			Msg:    imgManifest,
		}
		if diags := yoImg.Load(ctx, b.Log); diags == nil || !diags.HasAny() {
			if imgManifest.Storage != nil {
				if imgManifest.Storage.RawImage == "" {
					inst.Bootloader = "container"
				}
				if imgManifest.Storage.RootfsDevice != "" { rootDev = imgManifest.Storage.RootfsDevice }
				if imgManifest.Storage.RootfsType != "" { rootType = imgManifest.Storage.RootfsType }
			}
		}

		if inst.Bootloader != "container" {
			if inst.Bootloader == "" { inst.Bootloader = "linux" }
			if inst.Kernel == "" { inst.Kernel = "kernel" }
			if inst.Initrd == "" { inst.Initrd = "initrd" }
			if len(inst.Params) == 0 {
				inst.Params = []string{
					"console=hvc0", "oops=panic", "panic=10",
					"sysrq_always_enabled=1", "loglevel=4", "cpuidle.off=1",
					"root=" + rootDev, "rootfstype=" + rootType,
				}
			}
		}
	}

	if shares := lastEnv["INST_SHARE_HOST_DIRS"]; shares != "" {
		for _, share := range strings.Split(shares, ",") {
			s := strings.TrimSpace(share)
			found := false
			for _, existing := range inst.SharedDir {
				if existing == s { found = true; break }
			}
			if !found { inst.SharedDir = append(inst.SharedDir, s) }
		}
	}

	baseDiskName := lastEnv["INST_BASE_DISK"]
	if baseDiskName == "" {
		if kind == "Instance" {
			baseDiskName = inst.Name + ".raw"
		} else {
			baseDiskName = "disk.raw"
		}
	}
	
	foundBaseDisk := false
	for _, existing := range inst.Rwdisk {
		if existing == baseDiskName { foundBaseDisk = true; break }
	}
	if !foundBaseDisk {
		inst.Rwdisk = append(inst.Rwdisk, baseDiskName)
	}

	if devs := lastEnv["INST_DEVICES"]; devs != "" {
		for _, dev := range strings.Split(devs, ",") {
			parts := strings.Split(dev, ":")
			if len(parts) == 2 {
				devName := strings.TrimSpace(parts[0]) + ".raw"
				devSize, _ := ParseSize(parts[1])
				devPath := filepath.Join(targetDir, devName)
				if _, err := os.Stat(devPath); os.IsNotExist(err) {
					CreateSparse(devPath, devSize)
				}
				foundSecondary := false
				for _, existing := range inst.Rwdisk {
					if existing == devName { foundSecondary = true; break }
				}
				if !foundSecondary { inst.Rwdisk = append(inst.Rwdisk, devName) }
			}
		}
	}

	if inst.Bootloader == "container" {
		b.Log.Info(ctx, "configured manifest for container execution", "instance", inst.Name)
		return nil
	}

	for _, diskName := range inst.Rwdisk {
		if diskName != baseDiskName { continue }
		srcDiskPath := filepath.Join(b.RootDir, diskName)
		dstDiskPath := filepath.Join(targetDir, diskName)
		if _, err := os.Stat(dstDiskPath); os.IsNotExist(err) {
			if _, err := os.Stat(srcDiskPath); err == nil {
				CopySparse(srcDiskPath, dstDiskPath)
			}
		}
	}

	bootAssets := []string{"kernel", "initrd"}
	baseImageDir := filepath.Join(b.RuntimeDir, "images", baseImage)

	for _, asset := range bootAssets {
		dstPath := filepath.Join(targetDir, asset)
		if _, err := os.Stat(dstPath); err == nil { continue } 
		
		srcPath := filepath.Join(b.RootDir, asset)
		if _, err := os.Stat(srcPath); os.IsNotExist(err) {
			srcPath = filepath.Join(baseImageDir, asset)
		}
		if _, err := os.Stat(srcPath); err == nil {
			CopySparse(srcPath, dstPath)
		}
	}

	if kind == "Instance" {
		QueuePurification(b.RuntimeDir, []string{
			filepath.Join(targetDir, baseDiskName),
			filepath.Join(targetDir, "cidata.iso"),
		})
	}

	return nil
}

func (b *BuildApp) CopyBlueprints(ctx context.Context, outDir string) error {
	blueprintDir := filepath.Join(outDir, "blueprint")
	os.MkdirAll(blueprintDir, 0755)

	if b.Dockerfile != "" {
		if data, err := os.ReadFile(b.Dockerfile); err == nil {
			lines := strings.Split(string(data), "\n")
			var newLines []string
			
			if b.ActiveManifest != nil && b.ActiveManifest.Provenance != nil && b.ActiveManifest.Provenance.BuildId != "" {
				newLines = append(newLines, fmt.Sprintf("# CANONICAL_NAME: %s", b.Name))
				newLines = append(newLines, fmt.Sprintf("# LAST_BUILD: %s", b.ActiveManifest.Provenance.BuildId))
			}

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
