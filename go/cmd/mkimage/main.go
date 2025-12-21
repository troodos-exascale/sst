package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	mkimagepb "github.com/swiss-software-tool/runtime-smart/go/proto/yaml/mkimage/v1"
	runctlpb "github.com/swiss-software-tool/runtime-smart/go/proto/yaml/runctl/v1"

	rtsst "github.com/swiss-software-tool/runtime-smart/go/internal/rtsst"
	sst "github.com/swiss-software-tool/runtime-smart/go/internal/sst"
)

var (
	flagRoot    string
	flagForce   bool
	flagVerbose bool
)

var rootCmd = &cobra.Command{
	Use:           "mkimage",
	Short:         "Create SST VM image directories from upstream sources",
	SilenceUsage:  true,
	SilenceErrors: true,
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "mkimage error: %v\n", err)
		os.Exit(1)
	}
}

func init() {
	home, _ := os.UserHomeDir()
	defaultRoot := filepath.Join(home, ".runtimes")

	rootCmd.PersistentFlags().StringVarP(&flagRoot, "dir", "d", defaultRoot, "Runtime root directory")
	rootCmd.PersistentFlags().BoolVarP(&flagVerbose, "verbose", "v", false, "Verbose logging")
	rootCmd.PersistentFlags().BoolVarP(&flagForce, "force", "f", false, "Overwrite existing image directory")

	rootCmd.AddCommand(pullvmCmd())
	rootCmd.AddCommand(instanceCmd())
}

/* ======================= pullvm ======================= */

func pullvmCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "pullvm <upstream-image>",
		Short: "Pull an upstream VM image and create an SST image directory",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return pullVM(args[0])
		},
	}
}

func pullVM(upstream string) error {
	var sb rtsst.ImageSB
	var imageDir string

	sb.Ctx = context.Background()
	sb.Log = *sst.LogxRoot().Subsystem("mkimage.pullvm")
	sb.YamlObject = &mkimagepb.ImageYAML{}

	// ------------------------------------------------------------------
	// Resolve runtime dir
	// ------------------------------------------------------------------
	home, _ := os.UserHomeDir()
	if flagRoot != "" {
		sb.SstRuntimeDir = flagRoot
	} else if env := os.Getenv("SST_RUNTIMEDIR"); env != "" {
		sb.SstRuntimeDir = env
	} else {
		sb.SstRuntimeDir = filepath.Join(home, ".runtimes")
	}

	imagesRoot := filepath.Join(sb.SstRuntimeDir, "images")

	base := filepath.Base(upstream)
	imageName := strings.TrimSuffix(base, filepath.Ext(base))
	imageDir = filepath.Join(imagesRoot, imageName)

	if _, err := os.Stat(imageDir); err == nil && !flagForce {
		return fmt.Errorf("destination %s exists; use --force to overwrite", imageDir)
	}
	_ = os.RemoveAll(imageDir)
	if err := os.MkdirAll(imageDir, 0755); err != nil {
		return err
	}

	// ------------------------------------------------------------------
	// Copy upstream image
	// ------------------------------------------------------------------
	srcImage := filepath.Join(imageDir, base)
	if err := copyFile(upstream, srcImage); err != nil {
		return err
	}
	sha, err := rtsst.SHA256File(srcImage)
	if err != nil {
		return err
	}

	rawImageFile := imageName + ".raw"
	rawImage := filepath.Join(imageDir, rawImageFile) // raw disk
	if strings.HasSuffix(srcImage, ".qcow2") {
		sb.Log.Info(sb.Ctx, "image.convert",
			"from", srcImage,
			"to", rawImage,
			"format", "raw",
		)

		if _, err := sst.ExecShell(
			sb.Ctx,
			&sb.Log,
			"qemu-img",
			"convert",
			"-O", "raw",
			srcImage,
			rawImage,
		); err != nil {
			return fmt.Errorf("failed to convert qcow2 to raw: %w", err)
		}
	} else {
		// Assume raw already
		rawImage = srcImage
	}

	// ------------------------------------------------------------------
	// Initialise image.yaml object
	// ------------------------------------------------------------------

	sb.YamlObject = &mkimagepb.ImageYAML{
		Name:           imageName,
		SourceFileName: base,
		Upstream:       upstream,
		Format:         strings.TrimPrefix(filepath.Ext(base), "."),
		LocalSha:       sha,
		RetrievedAt:    time.Now().UTC().Format(time.RFC3339),
		RawImage:       rawImageFile,
	}

	// ------------------------------------------------------------------
	// STEP (ii): analyse partition table (pure GPT)
	// ------------------------------------------------------------------
	err = rtsst.AnalyseImg(&sb)
	if err != nil {
		return err
	}

	// ------------------------------------------------------------------
	// STEP (iv): deterministic kernel/initrd extraction
	// ------------------------------------------------------------------

	err = rtsst.ExtractKernelAndInitrdFromBootPartition(&sb)
	if err != nil {
		return err
	}

	// ------------------------------------------------------------------
	// Write image.yaml
	// ------------------------------------------------------------------
	yo := &sst.YamlObject{
		Path:   filepath.Join(imageDir, "image.yaml"),
		Schema: "sst.yaml.mkimage.v1.ImageYAML",
		Kind:   "Image",
		Msg:    sb.YamlObject,
	}

	if diags := yo.Save(sb.Ctx, &sb.Log); diags != nil {
		return diags
	}

	if flagVerbose {
		sb.Log.Info(sb.Ctx, "image.created",
			"name", imageName,
			"dir", imageDir,
		)
	}

	return nil
}

/* ======================= instance ======================= */

func instanceCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "instance <image> <name>",
		Short: "Create an instance directory from an image",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return createInstance(args[0], args[1])
		},
	}
}

func createInstance(image, name string) error {
	ctx := context.Background()
	log := sst.LogxRoot().Subsystem("mkimage.instance")

	imageDir := filepath.Join(flagRoot, "images", image)
	instDir := rtsst.InstanceDir(name)

	if _, err := os.Stat(instDir); err == nil && !flagForce {
		return fmt.Errorf("instance %s exists; use --force to overwrite", name)
	}
	_ = os.RemoveAll(instDir)
	if err := os.MkdirAll(instDir, 0755); err != nil {
		return err
	}

	// Load instance.yaml (proto-backed)
	inst := &runctlpb.Instance{}
	yo := &sst.YamlObject{
		Path:   filepath.Join(instDir, "instance.yaml"),
		Schema: "sst.yaml.runctl.v1.Instance",
		Kind:   "Instance",
		Msg:    inst,
	}

	if diags := yo.Load(ctx, log); diags != nil {
		return diags
	}

	// Copy base image
	src := filepath.Join(imageDir, filepath.Base(image))
	dst := filepath.Join(instDir, filepath.Base(image))
	if err := copyFile(src, dst); err != nil {
		return err
	}

	return nil
}

/* ======================= helpers ======================= */

func copyFile(src, dst string) error {
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

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}
