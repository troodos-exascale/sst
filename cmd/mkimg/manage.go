package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/swiss-software-tool/sst/internal/rtsst"
	"github.com/swiss-software-tool/sst/internal/sst"
)

func init() {
	rootCmd.AddCommand(mvImageCmd)
	rootCmd.AddCommand(mvInstanceCmd)
	rootCmd.AddCommand(rmImageCmd)
	rootCmd.AddCommand(rmInstanceCmd)
	rootCmd.AddCommand(catalogCmd)
}

// resolveCatalog finds the absolute target path of a symlink in the global registry
func resolveCatalog(kind, name string) (string, error) {
	link := filepath.Join(rtsst.RuntimeDir(), kind, name)
	info, err := os.Lstat(link)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return filepath.EvalSymlinks(link)
	}
	return link, nil
}

// artifactMvCompletion provides registry names for arg 1, and directory paths for arg 2
func artifactMvCompletion(kind string) func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if len(args) == 0 {
			names, _ := rtsst.ListCatalogNames(kind)
			var matches []string
			for _, n := range names {
				if strings.HasPrefix(n, toComplete) {
					matches = append(matches, n)
				}
			}
			return matches, cobra.ShellCompDirectiveNoFileComp
		}
		// Second argument is a destination directory, fallback to standard shell path completion
		return nil, cobra.ShellCompDirectiveFilterDirs
	}
}

// artifactRmCompletion strictly provides registry names for arg 1
func artifactRmCompletion(kind string) func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if len(args) == 0 {
			names, _ := rtsst.ListCatalogNames(kind)
			var matches []string
			for _, n := range names {
				if strings.HasPrefix(n, toComplete) {
					matches = append(matches, n)
				}
			}
			return matches, cobra.ShellCompDirectiveNoFileComp
		}
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
}

func moveArtifact(ctx context.Context, log *sst.Logx, kind, name, dest string) error {
	if kind == "instances" {
		if err := rtsst.EnsureInstanceNotBusy(name); err != nil {
			return err
		}
	}

	src, err := resolveCatalog(kind, name)
	if err != nil {
		return fmt.Errorf("could not resolve %s '%s': %w", kind, name, err)
	}

	destAbs, err := filepath.Abs(dest)
	if err != nil {
		return err
	}

	info, err := os.Stat(destAbs)
	if err == nil && info.IsDir() {
		// If they provided an existing directory, nest it inside
		destAbs = filepath.Join(destAbs, name)
	}

	if err := os.Rename(src, destAbs); err != nil {
		return fmt.Errorf("failed to move directory (if moving across different drives, use cp and rm): %w", err)
	}

	// Re-link the catalog to the new location
	if err := rtsst.UpdateCatalog(ctx, log, kind, name, destAbs, "moved"); err != nil {
		return fmt.Errorf("moved successfully, but failed to update catalog symlink: %w", err)
	}

	log.Info(ctx, "moved artifact and updated catalog", "kind", kind, "name", name, "new_path", destAbs)
	return nil
}

func rmArtifact(ctx context.Context, log *sst.Logx, kind, name string) error {
	if kind == "instances" {
		if err := rtsst.EnsureInstanceNotBusy(name); err != nil {
			return err
		}
	}

	targetDir, err := resolveCatalog(kind, name)
	if err != nil {
		return fmt.Errorf("could not resolve %s '%s': %w", kind, name, err)
	}

	entries, err := os.ReadDir(targetDir)
	if err != nil {
		return err
	}

	// Nuke everything EXCEPT the declarative dockerfiles
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".dockerfile") {
			if err := os.RemoveAll(filepath.Join(targetDir, e.Name())); err != nil {
				log.Warn(ctx, "failed to remove item", "item", e.Name(), "error", err)
			}
		}
	}

	// Clean up the global symlink
	symlink := filepath.Join(rtsst.RuntimeDir(), kind, name)
	_ = os.Remove(symlink)

	log.Info(ctx, "removed artifact data but retained blueprints", "kind", kind, "name", name, "dir", targetDir)
	return nil
}

var mvImageCmd = &cobra.Command{
	Use:               "mv-image <name> <dir>",
	Short:             "Move an image directory to a new location and update its catalog symlink",
	Args:              cobra.ExactArgs(2),
	ValidArgsFunction: artifactMvCompletion("images"),
	RunE: func(cmd *cobra.Command, args []string) error {
		return moveArtifact(cmd.Context(), sst.LogxRoot().Subsystem("manage"), "images", args[0], args[1])
	},
}

var mvInstanceCmd = &cobra.Command{
	Use:               "mv-instance <name> <dir>",
	Short:             "Move an instance directory to a new location and update its catalog symlink",
	Args:              cobra.ExactArgs(2),
	ValidArgsFunction: artifactMvCompletion("instances"),
	RunE: func(cmd *cobra.Command, args []string) error {
		return moveArtifact(cmd.Context(), sst.LogxRoot().Subsystem("manage"), "instances", args[0], args[1])
	},
}

var rmImageCmd = &cobra.Command{
	Use:               "rm-image <name>",
	Short:             "Wipe an image's storage data, leaving only its Dockerfile",
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: artifactRmCompletion("images"),
	RunE: func(cmd *cobra.Command, args []string) error {
		return rmArtifact(cmd.Context(), sst.LogxRoot().Subsystem("manage"), "images", args[0])
	},
}

var rmInstanceCmd = &cobra.Command{
	Use:               "rm-instance <name>",
	Short:             "Wipe an instance's storage data, leaving only its Dockerfile",
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: artifactRmCompletion("instances"),
	RunE: func(cmd *cobra.Command, args []string) error {
		return rmArtifact(cmd.Context(), sst.LogxRoot().Subsystem("manage"), "instances", args[0])
	},
}

var catalogCmd = &cobra.Command{
	Use:   "catalog",
	Short: "Physically move local workspace images and instances directly into the global ~/.runtimes area",
	RunE: func(cmd *cobra.Command, args []string) error {
		cwd, _ := os.Getwd()
		log := sst.LogxRoot().Subsystem("manage")
		ctx := cmd.Context()

		for _, kind := range []string{"images", "instances"} {
			localDir := filepath.Join(cwd, kind)
			entries, err := os.ReadDir(localDir)
			if err != nil {
				continue // No local images/instances directory found
			}

			for _, e := range entries {
				if !e.IsDir() {
					continue
				}
				name := e.Name()
				src := filepath.Join(localDir, name)
				
				dstDir := filepath.Join(rtsst.RuntimeDir(), kind)
				_ = os.MkdirAll(dstDir, 0755)
				dst := filepath.Join(dstDir, name)

				// If the global catalog currently has a symlink pointing back to this workspace, nuke it first
				if info, err := os.Lstat(dst); err == nil {
					if info.Mode()&os.ModeSymlink != 0 {
						_ = os.Remove(dst)
					} else {
						log.Warn(ctx, "target is already a physical directory in the catalog, skipping", "name", name)
						continue
					}
				}

				// Physically move the directory to ~/.runtimes
				if err := os.Rename(src, dst); err != nil {
					log.Error(ctx, "failed to move directory to catalog", "src", src, "dst", dst, "error", err)
				} else {
					log.Info(ctx, "moved local artifact to physical global catalog", "kind", kind, "name", name)
				}
			}

			// Clean up the empty local directories if they are fully migrated
			_ = os.Remove(localDir)
		}
		return nil
	},
}