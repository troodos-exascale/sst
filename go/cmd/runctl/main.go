package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	rtsst "github.com/swiss-software-tool/runtime-smart/go/internal/rtsst"
	sst "github.com/swiss-software-tool/runtime-smart/go/internal/sst"
	runctlpb "github.com/swiss-software-tool/runtime-smart/go/proto/yaml/runctl/v1"
)

var (
	debug bool
	force bool
)

func main() {
	runtime.GOMAXPROCS(runtime.NumCPU())

	rc := NewRunctl()

	rootCmd := &cobra.Command{
		Use:           "runctl",
		Short:         "Runtime VM controller",
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	rootCmd.PersistentFlags().BoolVar(&debug, "debug", false, "enable debug output")
	rootCmd.PersistentFlags().BoolVar(&force, "force", false, "force operation")

	rootCmd.AddCommand(
		rc.startCmd(),
		rc.stopCmd(),
		rc.pauseCmd(),
		rc.resumeCmd(),
		rc.listCmd(),
		rc.sshCmd(),
	)

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
}

type Runctl struct {
	Ctx context.Context
	Log *sst.Logx
}

func NewRunctl() *Runctl {
	ctx := context.Background()
	log := sst.LogxRoot().Subsystem("runctl")
	return &Runctl{Ctx: ctx, Log: log}
}

func (r *Runctl) startCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "start <name>",
		Short: "Start an instance (crosvm-aligned)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.Start(args[0])
		},
	}
}

func (r *Runctl) Start(name string) error {
	if err := rtsst.EnsureInstanceNotBusy(name); err != nil {
		return err
	}

	instDir, _ := filepath.Abs(r.instanceDir(name))
	inst, err := r.loadInstance(name)
	if err != nil {
		return err
	}

	_ = rtsst.EnsureCIDataISO(r.Ctx, r.Log, instDir)

	memMiB, _ := rtsst.ParseMemorySizeMiB(inst.MemorySize, inst.MemorySizeSuffix)

	// --- CROSVM ALIGNED ARGUMENTS ---
	args := []string{
		"run",
		"--cpus", inst.CpuCount,
		"--mem", strconv.FormatUint(memMiB, 10),
	}

	for _, netStr := range inst.Network {
		// e.g., converts user's "52:54:ef:cb:89:2c@nat" to standard crosvm
		parts := strings.Split(netStr, "@")
		mac := parts[0]
		// UNCONDITIONAL: Both Mac wrapper and Linux native get the exact same string
		args = append(args, "--net", fmt.Sprintf("mac=%s,tap-name=sst_tap", mac))
	}

	for _, d := range inst.Rwdisk {
		args = append(args, "--rwdisk", d)
	}

	for _, s := range inst.SharedDir {
		args = append(args, "--shared-dir", s)
	}

	if inst.Bootloader == "linux" {
		if inst.Kernel == "" {
			return fmt.Errorf("bootloader=linux requires kernel")
		}
		if inst.Initrd != "" {
			args = append(args, "--initrd", inst.Initrd)
		}
		if len(inst.Params) > 0 {
			args = append(args, "--params", strings.Join(inst.Params, " "))
		}
		args = append(args, inst.Kernel) // Positional kernel argument at the end
	}
	

	// --- BINARY DETECTION ---
	var bin string
	if runtime.GOOS == "darwin" {
		bin = "./" + inst.Name
	} else {
		bin = "crosvm"
	}

	r.Log.Info(r.Ctx, "starting aligned vmm", "bin", bin, "instance", name)

	cmd := exec.CommandContext(r.Ctx, bin, args...)
	cmd.Dir = instDir
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		state, err := term.GetState(fd)
		if err == nil {
			defer term.Restore(fd, state)
		}
	}

	lockPath := rtsst.LockFilePath(inst.Name)
	lock, err := rtsst.CreateLock(lockPath)
	if err != nil {
		return err
	}
	defer lock.Remove()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start VM process: %w", err)
	}

	go func() {
		for sig := range sigCh {
			_ = cmd.Process.Signal(sig)
		}
	}()

	return cmd.Wait()
}

func (r *Runctl) stopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop <name>",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error { return r.Stop(args[0]) },
	}
}

func (r *Runctl) Stop(name string) error {
	lp := rtsst.LockFilePath(name)
	info, err := rtsst.ReadLock(lp)
	if err != nil { return nil }
	if !rtsst.IsPidAlive(info.Pid) { return nil }
	r.Log.Info(r.Ctx, "stopping instance", "instance", name, "pid", info.Pid)
	return syscall.Kill(info.Pid, syscall.SIGTERM)
}

func (r *Runctl) pauseCmd() *cobra.Command {
	return &cobra.Command{
		Use: "pause <name>",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			lp := rtsst.LockFilePath(args[0])
			info, _ := rtsst.ReadLock(lp)
			if rtsst.IsPidAlive(info.Pid) { return syscall.Kill(info.Pid, syscall.SIGSTOP) }
			return nil
		},
	}
}

func (r *Runctl) resumeCmd() *cobra.Command {
	return &cobra.Command{
		Use: "resume <name>",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			lp := rtsst.LockFilePath(args[0])
			info, _ := rtsst.ReadLock(lp)
			if rtsst.IsPidAlive(info.Pid) { return syscall.Kill(info.Pid, syscall.SIGCONT) }
			return nil
		},
	}
}

func (r *Runctl) listCmd() *cobra.Command {
	return &cobra.Command{
		Use: "list",
		RunE: func(cmd *cobra.Command, args []string) error {
			ents, _ := os.ReadDir(rtsst.InstanceRoot())
			for _, e := range ents {
				if !e.IsDir() { continue }
				lp := rtsst.LockFilePath(e.Name())
				state := "stopped"
				if info, err := rtsst.ReadLock(lp); err == nil && rtsst.IsPidAlive(info.Pid) { state = "running" }
				fmt.Printf("%s\t%s\n", e.Name(), state)
			}
			return nil
		},
	}
}

func (r *Runctl) sshCmd() *cobra.Command {
	return &cobra.Command{
		Use: "ssh <name>",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			inst, _ := r.loadInstance(args[0])
			if len(inst.Network) == 0 { return fmt.Errorf("no network") }
			// Logic to resolve IP and exec SSH would go here
			return nil
		},
	}
}

func (r *Runctl) instanceDir(name string) string { return rtsst.InstanceDir(name) }

func (r *Runctl) loadInstance(name string) (*runctlpb.Instance, error) {
	inst := &runctlpb.Instance{}
	yo := &sst.YamlObject{
		Path: filepath.Join(r.instanceDir(name), "instance.yaml"),
		Schema: "sst.yaml.runctl.v1.Instance",
		Kind: "Instance",
		Msg: inst,
	}
	if diags := yo.Load(r.Ctx, r.Log); diags != nil { return nil, diags }
	return inst, nil
}
