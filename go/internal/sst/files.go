package sst

import (
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/go-git/go-git/v5" // Import the go-git library
)

type Files interface {
	Fence(p string) (string, error)
	Root() string
}

// fencer is the unexported implementation of the Files interface.
type fencer struct {
	root string
}

// newFiles creates a new Fencer by automatically finding the repository root.
func newFiles() (Files, error) {
	root, err := FindRepoRoot()
	if err != nil {
		return nil, err
	}

	// CORRECTED: Always store the canonical, symlink-resolved path for the root.
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("could not resolve symlinks for repo root %q: %w", root, err)
	}

	return &fencer{root: canonicalRoot}, nil
}

func (f *fencer) Root() string {
	return f.root
}

func (f *fencer) Fence(p string) (string, error) {
	if f.root == "" {
		return "", fmt.Errorf("fencer is not initialized with a root directory")
	}
	if p == "" {
		return "", fmt.Errorf("empty path is not allowed")
	}

	absP, err := filepath.Abs(p)
	if err != nil {
		return "", fmt.Errorf("could not resolve path %q: %w", p, err)
	}

	// CORRECTED: Also resolve symlinks on the input path before comparing.
	canonicalAbsP, err := filepath.EvalSymlinks(absP)
	if err != nil {
		// If resolving fails (e.g., the path doesn't exist yet),
		// fall back to the unresolved absolute path for the check.
		// This allows fencing paths that are about to be created.
		canonicalAbsP = absP
	}

	rel, err := filepath.Rel(f.root, canonicalAbsP)
	if err != nil {
		// This can happen if the paths are on different drives on Windows.
		// For our purposes, this also means it's "outside" the fence.
		return "", fmt.Errorf("refusing to touch path %q outside fence root %q", canonicalAbsP, f.root)
	}

	if strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("refusing to touch path %q outside fence root %q", canonicalAbsP, f.root)
	}

	first := strings.Split(rel, string(os.PathSeparator))[0]
	if first == ".git" {
		return "", fmt.Errorf("refusing to touch path %q within .git/", canonicalAbsP)
	}
	return absP, nil
}

// FindRepoRoot locates the top-level superproject root using the go-git library,
// correctly handling cases where the current directory is inside a submodule.
func FindRepoRoot() (string, error) {
	// As a fallback for specific container environments, check for /workspace first.
	if st, statErr := os.Stat("/workspace"); statErr == nil && st.IsDir() {
		return "/workspace", nil
	}

	path, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("could not get current working directory: %w", err)
	}

	var topLevelRoot string
	startPath := path

	// Walk up the directory tree from the current path.
	for {
		// Attempt to open the directory as a Git repository.
		repo, err := git.PlainOpen(path)
		if err == nil {
			// If successful, get the worktree.
			wt, err := repo.Worktree()
			if err == nil {
				// We've found a repository root. We'll tentatively save it.
				// If we find another one higher up the tree, this one was a submodule.
				topLevelRoot = wt.Filesystem.Root()
			}
		}

		// Move to the parent directory.
		parent := filepath.Dir(path)
		if parent == path {
			// We've reached the filesystem root (e.g., "/") and can go no further.
			break
		}
		path = parent
	}

	if topLevelRoot == "" {
		return "", fmt.Errorf("could not find a git repository root (from cwd=%s)", startPath)
	}

	return topLevelRoot, nil
}


// FormatLsLine creates a single line of `ls -l` style output for a given file.
func FormatLsLine(path string, info fs.FileInfo) string {
	mode := info.Mode().String()
	var nlink uint64 = 1
	var uid, gid uint32
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		nlink = uint64(stat.Nlink)
		uid = stat.Uid
		gid = stat.Gid
	}
	ownerName := strconv.Itoa(int(uid))
	if owner, err := user.LookupId(ownerName); err == nil {
		ownerName = owner.Username
	}
	groupName := strconv.Itoa(int(gid))
	if group, err := user.LookupGroupId(groupName); err == nil {
		groupName = group.Name
	}
	size := info.Size()
	modTime := info.ModTime().Format("Jan _2 15:04")

	name := info.Name()
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(path)
		if err == nil {
			name = fmt.Sprintf("%s -> %s", name, target)
		}
	} else if info.IsDir() {
		name += "/"
	}
	return fmt.Sprintf("%s %3d %-8s %-8s %8d %s %s", mode, nlink, ownerName, groupName, size, modTime, name)
}
