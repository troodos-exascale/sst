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

package sst

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type Project interface {
	Activate(path, containerPath string, force bool) error
}

// projectManager is the unexported implementation of the Project interface.
type projectManager struct{}

// newProject creates a new project manager.
func newProject() Project {
	return &projectManager{}
}

// Activate handles the logic for the `sst project` command.
func (pm *projectManager) Activate(path, containerPath string, force bool) error {
	// CORRECTED: Call the exported FindRepoRoot function.
	repoRoot, err := FindRepoRoot()
	if err != nil {
		return fmt.Errorf("failed to find repository root: %w", err)
	}

	if strings.HasPrefix(path, "projects/") {
		return pm.scaffoldProject(repoRoot, path, force)
	} else if strings.HasPrefix(path, "components/") {
		return pm.activateProvider(repoRoot, path, containerPath, force)
	}

	return fmt.Errorf("path must be within 'projects/' or 'components/'")
}

func (pm *projectManager) activateProvider(repoRoot, path, containerPath string, force bool) error {
	fmt.Printf("Activating provider for development: %s\n", path)

	fullPath := filepath.Join(repoRoot, path)
	if _, err := os.Stat(fullPath); os.IsNotExist(err) {
		return fmt.Errorf("path does not exist: %s", fullPath)
	}

	makefilePath := filepath.Join(path, "Makefile")

	err := createActiveProjectMakefile(repoRoot, "", "", makefilePath, force)
	if err != nil {
		return err
	}

	err = setupDevcontainerSymlink(repoRoot, fullPath, containerPath, force)
	if err != nil {
		return err
	}

	return nil
}

func (pm *projectManager) scaffoldProject(repoRoot, path string, force bool) error {
	fmt.Printf("Scaffolding new project at: %s\n", path)

	parts := strings.Split(path, "/")
	if len(parts) != 4 || parts[0] != "projects" {
		return fmt.Errorf("invalid project path format. Expected 'projects/<ORG>/<name>/<id>'")
	}
	org, projName, id := parts[1], parts[2], parts[3]
	fmt.Printf("  -> Org: %s, Project: %s, ID: %s\n", org, projName, id)

	projectDir := filepath.Join(repoRoot, path)
	if _, err := os.Stat(projectDir); err == nil && !force {
		return fmt.Errorf("project directory %s already exists. Use -f to overwrite", path)
	}

	subdirs := []string{"manifests", "orders", "containers", "clusters"}
	for _, subdir := range subdirs {
		dirToCreate := filepath.Join(projectDir, subdir)
		fmt.Printf("  -> Creating directory: %s\n", dirToCreate)
		if err := os.MkdirAll(dirToCreate, 0755); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", dirToCreate, err)
		}
	}

	templateDir := filepath.Join(repoRoot, "components/s2/sst/templates/manifests")
	manifestsDir := filepath.Join(projectDir, "manifests")
	ordersDir := filepath.Join(projectDir, "orders", "hetzner", "s2", "dev1")

	if err := os.MkdirAll(ordersDir, 0755); err != nil {
		return fmt.Errorf("failed to create orders subdirectory: %w", err)
	}

	templatesToCopy := map[string]string{
		"project.env":  filepath.Join(manifestsDir, "project.env"),
		"project.yaml": filepath.Join(manifestsDir, "project.yaml"),
		"order.yaml":   filepath.Join(ordersDir, "order.yaml"),
	}

	for src, dest := range templatesToCopy {
		fmt.Printf("  -> Copying template %s to %s\n", src, dest)
		srcPath := filepath.Join(templateDir, src)
		if err := copyFile(srcPath, dest); err != nil {
			return fmt.Errorf("failed to copy template %s: %w", src, err)
		}
	}

	envLinkTarget := filepath.Join(path, "manifests", "project.env")
	envLinkName := filepath.Join(repoRoot, "project.env")
	createSymlink(envLinkName, envLinkTarget, force)

	containerLinkTarget := "components/s2/sst/containers/work-container"
	containerLinkName := filepath.Join(repoRoot, ".container")
	createSymlink(containerLinkName, containerLinkTarget, force)

	fmt.Println("Project scaffolding complete.")
	return nil
}

// --- Package-internal helper functions ---

func createSymlink(linkName, target string, force bool) {
	if _, err := os.Lstat(linkName); err == nil {
		if !force {
			fmt.Printf("Symlink %s already exists. Use -f to overwrite.\n", filepath.Base(linkName))
			return
		}
		if err := os.Remove(linkName); err != nil {
			fmt.Printf("Error removing existing symlink %s: %v\n", linkName, err)
			return
		}
	}
	fmt.Printf("  -> Linking %s -> %s\n", filepath.Base(linkName), target)
	if err := os.Symlink(target, linkName); err != nil {
		fmt.Printf("Error creating symlink %s: %v\n", linkName, err)
	}
}

func createActiveProjectMakefile(repoRoot, manifestPath, envPath, makefilePath string, force bool) error {
	activeMkPath := filepath.Join(repoRoot, "active-project.mk")
	if _, err := os.Stat(activeMkPath); err == nil && !force {
		return fmt.Errorf("active-project.mk already exists. Use -f to overwrite")
	}

	var content strings.Builder
	content.WriteString("# WARNING: This file is auto-generated by 'sst project'. Do not edit.\n")
	if manifestPath != "" {
		content.WriteString(fmt.Sprintf("export ACTIVE_PROJECT_MANIFEST := %s\n", manifestPath))
	}
	if envPath != "" {
		content.WriteString(fmt.Sprintf("-include %s\nexport\n", envPath))
	}
	if makefilePath != "" {
		content.WriteString(fmt.Sprintf("include %s\n", makefilePath))
	}

	fmt.Println("Writing build context to active-project.mk")
	return os.WriteFile(activeMkPath, []byte(content.String()), 0644)
}

func setupDevcontainerSymlink(repoRoot, activePath, containerPath string, force bool) error {
	symlinkPath := filepath.Join(repoRoot, ".devcontainer")

	if _, err := os.Lstat(symlinkPath); err == nil {
		if !force {
			fmt.Println(".devcontainer symlink already exists. Use -f to overwrite.")
			return nil
		}
		if err := os.Remove(symlinkPath); err != nil {
			return fmt.Errorf("failed to remove existing .devcontainer symlink: %w", err)
		}
	}

	var targetPath string
	if containerPath != "" {
		targetPath = filepath.Join(repoRoot, containerPath)
		if _, err := os.Stat(targetPath); os.IsNotExist(err) {
			return fmt.Errorf("specified container path does not exist: %s", targetPath)
		}
	} else {
		prio2Path := filepath.Join(activePath, "devcontainers", "devcontainer")
		if _, err := os.Stat(prio2Path); err == nil {
			targetPath = prio2Path
		} else {
			devcontainersDir := filepath.Join(activePath, "devcontainers")
			entries, err := os.ReadDir(devcontainersDir)
			if err == nil {
				var subdirs []string
				for _, entry := range entries {
					if entry.IsDir() {
						subdirs = append(subdirs, entry.Name())
					}
				}
				if len(subdirs) == 1 {
					targetPath = filepath.Join(devcontainersDir, subdirs[0])
				}
			}
		}
	}

	if targetPath == "" {
		fmt.Println("No devcontainer found for this context, skipping symlink.")
		return nil
	}

	relTargetPath, err := filepath.Rel(repoRoot, targetPath)
	if err != nil {
		return fmt.Errorf("could not calculate relative path for symlink: %w", err)
	}

	fmt.Printf("Linking .devcontainer -> %s\n", relTargetPath)
	return os.Symlink(relTargetPath, symlinkPath)
}

func copyFile(src, dst string) (err error) {
	sourceFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := sourceFile.Close(); err == nil {
			err = closeErr
		}
	}()

	destFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := destFile.Close(); err == nil {
			err = closeErr
		}
	}()

	_, err = io.Copy(destFile, sourceFile)
	return err
}
