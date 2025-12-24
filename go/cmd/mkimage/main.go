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

/* ======================= pullvm (Refactored to Steps) ======================= */

// pullVMApp holds the state for the pullvm operation
type pullVMApp struct {
	// Inputs
	upstreamArg string

	// State
	sb           rtsst.ImageSB
	imageDir     string
	srcImage     string
	rawImageFile string

	// Behavior / Mockable Methods
	methods pullVMMethods

	// Framework
	steps *sst.StepsRunState
}

// pullVMMethods defines the operations for the step machine
type pullVMMethods struct {
	SetupEnvironment func(*pullVMApp) error
	DownloadImage    func(*pullVMApp) error
	ConvertImage     func(*pullVMApp) error
	AnalyseImage     func(*pullVMApp) error
	ExtractArtifacts func(*pullVMApp) error
	SaveManifest     func(*pullVMApp) error
}

// Default implementation of methods
var defaultPullVMOps = pullVMMethods{
	SetupEnvironment: pullVMSetupEnv,
	DownloadImage:    pullVMDownload,
	ConvertImage:     pullVMConvert,
	AnalyseImage:     pullVMAnalyse,
	ExtractArtifacts: pullVMExtract,
	SaveManifest:     pullVMSave,
}

func pullvmCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "pullvm <upstream-image>",
		Short: "Pull an upstream VM image and create an SST image directory",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := &pullVMApp{
				upstreamArg: args[0],
				methods:     defaultPullVMOps,
			}
			return runPullVMSteps(app)
		},
	}
}

// runPullVMSteps wires the steps together
func runPullVMSteps(app *pullVMApp) error {
	app.steps = sst.StepsInit("mkimage.pullvm")

	app.steps.StepsAdd("setup environment", func(c *sst.StepsContext, _ sst.StepState) error {
		return app.methods.SetupEnvironment(app)
	})
	app.steps.StepsAdd("download image", func(c *sst.StepsContext, _ sst.StepState) error {
		return app.methods.DownloadImage(app)
	})
	app.steps.StepsAdd("convert image", func(c *sst.StepsContext, _ sst.StepState) error {
		return app.methods.ConvertImage(app)
	})
	app.steps.StepsAdd("analyse partition table", func(c *sst.StepsContext, _ sst.StepState) error {
		return app.methods.AnalyseImage(app)
	})
	app.steps.StepsAdd("extract kernel/initrd", func(c *sst.StepsContext, _ sst.StepState) error {
		return app.methods.ExtractArtifacts(app)
	})
	app.steps.StepsAdd("save manifest", func(c *sst.StepsContext, _ sst.StepState) error {
		return app.methods.SaveManifest(app)
	})

	return sst.StepsRun(app.steps)
}

// --- Step Implementations ---

func pullVMSetupEnv(app *pullVMApp) error {
	app.sb.Ctx = context.Background()
	app.sb.Log = *sst.LogxRoot().Subsystem("mkimage.pullvm")
	app.sb.YamlObject = &mkimagepb.ImageYAML{}

	// Resolve runtime dir
	home, _ := os.UserHomeDir()
	if flagRoot != "" {
		app.sb.SstRuntimeDir = flagRoot
	} else if env := os.Getenv("SST_RUNTIMEDIR"); env != "" {
		app.sb.SstRuntimeDir = env
	} else {
		app.sb.SstRuntimeDir = filepath.Join(home, ".runtimes")
	}

	imagesRoot := filepath.Join(app.sb.SstRuntimeDir, "images")
	base := filepath.Base(app.upstreamArg)
	imageName := strings.TrimSuffix(base, filepath.Ext(base))
	app.imageDir = filepath.Join(imagesRoot, imageName)

	if _, err := os.Stat(app.imageDir); err == nil && !flagForce {
		return fmt.Errorf("destination %s exists; use --force to overwrite", app.imageDir)
	}
	_ = os.RemoveAll(app.imageDir)
	return os.MkdirAll(app.imageDir, 0755)
}

func pullVMDownload(app *pullVMApp) error {
	base := filepath.Base(app.upstreamArg)
	app.srcImage = filepath.Join(app.imageDir, base)
	return copyFile(app.upstreamArg, app.srcImage)
}

func pullVMConvert(app *pullVMApp) error {
	base := filepath.Base(app.upstreamArg)
	imageName := strings.TrimSuffix(base, filepath.Ext(base))

	sha, err := rtsst.SHA256File(app.srcImage)
	if err != nil {
		return err
	}

	app.rawImageFile = imageName + ".raw"
	rawImagePath := filepath.Join(app.imageDir, app.rawImageFile)

	// NEW LOGIC: Detect format by content, not extension
	isQcow, err := rtsst.IsQcow2(app.srcImage)
	if err != nil {
		return fmt.Errorf("failed to detect image format: %w", err)
	}

	if isQcow {
		app.sb.Log.Info(app.sb.Ctx, "image.convert",
			"from", app.srcImage,
			"to", rawImagePath,
			"format", "raw",
			"detected_type", "qcow2",
		)

		if _, err := sst.ExecShell(
			app.sb.Ctx,
			&app.sb.Log,
			"qemu-img",
			"convert",
			"-O", "raw",
			app.srcImage,
			rawImagePath,
		); err != nil {
			return fmt.Errorf("failed to convert qcow2 to raw: %w", err)
		}
	} else {
		// Assume raw already
		if app.srcImage != rawImagePath {
			if err := os.Rename(app.srcImage, rawImagePath); err != nil {
				return err
			}
		}
	}
	
	app.sb.YamlObject = &mkimagepb.ImageYAML{
		Name:           imageName,
		SourceFileName: base,
		Upstream:       app.upstreamArg,
		Format:         strings.TrimPrefix(filepath.Ext(base), "."),
		LocalSha:       sha,
		RetrievedAt:    time.Now().UTC().Format(time.RFC3339),
		RawImage:       app.rawImageFile,
	}

	return nil
}

func pullVMAnalyse(app *pullVMApp) error {
	return rtsst.AnalyseImg(&app.sb)
}

func pullVMExtract(app *pullVMApp) error {
	return rtsst.ExtractKernelAndInitrdFromBootPartition(&app.sb)
}

func pullVMSave(app *pullVMApp) error {
	yo := &sst.YamlObject{
		Path:   filepath.Join(app.imageDir, "image.yaml"),
		Schema: "sst.yaml.mkimage.v1.ImageYAML",
		Kind:   "Image",
		Msg:    app.sb.YamlObject,
	}

	if diags := yo.Save(app.sb.Ctx, &app.sb.Log); diags != nil {
		return diags
	}

	if flagVerbose {
		app.sb.Log.Info(app.sb.Ctx, "image.created",
			"name", app.sb.YamlObject.Name,
			"dir", app.imageDir,
		)
	}
	return nil
}

/* ======================= instance (PRESERVED) ======================= */

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
