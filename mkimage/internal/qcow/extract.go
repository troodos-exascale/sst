package qcow

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ExtractKernelInitrd scans <mnt>/boot for kernel + initrd and copies them
// into <dest>/kernel and <dest>/initrd.
func ExtractKernelInitrd(mnt string, dest string) (string, string, error) {

	bootDir := filepath.Join(mnt, "boot")

	var kernelCandidates []string
	var initrdCandidates []string

	// Walk /boot and collect all plausible kernel/initrd files.
	filepath.Walk(bootDir, func(path string, info os.FileInfo, _ error) error {
		if info == nil || info.IsDir() {
			return nil
		}

		name := info.Name()
		lower := strings.ToLower(name)

		// Kernels: vmlinuz*, image*, kernel*
		if strings.HasPrefix(lower, "vmlinuz") ||
			strings.HasPrefix(lower, "image") ||
			strings.HasPrefix(lower, "kernel") {
			kernelCandidates = append(kernelCandidates, path)
		}

		// Initrd: initrd*, initramfs*
		if strings.HasPrefix(lower, "initrd") ||
			strings.HasPrefix(lower, "initramfs") {
			initrdCandidates = append(initrdCandidates, path)
		}

		return nil
	})

	if len(kernelCandidates) == 0 || len(initrdCandidates) == 0 {
		return "", "", fmt.Errorf(
			"could not find kernel or initrd under %s/boot (kernels: %d, initrds: %d)",
			mnt, len(kernelCandidates), len(initrdCandidates),
		)
	}

	// Prefer the lexicographically last candidate (usually highest version)
	sort.Strings(kernelCandidates)
	sort.Strings(initrdCandidates)

	kernelPath := kernelCandidates[len(kernelCandidates)-1]
	initrdPath := initrdCandidates[len(initrdCandidates)-1]

	// Copy kernel
	kdst := filepath.Join(dest, "kernel")
	if err := copyFile(kernelPath, kdst); err != nil {
		return "", "", fmt.Errorf("failed to copy kernel from %s: %w", kernelPath, err)
	}

	// Copy initrd
	idst := filepath.Join(dest, "initrd")
	if err := copyFile(initrdPath, idst); err != nil {
		return "", "", fmt.Errorf("failed to copy initrd from %s: %w", initrdPath, err)
	}

	return kdst, idst, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}
