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
