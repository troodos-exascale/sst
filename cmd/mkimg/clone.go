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
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	rtsst "github.com/troodos-exascale/sst/internal/rtsst"
)

func init() {
	rootCmd.AddCommand(cloneCmd())
}

func cloneCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "clone <source-url-or-local-path> [name]",
		Aliases: []string{"import", "convert"},
		Short:   "Clone a VM image (qcow2/raw) from a URL or file into a native SST capsule",
		Args:    cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			runtimeDir := flagRoot
			if runtimeDir == "" {
				runtimeDir = rtsst.RuntimeDir()
			}

			source := args[0]
			if !strings.HasPrefix(source, "http://") && !strings.HasPrefix(source, "https://") {
				// Convert local paths to absolute for reliable resolution in the VM delegation context
				if absSource, err := filepath.Abs(source); err == nil {
					source = absSource
				}
			}

			name := ""
			if len(args) == 2 {
				name = args[1]
			} else {
				base := filepath.Base(source)
				name = strings.TrimSuffix(base, filepath.Ext(base))
				name = strings.TrimSuffix(name, ".qcow2")
				name = strings.TrimSuffix(name, ".raw")
				name = strings.TrimSuffix(name, ".img")
			}

			bArgs := map[string]string{
				"CLONE_SOURCE": source,
				"CLONE_NAME":   name,
			}
			
			// Execute build engine. InitEnvironment will handle Destroy() if flagForce is set.
			return RunBuildEngine(cmd.Context(), "clone.dockerfile", ".", flagBuildDir, runtimeDir, flagForce, bArgs, -1)
		},
	}
}
