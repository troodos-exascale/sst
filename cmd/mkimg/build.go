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
	"github.com/swiss-software-tool/sst/internal/rtsst"
	"github.com/swiss-software-tool/sst/internal/sst"
)

var (
	buildArgs    []string
	flagFile     string
	flagBuildDir string

	flagRestart int
	flagShow    bool
	flagSource  string
)

// RunBuildEngine is the universal entrypoint for parsing and executing VAXLang DAGs.
func RunBuildEngine(ctx context.Context, dockerfile, contextDir, buildDir, runtimeDir string, flagForce bool, bArgs map[string]string) error {
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

	bState, _ := builder.LoadState()

	if flagShow {
		fmt.Printf("--- Build Summary: %s ---\n", builder.Name)
		for _, inst := range builder.Instructions {
			step, ok := bState.Steps[inst.Index]
			if ok {
				fmt.Printf("[Step %02d] %-8s | Log: %s\n", inst.Index, step.Status, step.LogPath)
			} else {
				fmt.Printf("[Step %02d] PENDING  | %s\n", inst.Index, inst.Type)
			}
		}
		return nil
	}

	startIndex := 0

	if flagForce {
		startIndex = 0
		_ = builder.SaveState(&rtsst.BuildState{Status: "new", Steps: make(map[int]rtsst.BuildStepState)})
	} else if flagRestart >= 0 {
		startIndex = flagRestart
		log.Info(ctx, "manual restart requested", "step", startIndex)
	} else {
		divergence := -1
		for i, inst := range builder.Instructions {
			savedStep, ok := bState.Steps[i]
			if !ok || savedStep.Hash != inst.Hash || savedStep.Status != "OK" {
				divergence = i
				break
			}
		}
		if divergence == -1 {
			if bState.Status == "building" || bState.Status == "OK" {
				fmt.Println("✅ Build is up to date. No changed steps.")
				fmt.Printf("To force a cache refresh from step N, run: mkimg build --restart N -f %s\n", filepath.Base(builder.Dockerfile))
				return nil
			}
			startIndex = len(builder.Instructions)
		} else {
			startIndex = divergence
			log.Info(ctx, "detected hash divergence or failure", "step", startIndex)
		}
	}

	for i := 0; i < startIndex; i++ {
		inst := builder.Instructions[i]

		cleanArgs := inst.Args
		if inst.Type != "RUN" {
			cleanArgs = strings.SplitN(builder.Substitute(inst.Args), " #", 2)[0]
			cleanArgs = strings.TrimSpace(cleanArgs)
		}

		switch inst.Type {
		case "WORKDIR":
			builder.Workdir = builder.Substitute(cleanArgs)
		case "USER":
			builder.User = builder.Substitute(cleanArgs)
		case "RUN":
			if strings.HasPrefix(inst.Args, "sys.provision_dataset") {
				parts := strings.Fields(inst.Args)
				for _, p := range parts {
					if strings.HasPrefix(p, "--target=") {
						target := strings.TrimPrefix(p, "--target=")
						builder.StageMounts[target] = builder.DatasetMgr.MountPath("as-roots/" + target)
					}
				}
			}
		}
	}

	if startIndex > 0 && startIndex <= len(builder.Instructions) && len(builder.StageMounts) > 0 {
		prevInst := builder.Instructions[startIndex-1]
		hashStub := ""
		if len(prevInst.Hash) >= 8 {
			hashStub = prevInst.Hash[:8]
		}
		tag := fmt.Sprintf("step_%02d_hash_%s", prevInst.Index, hashStub)
		log.Info(ctx, "rolling back active datasets to previous valid state", "tag", tag)
		for stage := range builder.StageMounts {
			_ = builder.DatasetMgr.RollbackDataset("as-roots/"+stage, tag)
		}
	}

	runner := sst.StepsInit("mkimage.build")
	runner.StartIndex = startIndex

	for i, inst := range builder.Instructions {
		instruction := inst
		stepName := fmt.Sprintf("%s %02d", instruction.Type, i)

		runner.StepsAdd(stepName, func(c *sst.StepsContext, _ sst.StepState) error {
			return builder.ExecuteSingleInstruction(ctx, instruction)
		})
	}

	runner.StepsAdd("FINALIZE MANIFEST", func(c *sst.StepsContext, _ sst.StepState) error {
		if strings.HasSuffix(builder.Dockerfile, "instance.dockerfile") {
			return builder.FinalizeInstance(ctx)
		}
		return builder.FinalizeVM(ctx)
	})

	return sst.StepsRun(runner)
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

		return RunBuildEngine(cmd.Context(), flagFile, contextDir, flagBuildDir, runtimeDir, flagForce, bArgs)
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
