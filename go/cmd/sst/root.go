package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/swiss-software-tool/runtime-smart/go/internal/sst"
)

// This variable holds the core library functionality, making it available to all subcommands.
var core sst.Core

var rootCmd = &cobra.Command{
	Use:           "sst",
	Short:         "Swiss Service Tool for federated software lifecycle management.",
	SilenceUsage:  true,
	SilenceErrors: true,
	// CORRECTED: Use PersistentPreRunE to allow for error returns and proper chaining.
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		// Initialize the core sst library once.
		var err error
		core, err = sst.New()
		if err != nil {
			// Return an error instead of calling log.Fatalf directly.
			return fmt.Errorf("could not initialize sst core library: %w", err)
		}
		return nil
	},
}

// Execute is the main entry point for the CLI. It now handles printing errors and exiting.
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
