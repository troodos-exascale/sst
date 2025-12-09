package main

import (
    "fmt"
    "os"
	"path/filepath"
    "github.com/spf13/cobra"
    "io"
    "net/url"
    "path"
    "strconv"
    "strings"
    "time"
    "runtime-smart/internal"
	"os/exec"
	"os/user"
	"gopkg.in/yaml.v3"
	)

var (
	flagRoot    string
	flagForce   bool
	flagVerbose bool
	rootCmd     = &cobra.Command{
		Use:   "mkimage",
		Short: "Create SST VM image directories from upstream sources",
		Long:  "mkimage pulls, pushes, builds runtime images and instances",
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
	rootCmd.Flags().BoolVarP(&flagForce, "force", "f", false,
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

	instDir := internal.InstanceDir(name)
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

	if internal.IsQCOW2(imageFile) {
		internal.LogDebug(flagVerbose, "detected qcow2; running qemu-img convert")
		cmd := exec.Command("qemu-img", "convert", "-O", "raw", imageFile, dstDisk)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("qemu-img convert failed: %w", err)
		}
	} else {
		internal.LogDebug(flagVerbose, "assuming raw image; performing sparse copy")
		if err := internal.CopySparse(imageFile, dstDisk); err != nil {
			return fmt.Errorf("sparse copy failed: %w", err)
		}
	}

	// 2) Create empty EFI vars
	efiVars := filepath.Join(instDir, "efi-vars")
	if err := os.WriteFile(efiVars, []byte{}, 0644); err != nil {
		return fmt.Errorf("writing efi-vars: %w", err)
	}

	// 3) Create 30GB sparse data disk (default)
	dataImg := filepath.Join(instDir, "data.img")
	if _, err := os.Stat(dataImg); os.IsNotExist(err) {
		if err := internal.CreateSparse(dataImg, 30*1024*1024*1024); err != nil {
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
	params := "console=hvc0 root_device=dev/hda1 systemd.log_target=console"

	// 6) Defaults for CPU and memory
	defaultCPU := 4
	defaultMem := "4G"

	cfg := internal.RuntimeConfig{
		Name:       name,
		Boot:       "efi",
		SystemDisk: dstDisk,
		EFIVars:    efiVars,
		Params:     params,
		CPUCount:   defaultCPU,
		MemorySize: defaultMem,
		DataDisks: []internal.DataDisk{{ Path: filepath.Base(dataImg), Mode: "rw" }},
		Shares: []internal.Share{
			{Host: hostShare, Guest: guestShare},
		},
	}
	mac, err := internal.GenerateMAC()
	if err != nil {
		return err
	}
	cfg.Network.MAC = mac
	cfg.Network.Mode = "nat"

	data, err := yaml.Marshal(&cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(instDir, "runtime.yaml"), data, 0o644)
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

	if internal.IsURL(upstream) {
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
		distroDir := filepath.Join(imagesRoot, distro + "-" + version)
		variantDir := filepath.Join(distroDir, variant)
		upstreamDir := filepath.Join(distroDir,"upstream")

		// determine filesystem owner
		targetUid, targetGid, _ := internal.GetFileOwnership(absRoot)

		requireRoot()
		if flagVerbose {
			fmt.Println("-> mkimage pullvm")
			fmt.Println("  upstream:", upstream)
			fmt.Println("  dest:    ", distroDir)
			fmt.Printf("  target owner: %d:%d\n", targetUid, targetGid)
		}

		// Enforce directory existence rules
		_, err := os.Stat(variantDir);
		if err != nil  {
			return fmt.Errorf("destination %s exists; use --force to overwrite", variantDir)
		}
		if flagForce {
			_ = os.RemoveAll(variantDir)
		}

		// create images root and destination
		if _, err := os.Stat(upstreamDir); os.IsNotExist(err) {
			if err := os.MkdirAll(upstreamDir, 0o755); err != nil {
				return err
			}
		}
		if _, err := os.Stat(variantDir); os.IsNotExist(err) {
			if err := os.MkdirAll(variantDir, 0o755); err != nil {
				return err
			}
		}


		// Fix ownership early
		_ = os.Chown(distroDir, targetUid, targetGid)
		_ = os.Chown(upstreamDir, targetUid, targetGid)
		_ = os.Chown(variantDir, targetUid, targetGid)

		// Acquire LOCK for this image
		lockPath := filepath.Join(distroDir, ".mkimage.lock")
		lk, err := internal.CreateLock(lockPath)
		if err != nil {
			return err
		}
		defer lk.Remove()

		// Download or copy upstream source

		var localPath string
		step("Processing upstream source…")

		name := deriveUpstreamFilename(upstream)
		target := filepath.Join(upstreamDir, name)

		if internal.IsURL(upstream) {
			step("Downloading upstream image…")
			if err := internal.DownloadToFile(upstream, target); err != nil {
				return fmt.Errorf("download failed: %w", err)
			}
			localPath = target
			done("Download complete")
		} else {
			step("Copying upstream image into upstream dir")
			if err := internal.CopyFile(upstream, target); err != nil {
				return fmt.Errorf("copy upstream image: %w", err)
			}
			localPath = target
			done("Local upstream image copied")
		}


		// Attach via qemu-nbd

		fmt.Printf("connecting /dev/nbd0 to %s\n", localPath)
		nbd, err := internal.Attach(localPath)
		if err != nil {
			return err
		}
		attached := true

		cleanup := func() {
			if attached && nbd != "" {
				fmt.Printf("disconnecting %s\n", nbd)
				_ = internal.Detach(nbd)
			}
		}
		defer cleanup()

		// Find and Extract Kernel/Initrd from partitions

		step("Scanning for boot artifacts...")
		_, _, err = internal.FindAndExtractBootArtifacts(nbd, variantDir)
		if err != nil {
			return fmt.Errorf("extraction failed: %w", err)
		}
		done("Artifacts extracted")


		// Select root filesystem partition

		step("Selecting root filesystem partition…")
		rootPart, err := internal.FindRootPartition(nbd)
		if err != nil {
			return fmt.Errorf("root partition detection failed: %w", err)
		}
		done("Root partition: " + rootPart)

		// ---------------------------------------------------------------------
		// Determine size of root partition
		// ---------------------------------------------------------------------
		sizeOut, err := internal.Run("blockdev", "--getsize64", rootPart)
		if err != nil {
			return fmt.Errorf("failed to get size of %s: %v\noutput:\n%s",
				rootPart, err, sizeOut)
		}
		sizeOut = strings.TrimSpace(sizeOut)
		sizeBytes, err := strconv.ParseInt(sizeOut, 10, 64)
		if err != nil {
			return fmt.Errorf("invalid size returned for %s: %w", rootPart, err)
		}


		// Extract root filesystem (byte-for-byte copy)

		rootfsRaw := filepath.Join(variantDir, "rootfs.raw")
		fmt.Printf("extracting %d bytes from %s → %s…\n",
			sizeBytes, rootPart, rootfsRaw)

		err = internal.CopySparse(rootPart, rootfsRaw)
		if err != nil && err != io.EOF {
			return fmt.Errorf("copying root partition failed: %w", err)
		}
		done("Root filesystem written: " + rootfsRaw)
		if err := os.Chmod(rootfsRaw, 0o444); err != nil {
			fmt.Printf("warning: failed to mark %s read-only: %v\n", rootfsRaw, err)
		}

		// Detach AFTER rootfs extraction

		fmt.Printf("disconnecting %s\n", nbd)
		if err := internal.Detach(nbd); err != nil {
			return fmt.Errorf("nbd detach failed: %w", err)
		}
		attached = false
		done("Detached")


		// Write + Validate image.yaml

		step("Writing image.yaml…")

		meta := internal.ImageYAML{}
		meta.Image.Name = name
		meta.Image.Description = "Generated by mkimage pullvm"
		meta.Sources = append(meta.Sources, internal.Source{
			Name:        "cloud",
			Upstream:    upstream,
			Format:      "disk-image",
			RetrievedAt: time.Now().UTC().Format(time.RFC3339),
		})

		if err := internal.WriteYAML(variantDir, &meta); err != nil {
			return err
		}
		done("image.yaml written")

		step("Validating image.yaml…")
		if err := internal.ValidateFile(filepath.Join(variantDir, "image.yaml")); err != nil {
			return fmt.Errorf("image.yaml invalid: %w", err)
		}
		done("image.yaml validated")


		// Fix ownership (chown everything)

		step(fmt.Sprintf("Changing ownership to %d:%d…", targetUid, targetGid))
		if err := internal.ChownR(variantDir, targetUid, targetGid); err != nil {
			fmt.Printf("warning: failed to chown output files: %v\n", err)
		}

		fmt.Println("-> Image created:", variantDir)
		return nil
	},
}
