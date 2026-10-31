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
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/troodos-exascale/sst/internal/sst"
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

	// Fix DNS within the isolated chroot safely
	if _, err := os.Stat("/run/systemd/resolve/resolv.conf"); err == nil {
		sst.ExecShell(ctx, b.Log, "cp", "-L", "/run/systemd/resolve/resolv.conf", filepath.Join(targetDir, "etc/resolv.conf"))
	} else {
		sst.ExecShell(ctx, b.Log, "cp", "-L", "/etc/resolv.conf", filepath.Join(targetDir, "etc/resolv.conf"))
	}

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
