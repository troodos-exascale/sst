package util

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// IsURL reports whether a string looks like an HTTP/HTTPS URL.
func IsURL(s string) bool {
	return len(s) > 0 &&
		(s[:7] == "http://" || s[:8] == "https://")
}

// DownloadToFile downloads a URL directly into dstPath with a progress indicator.
// If an error occurs, dstPath is removed.
func DownloadToFile(url string, dstPath string) error {

	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download failed: %s", resp.Status)
	}

	// Create / truncate destination file
	f, err := os.Create(dstPath)
	if err != nil {
		return err
	}
	defer func() {
		f.Close()
		if err != nil {
			// best-effort cleanup on error
			_ = os.Remove(dstPath)
		}
	}()

	total := resp.ContentLength
	var downloaded int64 = 0

	isTTY := isTerminal(os.Stdout.Fd())
	buf := make([]byte, 32*1024)
	lastUpdate := time.Now()

	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			wn, werr := f.Write(buf[:n])
			if werr != nil {
				err = werr
				return werr
			}
			downloaded += int64(wn)
		}

		if isTTY && time.Since(lastUpdate) > 200*time.Millisecond {
			printProgress(downloaded, total)
			lastUpdate = time.Now()
		}

		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			err = rerr
			return rerr
		}
	}

	if isTTY {
		printProgress(downloaded, total)
		fmt.Print("\n")
	}

	return nil
}

// DownloadToTmp keeps the old behavior for any callers that still want a tmp file.
func DownloadToTmp(url string) (string, error) {
	f, err := os.CreateTemp("", "mkimage-download-*")
	if err != nil {
		return "", err
	}
	name := f.Name()
	f.Close()

	if err := DownloadToFile(url, name); err != nil {
		_ = os.Remove(name)
		return "", err
	}
	return name, nil
}

// Print progress like:
//   Downloading: 128.4 / 203.1 MiB (63%)
func printProgress(done, total int64) {
	if total <= 0 {
		fmt.Printf("\rDownloading: %6.1f MiB", float64(done)/1024/1024)
		return
	}
	percent := float64(done) / float64(total) * 100
	fmt.Printf("\rDownloading: %6.1f / %6.1f MiB (%.0f%%)",
		float64(done)/1024/1024,
		float64(total)/1024/1024,
		percent)
}

// Detect if stdout is a terminal
func isTerminal(fd uintptr) bool {
	// Best-effort fallback without importing golang.org/x/term
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}
