package main

import (
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/spf13/cobra"
	"github.com/swiss-software-tool/sst/internal/rtsst"
	"github.com/swiss-software-tool/sst/internal/sst"
)

var buildArgs []string

var buildCmd = &cobra.Command{
	Use:   "build <dockerfile>",
	Short: "Builds a bootable VM image from a hybrid Dockerfile",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if runtime.GOOS != "linux" {
			return fmt.Errorf("'mkimage build' requires Linux kernel features (chroot, loop devices). On macOS, this command must be executed inside a Linux container.")
		}

		dockerfile := args[0]
		log := sst.LogxRoot().Subsystem("mkimage.build")
		
		runtimeDir := flagRoot
		if runtimeDir == "" {
			runtimeDir = rtsst.RuntimeDir()
		}

		builder := rtsst.NewBuilder(log, nil, dockerfile, runtimeDir)
		
		for _, arg := range buildArgs {
			parts := strings.SplitN(arg, "=", 2)
			if len(parts) == 2 {
				builder.BuildArgs[parts[0]] = parts[1]
			} else {
				builder.BuildArgs[parts[0]] = os.Getenv(parts[0])
			}
		}

		if err := builder.Parse(cmd.Context()); err != nil {
			return err
		}
		
		if err := builder.PreProcess(cmd.Context()); err != nil {
			return err
		}

		runner := sst.StepsInit("mkimage.build")
		
		runner.StepsAdd("prepare workspace", func(ctx *sst.StepsContext, _ sst.StepState) error {
			return builder.ResolveOCIPaths()
		})
		runner.StepsAdd("fetch base image", func(ctx *sst.StepsContext, _ sst.StepState) error {
			return builder.FetchOCI(cmd.Context())
		})
		runner.StepsAdd("unpack rootfs", func(ctx *sst.StepsContext, _ sst.StepState) error {
			return builder.UnpackOCI(cmd.Context())
		})

		for i, inst := range builder.Instructions {
			instruction := inst 
			stepName := fmt.Sprintf("%s %d: %s", instruction.Type, i+1, strings.Split(instruction.Args, "\n")[0])
			
			runner.StepsAdd(stepName, func(ctx *sst.StepsContext, _ sst.StepState) error {
				return builder.ExecuteSingleInstruction(cmd.Context(), instruction)
			})
		}

		return sst.StepsRun(runner)
	},
}

func init() {
	buildCmd.Flags().StringSliceVar(&buildArgs, "build-arg", nil, "Set build-time variables (e.g., DEBARCH=amd64)")
	rootCmd.AddCommand(buildCmd)
}
