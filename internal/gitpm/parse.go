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
		Use:   "gitpm",
		Short: "Build reproducible .deb and .rpm packages (SST/gitpm)",
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
	)

	cmd := &cobra.Command{
		Use:   "build",
		Short: "Build a deterministic package (or manifest set)",
		Long: `gitpm build builds deterministic .deb and .rpm packages.

Usage:
  gitpm build -s dir -t deb -n mypkg -v 1.0 -C root/
  gitpm build -f manifests/*.yaml
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Manifest mode takes precedence if -f/--file was given
			if manifestGlob != "" {
				files, err := filepath.Glob(manifestGlob)
				if err != nil {
					return err
				}
				if len(files) == 0 {
					return fmt.Errorf("no manifest files matched %q", manifestGlob)
				}
				return BuildFromManifest(files)
			}

			// Legacy flag-based behavior
			if name == "" {
				return fmt.Errorf("package name (-n) required")
			}
			if targetType == "" {
				targetType = "deb"
			}
			if sourceType == "" {
				sourceType = "dir"
			}

			opts := Options{
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
				out, err = BuildDeb(&opts)
			case "rpm":
				out, err = BuildRPM(&opts)
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

	// Old flags (unchanged)
	cmd.Flags().StringVarP(&sourceType, "source", "s", "dir", "source type (dir)")
	cmd.Flags().StringVarP(&targetType, "type", "t", "deb", "target type (deb|rpm)")
	cmd.Flags().StringVarP(&name, "name", "n", "", "package name")
	cmd.Flags().StringVarP(&version, "version", "v", "0.0.0", "package version")
	cmd.Flags().StringVarP(&prefix, "prefix", "p", "/", "install prefix")
	cmd.Flags().StringVarP(&buildDir, "chdir", "C", ".", "directory to package")
	cmd.Flags().StringVarP(&description, "desc", "d", "", "package description")

	// NEW: manifest flag
	cmd.Flags().StringVarP(&manifestGlob, "file", "f", "", "YAML manifest file (glob allowed)")

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
		Long: `gitpm verify compares .deb and .rpm archives to ensure deterministic builds.

Example:
  gitpm verify --deb dist/demo_1.0.0_arm64.deb --rpm dist/demo_1.0.0_arm64.rpm
`,
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

// ---------------------------------------------------------------------
// UTILITIES
// ---------------------------------------------------------------------

// must is a helper to panic on error (used only in early startup).
func must(err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
