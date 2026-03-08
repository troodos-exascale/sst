package gitpm

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// TarOptions controls tar archive creation.
type TarOptions struct {
	BaseDir string // directory to archive
	Output  string // output tar.gz file
}

// CreateDeterministicTar builds a reproducible tar.gz archive
// with sorted files, fixed timestamps, and normalized metadata.
func CreateDeterministicTar(opts TarOptions) (string, error) {
	if opts.BaseDir == "" {
		return "", fmt.Errorf("missing BaseDir")
	}
	if opts.Output == "" {
		opts.Output = opts.BaseDir + ".tar.gz"
	}

	outFile, err := os.Create(opts.Output)
	if err != nil {
		return "", err
	}
	defer outFile.Close()

	gz := gzip.NewWriter(outFile)
	defer gz.Close()

	tw := tar.NewWriter(gz)
	defer tw.Close()

	// Collect and sort all paths
	var paths []string
	err = filepath.Walk(opts.BaseDir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if p == opts.BaseDir {
			return nil // skip root
		}
		paths = append(paths, p)
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(paths)

	// Normalize timestamp to UNIX epoch 0
	fixedTime := time.Unix(0, 0)

	for _, p := range paths {
		info, err := os.Lstat(p)
		if err != nil {
			return "", err
		}

		link := ""
		if info.Mode()&os.ModeSymlink != 0 {
			link, err = os.Readlink(p)
			if err != nil {
				return "", err
			}
		}

		hdr, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return "", err
		}

		// Normalize metadata
		hdr.Name, _ = filepath.Rel(opts.BaseDir, p)
		hdr.ModTime = fixedTime
		hdr.AccessTime = fixedTime
		hdr.ChangeTime = fixedTime
		hdr.Uid = 0
		hdr.Gid = 0
		hdr.Uname = "root"
		hdr.Gname = "root"

		if err := tw.WriteHeader(hdr); err != nil {
			return "", err
		}

		if info.Mode().IsRegular() {
			f, err := os.Open(p)
			if err != nil {
				return "", err
			}
			if _, err := io.Copy(tw, f); err != nil {
				f.Close()
				return "", err
			}
			f.Close()
		}
	}

	tw.Close()
	gz.Close()
	outFile.Close()

	// Compute SHA256 for deterministic verification
	f, err := os.Open(opts.Output)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	sum := fmt.Sprintf("%x", h.Sum(nil))
	return sum, nil
}
