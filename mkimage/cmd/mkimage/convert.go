package main

import (
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"mkimage/internal/config"
	"mkimage/internal/util"
	"mkimage/internal/image"
	"github.com/spf13/cobra"
)

var (
	flagRoot    string
	flagForce   bool
	flagVerbose bool
)

// -----------------------------------------------------------------------------
// Logging helpers
// -----------------------------------------------------------------------------
func step(msg string) {
	if flagVerbose {
		fmt.Println("→", msg)
	}
}

func done(msg string) {
	if flagVerbose {
		fmt.Println("✓", msg)
	}
}

// -----------------------------------------------------------------------------
// Human-friendly upstream filename
// -----------------------------------------------------------------------------
func deriveUpstreamFilename(upstream string) string {
	var base string

	if util.IsURL(upstream) {
		u, err := url.Parse(upstream)
		if err == nil {
			base = path.Base(u.Path)
		}
	}

	if base == "" {
		base = filepath.Base(upstream)
	}

	if base == "" || base == "." || base == "/" {
		base = "upstream.img"
	}

	ext := filepath.Ext(base)
	name := strings.TrimSuffix(base, ext)
	if ext == "" {
		ext = ".img"
	}

	ts := time.Now().Format("20060102-150405")
	return fmt.Sprintf("%s-%s%s", name, ts, ext)
}

// -----------------------------------------------------------------------------
// Cobra convert command
// -----------------------------------------------------------------------------
var convertCmd = &cobra.Command{
	Use:   "convert <qcow-or-url> <image-name>",
	Args:  cobra.ExactArgs(2),
	Short: "Convert a qcow2/URL upstream image into an SST image directory",

	RunE: func(cmd *cobra.Command, args []string) error {
		upstream := args[0]
		imgName := args[1]

		// ---------------------------------------------------------------------
		// Resolve image root and Ownership
		// ---------------------------------------------------------------------
		// Ensure we resolve the absolute path of the target directory to find owner
		absRoot, _ := filepath.Abs(flagRoot)

		// Check who owns the parent directory so we can chown files to them later
		// (since we are running as root)
		targetUid, targetGid, _ := util.GetFileOwnership(filepath.Dir(absRoot))

		dest := filepath.Join(flagRoot, imgName)
		cloudDir := filepath.Join(dest, "cloud")

		if flagVerbose {
			fmt.Println("→ mkimage convert")
			fmt.Println("  upstream:", upstream)
			fmt.Println("  dest:    ", dest)
			fmt.Printf("  target owner: %d:%d\n", targetUid, targetGid)
		}

		// ---------------------------------------------------------------------
		// Enforce directory existence rules
		// ---------------------------------------------------------------------
		if _, err := os.Stat(dest); err == nil && !flagForce {
			return fmt.Errorf("destination %s exists; use --force to overwrite", dest)
		}

		if flagForce {
			_ = os.RemoveAll(dest)
		}

		if err := os.MkdirAll(dest, 0o755); err != nil {
			return err
		}
		// Fix ownership of the image dir immediately
		_ = os.Chown(dest, targetUid, targetGid)

		// ---------------------------------------------------------------------
		// Acquire LOCK for this image
		// ---------------------------------------------------------------------
		lockPath := filepath.Join(dest, ".mkimage.lock")
		lk, err := util.CreateLock(lockPath)
		if err != nil {
			return err
		}
		defer lk.Remove()

		if err := os.MkdirAll(cloudDir, 0o755); err != nil {
			return err
		}

		// ---------------------------------------------------------------------
		// Download or copy upstream image into cloud/
		// ---------------------------------------------------------------------
		var localPath string

		if util.IsURL(upstream) {
			step("Downloading upstream image…")
			name := deriveUpstreamFilename(upstream)
			target := filepath.Join(cloudDir, name)
			if err := util.DownloadToFile(upstream, target); err != nil {
				return fmt.Errorf("download failed: %w", err)
			}
			localPath = target
			done("Download complete")
		} else {
			step("Copying upstream image into cloud/…")
			name := deriveUpstreamFilename(upstream)
			target := filepath.Join(cloudDir, name)
			if err := util.CopyFile(upstream, target); err != nil {
				return fmt.Errorf("copy upstream image: %w", err)
			}
			localPath = target
			done("Local upstream image copied")
		}

		// ---------------------------------------------------------------------
		// Attach via qemu-nbd
		// ---------------------------------------------------------------------
		fmt.Printf("connecting /dev/nbd0 to %s\n", localPath)
		nbd, err := image.Attach(localPath)
		if err != nil {
			return err
		}
		attached := true

		cleanup := func() {
			if attached && nbd != "" {
				fmt.Printf("disconnecting %s\n", nbd)
				_ = image.Detach(nbd)
			}
		}
		defer cleanup()

		// ---------------------------------------------------------------------
		// Find and Extract Kernel/Initrd (Iterates partitions)
		// ---------------------------------------------------------------------
		step("Scanning for boot artifacts...")
		_, _, err = image.FindAndExtractBootArtifacts(nbd, cloudDir)
		if err != nil {
			return fmt.Errorf("extraction failed: %w", err)
		}
		done("Artifacts extracted")

		// ---------------------------------------------------------------------
		// Detach BEFORE conversion
		// ---------------------------------------------------------------------
		fmt.Printf("disconnecting %s\n", nbd)
		if err := image.Detach(nbd); err != nil {
			return fmt.Errorf("nbd detach failed: %w", err)
		}
		attached = false
		done("Detached")

		// ---------------------------------------------------------------------
		// Convert full-disk image → raw
		// ---------------------------------------------------------------------
		rootfsRaw := filepath.Join(cloudDir, "rootfs.raw")
		fmt.Printf("extracting rootfs.raw filesystem...\n")
		if out, err := util.Run("qemu-img", "convert", "-O", "raw", localPath, rootfsRaw); err != nil {
			return fmt.Errorf("qemu-img convert failed: %v\noutput:\n%s", err, out)
		}
		done("Raw disk written: " + rootfsRaw)

		// ---------------------------------------------------------------------
		// Write image.yaml
		// ---------------------------------------------------------------------
		step("Writing image.yaml…")
		meta := config.ImageYAML{}
		meta.Image.Name = imgName
		meta.Image.Description = "Generated by mkimage --convert"
		meta.Sources = append(meta.Sources, config.Source{
			Name:        "cloud",
			Upstream:    upstream,
			Format:      "disk-image",
			RetrievedAt: time.Now().UTC().Format(time.RFC3339),
		})

		if err := config.WriteYAML(dest, &meta); err != nil {
			return err
		}
		done("image.yaml written")

		// ---------------------------------------------------------------------
		// Validate YAML
		// ---------------------------------------------------------------------
		step("Validating image.yaml…")
		if err := config.ValidateFile(filepath.Join(dest, "image.yaml")); err != nil {
			return fmt.Errorf("image.yaml invalid: %w", err)
		}
		done("image.yaml validated")

		// ---------------------------------------------------------------------
		// Fix Ownership
		// ---------------------------------------------------------------------
		step(fmt.Sprintf("Changing ownership to %d:%d...", targetUid, targetGid))
		if err := util.ChownR(dest, targetUid, targetGid); err != nil {
			fmt.Printf("warning: failed to chown output files: %v\n", err)
		}

		fmt.Println("✓ Image created:", dest)
		return nil
	},
}
