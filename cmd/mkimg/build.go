package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/troodos-exascale/sst/internal/rtsst"
	"github.com/troodos-exascale/sst/internal/sst"
)

var (
	buildArgs    []string
	flagFile     string
	flagBuildDir string

	flagRestart int
	flagShow    bool
	flagSource  string
)

func RunBuildEngine(ctx context.Context, dockerfile, contextDir, buildDir, runtimeDir string, flagForce bool, bArgs map[string]string, restartIndex int) error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("mkimg requires root privileges to provision datasets and execute isolated environments. Please run with sudo")
	}

	log := sst.LogxRoot().Subsystem("mkimage.build")
	builder := rtsst.NewBuilder(log, nil, dockerfile, contextDir, buildDir, runtimeDir)
	builder.Force = flagForce

	for k, v := range bArgs {
		builder.BuildArgs[k] = v
	}

	goArch := runtime.GOARCH

	builder.BuildArgs["ARCH"] = goArch
	builder.BuildArgs["DEB_ARCH"] = goArch

	var rpmArch string
	if goArch == "arm64" {
		rpmArch = "aarch64"
	} else if goArch == "amd64" {
		rpmArch = "x86_64"
	} else {
		rpmArch = goArch 
	}
	builder.BuildArgs["RPM_ARCH"] = rpmArch
	builder.BuildArgs["ALPINE_ARCH"] = rpmArch

	if err := builder.Parse(ctx); err != nil {
		return err
	}
	if err := builder.PreProcess(ctx); err != nil {
		return err
	}

	if flagSource != "" {
		parts := strings.Split(flagSource, ",")
		start, err := strconv.Atoi(strings.TrimSpace(parts[0]))
		if err != nil {
			return fmt.Errorf("invalid source step index: %s", parts[0])
		}
		end := start
		if len(parts) > 1 {
			if parsedEnd, err := strconv.Atoi(strings.TrimSpace(parts[1])); err == nil {
				end = parsedEnd
			}
		}
		fmt.Printf("--- IR Source (Steps %d to %d) ---\n", start, end)
		for _, inst := range builder.Instructions {
			if inst.Index >= start && inst.Index <= end {
				fmt.Printf("[Step %02d] %s %s\n", inst.Index, inst.Type, inst.Args)
			}
		}
		return nil
	}

	if err := builder.InitEnvironment(ctx); err != nil {
		return err
	}

	runner := &sst.Runner{
		Log:       log,
		StatePath: filepath.Join(builder.DatasetMgr.MountPath(""), "state.json"),
		State:     make(map[string]interface{}),
		Completed: make(map[string]bool),
	}

	builder.RegisterHandlers(runner)

	if err := runner.LoadState(); err == nil {
		stateMutated := false

		for i, inst := range builder.Instructions {
			if restartIndex >= 0 && inst.Index >= restartIndex {
				if runner.Completed[inst.Hash] {
					delete(runner.Completed, inst.Hash)
					stateMutated = true
				}
				continue
			}

			if runner.Completed[inst.Hash] && builder.DatasetMgr != nil {
				var nextHashStub string
				if i+1 < len(builder.Instructions) {
					nextHashStub = builder.Instructions[i+1].Hash
					if len(nextHashStub) >= 8 {
						nextHashStub = nextHashStub[:8]
					}
				} else {
					nextHashStub = "final"
				}

				tag := fmt.Sprintf("step_%02d_hash_%s", inst.Index, nextHashStub)
				
				if !builder.DatasetMgr.CheckSnapshot(builder.DatasetMgr.RuntimeRootName, tag) {
					log.Warn(ctx, "zfs out of sync with state.json; pruning future", "step", inst.Index, "missing_tag", tag)
					delete(runner.Completed, inst.Hash)
					stateMutated = true
				}
			}
		}

		if stateMutated {
			_ = runner.SaveState()
		}
	}

	if flagShow {
		fmt.Printf("--- Build Summary: %s ---\n", builder.Name)
		for _, inst := range builder.Instructions {
			status := "PENDING"
			if runner.Completed[inst.Hash] {
				status = "OK"
			}
			
			hashStub := inst.Hash
			if len(hashStub) >= 8 { hashStub = hashStub[:8] }
			logPath := filepath.Join(builder.DatasetMgr.MountPath(""), "logs", fmt.Sprintf("%02d_%s_%s.log", inst.Index, hashStub, strings.ToLower(inst.Type)))
			
			if _, err := os.Stat(logPath); err != nil {
				logPath = "-"
			}

			fmt.Printf("[Step %02d] %-8s | Log: %s\n", inst.Index, status, logPath)
		}
		return nil
	}

	var root *sst.Context
	var prev *sst.Context

	parentHash := ""

	for _, inst := range builder.Instructions {
		stepCtx := builder.CreateStepContext(inst, parentHash)
		
		if root == nil {
			root = stepCtx
		}

		if prev != nil {
			nextCtx := stepCtx
			prev.Interpret = func(res *sst.Result) ([]*sst.Context, error) {
				if builder.DatasetMgr != nil {
					hashStub := nextCtx.ID
					if len(hashStub) >= 8 { hashStub = hashStub[:8] }
					tag := fmt.Sprintf("step_%02d_hash_%s", inst.Index-1, hashStub)
					for stageTarget := range inst.StageMounts {
						_ = builder.DatasetMgr.SnapshotDataset(stageTarget, tag)
					}
					_ = builder.DatasetMgr.SnapshotDataset(builder.DatasetMgr.RuntimeRootName, tag)
				}
				return []*sst.Context{nextCtx}, nil
			}
		}

		prev = stepCtx
		parentHash = inst.Hash
	}

	return runner.Run(ctx, root)
}

var buildCmd = &cobra.Command{
	Use:   "build [context-dir]",
	Short: "Builds a bootable VM image from a hybrid Dockerfile",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		runtimeDir := flagRoot
		if runtimeDir == "" {
			runtimeDir = rtsst.RuntimeDir()
		}

		contextDir := "."
		if len(args) > 0 {
			contextDir = args[0]
		}

		bArgs := make(map[string]string)
		for _, arg := range buildArgs {
			parts := strings.SplitN(arg, "=", 2)
			if len(parts) == 2 {
				bArgs[parts[0]] = parts[1]
			} else {
				bArgs[parts[0]] = os.Getenv(parts[0])
			}
		}

		return RunBuildEngine(cmd.Context(), flagFile, contextDir, flagBuildDir, runtimeDir, flagForce, bArgs, flagRestart)
	},
}

func init() {
	buildCmd.Flags().StringSliceVar(&buildArgs, "build-arg", nil, "Set build-time variables (e.g., DEBARCH=amd64)")
	buildCmd.Flags().StringVarP(&flagFile, "file", "f", "./Dockerfile", "path to the Dockerfile")
	buildCmd.Flags().StringVarP(&flagBuildDir, "builddir", "b", "/runtimes", "Base directory for ZFS extraction and intermediate builds")

	buildCmd.Flags().IntVarP(&flagRestart, "restart", "r", -1, "Restart build from specific step index")
	buildCmd.Flags().BoolVarP(&flagShow, "show", "s", false, "Show output summary of all build steps")
	buildCmd.Flags().StringVar(&flagSource, "source", "", "Show IR step(s) source (e.g. '3' or '3,5')")

	rootCmd.AddCommand(buildCmd)
}
