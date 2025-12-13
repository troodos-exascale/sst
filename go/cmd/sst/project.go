package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

var (
	projectForce      bool
	projectDevcontainer string
)

var projectCmd = &cobra.Command{
	Use:   "project <path>",
	Short: "Activate a component or scaffold/activate a project.",
	Long: `Activates a component for development (e.g., 'components/s2/svc/my-svc')
or scaffolds/activates a user project (e.g., 'projects/s2/m1/braam').

This command creates the necessary symbolic links at the repository root and
generates the 'active-project.mk' file to set the active development context.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		path := args[0]
		// Delegate all logic to the centralized sst.Project interface.
		err := core.Project().Activate(path, projectDevcontainer, projectForce)
		if err != nil {
			return fmt.Errorf("project command failed: %w", err)
		}
		return nil
	},
}

func init() {
	rootCmd.AddCommand(projectCmd)
	projectCmd.Flags().BoolVarP(&projectForce, "force", "f", false, "Force overwrite of existing symlinks and config files")
	projectCmd.Flags().StringVarP(&projectDevcontainer, "container", "c", "", "Explicitly set the devcontainer path for a component")
}
