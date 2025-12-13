package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(checkMakefilesCmd)
}

var checkMakefilesCmd = &cobra.Command{
	Use:   "check-makefiles",
	Short: "Check that Makefiles are symbolic links (warn if not).",
	RunE: func(cmd *cobra.Command, args []string) error {
		root := core.Files().Root()
		var bad []string
		err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil // skip errors
			}
			if info.IsDir() {
				return nil
			}
			if filepath.Base(path) == "Makefile" {
				if _, err := os.Lstat(path); err == nil {
					if fi, _ := os.Lstat(path); fi.Mode()&os.ModeSymlink == 0 {
						rel, _ := filepath.Rel(root, path)
						bad = append(bad, rel)
					}
				}
			}
			return nil
		})
		if err != nil {
			return err
		}

		if len(bad) > 0 {
			fmt.Println("--> Makefile's can often be symbolic links for simplicity, see README.md . following are not symlinks:")
			for _, f := range bad {
				fmt.Printf("   %s\n", f)
			}
		}
		return nil
	},
}
