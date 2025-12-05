package main

import (
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"mkimage/internal/config"
	"mkimage/internal/image"
	"mkimage/internal/util"

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
		// Resolve runtimes root → DIR/images/<name>
		// ---------------------------------------------------------------------
		absRoot, _ := filepath.Abs(flagRoot)           // DIR
		imagesRoot := filepath.Join(absRoot, "images") // DIR/images
		dest := filepath.Join(imagesRoot, imgName)     // DIR/images/<name>
		cloudDir := filepath.Join(dest, "cloud")       // DIR/images/<name>/cloud

		// determine filesystem owner
		targetUid, targetGid, _ := util.GetFileOwnership(absRoot)

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

		// create images root and destination
		if err := os.MkdirAll(imagesRoot, 0o755); err != nil {
			return err
		}
		if err := os.MkdirAll(dest, 0o755); err != nil {
			return err
		}

		// Fix ownership early
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

		// cloud directory
		if err := os.MkdirAll(cloudDir, 0o755); err != nil {
			return err
		}

		// ---------------------------------------------------------------------
		// Download or copy upstream source
		// ---------------------------------------------------------------------
		var localPath string
		step("Processing upstream source…")

		name := deriveUpstreamFilename(upstream)
		target := filepath.Join(cloudDir, name)

		if util.IsURL(upstream) {
			step("Downloading upstream image…")
			if err := util.DownloadToFile(upstream, target); err != nil {
				return fmt.Errorf("download failed: %w", err)
			}
			localPath = target
			done("Download complete")
		} else {
			step("Copying upstream image into cloud/…")
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
		// Find and Extract Kernel/Initrd from partitions
		// ---------------------------------------------------------------------
		step("Scanning for boot artifacts...")
		_, _, err = image.FindAndExtractBootArtifacts(nbd, cloudDir)
		if err != nil {
			return fmt.Errorf("extraction failed: %w", err)
		}
		done("Artifacts extracted")

		// ---------------------------------------------------------------------
		// Select root filesystem partition
		// ---------------------------------------------------------------------
		step("Selecting root filesystem partition…")
		rootPart, err := image.FindRootPartition(nbd)
		if err != nil {
			return fmt.Errorf("root partition detection failed: %w", err)
		}
		done("Root partition: " + rootPart)

		// ---------------------------------------------------------------------
		// Determine size of root partition
		// ---------------------------------------------------------------------
		sizeOut, err := util.Run("blockdev", "--getsize64", rootPart)
		if err != nil {
			return fmt.Errorf("failed to get size of %s: %v\noutput:\n%s",
				rootPart, err, sizeOut)
		}
		sizeOut = strings.TrimSpace(sizeOut)
		sizeBytes, err := strconv.ParseInt(sizeOut, 10, 64)
		if err != nil {
			return fmt.Errorf("invalid size returned for %s: %w", rootPart, err)
		}

		// ---------------------------------------------------------------------
		// Extract root filesystem (byte-for-byte copy)
		// ---------------------------------------------------------------------
		rootfsRaw := filepath.Join(cloudDir, "rootfs.raw")
		fmt.Printf("extracting %d bytes from %s → %s…\n",
			sizeBytes, rootPart, rootfsRaw)

		src, err := os.Open(rootPart)
		if err != nil {
			return fmt.Errorf("failed to open %s: %w", rootPart, err)
		}
		defer src.Close()

		dst, err := os.Create(rootfsRaw)
		if err != nil {
			return fmt.Errorf("failed to create %s: %w", rootfsRaw, err)
		}
		defer dst.Close()

		written, err := io.CopyN(dst, src, sizeBytes)
		if err != nil && err != io.EOF {
			return fmt.Errorf("copying root partition failed: %w", err)
		}

		fmt.Printf("copied %d bytes\n", written)
		done("Root filesystem written: " + rootfsRaw)

		// Mark rootfs.raw as read-only to reflect that it must not be modified.
		if err := os.Chmod(rootfsRaw, 0o444); err != nil {
			fmt.Printf("warning: failed to mark %s read-only: %v\n", rootfsRaw, err)
		}

		// ---------------------------------------------------------------------
		// Detach AFTER rootfs extraction
		// ---------------------------------------------------------------------
		fmt.Printf("disconnecting %s\n", nbd)
		if err := image.Detach(nbd); err != nil {
			return fmt.Errorf("nbd detach failed: %w", err)
		}
		attached = false
		done("Detached")

		// ---------------------------------------------------------------------
		// Write + Validate image.yaml
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

		step("Validating image.yaml…")
		if err := config.ValidateFile(filepath.Join(dest, "image.yaml")); err != nil {
			return fmt.Errorf("image.yaml invalid: %w", err)
		}
		done("image.yaml validated")

		// ---------------------------------------------------------------------
		// Fix ownership (chown everything)
		// ---------------------------------------------------------------------
		step(fmt.Sprintf("Changing ownership to %d:%d…", targetUid, targetGid))
		if err := util.ChownR(dest, targetUid, targetGid); err != nil {
			fmt.Printf("warning: failed to chown output files: %v\n", err)
		}

		fmt.Println("✓ Image created:", dest)
		return nil
	},
}
