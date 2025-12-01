package main

import (
	"os"
	"path/filepath"
)

func init() {
	// Default to user's home .runtimes
	// The convert command will append "/images/<name>" to this root.
	home, _ := os.UserHomeDir()
	defaultRoot := filepath.Join(home, ".runtimes")

	convertCmd.Flags().StringVarP(&flagRoot, "dir", "d", defaultRoot,
		"Runtime root directory (e.g. /Users/you/.runtimes)")

	convertCmd.Flags().BoolVar(&flagForce, "force", false,
		"Overwrite existing image directory if it exists")

	convertCmd.Flags().BoolVar(&flagVerbose, "verbose", false,
		"Verbose logging")
}
