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
	"path/filepath"

	"github.com/spf13/cobra"
	rtsst "github.com/troodos-exascale/sst/internal/rtsst"
)

var flagRuntimeType string

// Automatically register the command with the root CLI
func init() {
	rootCmd.AddCommand(runtimeCmd())
}

func runtimeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "runtime <image-name> <runtime-name> [dockerfile-path]",
		Short: "Export a bootable runtime from an image directory",
		Args:  cobra.RangeArgs(2, 3), // Now accepts an optional 3rd argument
		RunE: func(cmd *cobra.Command, args []string) error {
			runtimeDir := flagRoot
			if runtimeDir == "" {
				runtimeDir = rtsst.RuntimeDir()
			}

			imageName := args[0]
			runtimeName := args[1]

			// Setup catalog directories
			targetDir := filepath.Join(runtimeDir, "runtimes", imageName, runtimeName)
			
			dockerfilePath := filepath.Join(targetDir, "runtime.dockerfile")
			contextDir := "."

			// If the user provided a 3rd argument, use it as the dockerfile source!
			if len(args) == 3 {
				dockerfilePath = args[2]
				// We keep ContextDir as "." so repo-relative INCLUDEs (like templates/...) resolve natively
			}

			if _, err := os.Stat(dockerfilePath); os.IsNotExist(err) {
				return fmt.Errorf("runtime dockerfile not found at %s", dockerfilePath)
			}

			bArgs := map[string]string{
				"BASE_IMAGE":   imageName,
				"RUNTIME_NAME": runtimeName,
				"RUNTIME_TYPE": flagRuntimeType,
			}
			
			// Execute the build engine against the localized or provided dockerfile
			return RunBuildEngine(cmd.Context(), dockerfilePath, contextDir, flagBuildDir, runtimeDir, flagForce, bArgs, -1)
		},
	}
	cmd.Flags().StringVarP(&flagBuildDir, "builddir", "b", "/runtimes", "Base directory for ZFS extraction")
	cmd.Flags().StringVarP(&flagRuntimeType, "type", "t", "developer", "Type of runtime template to use")
	return cmd
}
