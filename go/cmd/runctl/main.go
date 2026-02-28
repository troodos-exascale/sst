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
		Short: "Start an instance",
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

	// Ensure Host networking is ready on Linux
	r.setupLinuxNetwork()

	memMiB, _ := rtsst.ParseMemorySizeMiB(inst.MemorySize, inst.MemorySizeSuffix)

	// --- CROSVM ALIGNED ARGUMENTS ---
	args := []string{
		"run",
		"--cpus", inst.CpuCount,
		"--mem", strconv.FormatUint(memMiB, 10),
	}

	for _, netStr := range inst.Network {
		parts := strings.Split(netStr, "@")
		mac := parts[0]
		args = append(args, "--net", fmt.Sprintf("mac=%s,tap-name=sst_tap", mac))
	}

	// FIX 1: Use modern --block syntax and include BOTH read-write and read-only disks
	for _, d := range inst.Rwdisk {
		args = append(args, "--block", fmt.Sprintf("path=%s,ro=false", d))
	}
	for _, d := range inst.Disk {
		args = append(args, "--block", fmt.Sprintf("path=%s,ro=true", d)) // This mounts cidata.iso!
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
		args = append(args, inst.Kernel)
	}

	// --- SHIM FOR PROCESS VISIBILITY ---
	exePath := filepath.Join(instDir, inst.Name)
	_ = os.Remove(exePath)

	crosvmPath, err := exec.LookPath("crosvm")
	if err != nil {
		crosvmPath = "/usr/local/bin/crosvm"
	}

	if err := os.Symlink(crosvmPath, exePath); err != nil {
		return fmt.Errorf("failed to create process shim symlink: %w", err)
	}

	r.Log.Info(r.Ctx, "starting aligned vmm", "bin", "./"+inst.Name, "target", crosvmPath)

	cmd := exec.CommandContext(r.Ctx, "./"+inst.Name, args...)
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


// FIX 2: Idempotent Linux Network + DHCP Provisioning
func (r *Runctl) setupLinuxNetwork() {
	if runtime.GOOS != "linux" {
		return
	}

	script := `
	set -eu
	USER=$(whoami)
	ip link show sst_tap &>/dev/null || sudo ip tuntap add mode tap user $USER vnet_hdr sst_tap
	ip addr show sst_tap | grep -q "192.168.10.1/24" || sudo ip addr add 192.168.10.1/24 dev sst_tap
	sudo ip link set sst_tap up
	sudo sysctl -w net.ipv4.ip_forward=1 > /dev/null

	# 1. ALLOW DHCP/DNS INPUT FROM VM TO HOST
	sudo iptables -C INPUT -i sst_tap -j ACCEPT 2>/dev/null || \
	sudo iptables -I INPUT -i sst_tap -j ACCEPT

	# 2. SETUP NAT FORWARDING
	HOST_DEV=$(ip route get 8.8.8.8 | awk '{print $5}' | head -n 1)
	if [ -n "$HOST_DEV" ]; then
		sudo iptables -t nat -C POSTROUTING -o "$HOST_DEV" -j MASQUERADE 2>/dev/null || \
		sudo iptables -t nat -A POSTROUTING -o "$HOST_DEV" -j MASQUERADE
		
		sudo iptables -C FORWARD -i "$HOST_DEV" -o sst_tap -m state --state RELATED,ESTABLISHED -j ACCEPT 2>/dev/null || \
		sudo iptables -A FORWARD -i "$HOST_DEV" -o sst_tap -m state --state RELATED,ESTABLISHED -j ACCEPT
		
		sudo iptables -C FORWARD -i sst_tap -o "$HOST_DEV" -j ACCEPT 2>/dev/null || \
		sudo iptables -A FORWARD -i sst_tap -o "$HOST_DEV" -j ACCEPT
	fi

	# 3. EXPLICIT DNSMASQ CONFIG
	if command -v dnsmasq &>/dev/null; then
		if ! pgrep -f "dnsmasq.*sst_tap" &>/dev/null; then
			sudo dnsmasq --conf-file=/dev/null \
			--port=0 \
			--listen-address=192.168.10.1 \
			--bind-interfaces \
			--dhcp-range=192.168.10.50,192.168.10.200,12h \
			--dhcp-option=option:router,192.168.10.1 \
			--dhcp-option=option:dns-server,8.8.8.8 \
			--except-interface=lo --clear-on-reload
		fi
	fi
	`
	_, _ = sst.ExecShell(r.Ctx, r.Log, "bash", "-c", script)
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
