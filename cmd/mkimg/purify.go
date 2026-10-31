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

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// isWSL checks if the Linux environment is actually Windows Subsystem for Linux
func isWSL() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	b, err := os.ReadFile("/proc/sys/kernel/osrelease")
	if err != nil {
		return false
	}
	osRelease := strings.ToLower(string(b))
	return strings.Contains(osRelease, "microsoft") || strings.Contains(osRelease, "wsl")
}

func shouldPurify() bool {
	if runtime.GOOS == "darwin" {
		return true // Apple VZ Sandbox bug
	}
	if isWSL() {
		return true // WSL to Windows NTFS/9P translation quirks
	}
	return false // Native Linux (Ubuntu VM or bare metal) doesn't need this
}

// PurifyArtifacts takes a list of absolute file paths emitted by the VM.
// It natively copies them on the host to strip VirtioFS/9P poison while retaining sparseness.
func PurifyArtifacts(files []string) error {
	if !shouldPurify() {
		return nil
	}

	for _, file := range files {
		// Skip if the file doesn't exist
		if _, err := os.Stat(file); os.IsNotExist(err) {
			continue
		}

		cleanPath := file + ".clean"
		fmt.Printf("--> Purifying emitted artifact natively: %s\n", filepath.Base(file))

		var cmd *exec.Cmd
		if runtime.GOOS == "darwin" {
			// macOS native 'cp' uses fcopyfile() which perfectly preserves APFS sparse holes
			cmd = exec.Command("cp", file, cleanPath)
		} else {
			// WSL (Linux) uses GNU 'cp' with an explicit sparse flag
			cmd = exec.Command("cp", "--sparse=always", file, cleanPath)
		}

		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("failed to purify %s: %s (%w)", file, string(out), err)
		}

		// Remove the original poisoned file
		if err := os.Remove(file); err != nil {
			return fmt.Errorf("failed to remove poisoned file %s: %w", file, err)
		}

		// Atomically replace it with the clean sparse file
		if err := os.Rename(cleanPath, file); err != nil {
			return fmt.Errorf("failed to rename clean file to %s: %w", file, err)
		}
	}
	return nil
}

// ProcessPurificationQueue is called natively on the Mac/Windows host after delegation finishes.
func ProcessPurificationQueue(runtimeDir string) error {
	if !shouldPurify() {
		return nil
	}

	queueFile := filepath.Join(runtimeDir, ".purify_queue")
	data, err := os.ReadFile(queueFile)
	if err != nil {
		return nil // No queue file means nothing to purify
	}

	// Clean up the queue file immediately
	_ = os.Remove(queueFile)

	var validFiles []string
	for _, f := range strings.Split(string(data), "\n") {
		if f = strings.TrimSpace(f); f != "" {
			validFiles = append(validFiles, f)
		}
	}

	if len(validFiles) > 0 {
		return PurifyArtifacts(validFiles)
	}
	return nil
}
