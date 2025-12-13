package rtsst

import (
	"fmt"
	"os"
	"time"
)

type Lock struct {
	Path string
}

// CreateLock attempts to create a lock file exclusively.
// Returns an error if the lock already exists.
func CreateLock(lockPath string) (*Lock, error) {
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("lock exists at %s (use --force to override)", lockPath)
	}

	pid := os.Getpid()
	hostname, _ := os.Hostname()
	timestamp := time.Now().UTC().Format(time.RFC3339)

	fmt.Fprintf(f, "pid=%d\nhost=%s\ntime=%s\n", pid, hostname, timestamp)
	f.Close()

	return &Lock{Path: lockPath}, nil
}

// Remove deletes the lock file.
func (l *Lock) Remove() {
	_ = os.Remove(l.Path)
}
