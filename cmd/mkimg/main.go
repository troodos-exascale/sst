package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"
	rtsst "github.com/swiss-software-tool/sst/internal/rtsst"
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

func delegateToLinuxVM() {
	vmName := os.Getenv("SST_BUILD_VM")
	if vmName == "" {
		vmName = "ub"
	}

	// Do not delegate the 'help' command so local usage docs still render instantly
	if len(os.Args) > 1 && (os.Args[1] == "help" || os.Args[1] == "--help") {
		return
	}

	fmt.Printf("--> macOS detected. Transparently delegating command to Linux VM '%s'...\n", vmName)

	runctlPath, err := exec.LookPath("runctl")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: runctl not found in PATH for delegation\n")
		os.Exit(1)
	}

	// Reconstruct the command for remote execution. 
	// We inject 'sudo' because mkimg requires root privileges on Linux.
	remoteCmd := []string{"sudo"}
	if logFmt := os.Getenv("SST_LOG_FORMAT"); logFmt != "" {
		remoteCmd = append(remoteCmd, "SST_LOG_FORMAT="+logFmt)
	}

	// --- NEW: Forward Mac identity to the Linux VM ---
	if u, err := user.Current(); err == nil {
		remoteCmd = append(remoteCmd, "SST_HOST_USER="+u.Username)
	}
	if pubKey, err := rtsst.GetLocalPublicKey(); err == nil && pubKey != "" {
		remoteCmd = append(remoteCmd, fmt.Sprintf("SST_HOST_PUBKEY='%s'", strings.TrimSpace(pubKey)))
	}
	// -------------------------------------------------

	remoteCmd = append(remoteCmd, "mkimg")
	remoteCmd = append(remoteCmd, os.Args[1:]...)

	// Construct the SSH call. We pass the remoteCmd as a single joined string 
	// so runctl executes it seamlessly over SSH.
	args := []string{runctlPath, "ssh", vmName, strings.Join(remoteCmd, " ")}

	// CHANGED: Use cmd.Run() instead of syscall.Exec so the Mac process stays alive
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "delegation failed: %v\n", err)
		os.Exit(1)
	}

	// --- NEW: Process the purification queue natively on the Mac ---
	runtimeDir := os.Getenv("SST_RUNTIMEDIR")
	for i, arg := range os.Args {
		if (arg == "-d" || arg == "--dir") && i+1 < len(os.Args) {
			runtimeDir = os.Args[i+1]
		} else if strings.HasPrefix(arg, "--dir=") {
			runtimeDir = strings.SplitN(arg, "=", 2)[1]
		}
	}
	if runtimeDir == "" {
		runtimeDir = rtsst.RuntimeDir()
	}

	if err := ProcessPurificationQueue(runtimeDir); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: host artifact purification failed: %v\n", err)
	}
	os.Exit(0)
}

func main() {
	// Intercept execution on macOS
	if runtime.GOOS == "darwin" {
		delegateToLinuxVM()
	}

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

var extractKernelCmd = &cobra.Command{
	Use:   "extract_kernel <in> <out>",
	Short: "Extracts an uncompressed raw kernel (vmlinux) from a compressed kernel image",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Printf("--> Extracting raw kernel from %s to %s\n", args[0], args[1])
		return rtsst.ExtractRawKernel(args[0], args[1])
	},
}

func pullvmCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "pullvm <upstream-url>",
		Short: "Pull an upstream VM image and create an SST image directory",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			runtimeDir := flagRoot
			if runtimeDir == "" { runtimeDir = rtsst.RuntimeDir() }
			
			base := filepath.Base(args[0])
			name := strings.TrimSuffix(base, filepath.Ext(base))
			
			bArgs := map[string]string{
				"URL":       args[0],
				"PULL_NAME": name,
			}
			return RunBuildEngine(cmd.Context(), "pullvm.dockerfile", runtimeDir, bArgs)
		},
	}
}

func instanceCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "instance <image> <name>",
		Short: "Create an instance directory from an image",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			runtimeDir := flagRoot
			if runtimeDir == "" { runtimeDir = rtsst.RuntimeDir() }
			
			bArgs := map[string]string{
				"BASE_IMAGE": args[0],
				"INST_NAME":  args[1],
			}
			return RunBuildEngine(cmd.Context(), "instance.dockerfile", runtimeDir, bArgs)
		},
	}
}
