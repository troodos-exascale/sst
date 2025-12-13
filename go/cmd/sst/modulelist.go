package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
)

var (
	moduleListWrite bool
)

var moduleListCmd = &cobra.Command{
	Use:   "module-list",
	Short: "Scans for Go modules and generates a go.work 'use' block.",
	Long: `This utility scans the current directory and all subdirectories to find Go modules,
based on the SST project's Makefile conventions (symlink to make-leaf.mk).

It outputs a correctly formatted block of 'use' directives to standard output,
which can be pasted into a go.work file. It also checks for and warns about
incompletely scaffolded component directories.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		pwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("could not get current directory: %w", err)
		}

		scanner := &modScanner{
			repoRoot:     core.Files().Root(),
			localModules: make(map[string]*moduleInfo),
			diagnostics:  make([]string, 0),
		}

		// Scan from the current directory downwards.
		if err := scanner.scanModules(pwd); err != nil {
			return fmt.Errorf("failed during module scan: %w", err)
		}

		// Print any warnings to stderr.
		for _, diag := range scanner.diagnostics {
			fmt.Fprintln(os.Stderr, color.YellowString("[WARN] %s", diag))
		}

		// Get the list of relative paths for the 'use' directives.
		var modulePaths []string
		for _, modInfo := range scanner.localModules {
			relPath, err := filepath.Rel(pwd, modInfo.FSPath)
			if err != nil {
				return err
			}
			modulePaths = append(modulePaths, "./"+relPath)
		}
		sort.Strings(modulePaths)

		var out strings.Builder
		out.WriteString("go 1.25\n\n")
		out.WriteString("use (\n")
		for _, p := range modulePaths {
			out.WriteString(fmt.Sprintf("    %s\n", p))
		}
		out.WriteString(")\n")

		if moduleListWrite {
			goWorkPath := filepath.Join(pwd, "go.work")
			fmt.Printf("--> Writing %d modules to %s\n", len(modulePaths), goWorkPath)
			return os.WriteFile(goWorkPath, []byte(out.String()), 0644)
		}

		fmt.Print(out.String())
		return nil
	},
}

func init() {
	rootCmd.AddCommand(moduleListCmd)
	moduleListCmd.Flags().BoolVarP(&moduleListWrite, "write", "w", false, "Write the output directly to a go.work file in the current directory.")
}

type moduleInfo struct {
	ModulePath string
	FSPath     string
}

type modScanner struct {
	repoRoot     string
	localModules map[string]*moduleInfo // map[modulePath]*moduleInfo
	diagnostics  []string
}

func (s *modScanner) scanModules(scanRoot string) error {
	return filepath.WalkDir(scanRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}

		isLeaf, _ := isModuleDir(path, "make-leaf.mk")
		isPassthrough, _ := isModuleDir(path, "make-passthrough.mk")

		if isLeaf {
			modPath := strings.Replace(path, s.repoRoot, "github.com/swiss-software-tool/sst", 1)
			s.localModules[modPath] = &moduleInfo{ModulePath: modPath, FSPath: path}
		}
		if isPassthrough {
			diags, err := checkPassthroughSubdirs(s.repoRoot, path)
			if err != nil {
				return err
			}
			s.diagnostics = append(s.diagnostics, diags...)
		}
		return nil
	})
}

func isModuleDir(path, makefileName string) (bool, error) {
	makefile := filepath.Join(path, "Makefile")
	if _, err := os.Lstat(makefile); err != nil {
		return false, nil
	}
	target, err := os.Readlink(makefile)
	if err != nil {
		return false, nil
	}
	return strings.HasSuffix(target, makefileName), nil
}

func checkPassthroughSubdirs(repoRoot, passthroughPath string) ([]string, error) {
	var diagnostics []string
	envPath := filepath.Join(passthroughPath, "ENV.mk")
	subdirs, err := parseEnvMk(envPath)
	if err != nil {
		return nil, fmt.Errorf("error parsing %s: %w", envPath, err)
	}

	for _, subdir := range subdirs {
		subdirPath := filepath.Join(passthroughPath, subdir)
		if _, err := os.Stat(subdirPath); os.IsNotExist(err) {
			continue
		}
		isLeaf, _ := isModuleDir(subdirPath, "make-leaf.mk")
		isPassthrough, _ := isModuleDir(subdirPath, "make-passthrough.mk")
		if !isLeaf && !isPassthrough {
			relPath, _ := filepath.Rel(repoRoot, subdirPath)
			relEnvPath, _ := filepath.Rel(repoRoot, envPath)
			diag := fmt.Sprintf("Incomplete scaffolding: '%s' is listed in '%s' but contains no leaf or passthrough Makefile.", relPath, relEnvPath)
			diagnostics = append(diagnostics, diag)
		}
	}
	return diagnostics, nil
}

// CORRECTED: Use a named return to handle the deferred Close error.
func parseEnvMk(path string) (subdirs []string, err error) {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer func() {
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
	}()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "SUBDIRS") {
			var values string
			if i := strings.Index(line, "="); i != -1 {
				values = line[i+1:]
			} else {
				continue
			}
			if commentIndex := strings.Index(values, "#"); commentIndex != -1 {
				values = values[:commentIndex]
			}
			subdirs = append(subdirs, strings.Fields(strings.TrimSpace(values))...)
		}
	}
	return subdirs, scanner.Err()
}