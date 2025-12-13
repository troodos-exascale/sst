package main

import (
	"fmt"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
	"net/url"
	"os"
	"os/exec"
	"os/user"
	"path"
	"path/filepath"
	rtsst "github.com/swiss-software-tool/runtime-smart/go/internal/rtsst"
	"strings"
	"time"
)

var (
	flagRoot    string
	flagForce   bool
	flagVerbose bool
	rootCmd     = &cobra.Command{
		Use:           "mkimage",
		Short:         "Create SST VM image directories from upstream sources",
		Long:          "mkimage pulls, pushes, builds runtime images and instances",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
)

func init() {
	rootCmd.AddCommand(pullvmCmd)
	rootCmd.AddCommand(instanceCmd)
	home, _ := os.UserHomeDir()
	defaultRoot := filepath.Join(home, ".runtimes")
	rootCmd.PersistentFlags().StringVarP(&flagRoot, "dir", "d", defaultRoot,
		"Runtime root directory (e.g. /Users/you/.runtimes)")
	rootCmd.PersistentFlags().BoolVarP(&flagVerbose, "verbose", "v", false,
		"Verbose logging")
	rootCmd.PersistentFlags().BoolVarP(&flagForce, "force", "f", false,
		"Overwrite existing image directory if it exists")
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "mkimage error: %v\n", err)
		os.Exit(1)
	}
}


var instanceCmd = &cobra.Command{
	Use:   "instance <image-file> <name>",
	Short: "Create an instance directory from an image",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		imageFile := args[0]
		name := args[1]

		instDir := rtsst.InstanceDir(name)
		if st, err := os.Stat(instDir); err == nil && st.IsDir() {
			if !flagForce {
				return fmt.Errorf("instance %s exists (use --force)", name)
			}
			_ = os.RemoveAll(instDir)
		}
		if err := os.MkdirAll(instDir, 0755); err != nil {
			return err
		}

		// 1) Create disk.raw from source image
		dstDisk := filepath.Join(instDir, "disk.raw")

		if rtsst.IsQCOW2(imageFile) {
			rtsst.LogDebug(flagVerbose, "detected qcow2; running qemu-img convert")
			cmd := exec.Command("qemu-img", "convert", "-O", "raw", imageFile, dstDisk)
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			if err := cmd.Run(); err != nil {
				return fmt.Errorf("qemu-img convert failed: %w", err)
			}
		} else {
			rtsst.LogDebug(flagVerbose, "assuming raw image; performing sparse copy")
			if err := rtsst.CopySparse(imageFile, dstDisk); err != nil {
				return fmt.Errorf("sparse copy failed: %w", err)
			}
		}

		// 2
		err := rtsst.WriteCIData(instDir, name)
		if err != nil {
			return fmt.Errorf("Failed to write cidata: %w", err)
		}
		if err := rtsst.UpdateCIDATAISO(instDir); err != nil {
			return fmt.Errorf("Failed to update cidata.iso")
		}
		
		// 3) Create 30GB sparse data disk (default)
		dataImg := filepath.Join(instDir, "data.img")
		if _, err := os.Stat(dataImg); os.IsNotExist(err) {
			if err := rtsst.CreateSparse(dataImg, 30*1024*1024*1024); err != nil {
				return fmt.Errorf("creating data.img: %w", err)
			}
		} else if err != nil {
			return fmt.Errorf("stat data.img: %w", err)
		}

		// 4) Add share /Users/$USER -> /Users/$USER
		u, _ := user.Current()
		hostShare := filepath.Join("/Users", u.Username)
		guestShare := filepath.Join("/Users", u.Username)

		// 5) Verbose kernel params (still stored even if EFI mode)
		params := "console=hvc0 systemd.log_target=console"

		// 6) Defaults for CPU and memory
		defaultCPU := 4
		defaultMem := "8G"

		cfg := rtsst.RuntimeConfig{
			Name:       name,
			Boot:       "efi",
			SystemDisk: dstDisk,
			CIDataDisk: "cidata.iso",
			EFIVars:    "efi-vars.fd",
			Params:     params,
			CPUCount:   defaultCPU,
			MemorySize: defaultMem,
			DataDisks:  []rtsst.DataDisk{{Path: filepath.Base(dataImg), Mode: "rw"}},
			Shares: []rtsst.Share{
				{Host: hostShare, Guest: guestShare},
			},
		}
		mac, err := rtsst.GenerateMAC()
		if err != nil {
			return err
		}
		cfg.Network.MAC = mac
		cfg.Network.Mode = "nat"

		data, err := yaml.Marshal(&cfg)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(instDir, "rtsst.yaml"), data, 0o644)
	},
}

// Logging helpers

func requireRoot() error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("mkimage must be run as root")
	}
	return nil
}

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

func deriveUpstreamFilename(upstream string) string {
	var base string

	if rtsst.IsURL(upstream) {
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
	return fmt.Sprintf("%s%s", name, ext)
}

// Cobra convert command

var pullvmCmd = &cobra.Command{
	Use:   "pullvm <distro> <version> <variant> <qcow-or-url>",
	Args:  cobra.ExactArgs(4),
	Short: "Convert a qcow2/URL upstream image into an SST image directory",

	RunE: func(cmd *cobra.Command, args []string) error {
		distro := args[0]
		version := args[1]
		variant := args[2]
		upstream := args[3]

		// Resolve runtimes root → DIR/images/<name>

		absRoot, _ := filepath.Abs(flagRoot)           // DIR
		imagesRoot := filepath.Join(absRoot, "images") // DIR/images
		distroDir := filepath.Join(imagesRoot, distro+"-"+version)
		variantDir := filepath.Join(distroDir, variant)

		// determine filesystem owner
		targetUid, targetGid, _ := rtsst.GetFileOwnership(absRoot)

		// XXX not needed anymore  requireRoot()
		if flagVerbose {
			fmt.Println("-> mkimage pullvm")
			fmt.Println("  upstream:", upstream)
			fmt.Println("  dest:    ", distroDir)
			fmt.Printf("  target owner: %d:%d\n", targetUid, targetGid)
		}

		// Enforce directory existence rules
		_, err := os.Stat(variantDir)
		if err == nil {
			// directory exists
			if !flagForce {
				return fmt.Errorf("destination %s exists; use --force to overwrite", variantDir)
			}
			_ = os.RemoveAll(variantDir)
		}

		// create images root and destination
		if _, err := os.Stat(variantDir); os.IsNotExist(err) {
			if err := os.MkdirAll(variantDir, 0o755); err != nil {
				return err
			}
		}

		// Fix ownership early
		_ = os.Chown(distroDir, targetUid, targetGid)
		_ = os.Chown(variantDir, targetUid, targetGid)

		// Acquire LOCK for this image
		lockPath := filepath.Join(distroDir, ".mkimage.lock")
		lk, err := rtsst.CreateLock(lockPath)
		if err != nil {
			return err
		}
		defer lk.Remove()

		// Download or copy upstream source

		step("Processing upstream source…")
		name := deriveUpstreamFilename(upstream)
		target := filepath.Join(variantDir, name)

		if rtsst.IsURL(upstream) {
			step("Downloading upstream image…")
			if err := rtsst.DownloadToFile(upstream, target); err != nil {
				return fmt.Errorf("download failed: %w", err)
			}
			done("Download complete")
		} else {
			step("Copying upstream image into upstream dir")
			if err := rtsst.CopyFile(upstream, target); err != nil {
				return fmt.Errorf("copy upstream image: %w", err)
			}
			done("Local upstream image copied")
		}

		// Write + Validate image.yaml

		step("Writing image.yaml…")

		meta := rtsst.ImageYAML{}
		meta.Image.Name = name
		meta.Image.Description = "Generated by mkimage pullvm"
		meta.Sources = append(meta.Sources, rtsst.Source{
			Name:        variant,
			Upstream:    upstream,
			Format:      "disk-image",
			RetrievedAt: time.Now().UTC().Format(time.RFC3339),
		})

		if err := rtsst.WriteYAML(variantDir, &meta); err != nil {
			return err
		}
		done("image.yaml written")

		step("Validating image.yaml…")
		if err := rtsst.ValidateFile(filepath.Join(variantDir, "image.yaml")); err != nil {
			return fmt.Errorf("image.yaml invalid: %w", err)
		}
		done("image.yaml validated")

		fmt.Println("-> Image created:", variantDir)
		return nil
	},
}
