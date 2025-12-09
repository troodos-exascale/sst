package internal

import (
	"io"
	"os"
)

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
