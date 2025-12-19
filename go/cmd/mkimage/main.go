package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	mkimagepb "github.com/swiss-software-tool/runtime-smart/go/proto/yaml/mkimage/v1"
	runctlpb "github.com/swiss-software-tool/runtime-smart/go/proto/yaml/runctl/v1"

	sst "github.com/swiss-software-tool/runtime-smart/go/internal/sst"
	rtsst "github.com/swiss-software-tool/runtime-smart/go/internal/rtsst"
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
	ctx := context.Background()
	log := sst.LogxRoot().Subsystem("mkimage.pullvm")

	name := strings.TrimSuffix(filepath.Base(upstream), filepath.Ext(upstream))
	imageDir := filepath.Join(flagRoot, "images", name)

	if _, err := os.Stat(imageDir); err == nil && !flagForce {
		return fmt.Errorf("destination %s exists; use --force to overwrite", imageDir)
	}
	_ = os.RemoveAll(imageDir)
	if err := os.MkdirAll(imageDir, 0755); err != nil {
		return err
	}

	// Copy upstream image
	dst := filepath.Join(imageDir, filepath.Base(upstream))
	if err := copyFile(upstream, dst); err != nil {
		return err
	}

	sha, err := sha256File(dst)
	if err != nil {
		return err
	}

	// Write image.yaml via proto + YamlObject
	meta := &mkimagepb.ImageYAML{
		Image: &mkimagepb.ImageMeta{
			Name: name,
		},
		Sources: []*mkimagepb.Source{
			{
				Name:        name,
				Upstream:    upstream,
				Format:      filepath.Ext(upstream),
				Sha256:      sha,
				RetrievedAt: time.Now().UTC().Format(time.RFC3339),
			},
		},
	}

	yo := &sst.YamlObject{
		Path:   filepath.Join(imageDir, "image.yaml"),
		Schema: "sst.yaml.mkimage.v1.ImageYAML",
		Kind:   "Image",
		Msg:    meta,
	}

	if diags := yo.Save(ctx, log); diags != nil {
		return fmt.Errorf(diags.Error())
	}

	if flagVerbose {
		fmt.Printf("Image %s created\n", name)
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
		return fmt.Errorf(diags.Error())
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

	_, err = io.Copy(out, in)
	return err
}

func sha256File(path string) (string, error) {
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
