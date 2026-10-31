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

package gitpm

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

// NewRootCmd is the top-level CLI constructor.
func NewRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:          "gitpm",
		Short:        "Build reproducible .deb and .rpm packages (SST/gitpm)",
		SilenceUsage: true, // Suppress help menu on execution failures
	}
	cmd.AddCommand(newBuildCmd())
	cmd.AddCommand(newVerifyCmd())
	return cmd
}

// ---------------------------------------------------------------------
// BUILD
// ---------------------------------------------------------------------

func newBuildCmd() *cobra.Command {
	var (
		sourceType   string
		targetType   string
		name         string
		version      string
		prefix       string
		buildDir     string
		description  string
		manifestGlob string

		// Time-Travel REPL Flags
		flagList   bool
		flagCont   bool
		flagClean  bool
		flagRevert string
		flagDebug  bool
	)

	cmd := &cobra.Command{
		Use:   "build",
		Short: "Build a deterministic package (or manifest set)",
		Long: `gitpm build builds deterministic .deb and .rpm packages.

Time-Travel REPL (ZFS-backed):
  gitpm build -f pkg.yaml --list        # View execution steps and cached status
  gitpm build -f pkg.yaml --revert 03   # Rollback to the state just before step 03
  gitpm build -f pkg.yaml --cont        # Continue execution, using cached snapshots
  gitpm build -f pkg.yaml --clean       # Destroy all cached ZFS snapshots for this package
  gitpm build -f my-project/            # Build via local clone using .gitpm/manifest.yaml
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			opts := &BuildSessionOptions{
				List:   flagList,
				Cont:   flagCont,
				Clean:  flagClean,
				Revert: flagRevert,
				Debug:  flagDebug,
			}

			// Manifest mode takes precedence if -f/--file was given
			if manifestGlob != "" {
				// 1. Check if it's a local directory (triggers ZFS clone behavior)
				stat, err := os.Stat(manifestGlob)
				if err == nil && stat.IsDir() {
					return BuildTarget(cmd.Context(), manifestGlob, opts)
				}

				// 2. Otherwise, treat it as a file or glob of files
				files, err := filepath.Glob(manifestGlob)
				if err != nil {
					return err
				}
				if len(files) == 0 {
					return fmt.Errorf("no manifest files matched %q", manifestGlob)
				}

				for _, f := range files {
					if err := BuildTarget(cmd.Context(), f, opts); err != nil {
						return err
					}
				}
				return nil
			}

			// Legacy flag-based behavior (does not support Time-Travel DAG)
			if name == "" {
				return fmt.Errorf("package name (-n) required")
			}
			if targetType == "" {
				targetType = "deb"
			}
			if sourceType == "" {
				sourceType = "dir"
			}

			legacyOpts := Options{
				InputType:   sourceType,
				OutputType:  targetType,
				Name:        name,
				Version:     version,
				Prefix:      prefix,
				Chdir:       buildDir,
				Description: description,
			}

			var (
				out string
				err error
			)
			switch targetType {
			case "deb":
				out, err = BuildDeb(&legacyOpts)
			case "rpm":
				out, err = BuildRPM(&legacyOpts)
			default:
				return fmt.Errorf("unsupported target type: %s", targetType)
			}
			if err != nil {
				return err
			}
			fmt.Printf("✅ built %s\n", out)
			return nil
		},
	}

	cmd.Flags().StringVarP(&sourceType, "source", "s", "dir", "source type (dir)")
	cmd.Flags().StringVarP(&targetType, "type", "t", "deb", "target type (deb|rpm)")
	cmd.Flags().StringVarP(&name, "name", "n", "", "package name")
	cmd.Flags().StringVarP(&version, "version", "v", "0.0.0", "package version")
	cmd.Flags().StringVarP(&prefix, "prefix", "p", "/", "install prefix")
	cmd.Flags().StringVarP(&buildDir, "chdir", "C", ".", "directory to package")
	cmd.Flags().StringVarP(&description, "desc", "d", "", "package description")

	cmd.Flags().StringVarP(&manifestGlob, "file", "f", "", "YAML manifest file or directory")

	cmd.Flags().BoolVar(&flagList, "list", false, "List the parsed AST steps and their completion status")
	cmd.Flags().BoolVar(&flagCont, "cont", false, "Continue a failed build from the last successful snapshot")
	cmd.Flags().BoolVar(&flagClean, "clean", false, "Remove all ZFS snapshots associated with this package")
	cmd.Flags().StringVar(&flagRevert, "revert", "", "Revert the ZFS dataset to the state just before the given step index (e.g., '03')")
	cmd.Flags().BoolVar(&flagDebug, "debug", false, "Enable verbose shell execution output")

	return cmd
}

// ---------------------------------------------------------------------
// VERIFY
// ---------------------------------------------------------------------

func newVerifyCmd() *cobra.Command {
	var (
		debPath string
		rpmPath string
	)
	cmd := &cobra.Command{
		Use:   "verify",
		Short: "Verify deterministic builds",
		Long: `gitpm verify compares .deb and .rpm archives to ensure deterministic builds.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if debPath == "" || rpmPath == "" {
				return fmt.Errorf("--deb and --rpm required")
			}
			return ComparePackages(debPath, rpmPath)
		},
	}
	cmd.Flags().StringVar(&debPath, "deb", "", "path to deb")
	cmd.Flags().StringVar(&rpmPath, "rpm", "", "path to rpm")
	return cmd
}

func must(err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
