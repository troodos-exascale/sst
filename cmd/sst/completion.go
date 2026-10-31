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

func init() {
	rootCmd.AddCommand(completionCmd)
}

var completionCmd = &cobra.Command{
	Use:   "completion [bash|zsh|fish|powershell]",
	Short: "Generate completion script",
	Long: `To load completions:

Bash:
  $ source <(sst completion bash)

  # To load completions for each session, execute once:
  # Linux:
  $ sst completion bash > /etc/bash_completion.d/sst
  # macOS:
  $ sst completion bash > "$(brew --prefix)/etc/bash_completion.d/sst"
`,
	DisableFlagsInUseLine: true,
	ValidArgs:             []string{"bash", "zsh", "fish", "powershell"},
	// CORRECTED: Use cobra.MatchAll as recommended by staticcheck.
	Args: cobra.MatchAll(cobra.ExactArgs(1), cobra.OnlyValidArgs),
	Run: func(cmd *cobra.Command, args []string) {
		var err error
		switch args[0] {
		case "bash":
			// CORRECTED: Check error return value.
			err = cmd.Root().GenBashCompletion(os.Stdout)
		case "zsh":
			// CORRECTED: Check error return value.
			err = cmd.Root().GenZshCompletion(os.Stdout)
		case "fish":
			// CORRECTED: Check error return value.
			err = cmd.Root().GenFishCompletion(os.Stdout, true)
		case "powershell":
			// CORRECTED: Check error return value.
			err = cmd.Root().GenPowerShellCompletionWithDesc(os.Stdout)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error generating completion script: %v\n", err)
			os.Exit(1)
		}
	},
}
