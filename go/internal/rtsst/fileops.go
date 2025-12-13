package rtsst

import (
	"io"
	"os"
	"fmt"
	"path/filepath"
	"syscall"
)

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

func CreateSparse(path string, size int64) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := f.Truncate(size); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func CopySparse(srcPath, dstPath string) error {
	src, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer src.Close()

	dst, err := os.Create(dstPath)
	if err != nil {
		return err
	}
	defer dst.Close()

	buf := make([]byte, 1024*1024) // 1 MiB blocks

	for {
		n, readErr := src.Read(buf)
		if n > 0 {
			block := buf[:n]
			allZero := true
			for _, b := range block {
				if b != 0 {
					allZero = false
					break
				}
			}
			if allZero {
				if _, err := dst.Seek(int64(n), io.SeekCurrent); err != nil {
					return err
				}
			} else {
				if _, err := dst.Write(block); err != nil {
					return err
				}
			}
		}

		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return readErr
		}
	}

	return nil
}

func CopyFile(src, dst string) error {
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
