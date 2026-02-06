package main

import (
	"fmt"
	"strings" // Added missing import

	"github.com/spf13/cobra"
	"github.com/swiss-software-tool/runtime-smart/go/internal/rtsst"
	"github.com/swiss-software-tool/runtime-smart/go/internal/sst"
)

var buildCmd = &cobra.Command{
	Use:   "build <dockerfile>",
	Short: "Builds a bootable VM image from a hybrid Dockerfile",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		dockerfile := args[0]
		log := sst.LogxRoot().Subsystem("mkimage.build")
		builder := rtsst.NewBuilder(log, nil, dockerfile)
		
		// 1. Initial Parse to discover instructions
		if err := builder.Parse(cmd.Context()); err != nil {
			return err
		}

		runner := sst.StepsInit("mkimage.build")
		
		// 2. Setup staging and OCI retrieval
		runner.StepsAdd("prepare workspace", func(ctx *sst.StepsContext, _ sst.StepState) error {
			return builder.ResolveOCIPaths()
		})
		runner.StepsAdd("fetch base image", func(ctx *sst.StepsContext, _ sst.StepState) error {
			return builder.FetchOCI(cmd.Context())
		})
		runner.StepsAdd("unpack rootfs", func(ctx *sst.StepsContext, _ sst.StepState) error {
			return builder.UnpackOCI(cmd.Context())
		})

		// 3. Dynamically register instructions as individual steps
		for i, inst := range builder.Instructions {
			instruction := inst // capture for closure
			// Use strings.Split to show only the first line of multiline blocks in the step name
			stepName := fmt.Sprintf("%s %d: %s", instruction.Type, i+1, strings.Split(instruction.Args, "\n")[0])
			
			runner.StepsAdd(stepName, func(ctx *sst.StepsContext, _ sst.StepState) error {
				return builder.ExecuteSingleInstruction(cmd.Context(), instruction)
			})
		}

		// 4. Final conversion to VM image
		runner.StepsAdd("finalize vm output", func(ctx *sst.StepsContext, _ sst.StepState) error {
			return builder.FinalizeVM(cmd.Context())
		})

		return sst.StepsRun(runner)
	},
}

func init() {
	rootCmd.AddCommand(buildCmd)
}
