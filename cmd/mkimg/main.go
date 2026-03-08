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

	mkimagepb "github.com/swiss-software-tool/sst/proto/yaml/mkimage/v1"
	runctlpb "github.com/swiss-software-tool/sst/proto/yaml/runctl/v1"

	rtsst "github.com/swiss-software-tool/sst/internal/rtsst"
	sst "github.com/swiss-software-tool/sst/internal/sst"
)

var (
	flagRoot    string
	flagForce   bool
	flagVerbose bool
	
	flagSize    string 
	flagEfi     bool
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
	rootCmd.PersistentFlags().StringVarP(&flagRoot, "dir", "d", "", "Runtime root directory (default: $SST_RUNTIMEDIR or ~/.runtimes)")
	rootCmd.PersistentFlags().BoolVarP(&flagVerbose, "verbose", "v", false, "Verbose logging")
	rootCmd.PersistentFlags().BoolVarP(&flagForce, "force", "f", false, "Overwrite existing image directory")

	rootCmd.AddCommand(pullvmCmd())
	
	instCmd := instanceCmd()
	instCmd.Flags().StringVarP(&flagSize, "size", "s", "20G", "Target size for the root disk (e.g. 20G, 100GiB)")
	instCmd.Flags().BoolVar(&flagEfi, "efi", false, "Use EFI bootloader instead of direct Linux kernel boot")
	rootCmd.AddCommand(instCmd)
	rootCmd.AddCommand(extractKernelCmd)
}

/* ======================= pullvm (Refactored to Steps) ======================= */

type pullVMApp struct {
	upstreamArg  string
	sb           rtsst.ImageSB
	imageDir     string
	srcImage     string
	rawImageFile string
	methods      pullVMMethods
	steps        *sst.StepsRunState
}

type pullVMMethods struct {
	SetupEnvironment func(*pullVMApp) error
	DownloadImage    func(*pullVMApp) error
	ConvertImage     func(*pullVMApp) error
	AnalyseImage     func(*pullVMApp) error
	EnlargeRoot 	 func(*pullVMApp) error
	ExtractArtifacts func(*pullVMApp) error
	SaveManifest     func(*pullVMApp) error
}

var defaultPullVMOps = pullVMMethods{
	SetupEnvironment: pullVMSetupEnv,
	DownloadImage:    pullVMDownload,
	ConvertImage:     pullVMConvert,
	AnalyseImage:     pullVMAnalyse,
	EnlargeRoot:      pullVMEnlargeRoot,
	ExtractArtifacts: pullVMExtract,
	SaveManifest:     pullVMSave,
}

var extractKernelCmd = &cobra.Command{
	Use:   "extract_kernel <in> <out>",
	Short: "Extracts an uncompressed raw kernel (vmlinux) from a compressed kernel image",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		inPath := args[0]
		outPath := args[1]
		
		fmt.Printf("--> Extracting raw kernel from %s to %s\n", inPath, outPath)
		if err := rtsst.ExtractRawKernel(inPath, outPath); err != nil { // Adjust package prefix as needed
			return fmt.Errorf("failed to extract kernel: %w", err)
		}
		return nil
	},
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

func runPullVMSteps(app *pullVMApp) error {
	app.steps = sst.StepsInit("mkimage.pullvm")
	app.steps.StepsAdd("setup environment", func(c *sst.StepsContext, _ sst.StepState) error { return app.methods.SetupEnvironment(app) })
	app.steps.StepsAdd("download image", func(c *sst.StepsContext, _ sst.StepState) error { return app.methods.DownloadImage(app) })
	app.steps.StepsAdd("convert image", func(c *sst.StepsContext, _ sst.StepState) error { return app.methods.ConvertImage(app) })
	app.steps.StepsAdd("analyse partition table", func(c *sst.StepsContext, _ sst.StepState) error { return app.methods.AnalyseImage(app) })
	app.steps.StepsAdd("enlarge root filesystem", func(c *sst.StepsContext, _ sst.StepState) error { return app.methods.EnlargeRoot(app) })
	app.steps.StepsAdd("analyse partition table again", func(c *sst.StepsContext, _ sst.StepState) error { return app.methods.AnalyseImage(app) })
	app.steps.StepsAdd("extract kernel/initrd", func(c *sst.StepsContext, _ sst.StepState) error { return app.methods.ExtractArtifacts(app) })
	app.steps.StepsAdd("save manifest", func(c *sst.StepsContext, _ sst.StepState) error { return app.methods.SaveManifest(app) })
	return sst.StepsRun(app.steps)
}

func pullVMSetupEnv(app *pullVMApp) error {
	app.sb.Ctx = context.Background()
	// FIX: app.sb.Log is now a pointer directly returned by Subsystem()
	app.sb.Log = sst.LogxRoot().Subsystem("mkimage.pullvm")
	app.sb.YamlObject = &mkimagepb.ImageYAML{}

	if flagRoot != "" {
		app.sb.SstRuntimeDir = flagRoot
	} else {
		app.sb.SstRuntimeDir = rtsst.RuntimeDir()
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
	if err != nil { return err }

	app.rawImageFile = imageName + ".raw"
	rawImagePath := filepath.Join(app.imageDir, app.rawImageFile)

	isQcow, err := rtsst.IsQcow2(app.srcImage)
	if err != nil { return fmt.Errorf("failed to detect image format: %w", err) }

	if isQcow {
		app.sb.Log.Info(app.sb.Ctx, "image.convert", "from", app.srcImage, "to", rawImagePath, "format", "raw", "detected_type", "qcow2")
		// FIX: app.sb.Log is passed directly as it's already a pointer
		if _, err := sst.ExecShell(app.sb.Ctx, app.sb.Log, "qemu-img", "convert", "-O", "raw", app.srcImage, rawImagePath); err != nil {
			return fmt.Errorf("failed to convert qcow2 to raw: %w", err)
		}
	} else {
		if app.srcImage != rawImagePath {
			if err := os.Rename(app.srcImage, rawImagePath); err != nil { return err }
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

func pullVMEnlargeRoot(app *pullVMApp) error {
	ctx := app.sb.Ctx
	// FIX: Use pointer directly
	logx := app.sb.Log
	imagePath := filepath.Join(app.imageDir, app.rawImageFile)
	targetSize := int64(10737418240)
	
	y := app.sb.YamlObject
	if !y.RootfsIsLast {
		logx.Info(ctx, "image.resize.skip", "reason", "rootfs not last partition")
		return nil
	}

	rootPartNo := partitionIndex(y.RootfsDevice)
	if rootPartNo == "" { return fmt.Errorf("cannot extract partition number from %q", y.RootfsDevice) }

	fi, err := os.Stat(imagePath)
	if err != nil { return fmt.Errorf("stat image: %w", err) }
	if fi.Size() >= targetSize {
		logx.Info(ctx, "image.resize.skip", "file", imagePath, "current_bytes", fi.Size(), "target_bytes", targetSize)
		return nil
	}

	logx.Info(ctx, "image.resize.start", "file", imagePath, "target_bytes", targetSize, "root_partition", rootPartNo)

	_, err = sst.ExecShell(ctx, logx, "sh", "-euxc", fmt.Sprintf(`
image=%q
partno=%q
target=%d

fsck_ok() {
  e2fsck -f -p "$1"
  rc=$?
  if [ $rc -le 1 ]; then return 0; fi
  return $rc
}

loopdev=$(losetup -f -P --show "$image")
trap 'losetup -d "$loopdev"' EXIT
partdev="${loopdev}p${partno}"
fsck_ok "$partdev"
truncate -s "$target" "$image"
losetup -c "$loopdev"
growpart "$loopdev" "$partno"
partprobe "$loopdev" || true
udevadm settle || true
resize2fs "$partdev"
fsck_ok "$partdev"
`, imagePath, rootPartNo, targetSize))

	if err != nil { return fmt.Errorf("offline root resize failed: %w", err) }
	logx.Info(ctx, "image.resize.done", "file", imagePath)
	return nil
}

func partitionIndex(dev string) string {
	const prefix = "/dev/vda"
	if !strings.HasPrefix(dev, prefix) { return "" }
	return strings.TrimPrefix(dev, prefix)
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
	// FIX: Use pointer directly
	if diags := yo.Save(app.sb.Ctx, app.sb.Log); diags != nil { return diags }
	if flagVerbose { app.sb.Log.Info(app.sb.Ctx, "image.created", "name", app.sb.YamlObject.Name, "dir", app.imageDir) }
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

	runtimeRoot := flagRoot
	if runtimeRoot == "" {
		runtimeRoot = rtsst.RuntimeDir()
	}

	var imageDir string
	if info, err := os.Stat(image); err == nil && info.IsDir() {
		imageDir = image
	} else {
		imageDir = filepath.Join(runtimeRoot, "images", image)
	}

	if _, err := os.Stat(imageDir); os.IsNotExist(err) {
		return fmt.Errorf("image directory not found at: %s", imageDir)
	}

	instDir := rtsst.InstanceDir(name)

	if _, err := os.Stat(instDir); err == nil && !flagForce {
		return fmt.Errorf("instance %s exists; use --force to overwrite", name)
	}
	_ = os.RemoveAll(instDir)
	if err := os.MkdirAll(instDir, 0755); err != nil { return err }

	imageYaml := &mkimagepb.ImageYAML{}
	imgYo := &sst.YamlObject{
		Path:   filepath.Join(imageDir, "image.yaml"),
		Schema: "sst.yaml.mkimage.v1.ImageYAML",
		Kind:   "Image",
		Msg:    imageYaml,
	}
	if diags := imgYo.Load(ctx, log); diags != nil { return diags }

	instanceYaml := &runctlpb.Instance{}
	instYo := &sst.YamlObject{
		Path:   filepath.Join(instDir, "instance.yaml"),
		Schema: "sst.yaml.runctl.v1.Instance",
		Kind:   "Instance",
		Msg:    instanceYaml,
	}

	src := filepath.Join(imageDir, imageYaml.RawImage)
	dst := filepath.Join(instDir, imageYaml.RawImage)
	
	log.Info(ctx, "copying root image", "src", src, "dst", dst)
	if err := rtsst.CopySparse(src, dst); err != nil { return err }
	
	if !flagEfi {
		if imageYaml.Kernel == "" {
			return fmt.Errorf("STRICT: No kernel was extracted from the base image '%s'. A kernel must be installed in the Dockerfile to use the direct 'linux' bootloader (required for early console). If you intend to use an embedded bootloader like GRUB, use the --efi flag", image)
		}
		if err := rtsst.CopyFile(filepath.Join(imageDir, imageYaml.Kernel), filepath.Join(instDir, imageYaml.Kernel)); err != nil {
			return err
		}
		if imageYaml.Initrd != "" {
			if err := rtsst.CopyFile(filepath.Join(imageDir, imageYaml.Initrd), filepath.Join(instDir, imageYaml.Initrd)); err != nil {
				return err
			}
		}
	}
    
	mac, err := rtsst.GenerateMAC()
	if err != nil { return err }
	u, err := user.Current()
	if err != nil { return err }

	size, err := rtsst.ParseSize(flagSize)
	if err != nil {
		return fmt.Errorf("invalid size %q: %w", flagSize, err)
	}

	f, err := os.Create(filepath.Join(instDir, "data.img"))
	if err != nil { return err }
	defer f.Close()
	if err := f.Truncate(size); err != nil { return err }

	instanceYaml.ApiVersion = "sst.io/v1"
	instanceYaml.Kind = "Instance"
	instanceYaml.Name = name
	instanceYaml.Hostname = name
	instanceYaml.Gui = false
	instanceYaml.CpuCount = "4"
	instanceYaml.MemorySize = "8096"
	instanceYaml.MemorySizeSuffix = "MiB"
	instanceYaml.Balloon = false
	
	if !flagEfi {
		instanceYaml.Bootloader = "linux"
		instanceYaml.Kernel = imageYaml.Kernel
		instanceYaml.Initrd = imageYaml.Initrd
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
	} else {
		instanceYaml.Bootloader = "efi"
	}
	
	instanceYaml.EfiVars = "efi-vars"
	instanceYaml.Rwdisk = []string{
		imageYaml.RawImage,
		"data.img",
	}
	instanceYaml.Disk = []string{"cidata.iso"}
	instanceYaml.SharedDir = []string{
		fmt.Sprintf("%s:%s", u.HomeDir, u.HomeDir),
	}
	instanceYaml.Network = []string{fmt.Sprintf("%s@nat", mac)}

	if diags := instYo.Save(ctx, log); diags != nil { return diags }
	if err := rtsst.WriteCIData(ctx, log, instDir, instanceYaml); err != nil {
		return fmt.Errorf("failed to write cidata: %w", err)
	} else {
		log.Info(ctx, "updated cidata.iso", "instDir", instDir)
		if err := rtsst.UpdateCIDATAISO(instDir); err != nil {
			return fmt.Errorf("update cidata.iso: %w", err)
		}
	}

	return nil
}
