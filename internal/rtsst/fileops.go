package rtsst

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// ParseOSRelease reads os-release from a mounted filesystem.
func ParseOSRelease(rootfsPath string) (map[string]string, error) {
	target := filepath.Join(rootfsPath, "etc", "os-release")
	if _, err := os.Stat(target); os.IsNotExist(err) {
		target = filepath.Join(rootfsPath, "usr", "lib", "os-release")
	}

	f, err := os.Open(target)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	data := make(map[string]string)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			data[strings.TrimSpace(parts[0])] = strings.Trim(strings.TrimSpace(parts[1]), `"'`)
		}
	}
	return data, scanner.Err()
}

// GenerateSemanticName constructs the canonical <ID>-<VERSION>-<VARIANT>-<BUILD> name.
func GenerateSemanticName(info map[string]string) string {
	id := info["ID"]
	if id == "" {
		id = "linux"
	}
	versionID := info["VERSION_ID"]
	if versionID == "" {
		versionID = "unknown"
	}

	name := fmt.Sprintf("%s-%s", id, versionID)

	if variant, ok := info["VARIANT_ID"]; ok && variant != "" {
		name = fmt.Sprintf("%s-%s", name, variant)
	}
	if buildID, ok := info["BUILD_ID"]; ok && buildID != "" {
		// Clean the build ID of unsafe filename characters just in case
		cleanBuild := strings.ReplaceAll(buildID, ":", "")
		name = fmt.Sprintf("%s-%s", name, cleanBuild)
	}

	return name
}

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
	// Use a closure to ensure cleanup on error, but ignoring double-close on success
	defer func() { _ = f.Close() }()

	if err := f.Truncate(size); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
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
	// Use a closure to ensure cleanup on error, but ignoring double-close on success
	defer func() { _ = dst.Close() }()

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

	// Critical fix: Sync to flush buffers to disk before closing
	if err := dst.Sync(); err != nil {
		return err
	}

	// Critical fix: Check Close() error to catch final flush errors
	return dst.Close()
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
	defer func() { _ = out.Close() }()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}

	// Critical fix: Sync and explicit Close
	if err := out.Sync(); err != nil {
		return err
	}
	return out.Close()
}

func SHA256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

// ResizeFile changes the size of the named file.
// If the file is smaller than size, it is extended with sparse zeros.
func ResizeFile(path string, size int64) error {
	return os.Truncate(path, size)
}

// ParseSize parses a size string like "20G", "512MiB", or "1024" (bytes).
func ParseSize(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty size")
	}

	// Find the first non-digit character
	idx := strings.IndexFunc(s, func(r rune) bool {
		return r < '0' || r > '9'
	})

	var numStr, suffix string
	if idx == -1 {
		numStr = s
	} else {
		numStr = s[:idx]
		suffix = strings.ToLower(s[idx:])
	}

	val, err := strconv.ParseInt(numStr, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid number in size '%s': %w", s, err)
	}

	switch suffix {
	case "", "b":
		return val, nil
	case "k", "kb":
		return val * 1000, nil
	case "kib":
		return val * 1024, nil
	case "m", "mb":
		return val * 1000 * 1000, nil
	case "mib":
		return val * 1024 * 1024, nil
	case "g", "gb":
		return val * 1000 * 1000 * 1000, nil
	case "gib":
		return val * 1024 * 1024 * 1024, nil
	case "t", "tb":
		return val * 1000 * 1000 * 1000 * 1000, nil
	case "tib":
		return val * 1024 * 1024 * 1024 * 1024, nil
	default:
		return 0, fmt.Errorf("unknown size suffix '%s'", suffix)
	}
}
