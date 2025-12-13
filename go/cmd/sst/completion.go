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