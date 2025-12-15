package main

import (
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
	"gopkg.in/yaml.v3"

	rtsst "github.com/swiss-software-tool/runtime-smart/go/internal/rtsst"
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

/* ======================= helpers ======================= */

func instanceIP(cfg *rtsst.RuntimeConfig) (string, error) {
	mac := strings.ToLower(cfg.Network.MAC)

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

	return "", fmt.Errorf("no IP found for MAC %s", cfg.Network.MAC)
}

func loadConfig(instDir string) (*rtsst.RuntimeConfig, error) {
	data, err := os.ReadFile(filepath.Join(instDir, "runtime.yaml"))
	if err != nil {
		return nil, err
	}
	var cfg rtsst.RuntimeConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
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
	cfg, err := loadConfig(instDir)
	if err != nil {
		return err
	}

	cpu := cfg.CPUCount
	if cpu <= 0 {
		cpu = 4
	}

	if err := rtsst.UpdateCIDATAISO(instDir); err != nil {
		return fmt.Errorf("failed to update cidata.iso")
	}

	memMiB, err := parseMemorySizeMiB(cfg.MemorySize)
	if err != nil {
		return err
	}

	args := []string{
		"--name", cfg.Name,
		"--net", fmt.Sprintf("%s@%s", cfg.Network.MAC, cfg.Network.Mode),
		"--cpu-count", strconv.Itoa(cpu),
		"--memory-size", strconv.FormatUint(memMiB, 10),
	}

	if cfg.CIDataDisk != "" {
		args = append(args, "--disk", cfg.CIDataDisk)
	}

	if cfg.Boot == "efi" {
		args = append(args,
			"--bootloader", "efi",
			"--efi-vars", filepath.Join(instDir, "efi-vars"),
			"--rwdisk", cfg.SystemDisk,
		)
		for _, d := range cfg.DataDisks {
			if d.Mode == "rw" {
				args = append(args, "--rwdisk", d.Path)
			} else {
				args = append(args, "--disk", d.Path)
			}
		}
	} else {
		if cfg.Kernel == "" {
			return fmt.Errorf("kernel boot requires 'kernel' field")
		}
		args = append(args, "--bootloader", "linux", "--kernel", cfg.Kernel)
		if cfg.Initrd != "" {
			args = append(args, "--initrd", cfg.Initrd)
		}
		args = append(args, "--rwdisk", cfg.SystemDisk)
		if cfg.Params != "" {
			args = append(args, "--params", cfg.Params)
		}
		for _, d := range cfg.DataDisks {
			if d.Mode == "rw" {
				args = append(args, "--rwdisk", d.Path)
			} else {
				args = append(args, "--disk", d.Path)
			}
		}
	}

	for _, s := range cfg.Shares {
		args = append(args, "--folder", fmt.Sprintf("%s:%s", s.Host, s.Guest))
	}

	exeName := cfg.Name
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
	_ = rtsst.WriteLock(lp, cfg.Name, cmd.Process.Pid)
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
	cfg, err := loadConfig(rtsst.InstanceDir(name))
	if err != nil {
		return err
	}
	ip, err := instanceIP(cfg)
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
