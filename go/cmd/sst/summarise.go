package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/swiss-software-tool/runtime-smart/go/internal/sst"
)

// state is a simple map used to pass data between step functions.
type state map[string]interface{}

var (
	repoRootFlag string
	outputFile   string
)

var summariseCmd = &cobra.Command{
	Use:   "summarise [submodules...]",
	Short: "Consolidate repository text files into a single report.",
	Long: `This tool scans the repository and consolidates key text files into a single report.
If no submodule names are provided, it only scans the main repository files, skipping all submodules.
To include specific submodules in the summary, list their directory names as arguments.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Fprintf(os.Stderr, "[INFO] Starting repository summary...\n")
		s := make(state)
		s["submodulesToScan"] = args // Pass the submodules to the state

		if err := stepSetup(s, repoRootFlag); err != nil {
			return fmt.Errorf("setup step failed: %w", err)
		}
		if err := stepGetFileList(s); err != nil {
			return fmt.Errorf("get file list step failed: %w", err)
		}
		if err := stepAddFileContents(s); err != nil {
			return fmt.Errorf("add file contents step failed: %w", err)
		}
		if err := stepWriteSummaryOutput(s, outputFile); err != nil {
			return fmt.Errorf("write output step failed: %w", err)
		}

		fmt.Fprintf(os.Stderr, "[INFO] Repository summary complete.\n")
		return nil
	},
}

func init() {
	// CORRECTED: Attach directly to rootCmd for a flat command structure.
	rootCmd.AddCommand(summariseCmd)

	summariseCmd.Flags().StringVarP(&repoRootFlag, "repo-root", "r", "", "Path to the target repository root (default: auto-detect git repo)")
	summariseCmd.Flags().StringVarP(&outputFile, "output", "o", "docs/repo-summary.md", "Path to the output file (use '-' for stdout)")
}

func stepSetup(s state, repoRoot string) error {
	if repoRoot == "" {
		gitRoot, err := sst.FindRepoRoot()
		if err != nil {
			return fmt.Errorf("could not find repo root: %w", err)
		}
		repoRoot = gitRoot
	}
	s["repoRoot"] = repoRoot
	fmt.Fprintf(os.Stderr, "[INFO] Using repository root: %s\n", repoRoot)

	var outputBuilder strings.Builder
	fmt.Fprintf(&outputBuilder, "# Repository Summary: %s\n", repoRoot)
	fmt.Fprintf(&outputBuilder, "* **Date:** %s\n\n", time.Now().UTC().Format(time.RFC1123))
	s["outputBuilder"] = &outputBuilder

	return nil
}

func stepGetFileList(s state) error {
	root := s["repoRoot"].(string)
	submodulesToScan := s["submodulesToScan"].([]string)
	var fileList []string

	includePatterns := []string{
		"Makefile", "README.md", "Dockerfile", "LICENSE", "go.mod", "go.sum", "go.work", "*.cue",
		"*.go", "*.sh", "*.env", "*.yaml", "*.yml", "*.json", "*.mk", "*.tpl", "*.tmpl", "*.docker", "*.swift",
	}

	// Always exclude these directories from any scan.
	baseDirExcludes := map[string]bool{".git": true, ".idea": true, ".vscode": true, "logs": true, "VZRunner/.build": true}

	// Find all submodule paths, at any depth.
	allSubmodules, err := findSubmodules(root)
	if err != nil {
		return fmt.Errorf("could not detect submodules: %w", err)
	}

	// Create a quick-lookup map of submodules the user wants to include.
	scanMap := make(map[string]bool)
	for _, sub := range submodulesToScan {
		scanMap[sub] = true
	}

	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			fmt.Fprintf(os.Stderr, "[WARN] Skipping path due to error: %v\n", err)
			return nil // Skip paths we can't read
		}

		if d.IsDir() {
			dirName := d.Name()
			// Check against base excludes only at the top level
			if _, excluded := baseDirExcludes[dirName]; excluded && filepath.Dir(path) == root {
				return filepath.SkipDir
			}

			// This is the new core logic for skipping submodules.
			relPath, _ := filepath.Rel(root, path)
			if isSubmodule := allSubmodules[relPath]; isSubmodule {
				// The name of the submodule is the last element of its path.
				submoduleName := filepath.Base(relPath)
				// If this submodule is NOT in the user's scan list, skip the whole directory.
				if !scanMap[submoduleName] {
					return filepath.SkipDir
				}
			}
			return nil // It's a directory we should enter.
		}

		// It's a file, check if it matches our include patterns.
		for _, pattern := range includePatterns {
			if matched, _ := filepath.Match(pattern, d.Name()); matched {
				relPath, _ := filepath.Rel(root, path)
				fileList = append(fileList, relPath)
				break
			}
		}
		return nil
	})

	if err != nil {
		return fmt.Errorf("error during directory walk: %w", err)
	}

	sort.Strings(fileList)
	s["fileList"] = fileList
	fmt.Fprintf(os.Stderr, "[INFO] Found %d files to summarize.\n", len(fileList))
	return nil
}

// findSubmodules identifies all submodule paths within the repository using a more reliable git command.
func findSubmodules(root string) (map[string]bool, error) {
	submodules := make(map[string]bool)
	// 'git submodule' provides a stable, multi-column output.
	cmd := exec.Command("git", "submodule")
	cmd.Dir = root
	output, err := cmd.Output()
	if err != nil {
		// This can fail if there are no submodules, which is not an error for us.
		return submodules, nil
	}

	scanner := bufio.NewScanner(bytes.NewReader(output))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			// The path is the second field in the output from 'git submodule'.
			// Since we run from the root, the path is relative to the root.
			path := fields[1]
			submodules[path] = true
		}
	}
	return submodules, scanner.Err()
}

func stepAddFileContents(s state) error {
	root := s["repoRoot"].(string)
	fileList := s["fileList"].([]string)
	outputBuilder := s["outputBuilder"].(*strings.Builder)

	for _, file := range fileList {
		fullPath := filepath.Join(root, file)
		fmt.Fprintf(outputBuilder, "FILE: %s\n", file)
		content, err := os.ReadFile(fullPath)
		if err != nil {
			fmt.Fprintf(outputBuilder, "--- ERROR READING FILE: %v ---\n", err)
		} else {
			outputBuilder.Write(content)
			if len(content) > 0 && content[len(content)-1] != '\n' {
				outputBuilder.WriteByte('\n')
			}
		}
		fmt.Fprintf(outputBuilder, "END: %s\n\n", file)
	}
	return nil
}

func stepWriteSummaryOutput(s state, outputFile string) error {
	root := s["repoRoot"].(string)
	outputBuilder := s["outputBuilder"].(*strings.Builder)

	if !filepath.IsAbs(outputFile) && outputFile != "-" {
		outputFile = filepath.Join(root, outputFile)
	}
	fmt.Fprintf(os.Stderr, "[INFO] Writing summary to: %s\n", outputFile)

	finalOutput := outputBuilder.String()
	if outputFile == "-" {
		fmt.Print(finalOutput)
	} else {
		if err := os.MkdirAll(filepath.Dir(outputFile), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(outputFile, []byte(finalOutput), 0o644); err != nil {
			return err
		}
	}
	return nil
}
