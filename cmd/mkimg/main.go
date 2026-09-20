package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"
	rtsst "github.com/troodos-exascale/sst/internal/rtsst"
	"github.com/troodos-exascale/sst/internal/sst"
	"github.com/troodos-exascale/sst/proto/yaml/runctl/v1"
)

var (
	flagRoot    string
	flagForce   bool
	flagVerbose bool
	flagSize    string
	flagEfi     bool
)

var rootCmd = &cobra.Command{
	Use:           "mkimg",
	Short:         "Create SST VM image directories from upstream sources",
	SilenceUsage:  true,
	SilenceErrors: true,
}

func delegateToLinuxVM() {
	vmBuildHost := "ub"

	// 1. If no arguments are provided, return to let Cobra show the main help menu.
	if len(os.Args) == 1 {
		return
	}

	// 2. If ANY argument is a help flag, return to let Cobra handle it.
	for _, arg := range os.Args {
		if arg == "help" || arg == "-h" || arg == "--help" {
			return
		}
	}

	// 3. Skip delegation for local management commands and completions
	arg1 := os.Args[1]
	if arg1 == "completion" || strings.HasPrefix(arg1, "__complete") ||
		arg1 == "mv-image" || arg1 == "mv-instance" || arg1 == "rm-image" || arg1 == "rm-instance" || arg1 == "catalog" {
		return
	}

	hostContextDir := "."
	instanceName := ""

	if len(os.Args) >= 3 {
		for i := len(os.Args) - 1; i >= 2; i-- {
			if info, err := os.Stat(os.Args[i]); err == nil && info.IsDir() {
				hostContextDir = os.Args[i]
				break
			}
		}
		if os.Args[1] == "instance" {
			var positionals []string
			for i := 2; i < len(os.Args); i++ {
				if strings.HasPrefix(os.Args[i], "-") {
					if os.Args[i] == "-b" || os.Args[i] == "--builddir" || os.Args[i] == "-d" || os.Args[i] == "--dir" || os.Args[i] == "-s" || os.Args[i] == "--size" {
						i++
					}
					continue
				}
				positionals = append(positionals, os.Args[i])
			}
			if len(positionals) >= 2 {
				instanceName = positionals[1]
			}
		}
	}

	fmt.Printf("--> macOS detected. Delegating command to Linux VM '%s'...\n", vmBuildHost)

	hostRunctlPath, err := exec.LookPath("runctl")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: runctl not found in PATH for delegation\n")
		os.Exit(1)
	}

	var vmCmd []string
	if hostCwd, err := os.Getwd(); err == nil {
		vmCmd = append(vmCmd, "cd", fmt.Sprintf("'%s'", hostCwd), "&&")
	}
	
	vmCmd = append(vmCmd, "sudo")
	logFmt := os.Getenv("SST_LOG_FORMAT")
	if logFmt == "" {
		logFmt = "text"
	}
	vmCmd = append(vmCmd, fmt.Sprintf("SST_LOG_FORMAT='%s'", logFmt))
	vmCmd = append(vmCmd, fmt.Sprintf("HOME='%s'", rtsst.Home()))
	
	if pubKey, err := rtsst.GetLocalPublicKey(); err == nil && pubKey != "" {
		vmCmd = append(vmCmd, fmt.Sprintf("SST_HOST_PUBKEY='%s'", strings.TrimSpace(pubKey)))
	}

	vmCmd = append(vmCmd, "mkimg")
	vmCmd = append(vmCmd, os.Args[1:]...)

	hostCmd := exec.Command(hostRunctlPath, "ssh", vmBuildHost, strings.Join(vmCmd, " "))
	hostCmd.Stdin, hostCmd.Stdout, hostCmd.Stderr = os.Stdin, os.Stdout, os.Stderr

	if err := hostCmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "delegation failed: %v\n", err)
		os.Exit(1)
	}

	if os.Args[1] == "build" || os.Args[1] == "instance" {
		if err := ProcessPurificationQueue(hostContextDir); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: host purification failed: %v\n", err)
		}
	}

	if instanceName != "" {
		targetDir := filepath.Join(rtsst.RuntimeDir(), "instances", instanceName)

		inst := &runctlpb.Instance{}
		yo := &sst.YamlObject{
			Path:   filepath.Join(targetDir, "instance.yaml"),
			Schema: "sst.yaml.runctl.v1.Instance",
			Kind:   "Instance",
			Msg:    inst,
		}
		
		if diags := yo.Load(context.Background(), sst.LogxRoot()); diags == nil || !diags.HasAny() {
			u, _ := user.Current()
			if err := rtsst.WriteCIData(context.Background(), sst.LogxRoot(), targetDir, inst, u.Username); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to generate cidata natively: %v\n", err)
			}
			_ = rtsst.EnsureCIDataISO(context.Background(), sst.LogxRoot(), targetDir)
		} else {
			fmt.Fprintf(os.Stderr, "Warning: could not load instance.yaml from %s: %v\n", targetDir, diags.Error())
		}
	}

	os.Exit(0)
}

func main() {
	if runtime.GOOS == "darwin" {
		delegateToLinuxVM()
	}
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "mkimage error: %v\n", err)
		os.Exit(1)
	}
}

func init() {
	rootCmd.PersistentFlags().StringVarP(&flagRoot, "dir", "d", "", "Runtime root directory (workspace output)")
	rootCmd.PersistentFlags().BoolVarP(&flagVerbose, "verbose", "v", false, "Verbose logging")
	rootCmd.PersistentFlags().BoolVar(&flagForce, "force", false, "Overwrite existing image directory")

	rootCmd.AddCommand(pullvmCmd())
	rootCmd.AddCommand(clearCacheCmd)

	instCmd := instanceCmd()
	instCmd.Flags().StringVarP(&flagSize, "size", "s", "20G", "Target size for the root disk")
	instCmd.Flags().BoolVar(&flagEfi, "efi", false, "Use EFI bootloader")
	rootCmd.AddCommand(instCmd)

	rootCmd.AddCommand(extractKernelCmd)
}

var extractKernelCmd = &cobra.Command{
	Use:   "extract_kernel <in> <out>",
	Short: "Extracts an uncompressed raw kernel (vmlinux)",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Printf("--> Extracting raw kernel from %s to %s\n", args[0], args[1])
		return rtsst.ExtractRawKernel(args[0], args[1])
	},
}

func pullvmCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "pullvm <upstream-url-or-local-path>",
		Short: "Pull an upstream VM image or local archive and create an SST image directory",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			runtimeDir := flagRoot
			if runtimeDir == "" {
				runtimeDir = rtsst.RuntimeDir()
			}

			source := args[0]
			if !strings.HasPrefix(source, "http://") && !strings.HasPrefix(source, "https://") {
				if absSource, err := filepath.Abs(source); err == nil {
					source = absSource
				}
			}

			base := filepath.Base(source)
			name := strings.TrimSuffix(base, filepath.Ext(base))
			if strings.HasSuffix(base, ".tar.gz") {
				name = strings.TrimSuffix(name, ".tar")
			}

			bArgs := map[string]string{
				"URL":       source,
				"PULL_NAME": name,
			}
			return RunBuildEngine(cmd.Context(), "pullpullvm.dockerfile", ".", flagBuildDir, runtimeDir, flagForce, bArgs)
		},
	}
}

var clearCacheCmd = &cobra.Command{
	Use:   "clear-cache [image-name]",
	Short: "Clear persistent caches",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if runtime.GOOS == "darwin" {
			fmt.Println("--> macOS detected. Transparently delegating clear-cache to Linux VM...")
			delegateToLinuxVM()
			return nil
		}

		runtimeDir := flagRoot
		if runtimeDir == "" {
			runtimeDir = "/runtimes"
		}

		var buildIDs []string
		if len(args) == 1 {
			buildIDs = append(buildIDs, args[0])
		} else {
			buildsDir := filepath.Join(runtimeDir, "builds")
			entries, err := os.ReadDir(buildsDir)
			if err == nil {
				for _, e := range entries {
					if e.IsDir() && strings.HasPrefix(e.Name(), "build-") {
						buildIDs = append(buildIDs, strings.TrimPrefix(e.Name(), "build-"))
					}
				}
			}
		}

		if len(buildIDs) == 0 {
			fmt.Println("--> No caches found to clear.")
			return nil
		}

		for _, id := range buildIDs {
			fmt.Printf("--> Clearing caches for build: %s\n", id)
			mgr := rtsst.NewDatasetManager(runtimeDir, id, "cache-clear")
			if err := mgr.ClearCache(); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to clear cache for %s: %v\n", id, err)
			}
		}

		fmt.Println("--> Caches cleared successfully.")
		return nil
	},
}

func instanceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "instance <image> <name>",
		Short: "Create an instance directory from an image",
		Args:  cobra.ExactArgs(2),
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			if len(args) != 0 {
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
			names, err := rtsst.ListCatalogNames("images")
			if err != nil {
				return nil, cobra.ShellCompDirectiveError
			}
			var matches []string
			for _, name := range names {
				if strings.HasPrefix(name, toComplete) {
					matches = append(matches, name)
				}
			}
			return matches, cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			runtimeDir := flagRoot
			if runtimeDir == "" {
				runtimeDir = rtsst.RuntimeDir()
			}

			baseImage := args[0]

			dockerfilePath := baseImage
			if !strings.HasSuffix(dockerfilePath, ".dockerfile") {
				dockerfilePath += ".dockerfile"
			}
			if data, err := os.ReadFile(dockerfilePath); err == nil {
				lines := strings.Split(string(data), "\n")
				for _, line := range lines {
					if strings.HasPrefix(line, "# CANONICAL_NAME:") {
						baseImage = strings.TrimSpace(strings.TrimPrefix(line, "# CANONICAL_NAME:"))
						break
					}
				}
			}

			bArgs := map[string]string{
				"BASE_IMAGE": baseImage,
				"INST_NAME":  args[1],
			}
			return RunBuildEngine(cmd.Context(), "instance.dockerfile", ".", flagBuildDir, runtimeDir, flagForce, bArgs)
		},
	}
	cmd.Flags().StringVarP(&flagBuildDir, "builddir", "b", "/runtimes", "Base directory for ZFS extraction")
	return cmd
}
