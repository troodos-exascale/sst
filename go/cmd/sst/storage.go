package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/swiss-software-tool/runtime-smart/go/internal/sst"
	"gopkg.in/yaml.v3"
)

//
// ======================================================
// Planner domain model (compose-storageplan)
// ======================================================

type storagePlanApp struct {
	// inputs
	nodeFile      string
	osSize        string
	remainderSpec string
	filesetNames  []string
	outputFile    string

	// derived state
	probe  sst.HardwareProbe
	layout sst.StorageLayout

	// behavior + framework
	methods storagePlanMethods
	steps   *sst.StepsRunState
}

type storagePlanMethods struct {
	LoadProbe      func(*storagePlanApp) error
	ValidateInputs func(*storagePlanApp) error
	BuildLayout    func(*storagePlanApp) error
	WriteOutput    func(*storagePlanApp) error
}

func newStoragePlanMethods() storagePlanMethods {
	m := storagePlanMethods{
		LoadProbe:      storagePlanLoadProbe,
		ValidateInputs: storagePlanValidate,
		BuildLayout:    storagePlanBuildLayout,
		WriteOutput:    storagePlanWriteOutput,
	}
	m.mustValidate()
	return m
}

func (m storagePlanMethods) mustValidate() {
	if m.LoadProbe == nil ||
		m.ValidateInputs == nil ||
		m.BuildLayout == nil ||
		m.WriteOutput == nil {
		panic("storagePlanMethods: missing required method")
	}
}

//
// ======================================================
// compose-storageplan command (refactored)
// ======================================================

var composeStoragePlanCmd = &cobra.Command{
	Use:   "compose-storageplan",
	Short: "Generates a declarative storage-layout.yaml from high-level intent.",
	Long: `A planning command that takes a hardware probe file and user requirements
to generate a detailed, declarative storage-layout.yaml manifest.`,
	RunE: func(cmd *cobra.Command, args []string) error {

		nodeFile, _ := cmd.Flags().GetString("node")
		osSize, _ := cmd.Flags().GetString("ossize")
		remainderSpec, _ := cmd.Flags().GetString("remainder")
		filesetNames, _ := cmd.Flags().GetStringSlice("filesets")
		outputFile, _ := cmd.Flags().GetString("output")

		app := &storagePlanApp{
			nodeFile:      nodeFile,
			osSize:        osSize,
			remainderSpec: remainderSpec,
			filesetNames:  filesetNames,
			outputFile:    outputFile,
			methods:       newStoragePlanMethods(),
		}

		app.steps = sst.StepsInit("compose-storageplan")

		app.steps.StepsAdd("load probe", func(ctx *sst.StepsContext, _ sst.StepState) error {
			return app.methods.LoadProbe(app)
		})
		app.steps.StepsAdd("validate inputs", func(ctx *sst.StepsContext, _ sst.StepState) error {
			return app.methods.ValidateInputs(app)
		})
		app.steps.StepsAdd("build layout", func(ctx *sst.StepsContext, _ sst.StepState) error {
			return app.methods.BuildLayout(app)
		})
		app.steps.StepsAdd("write output", func(ctx *sst.StepsContext, _ sst.StepState) error {
			return app.methods.WriteOutput(app)
		})

		return sst.StepsRun(app.steps)
	},
}

//
// ======================================================
// Planner implementations
// ======================================================

func storagePlanLoadProbe(app *storagePlanApp) error {
	if err := sst.LoadManifest(app.nodeFile, &app.probe); err != nil {
		return err
	}
	if len(app.probe.Spec.BlockDevices) == 0 {
		return fmt.Errorf("no block devices found in probe file %s", app.nodeFile)
	}
	return nil
}

func storagePlanValidate(app *storagePlanApp) error {
	parts := strings.Split(app.remainderSpec, ",")
	if len(parts) != 2 {
		return fmt.Errorf("--remainder must be 'pool-name,raid-level'")
	}
	return nil
}

func storagePlanBuildLayout(app *storagePlanApp) error {
	app.layout = sst.StorageLayout{
		APIVersion: "sst.io/v1",
		Kind:       "StorageLayout",
	}

	parts := strings.Split(app.remainderSpec, ",")
	poolName, raidLevel := parts[0], parts[1]

	numDisks := 1
	if raidLevel == "mirror" {
		numDisks = 2
	}

	if len(app.probe.Spec.BlockDevices) < numDisks {
		return fmt.Errorf("%s requires %d block devices, probe has %d",
			raidLevel, numDisks, len(app.probe.Spec.BlockDevices))
	}

	var deviceRefs []string
	for i := 0; i < numDisks; i++ {
		pdev := app.probe.Spec.BlockDevices[i]
		dev := sst.Device{
			Name:      fmt.Sprintf("disk%d", i),
			Kind:      "block-device",
			DeviceRef: pdev.HostPath,
			Status:    sst.ResourceStatus{State: "plan-recorded"},
		}
		app.layout.Spec.Devices = append(app.layout.Spec.Devices, dev)
		deviceRefs = append(deviceRefs, dev.Name)
	}

	pool := sst.Pool{
		Name:      poolName,
		RaidLevel: raidLevel,
		Devices:   deviceRefs,
		Status:    sst.ResourceStatus{State: "plan-recorded"},
	}
	app.layout.Spec.Pools = append(app.layout.Spec.Pools, pool)

	for _, fsName := range app.filesetNames {
		fs := sst.Filesystem{
			Name:       fmt.Sprintf("%s-data", fsName),
			Kind:       "zfs-fileset",
			PoolRef:    pool.Name,
			Mountpoint: fmt.Sprintf("/mnt/data/%s", fsName),
			Status:     sst.ResourceStatus{State: "plan-recorded"},
		}
		app.layout.Spec.Filesystems = append(app.layout.Spec.Filesystems, fs)
	}
	return nil
}

func storagePlanWriteOutput(app *storagePlanApp) error {
	out, err := yaml.Marshal(&app.layout)
	if err != nil {
		return err
	}
	if err := os.WriteFile(app.outputFile, out, 0644); err != nil {
		return err
	}
	fmt.Printf("✅ Successfully generated '%s'\n", app.outputFile)
	fmt.Println("   Review the file and change state to 'ready-for-implementation'.")
	return nil
}

//
// ======================================================
// Existing execution commands (unchanged)
// ======================================================

var (
	mkdevCmd      = &cobra.Command{Use: "mkdev", Short: "Idempotently create/provision devices from a manifest.", RunE: placeholderRunE}
	destroydevCmd = &cobra.Command{Use: "destroydev", Short: "Idempotently destroy file-based devices from a manifest.", RunE: placeholderRunE}
	ejectdevCmd   = &cobra.Command{Use: "ejectdev", Short: "Idempotently detach loopback devices from a manifest.", RunE: placeholderRunE}

	mkpoolCmd      = &cobra.Command{Use: "mkpool", Short: "Idempotently create storage pools from a manifest.", RunE: placeholderRunE}
	destroypoolCmd = &cobra.Command{Use: "destroypool", Short: "Idempotently destroy storage pools from a manifest.", RunE: placeholderRunE}

	formatCmd = &cobra.Command{Use: "format", Short: "Idempotently format a device with a filesystem.", RunE: placeholderRunE}
	mountCmd  = &cobra.Command{Use: "mount", Short: "Idempotently mount a filesystem.", RunE: reconcileMount}
	umountCmd = &cobra.Command{Use: "umount", Short: "Idempotently unmount a filesystem.", RunE: placeholderRunE}

	mkfsetCmd      = &cobra.Command{Use: "mkfset", Short: "Idempotently create a ZFS fileset.", RunE: placeholderRunE}
	destroyfsetCmd = &cobra.Command{Use: "destroyfset", Short: "Idempotently destroy a ZFS fileset.", RunE: placeholderRunE}
)

func reconcileMount(cmd *cobra.Command, args []string) error {
	log := sst.LogxRoot().Subsystem("mount")
	ctx := context.Background()
	manifestFile, _ := cmd.Flags().GetString("manifest")

	var layout sst.StorageLayout
	if err := sst.LoadManifest(manifestFile, &layout); err != nil {
		return err
	}

	manifestModified := false
	for i, fs := range layout.Spec.Filesystems {
		if fs.Status.State != "ready-for-implementation" {
			continue
		}

		log.Info(ctx, "Reconciling filesystem mount", "name", fs.Name)
		runner := &sst.Runner{Log: log}

		mountCtx := &sst.Context{
			Description: fmt.Sprintf("Mount %s", fs.Name),
			Action: func(ctx context.Context) (interface{}, error) {
				if fs.Kind == "zfs-fileset" {
					datasetName := fmt.Sprintf("%s/%s", fs.PoolRef, fs.Name)
					return runner.Command("zfs", "mount", datasetName)(ctx)
				}
				opts := ""
				if len(fs.MountOptions) > 0 {
					opts = "-o " + strings.Join(fs.MountOptions, ",")
				}
				return runner.Command("mount", opts, fs.DeviceRef, fs.Mountpoint)(ctx)
			},
			Interpret: func(result *sst.Result) ([]*sst.Context, error) {
				if result.Error != nil {
					if strings.Contains(result.Error.Error(), "already mounted") {
						log.Info(ctx, "Filesystem already mounted", "name", fs.Name)
					} else {
						return nil, result.Error
					}
				}
				return nil, nil
			},
			InterpretOnError: true,
		}

		if err := runner.Run(ctx, mountCtx); err != nil {
			layout.Spec.Filesystems[i].Status.State = "error"
			layout.Spec.Filesystems[i].Status.Message = err.Error()
		} else {
			layout.Spec.Filesystems[i].Status.State = "deployed"
			layout.Spec.Filesystems[i].Status.Message = "Successfully mounted."
		}
		manifestModified = true
	}

	if manifestModified {
		out, err := yaml.Marshal(&layout)
		if err != nil {
			return err
		}
		return os.WriteFile(manifestFile, out, 0644)
	}
	return nil
}

func placeholderRunE(cmd *cobra.Command, args []string) error {
	fmt.Printf("'%s' command is not yet implemented.\n", cmd.CommandPath())
	return nil
}

func init() {
	rootCmd.AddCommand(
		composeStoragePlanCmd,
		mkdevCmd,
		destroydevCmd,
		ejectdevCmd,
		mkpoolCmd,
		destroypoolCmd,
		formatCmd,
		mountCmd,
		umountCmd,
		mkfsetCmd,
		destroyfsetCmd,
	)

	composeStoragePlanCmd.Flags().String("node", "hardware.yaml", "Path to hardware probe YAML file.")
	composeStoragePlanCmd.Flags().String("ossize", "30G", "Size to reserve for OS.")
	composeStoragePlanCmd.Flags().String("remainder", "", "Remaining space (pool,raid).")
	composeStoragePlanCmd.Flags().StringSlice("filesets", []string{}, "ZFS filesets.")
	composeStoragePlanCmd.Flags().StringP("output", "o", "storage-layout.yaml", "Output path.")

	for _, cmd := range []*cobra.Command{
		mkdevCmd, destroydevCmd, ejectdevCmd, mkpoolCmd,
		destroypoolCmd, formatCmd, mountCmd, umountCmd,
		mkfsetCmd, destroyfsetCmd,
	} {
		cmd.Flags().String("manifest", "storage-layout.yaml", "Path to storage layout manifest.")
	}
}
