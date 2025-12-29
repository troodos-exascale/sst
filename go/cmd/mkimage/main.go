package main

import (
	"context"
	"fmt"
	"os"
	"os/user"
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
	return rtsst.CopySparse(app.upstreamArg, app.srcImage)
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

	var imageDir string

	// 1. Check if the argument is a valid path to an existing directory
	if info, err := os.Stat(image); err == nil && info.IsDir() {
		imageDir = image
	} else {
		// 2. Otherwise, assume it is a name in the default images directory
		imageDir = filepath.Join(flagRoot, "images", image)
	}

	// Validate the resolved directory
	if _, err := os.Stat(imageDir); os.IsNotExist(err) {
		return fmt.Errorf("image directory not found at: %s", imageDir)
	}

	instDir := rtsst.InstanceDir(name)

	if _, err := os.Stat(instDir); err == nil && !flagForce {
		return fmt.Errorf("instance %s exists; use --force to overwrite", name)
	}
	_ = os.RemoveAll(instDir)
	if err := os.MkdirAll(instDir, 0755); err != nil {
		return err
	}

	imageYaml := &mkimagepb.ImageYAML{}
	imgYo := &sst.YamlObject{
		Path:   filepath.Join(imageDir, "image.yaml"),
		Schema: "sst.yaml.mkimage.v1.ImageYAML",
		Kind:   "Image",
		Msg:    imageYaml,
	}
	if diags := imgYo.Load(ctx, log); diags != nil {
		return diags
	}

	// Load instance.yaml (proto-backed)
	instanceYaml := &runctlpb.Instance{}
	instYo := &sst.YamlObject{
		Path:   filepath.Join(instDir, "instance.yaml"),
		Schema: "sst.yaml.runctl.v1.Instance",
		Kind:   "Instance",
		Msg:    instanceYaml,
	}

	// Copy base image + kernel + initrd (unchanged)
	src := filepath.Join(imageDir, imageYaml.RawImage)
	dst := filepath.Join(instDir, imageYaml.RawImage)
	if err := rtsst.CopySparse(src, dst); err != nil {
		return err
	}
	if err := rtsst.CopyFile(filepath.Join(imageDir, imageYaml.Kernel), filepath.Join(instDir, imageYaml.Kernel)); err != nil {
		return err
	}
	if err := rtsst.CopyFile(filepath.Join(imageDir, imageYaml.Initrd), filepath.Join(instDir, imageYaml.Initrd)); err != nil {
		return err
	}
	mac, err := rtsst.GenerateMAC()
	if err != nil {
		return err
	}

	u, err := user.Current()
	if err != nil {
		return err
	}

	// Create data.img (unchanged)
	size := int64(30) * 1024 * 1024 * 1024 // 30 GiB
	f, err := os.Create(filepath.Join(instDir, "data.img"))
	if err != nil {
		return err
	}
	defer f.Close()
	if err := f.Truncate(size); err != nil {
		return err
	}

	// Populate new schema
	instanceYaml.ApiVersion = "sst.io/v1"
	instanceYaml.Kind = "Instance"
	instanceYaml.Name = name
	instanceYaml.Hostname = name

	instanceYaml.Gui = false
	instanceYaml.CpuCount = "4"
	instanceYaml.MemorySize = "8096"
	instanceYaml.MemorySizeSuffix = "MiB" // matches runvm default; optional but explicit

	instanceYaml.Balloon = false
	instanceYaml.Bootloader = "linux"
	instanceYaml.EfiVars = "efi-vars"

	instanceYaml.Kernel = imageYaml.Kernel
	instanceYaml.Initrd = imageYaml.Initrd

	// Root disk is RW; extra data disk RW; cidata is CDROM (USB mass storage in your runvm)
	instanceYaml.Rwdisk = []string{
		imageYaml.RawImage, // the copied root image in instDir
		"data.img",
	}
	instanceYaml.Disk = []string{
		"cidata.iso",
	}

	// Shared folders: prefer --shared-dir (your unified name); put host:tag or host:tag:ro per your parser.
	// If you want host path == tag (as your current runvm supports), this is fine too.
	instanceYaml.SharedDir = []string{
		fmt.Sprintf("%s:%s", "/Users/"+u.Username, "/Users/"+u.Username),
	}

	// Network: your runvm accepts "aa:bb:...@nat" to set MAC, so store exactly that argv value.
	instanceYaml.Network = []string{
		fmt.Sprintf("%s@nat", mac),
	}

	// Kernel params: one token per line (runctl joins into --params string later)
	instanceYaml.Params = []string{
		"console=hvc0",
		"oops=panic",
		"panic=10",
		"sysrq_always_enabled=1",
		"loglevel=4",
		"cpuidle.off=1",
		fmt.Sprintf("root=%s", imageYaml.RootfsDevice),
		fmt.Sprintf("rootfstype=%s", imageYaml.RootfsType),
	}

	// Write instance.yaml
	if diags := instYo.Save(ctx, log); diags != nil {
		return diags
	}
	if err := rtsst.WriteCIData(ctx, log, instDir, instanceYaml); err != nil {
		return fmt.Errorf("failed to write cidata: %w", err)
	}

	err = rtsst.UpdateCIDATAISO(instDir)
	if err != nil {
		return err
	}

	return nil
}
