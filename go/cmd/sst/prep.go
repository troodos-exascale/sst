package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(prepCmd)
	rootCmd.AddCommand(unprepCmd)
}

func findGitRoot() (string, error) {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("could not find git repository root: %w", err)
	}
	return strings.TrimSpace(out.String()), nil
}

var prepCmd = &cobra.Command{
	Use:   "prep-sst-dev",
	Short: "Prepare the sst monorepo for integrated development.",
	Long: `This command optimizes the sst monorepo for a developer's workflow.
It finds all top-level submodules (e.g., hetzner.com) and replaces their
nested 'templates' submodule with a symbolic link to the top-level 'templates'
submodule. This ensures all components build against the same contracts.

The original nested submodule is safely moved to 'templates.original' and
the changes are added to .git/info/exclude to prevent accidental commits.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		repoRoot, err := findGitRoot()
		if err != nil {
			return err
		}
		fmt.Printf("[DEBUG] Found repository root: %s\n", repoRoot)

		entries, err := os.ReadDir(repoRoot)
		if err != nil {
			return fmt.Errorf("could not read repo root directory: %w", err)
		}
		fmt.Printf("[DEBUG] Scanning for submodules in %s...\n", repoRoot)

		for _, entry := range entries {
			if entry.IsDir() {
				submodulePath := filepath.Join(repoRoot, entry.Name())
				if _, err := os.Stat(filepath.Join(submodulePath, ".git")); err == nil {
					fmt.Printf("[DEBUG] Found potential submodule: %s\n", entry.Name())
					if err := prepSubmodule(repoRoot, submodulePath); err != nil {
						fmt.Fprintf(os.Stderr, "[ERROR] Failed to prep submodule '%s': %v\n", entry.Name(), err)
					}
				}
			}
		}
		fmt.Println("Prep complete.")
		return nil
	},
}

var unprepCmd = &cobra.Command{
	Use:   "unprep-sst-dev",
	Short: "Revert the changes made by prep-sst-dev.",
	Long:  `Restores the original nested 'templates' submodules, removing the symbolic links.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		repoRoot, err := findGitRoot()
		if err != nil {
			return err
		}
		fmt.Printf("[DEBUG] Found repository root: %s\n", repoRoot)

		entries, err := os.ReadDir(repoRoot)
		if err != nil {
			return fmt.Errorf("could not read repo root directory: %w", err)
		}
		fmt.Printf("[DEBUG] Scanning for submodules in %s...\n", repoRoot)

		for _, entry := range entries {
			if entry.IsDir() {
				submodulePath := filepath.Join(repoRoot, entry.Name())
				if _, err := os.Stat(filepath.Join(submodulePath, ".git")); err == nil {
					fmt.Printf("[DEBUG] Found potential submodule: %s\n", entry.Name())
					if err := unprepSubmodule(repoRoot, submodulePath); err != nil {
						fmt.Fprintf(os.Stderr, "[ERROR] Failed to unprep submodule '%s': %v\n", entry.Name(), err)
					}
				}
			}
		}
		fmt.Println("Unprep complete.")
		return nil
	},
}

// CORRECTED: Use a named return to handle the deferred Close error.
func prepSubmodule(repoRoot, submodulePath string) (err error) {
	if filepath.Base(submodulePath) == "templates" {
		fmt.Printf("[DEBUG] Skipping 'templates' submodule itself.\n")
		return nil
	}

	nestedTemplatesPath := filepath.Join(submodulePath, "templates")
	nestedTemplatesBackupPath := filepath.Join(submodulePath, "templates.original")

	fmt.Printf("[DEBUG] Checking for nested 'templates' in %s\n", submodulePath)
	fi, err := os.Stat(nestedTemplatesPath)
	if os.IsNotExist(err) {
		fmt.Printf("[DEBUG] -> No 'templates' directory found. Skipping.\n")
		return nil
	}
	if !fi.IsDir() {
		return nil
	}

	if lfi, err := os.Lstat(nestedTemplatesPath); err == nil && lfi.Mode()&os.ModeSymlink != 0 {
		fmt.Printf("[INFO] Submodule '%s' is already prepped.\n", filepath.Base(submodulePath))
		return nil
	}

	fmt.Printf("[PREP] Processing submodule: %s\n", filepath.Base(submodulePath))

	fmt.Printf("  -> Moving %s to %s\n", nestedTemplatesPath, nestedTemplatesBackupPath)
	if err := os.Rename(nestedTemplatesPath, nestedTemplatesBackupPath); err != nil {
		return fmt.Errorf("failed to rename nested submodule: %w", err)
	}

	symlinkTarget := filepath.Join("..", "templates")
	fmt.Printf("  -> Creating symlink %s -> %s\n", nestedTemplatesPath, symlinkTarget)
	if err := os.Symlink(symlinkTarget, nestedTemplatesPath); err != nil {
		// CORRECTED: Check error on the revert attempt.
		if revertErr := os.Rename(nestedTemplatesBackupPath, nestedTemplatesPath); revertErr != nil {
			fmt.Fprintf(os.Stderr, "[CRITICAL] Failed to create symlink and also failed to restore backup for %s: %v\n", submodulePath, revertErr)
		}
		return fmt.Errorf("failed to create symlink: %w", err)
	}

	excludeFilePath := filepath.Join(repoRoot, ".git", "info", "exclude")
	f, err := os.OpenFile(excludeFilePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("failed to open .git/info/exclude: %w", err)
	}
	defer func() {
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
	}()

	relNestedPath, _ := filepath.Rel(repoRoot, nestedTemplatesPath)
	relBackupPath, _ := filepath.Rel(repoRoot, nestedTemplatesBackupPath)
	lineToAdd1 := "/" + relNestedPath
	lineToAdd2 := "/" + relBackupPath

	fmt.Printf("  -> Adding rules to %s\n", excludeFilePath)
	if _, err := fmt.Fprintf(f, "\n# Added by ss-dev prep-sst-dev\n%s\n%s\n", lineToAdd1, lineToAdd2); err != nil {
		return fmt.Errorf("failed to write to exclude file: %w", err)
	}

	return nil
}

func unprepSubmodule(repoRoot, submodulePath string) error {
	nestedTemplatesPath := filepath.Join(submodulePath, "templates")
	nestedTemplatesBackupPath := filepath.Join(submodulePath, "templates.original")

	fmt.Printf("[DEBUG] Checking for prepped state in %s\n", submodulePath)
	lfi, err := os.Lstat(nestedTemplatesPath)
	if os.IsNotExist(err) {
		fmt.Printf("[DEBUG] -> No 'templates' directory or symlink found. Skipping.\n")
		return nil
	}

	if lfi.Mode()&os.ModeSymlink == 0 {
		fmt.Printf("[DEBUG] -> 'templates' is not a symlink. Skipping.\n")
		return nil
	}

	if _, err := os.Stat(nestedTemplatesBackupPath); os.IsNotExist(err) {
		fmt.Printf("[WARN] Symlink exists but backup path %s does not exist. Cannot unprep '%s'.\n", nestedTemplatesBackupPath, filepath.Base(submodulePath))
		return nil
	}

	fmt.Printf("[UNPREP] Reverting submodule: %s\n", filepath.Base(submodulePath))

	fmt.Printf("  -> Removing symlink %s\n", nestedTemplatesPath)
	if err := os.Remove(nestedTemplatesPath); err != nil {
		return fmt.Errorf("failed to remove symlink: %w", err)
	}

	fmt.Printf("  -> Restoring %s from %s\n", nestedTemplatesPath, nestedTemplatesBackupPath)
	if err := os.Rename(nestedTemplatesBackupPath, nestedTemplatesPath); err != nil {
		return fmt.Errorf("failed to restore original submodule: %w", err)
	}

	// CORRECTED: The git command should be run *from* the parent submodule's directory,
	// and the pathspec for the nested submodule should be relative to that directory.
	fmt.Printf("  -> Updating submodule 'templates' to its tracked commit...\n")
	cmd := exec.Command("git", "submodule", "update", "--init", "--recursive", "templates")
	cmd.Dir = submodulePath // This sets the working directory for the command.
	output, err := cmd.CombinedOutput()
	if err != nil {
		fmt.Printf("    [ERROR] 'git submodule update' failed: %v\n", err)
		fmt.Printf("    [OUTPUT] %s\n", string(output))
		return err
	}

	excludeFilePath := filepath.Join(repoRoot, ".git", "info", "exclude")
	input, err := os.ReadFile(excludeFilePath)
	if err != nil {
		fmt.Printf("  [WARN] Could not read exclude file to clean it up: %v\n", err)
		return nil
	}

	relNestedPath, _ := filepath.Rel(repoRoot, nestedTemplatesPath)
	relBackupPath, _ := filepath.Rel(repoRoot, nestedTemplatesBackupPath)
	lineToRemove1 := "/" + relNestedPath
	lineToRemove2 := "/" + relBackupPath

	lines := strings.Split(string(input), "\n")
	var newLines []string
	var removed bool
	for _, line := range lines {
		if line == lineToRemove1 || line == lineToRemove2 || strings.Contains(line, "Added by ss-dev prep-sst-dev") {
			removed = true
			continue
		}
		newLines = append(newLines, line)
	}

	if removed {
		fmt.Printf("  -> Cleaning up %s\n", excludeFilePath)
		outputToWrite := strings.Join(newLines, "\n")
		// Trim leading/trailing whitespace which can accumulate
		return os.WriteFile(excludeFilePath, []byte(strings.TrimSpace(outputToWrite)+"\n"), 0644)
	}

	return nil
}