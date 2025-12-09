package internal

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// GetFileOwnership returns the UID and GID of the specified path.
// If the path doesn't exist, it returns the current user's EUID/EGID.
func GetFileOwnership(path string) (int, int, error) {
	info, err := os.Stat(path)
	if err != nil {
		return os.Geteuid(), os.Getegid(), nil
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return -1, -1, fmt.Errorf("unsupported platform for file ownership")
	}
	return int(stat.Uid), int(stat.Gid), nil
}

// ChownR recursively changes ownership of a directory and its contents.
func ChownR(path string, uid, gid int) error {
	return filepath.Walk(path, func(name string, info os.FileInfo, err error) error {
		if err == nil {
			_ = os.Chown(name, uid, gid)
		}
		return nil
	})
}
