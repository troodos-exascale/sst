package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// rootCmd is the top-level CLI command.
var rootCmd = &cobra.Command{
	Use:           "sst",
	Short:         "Swiss Service Tool for federated software lifecycle management.",
	SilenceUsage:  true,
	SilenceErrors: true,
}

// Execute is the main entry point for the CLI.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		// Since cobra's SilenceErrors is on, we are responsible for printing the error.
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func init() {
	// Wire up the persistent debug flags defined in flags_debug.go
	AttachDebugFlags(rootCmd)
}
