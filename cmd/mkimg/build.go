package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/swiss-software-tool/sst/internal/rtsst"
	"github.com/swiss-software-tool/sst/internal/sst"
)

var buildArgs []string

// RunBuildEngine is the universal entrypoint for parsing and executing VAXLang DAGs.
func RunBuildEngine(ctx context.Context, dockerfile, runtimeDir string, bArgs map[string]string) error {
	log := sst.LogxRoot().Subsystem("mkimage.build")
	builder := rtsst.NewBuilder(log, nil, dockerfile, runtimeDir)
	
	for k, v := range bArgs {
		builder.BuildArgs[k] = v
	}

	if err := builder.Parse(ctx); err != nil { return err }
	if err := builder.PreProcess(ctx); err != nil { return err }

	runner := sst.StepsInit("mkimage.build")
	
	for i, inst := range builder.Instructions {
		instruction := inst 
		stepName := fmt.Sprintf("%s %02d", instruction.Type, i+1)
		
		runner.StepsAdd(stepName, func(c *sst.StepsContext, _ sst.StepState) error {
			return builder.ExecuteSingleInstruction(ctx, instruction)
		})
	}

	// Finalize writes the declarative image.yaml at the end of the graph
	runner.StepsAdd("FINALIZE MANIFEST", func(c *sst.StepsContext, _ sst.StepState) error {
		if strings.HasSuffix(builder.Dockerfile, "instance.dockerfile") {
			return builder.FinalizeInstance(ctx)
		}
		return builder.FinalizeVM(ctx)
	})

	return sst.StepsRun(runner)
}

var buildCmd = &cobra.Command{
	Use:   "build <dockerfile>",
	Short: "Builds a bootable VM image from a hybrid Dockerfile",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		runtimeDir := flagRoot
		if runtimeDir == "" { runtimeDir = rtsst.RuntimeDir() }

		bArgs := make(map[string]string)
		for _, arg := range buildArgs {
			parts := strings.SplitN(arg, "=", 2)
			if len(parts) == 2 { bArgs[parts[0]] = parts[1] } else { bArgs[parts[0]] = os.Getenv(parts[0]) }
		}

		return RunBuildEngine(cmd.Context(), args[0], runtimeDir, bArgs)
	},
}

func init() {
	buildCmd.Flags().StringSliceVar(&buildArgs, "build-arg", nil, "Set build-time variables (e.g., DEBARCH=amd64)")
	rootCmd.AddCommand(buildCmd)
}
