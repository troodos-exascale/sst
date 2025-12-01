package util

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

func IsURL(s string) bool {
	return strings.HasPrefix(s, "http://") ||
		strings.HasPrefix(s, "https://")
}

// DownloadToTmp downloads a URL to a temporary file with a progress indicator.
func DownloadToTmp(url string) (string, error) {

	resp, err := http.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("download failed: %s", resp.Status)
	}

	// Create temp file
	f, err := os.CreateTemp("", "mkimage-download-*")
	if err != nil {
		return "", err
	}
	defer f.Close()

	total := resp.ContentLength
	var downloaded int64 = 0

	// Detect terminal
	isTTY := isTerminal(os.Stdout.Fd())

	buf := make([]byte, 32*1024)
	lastUpdate := time.Now()

	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			wn, werr := f.Write(buf[:n])
			if werr != nil {
				return "", werr
			}
			downloaded += int64(wn)
		}

		if isTTY && time.Since(lastUpdate) > 200*time.Millisecond {
			printProgress(downloaded, total)
			lastUpdate = time.Now()
		}

		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
	}

	if isTTY {
		printProgress(downloaded, total)
		fmt.Print("\n")
	}

	return f.Name(), nil
}

// Print progress like:
//
//	Downloading: 128.4 / 203.1 MiB (63%)
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
