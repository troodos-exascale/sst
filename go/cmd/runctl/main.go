package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
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

/* ======================= main / cobra ======================= */

func main() {
	runtime.GOMAXPROCS(runtime.NumCPU())

	rootCmd := &cobra.Command{
		Use:   "runctl",
		Short: "Runtime VM controller",
	}

	rootCmd.PersistentFlags().BoolVar(&debug, "debug", false, "enable debug output")
	rootCmd.PersistentFlags().BoolVar(&force, "force", false, "force operation")

	rootCmd.AddCommand(
		startCmd(),
		stopCmd(),
		pauseCmd(),
		resumeCmd(),
		listCmd(),
		sshCmd(),
	)

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "runctl error: %v\n", err)
		os.Exit(1)
	}
}

func startCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "start <name>",
		Short: "Start an instance",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return startInstance(args[0])
		},
	}
}

func stopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop <name>",
		Short: "Stop an instance",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return stopInstance(args[0])
		},
	}
}

func pauseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "pause <name>",
		Short: "Pause an instance",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return pauseInstance(args[0])
		},
	}
}

func resumeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "resume <name>",
		Short: "Resume an instance",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return resumeInstance(args[0])
		},
	}
}

func listCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List instances",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return listInstances()
		},
	}
}

func sshCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ssh <name>",
		Short: "SSH into an instance",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return sshToInstance(args[0])
		},
	}
}

/* ======================= instance.yaml loading ======================= */

func loadInstance(instDir string) (*runctlpb.Instance, error) {
	log := sst.LogxRoot().Subsystem("runctl.instance")
	ctx := context.Background()

	p := filepath.Join(instDir, "instance.yaml")
	inst := &runctlpb.Instance{}

	yo := &sst.YamlObject{
		Path:   p,
		Schema: "sst.yaml.runctl.v1.Instance",
		Kind:   "Instance",
		Msg:    inst,
	}
	if diags := yo.Load(ctx, log); diags != nil {
		return nil, fmt.Errorf(diags.Error())
	}
	if inst.Spec == nil {
		log.Diagnose(ctx, "INSTANCE_SPEC_MISSING", "instance.spec missing", "path", p)
		return nil, fmt.Errorf("instance.spec missing in %s", p)
	}
	return inst, nil
}

func resolvePath(instDir, p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(instDir, p)
}

func diskAlreadyListed(instDir string, want string, disks []*runctlpb.Disk) bool {
	wantAbs := resolvePath(instDir, want)
	for _, d := range disks {
		if d == nil {
			continue
		}
		if resolvePath(instDir, d.Path) == wantAbs {
			return true
		}
	}
	return false
}

func netArg(mac, mode string) string {
	mode = strings.TrimSpace(mode)
	if mode == "" {
		mode = "nat"
	}
	mac = strings.TrimSpace(mac)
	if mac == "" {
		return mode
	}
	return fmt.Sprintf("%s@%s", mac, mode)
}

/* ======================= helpers ======================= */

func instanceIP(mac string) (string, error) {
	mac = strings.ToLower(strings.TrimSpace(mac))
	if mac == "" {
		return "", fmt.Errorf("instance has no network.mac; cannot locate IP")
	}

	out, err := exec.Command("arp", "-a").CombinedOutput()
	if err == nil {
		for _, ln := range strings.Split(string(out), "\n") {
			if strings.Contains(strings.ToLower(ln), mac) {
				if start, end := strings.Index(ln, "("), strings.Index(ln, ")"); start > -1 && end > start {
					return ln[start+1 : end], nil
				}
			}
		}
	}

	out6, err6 := exec.Command("ndp", "-a").CombinedOutput()
	if err6 == nil {
		for _, ln := range strings.Split(string(out6), "\n") {
			if strings.Contains(strings.ToLower(ln), mac) {
				if f := strings.Fields(ln); len(f) > 0 {
					return f[0], nil
				}
			}
		}
	}

	out6b, err6b := exec.Command("ip", "neigh").CombinedOutput()
	if err6b == nil {
		for _, ln := range strings.Split(string(out6b), "\n") {
			if strings.Contains(strings.ToLower(ln), mac) {
				if f := strings.Fields(ln); len(f) > 0 {
					return f[0], nil
				}
			}
		}
	}

	return "", fmt.Errorf("no IP found for MAC %s", mac)
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

/* ======================= instance control ======================= */

func startInstance(name string) error {
	if err := rtsst.EnsureInstanceNotBusy(name); err != nil {
		return err
	}

	instDir, _ := filepath.Abs(rtsst.InstanceDir(name))

	// Keep existing behavior: ensure cidata.iso is up-to-date if present.
	_ = rtsst.UpdateCIDATAISO(instDir)

	inst, err := loadInstance(instDir)
	if err != nil {
		return err
	}
	spec := inst.Spec

	// resources
	cpu := int(spec.CpuCount)
	if cpu <= 0 {
		cpu = 4
	}
	memMiB, err := parseMemorySizeMiB(spec.MemorySize)
	if err != nil {
		return err
	}

	// base args
	args := []string{
		"--name", spec.Name,
		"--net", netArg(spec.Network.GetMac(), spec.Network.GetMode()),
		"--cpu-count", strconv.Itoa(cpu),
		"--memory-size", strconv.FormatUint(memMiB, 10),
	}

	// bootloader wiring
	boot := strings.ToLower(strings.TrimSpace(spec.Boot))
	switch boot {
	case "efi":
		args = append(args, "--bootloader", "efi")

		efiVars := spec.EfiVars
		if strings.TrimSpace(efiVars) == "" {
			efiVars = "efi-vars"
		}
		args = append(args, "--efi-vars", resolvePath(instDir, efiVars))

	case "kernel":
		if strings.TrimSpace(spec.Kernel) == "" {
			return fmt.Errorf("boot=kernel requires spec.kernel")
		}
		args = append(args, "--bootloader", "linux", "--kernel", resolvePath(instDir, spec.Kernel))
		if strings.TrimSpace(spec.Initrd) != "" {
			args = append(args, "--initrd", resolvePath(instDir, spec.Initrd))
		}
		if strings.TrimSpace(spec.Params) != "" {
			args = append(args, "--params", spec.Params)
		}

	default:
		return fmt.Errorf("invalid spec.boot %q (expected 'efi' or 'kernel')", spec.Boot)
	}

	// system disk: spec.image (treated as RW)
	if strings.TrimSpace(spec.Image) == "" {
		return fmt.Errorf("spec.image missing (system disk path)")
	}
	args = append(args, "--rwdisk", resolvePath(instDir, spec.Image))

	// optional auto-attach cidata.iso if present and not already listed
	if _, err := os.Stat(filepath.Join(instDir, "cidata.iso")); err == nil {
		if !diskAlreadyListed(instDir, "cidata.iso", spec.Disks) {
			args = append(args, "--disk", filepath.Join(instDir, "cidata.iso"))
		}
	}

	// extra disks
	for _, d := range spec.Disks {
		if d == nil {
			continue
		}
		p := resolvePath(instDir, d.Path)
		if p == "" {
			continue
		}
		mode := strings.ToLower(strings.TrimSpace(d.Mode))
		if mode == "rw" {
			args = append(args, "--rwdisk", p)
		} else {
			// default: ro
			args = append(args, "--disk", p)
		}
	}

	// shares
	for _, sh := range spec.Shares {
		if sh == nil {
			continue
		}
		host := strings.TrimSpace(sh.Host)
		guest := strings.TrimSpace(sh.Guest)
		if host == "" || guest == "" {
			continue
		}
		args = append(args, "--folder", fmt.Sprintf("%s:%s", host, guest))
	}

	// symlink runvm launcher (same as previous runctl behavior)
	exeName := spec.Name
	exePath := filepath.Join(instDir, exeName)
	_ = os.Remove(exePath)
	if err := os.Symlink("/usr/local/bin/runvm", exePath); err != nil {
		return err
	}

	cmd := exec.Command("./"+exeName, args...)
	cmd.Dir = instDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin

	if err := cmd.Start(); err != nil {
		return err
	}

	lp := rtsst.LockFilePath(name)
	_ = rtsst.WriteLock(lp, spec.Name, cmd.Process.Pid)
	return nannyWait(cmd, lp, cmd.Process.Pid)
}

func nannyWait(cmd *exec.Cmd, lock string, pid int) error {
	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()

	sigCh := make(chan os.Signal, 8)
	signal.Notify(sigCh,
		syscall.SIGINT, syscall.SIGTERM,
		syscall.SIGHUP, syscall.SIGTSTP,
		syscall.SIGCONT,
	)

	for {
		select {
		case err := <-waitCh:
			_ = os.Remove(lock)
			return err
		case s := <-sigCh:
			_ = syscall.Kill(pid, s.(syscall.Signal))
		}
	}
}

func sshToInstance(name string) error {
	instDir := rtsst.InstanceDir(name)
	inst, err := loadInstance(instDir)
	if err != nil {
		return err
	}
	spec := inst.Spec

	ip, err := instanceIP(spec.Network.GetMac())
	if err != nil {
		return err
	}

	u, _ := user.Current()
	cmd := exec.Command("ssh",
		fmt.Sprintf("%s@%s", u.Username, ip),
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
	)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

func stopInstance(name string) error {
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

func pauseInstance(name string) error {
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

func resumeInstance(name string) error {
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

func listInstances() error {
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
