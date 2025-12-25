package main

import (
	"context"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	rtsst "github.com/swiss-software-tool/runtime-smart/go/internal/rtsst"
	sst "github.com/swiss-software-tool/runtime-smart/go/internal/sst"
	runctlpb "github.com/swiss-software-tool/runtime-smart/go/proto/yaml/runctl/v1"
)

var (
	debug bool
	force bool
)

/* ======================= main ======================= */

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

/* ======================= superblock ======================= */

type Runctl struct {
	Ctx context.Context
	Log *sst.Logx // CHANGED: Now a pointer
}

func NewRunctl() *Runctl {
	ctx := context.Background()
	log := sst.LogxRoot().Subsystem("runctl")
	// CHANGED: Pass pointer directly, no dereference (*log)
	return &Runctl{Ctx: ctx, Log: log}
}

/* ======================= cobra commands ======================= */

func (r *Runctl) startCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "start <name>",
		Short: "Start an instance",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.Start(args[0])
		},
	}
}

func (r *Runctl) stopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop <name>",
		Short: "Stop an instance",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.Stop(args[0])
		},
	}
}

func (r *Runctl) pauseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "pause <name>",
		Short: "Pause an instance",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.Pause(args[0])
		},
	}
}

func (r *Runctl) resumeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "resume <name>",
		Short: "Resume an instance",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.Resume(args[0])
		},
	}
}

func (r *Runctl) listCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List instances",
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.List()
		},
	}
}

func (r *Runctl) sshCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ssh <name>",
		Short: "SSH into an instance",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.SSH(args[0])
		},
	}
}

/* ======================= helpers ======================= */

func (r *Runctl) instanceDir(name string) string {
	return rtsst.InstanceDir(name)
}

func (r *Runctl) loadInstance(name string) (*runctlpb.Instance, error) {
	instDir := r.instanceDir(name)
	path := filepath.Join(instDir, "instance.yaml")

	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("instance '%s' has no instance.yaml (expected %s)", name, path)
	}

	inst := &runctlpb.Instance{}
	yo := &sst.YamlObject{
		Path:   path,
		Schema: "sst.yaml.runctl.v1.Instance",
		Kind:   "Instance",
		Msg:    inst,
	}

	// r.Log is already *sst.Logx, so we pass it directly
	if diags := yo.Load(r.Ctx, r.Log); diags != nil {
		return nil, diags // Return error directly
	}
	return inst, nil
}

func parseMemorySizeMiB(s string) (uint64, error) {
	s = strings.TrimSpace(strings.ToUpper(s))
	if s == "" {
		return 4096, nil
	}
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	n, err := strconv.ParseUint(s[:i], 10, 64)
	if err != nil {
		return 0, err
	}
	switch strings.TrimSpace(s[i:]) {
	case "", "M", "MB", "MIB":
		return n, nil
	case "G", "GB", "GIB":
		return n * 1024, nil
	case "K", "KB", "KIB":
		return n / 1024, nil
	default:
		return 0, fmt.Errorf("invalid memory size %q", s)
	}
}

/* ======================= actions ======================= */

func (r *Runctl) Start(name string) error {
	if err := rtsst.EnsureInstanceNotBusy(name); err != nil {
		return err
	}

	instDir, _ := filepath.Abs(r.instanceDir(name))
	inst, err := r.loadInstance(name)
	if err != nil {
		return err
	}
	
	r.Log.Info(r.Ctx, "starting instance",
		"instance", inst.Name,
		"boot", inst.Boot,
		"image", inst.Image,
	)

	cpu := int(inst.CpuCount)
	if cpu <= 0 {
		cpu = 4
	}
	memMiB, err := parseMemorySizeMiB(inst.MemorySize)
	if err != nil {
		return err
	}

	args := []string{
		"--name", inst.Name,
		"--cpu-count", strconv.Itoa(cpu),
		"--memory-size", strconv.FormatUint(memMiB, 10),
	}

	switch inst.Boot {
	case "efi":
		efiVars := inst.EfiVars
		if efiVars == "" {
			efiVars = "efi-vars"
		}
		args = append(args,
			"--bootloader", "efi",
			"--efi-vars", filepath.Join(instDir, efiVars),
		)

	case "kernel":
		if inst.Kernel == "" {
			return fmt.Errorf("boot=kernel requires inst.kernel")
		}
		args = append(args,
			"--bootloader", "linux",
			"--kernel", inst.Kernel,
		)
		if inst.Initrd != "" {
			args = append(args, "--initrd", inst.Initrd)
		}
		if inst.Params != "" {
			args = append(args, "--params", inst.Params)
		}

	default:
		return fmt.Errorf("invalid boot mode %q", inst.Boot)
	}

	args = append(args, "--rwdisk", inst.Image)

	if inst.Cidata != "" {
		args = append(args, "--disk", filepath.Join(instDir, inst.Cidata))
	}

	for _, d := range inst.Disks {
		if d.Mode == "rw" {
			args = append(args, "--rwdisk", d.Path)
		} else {
			args = append(args, "--disk", d.Path)
		}
	}

	for _, s := range inst.Shares {
		args = append(args, "--folder", fmt.Sprintf("%s:%s", s.Host, s.Guest))
	}

	exePath := filepath.Join(instDir, inst.Name)
	_ = os.Remove(exePath)
	if err := os.Symlink("/usr/local/bin/runvm", exePath); err != nil {
		return err
	}

	// CHANGED: Pass r.Log directly (it's already a pointer)
	_, err = sst.Exec(r.Ctx, r.Log, sst.ExecRequest{
		Cmd:  "./" + inst.Name,
		Args: args,
		Dir:  instDir,
		Mode: sst.ExecForeground,
	})
	if err != nil {
		r.Log.Diagnose(
			r.Ctx,
			"RUNVM_FAILED",
			"runvm exited with failure",
			"instance", inst.Name,
			"error", err.Error(),
		)
		return err
	}

		r.Log.Info(r.Ctx, "instance exited cleanly", "instance", inst.Name)
	return nil
}

func (r *Runctl) Stop(name string) error {
	lp := rtsst.LockFilePath(name)
	info, err := rtsst.ReadLock(lp)
	if err != nil {
		return nil
	}
	if rtsst.IsPidAlive(info.Pid) {
		return syscall.Kill(info.Pid, syscall.SIGTERM)
	}
	return nil
}

func (r *Runctl) Pause(name string) error {
	lp := rtsst.LockFilePath(name)
	info, err := rtsst.ReadLock(lp)
	if err != nil {
		return nil
	}
	if rtsst.IsPidAlive(info.Pid) {
		return syscall.Kill(info.Pid, syscall.SIGSTOP)
	}
	return nil
}

func (r *Runctl) Resume(name string) error {
	lp := rtsst.LockFilePath(name)
	info, err := rtsst.ReadLock(lp)
	if err != nil {
		return nil
	}
	if rtsst.IsPidAlive(info.Pid) {
		return syscall.Kill(info.Pid, syscall.SIGCONT)
	}
	return nil
}

func (r *Runctl) SSH(name string) error {
	inst, err := r.loadInstance(name)
	if err != nil {
		return err
	}
	mac := inst.Network.Mac
	if mac == "" {
		return fmt.Errorf("no MAC address in instance spec")
	}

	ip, err := instanceIP(mac)
	if err != nil {
		return err
	}

	u, _ := user.Current()
	// CHANGED: Pass r.Log directly
	_, err = sst.Exec(r.Ctx, r.Log, sst.ExecRequest{
		Cmd: "ssh",
		Args: []string{
			fmt.Sprintf("%s@%s", u.Username, ip),
			"-o", "StrictHostKeyChecking=no",
			"-o", "UserKnownHostsFile=/dev/null",
		},
		Mode: sst.ExecForeground,
	})
	return err
}

func (r *Runctl) List() error {
	root := rtsst.InstanceRoot()
	ents, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		lp := rtsst.LockFilePath(name)
		info, err := rtsst.ReadLock(lp)
		state := "stopped"
		if err == nil && rtsst.IsPidAlive(info.Pid) {
			state = "running"
		}
		fmt.Printf("%s\t%s\n", name, state)
	}
	return nil
}

/* ======================= network helper ======================= */

func instanceIP(mac string) (string, error) {
	mac = strings.ToLower(mac)

	// CHANGED: sst.LogxRoot() returns *Logx, so we pass it directly.
	log := sst.LogxRoot().Subsystem("runctl.instanceip")

	for _, cmd := range [][]string{
		{"arp", "-a"},
		{"ndp", "-a"},
		{"ip", "neigh"},
	} {
		res, err := sst.Exec(context.Background(), log, sst.ExecRequest{
			Cmd:  cmd[0],
			Args: cmd[1:],
			Mode: sst.ExecCaptured,
		})
		if err != nil {
			continue
		}
		for _, ln := range strings.Split(res.Stdout, "\n") {
			if strings.Contains(strings.ToLower(ln), mac) {
				if start, end := strings.Index(ln, "("), strings.Index(ln, ")"); start > -1 && end > start {
					return ln[start+1 : end], nil
				}
				if f := strings.Fields(ln); len(f) > 0 {
					return f[0], nil
				}
			}
		}
	}

	return "", fmt.Errorf("no IP found for MAC %s", mac)
}