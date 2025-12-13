package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/swiss-software-tool/runtime-smart/go/internal/sst"
	"gopkg.in/yaml.v3"
)

// NOTE: The parent grouping commands like 'storageCmd', 'deviceCmd', etc., have been removed.
// All storage commands are now attached directly to the rootCmd for a flat structure.

// composeStoragePlanCmd implements the "planning" phase.
var composeStoragePlanCmd = &cobra.Command{
	Use:   "compose-storageplan",
	Short: "Generates a declarative storage-layout.yaml from high-level intent.",
	Long: `A planning command that takes a hardware probe file and user requirements
to generate a detailed, declarative storage-layout.yaml manifest.
This is the first step in the 'plan -> execute' workflow.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		log := core.Logx().Subsystem("compose-storageplan")
		nodeFile, _ := cmd.Flags().GetString("node")
		osSize, _ := cmd.Flags().GetString("ossize")
		remainderSpec, _ := cmd.Flags().GetString("remainder")
		filesetNames, _ := cmd.Flags().GetStringSlice("filesets")
		outputFile, _ := cmd.Flags().GetString("output")

		log.Info(context.Background(), "Generating storage plan from intent...",
			"hardware_inventory", nodeFile,
			"os_partition_size", osSize,
			"remainder_action", remainderSpec,
			"zfs_filesets", filesetNames)

		var probe sst.HardwareProbe
		if err := sst.LoadManifest(nodeFile, &probe); err != nil {
			return err
		}
		if len(probe.Spec.BlockDevices) == 0 {
			return fmt.Errorf("no block devices found in probe file %s", nodeFile)
		}

		layout := sst.StorageLayout{
			APIVersion: "sst.io/v1",
			Kind:       "StorageLayout",
		}

		parts := strings.Split(remainderSpec, ",")
		if len(parts) != 2 {
			return fmt.Errorf("--remainder format must be 'pool-name,raid-level', e.g., 'data-pool,mirror'")
		}
		poolName, raidLevel := parts[0], parts[1]

		numDisksRequired := 1
		if raidLevel == "mirror" {
			numDisksRequired = 2
		}

		if len(probe.Spec.BlockDevices) < numDisksRequired {
			return fmt.Errorf("%s requires %d block devices, but probe only found %d", raidLevel, numDisksRequired, len(probe.Spec.BlockDevices))
		}

		var deviceRefs []string
		for i := 0; i < numDisksRequired; i++ {
			pdev := probe.Spec.BlockDevices[i]
			dev := sst.Device{
				Name:      fmt.Sprintf("disk%d", i),
				Kind:      "block-device",
				DeviceRef: pdev.HostPath,
				Status:    sst.ResourceStatus{State: "plan-recorded"},
			}
			layout.Spec.Devices = append(layout.Spec.Devices, dev)
			deviceRefs = append(deviceRefs, dev.Name)
		}

		pool := sst.Pool{
			Name:      poolName,
			RaidLevel: raidLevel,
			Devices:   deviceRefs,
			Status:    sst.ResourceStatus{State: "plan-recorded"},
		}
		layout.Spec.Pools = append(layout.Spec.Pools, pool)

		for _, fsName := range filesetNames {
			fs := sst.Filesystem{
				Name:       fmt.Sprintf("%s-data", fsName),
				Kind:       "zfs-fileset",
				PoolRef:    pool.Name,
				Mountpoint: fmt.Sprintf("/mnt/data/%s", fsName),
				Status:     sst.ResourceStatus{State: "plan-recorded"},
			}
			layout.Spec.Filesystems = append(layout.Spec.Filesystems, fs)
		}

		out, err := yaml.Marshal(&layout)
		if err != nil {
			return fmt.Errorf("failed to marshal storage layout: %w", err)
		}
		if err := os.WriteFile(outputFile, out, 0644); err != nil {
			return fmt.Errorf("failed to write %s: %w", outputFile, err)
		}

		fmt.Printf("✅ Successfully generated '%s' with all resources in 'plan-recorded' state.\n", outputFile)
		fmt.Println("   Review the file and change the state to 'ready-for-implementation' to approve for execution.")
		return nil
	},
}

// --- Granular Execution Commands ---

var (
	// Device Commands
	mkdevCmd      = &cobra.Command{Use: "mkdev", Short: "Idempotently create/provision devices from a manifest.", RunE: placeholderRunE}
	destroydevCmd = &cobra.Command{Use: "destroydev", Short: "Idempotently destroy file-based devices from a manifest.", RunE: placeholderRunE}
	ejectdevCmd   = &cobra.Command{Use: "ejectdev", Short: "Idempotently detach loopback devices from a manifest.", RunE: placeholderRunE}

	// Pool Commands
	mkpoolCmd      = &cobra.Command{Use: "mkpool", Short: "Idempotently create storage pools from a manifest.", RunE: placeholderRunE}
	destroypoolCmd = &cobra.Command{Use: "destroypool", Short: "Idempotently destroy storage pools from a manifest.", RunE: placeholderRunE}

	// Filesystem Commands
	formatCmd   = &cobra.Command{Use: "format", Short: "Idempotently format a device with a filesystem.", RunE: placeholderRunE}
	mountCmd    = &cobra.Command{Use: "mount", Short: "Idempotently mount a filesystem.", RunE: reconcileMount}
	umountCmd   = &cobra.Command{Use: "umount", Short: "Idempotently unmount a filesystem.", RunE: placeholderRunE}
	// destroyfs is ambiguous, rmdev or destroypool should be used.

	// ZFS Fileset Commands
	mkfsetCmd      = &cobra.Command{Use: "mkfset", Short: "Idempotently create a ZFS fileset.", RunE: placeholderRunE}
	destroyfsetCmd = &cobra.Command{Use: "destroyfset", Short: "Idempotently destroy a ZFS fileset.", RunE: placeholderRunE}
)

func reconcileMount(cmd *cobra.Command, args []string) error {
	log := core.Logx().Subsystem("mount")
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
				mountOptions := ""
				if len(fs.MountOptions) > 0 {
					mountOptions = "-o " + strings.Join(fs.MountOptions, ",")
				}
				return runner.Command("mount", mountOptions, fs.DeviceRef, fs.Mountpoint)(ctx)
			},
			Interpret: func(result *sst.Result) ([]*sst.Context, error) {
				if result.Error != nil {
					if strings.Contains(result.Error.Error(), "already mounted") {
						log.Info(ctx, "Filesystem already mounted", "name", fs.Name)
					} else {
						return nil, result.Error
					}
				}
				if len(fs.Subdirectories) > 0 {
					return []*sst.Context{
						{
							Description: "Create service subdirectories",
							Action: func(ctx context.Context) (interface{}, error) {
								for _, dir := range fs.Subdirectories {
									fullPath := filepath.Join(fs.Mountpoint, dir)
									log.Info(ctx, "Ensuring subdirectory exists", "path", fullPath)
									action := runner.Command("mkdir", "-p", fullPath)
									if _, err := action(ctx); err != nil {
										return nil, fmt.Errorf("failed to create subdirectory %s: %w", fullPath, err)
									}
								}
								return nil, nil
							},
						},
					}, nil
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
			layout.Spec.Filesystems[i].Status.Message = "Successfully mounted and configured."
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
	// Add all commands directly to the root command.
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

	// --- Planner Flags ---
	composeStoragePlanCmd.Flags().String("node", "hardware.yaml", "Path to hardware probe YAML file.")
	composeStoragePlanCmd.Flags().String("ossize", "30G", "Size to reserve for the OS partition.")
	composeStoragePlanCmd.Flags().String("remainder", "", "Action for remaining disk space (e.g., 'data-pool,mirror').")
	composeStoragePlanCmd.Flags().StringSlice("filesets", []string{}, "Comma-separated list of ZFS filesets to create.")
	composeStoragePlanCmd.Flags().StringP("output", "o", "storage-layout.yaml", "Output path for the generated manifest.")

	// --- Reconciler Flags ---
	// Add a persistent --manifest flag to all low-level execution commands.
	for _, cmd := range []*cobra.Command{
		mkdevCmd, destroydevCmd, ejectdevCmd, mkpoolCmd, destroypoolCmd,
		formatCmd, mountCmd, umountCmd, mkfsetCmd, destroyfsetCmd,
	} {
		cmd.Flags().String("manifest", "storage-layout.yaml", "Path to the storage layout manifest.")
	}
}
