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
	"os/user"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	rtsst "github.com/swiss-software-tool/sst/internal/rtsst"
	sst "github.com/swiss-software-tool/sst/internal/sst"
	runctlpb "github.com/swiss-software-tool/sst/proto/yaml/runctl/v1"
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
		rc.setupHostCmd(), // Added host network setup command
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

	for _, d := range inst.Rwdisk {
		args = append(args, "--block", fmt.Sprintf("path=%s,ro=false", d))
	}
	for _, d := range inst.Disk {
		args = append(args, "--block", fmt.Sprintf("path=%s,ro=true", d))
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
		// Positional kernel argument at the very end
		args = append(args, inst.Kernel)
	} else if inst.Bootloader == "efi" {
		// FIX: Use upstream crosvm's --pflash argument to pass the UEFI variable store.
		// The macOS wrapper now natively intercepts this.
		if inst.EfiVars != "" {
			args = append(args, "--pflash", inst.EfiVars)
		}
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

func (r *Runctl) setupHostCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "setup-host",
		Short: "Provision host networking and routing (requires root)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if os.Geteuid() != 0 {
				return fmt.Errorf("setup-host must be run as root (e.g. via sudo)")
			}
			return r.SetupLinuxNetwork()
		},
	}
}


func (r *Runctl) SetupLinuxNetwork() error {
	if runtime.GOOS != "linux" {
		r.Log.Info(r.Ctx, "setup-host is a no-op on non-Linux platforms")
		return nil
	}

	script := `
	set -eu
	# Grab the real user who invoked sudo, default to current user
	TARGET_USER=${SUDO_USER:-$(whoami)}
	
	echo "Configuring TAP device for user: $TARGET_USER"

	ip link show sst_tap &>/dev/null || ip tuntap add mode tap user "$TARGET_USER" vnet_hdr sst_tap
	ip addr show sst_tap | grep -q "192.168.10.1/24" || ip addr add 192.168.10.1/24 dev sst_tap
	ip link set sst_tap up
	sysctl -w net.ipv4.ip_forward=1 > /dev/null

	# 1. ALLOW DHCP/DNS INPUT FROM VM TO HOST
	iptables -C INPUT -i sst_tap -j ACCEPT 2>/dev/null || \
	iptables -I INPUT -i sst_tap -j ACCEPT

	# 2. SETUP NAT FORWARDING
	HOST_DEV=$(ip route get 8.8.8.8 | awk '{print $5}' | head -n 1)
	if [ -n "$HOST_DEV" ]; then
		iptables -t nat -C POSTROUTING -o "$HOST_DEV" -j MASQUERADE 2>/dev/null || \
		iptables -t nat -A POSTROUTING -o "$HOST_DEV" -j MASQUERADE
		
		iptables -C FORWARD -i "$HOST_DEV" -o sst_tap -m state --state RELATED,ESTABLISHED -j ACCEPT 2>/dev/null || \
		iptables -A FORWARD -i "$HOST_DEV" -o sst_tap -m state --state RELATED,ESTABLISHED -j ACCEPT
		
		iptables -C FORWARD -i sst_tap -o "$HOST_DEV" -j ACCEPT 2>/dev/null || \
		iptables -A FORWARD -i sst_tap -o "$HOST_DEV" -j ACCEPT
	fi

# 3. EXPLICIT DNSMASQ CONFIG (Using PID file instead of pgrep)
	if command -v dnsmasq &>/dev/null; then
		PIDFILE=/var/run/sst_tap_dnsmasq.pid
		if [ ! -f "$PIDFILE" ] || ! kill -0 $(cat "$PIDFILE") 2>/dev/null; then
			dnsmasq --conf-file=/dev/null \
			--user=root \
			--pid-file=$PIDFILE \
			--dhcp-leasefile=/var/run/sst_tap_dnsmasq.leases \
			--port=0 \
			--interface=sst_tap \
			--listen-address=192.168.10.1 \
			--bind-interfaces \
			--dhcp-range=192.168.10.50,192.168.10.200,12h \
			--dhcp-option=option:router,192.168.10.1 \
			--dhcp-option=option:dns-server,8.8.8.8 \
			--except-interface=lo --clear-on-reload
		fi
	fi
	`
	_, err := sst.ExecShell(r.Ctx, r.Log, "bash", "-c", script)
	return err
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
	
	r.Log.Info(r.Ctx, "stopping instance", "instance", name, "pid", info.Pid, "arch", runtime.GOARCH)

	var sig syscall.Signal
	if runtime.GOARCH == "arm64" || runtime.GOARCH == "arm" {
		// kill -9 for ARM architectures, no ACIPI intercept exists
		r.Log.Info(r.Ctx, "sending SIGKILL")
		sig = syscall.SIGKILL
	} else {
		sig = syscall.SIGTERM
	}

	return syscall.Kill(info.Pid, sig)
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
		Use:   "ssh <name>",
		Short: "SSH into a running instance",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			instName := args[0]
			inst, err := r.loadInstance(instName)
			if err != nil {
				return fmt.Errorf("failed to load instance: %w", err)
			}
			if len(inst.Network) == 0 {
				return fmt.Errorf("instance has no network configured")
			}

			// Extract the MAC address from the YAML (format: "mac@nat" or just "mac")
			mac := strings.Split(inst.Network[0], "@")[0]
			
			// Resolve the IP from the MAC address
			ip, err := r.resolveIP(mac)
			if err != nil {
				return fmt.Errorf("could not resolve IP for instance %s (MAC: %s): %w\nWait a few seconds after boot or ensure it acquired a DHCP lease", instName, mac, err)
			}

			// Get the current user to use as the default SSH login
			u, err := user.Current()
			if err != nil {
				return err
			}

			sshBin, err := exec.LookPath("ssh")
			if err != nil {
				return fmt.Errorf("ssh binary not found in PATH")
			}

			fmt.Printf("Connecting to %s@%s...\n", u.Username, ip)

			// Exec syscall replaces the runctl process with ssh
			sshArgs := []string{
				"ssh",
				"-i", filepath.Join(u.HomeDir, ".ssh", "id_ed25519"),
				"-o", "StrictHostKeyChecking=no",
				"-o", "UserKnownHostsFile=/dev/null",
				"-o", "LogLevel=ERROR",
				fmt.Sprintf("%s@%s", u.Username, ip),
			}
			
			return syscall.Exec(sshBin, sshArgs, os.Environ())
		},
	}
}


// resolveIP scans DHCP leases and ARP caches to find the IP associated with a MAC address.
func (r *Runctl) resolveIP(mac string) (string, error) {
	mac = strings.ToLower(mac)

	// macOS 'arp' and 'bootpd' drop leading zeros (e.g., "09" becomes "9")
	var macOSMacParts []string
	for _, p := range strings.Split(mac, ":") {
		trimmed := strings.TrimLeft(p, "0")
		if trimmed == "" {
			trimmed = "0"
		}
		macOSMacParts = append(macOSMacParts, trimmed)
	}
	macOSMac := strings.Join(macOSMacParts, ":")

	// 1. Linux: Check our explicit dnsmasq lease file
	if data, err := os.ReadFile("/var/run/sst_tap_dnsmasq.leases"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			parts := strings.Fields(line)
			// dnsmasq format: <expiry> <mac> <ip> <hostname> <client_id>
			if len(parts) >= 3 && strings.ToLower(parts[1]) == mac {
				return parts[2], nil
			}
		}
	}

	// 2. macOS: Check native bootpd DHCP leases
	if data, err := os.ReadFile("/var/db/dhcpd_leases"); err == nil {
		var lastIP string
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "ip_address=") {
				lastIP = strings.TrimPrefix(line, "ip_address=")
			} else if strings.HasPrefix(line, "hw_address=") {
				lowerLine := strings.ToLower(line)
				if strings.Contains(lowerLine, mac) || strings.Contains(lowerLine, macOSMac) {
					return lastIP, nil
				}
			}
		}
	}

	// 3. Linux: Fallback to modern 'ip neigh'
	if out, err := exec.Command("ip", "neigh").Output(); err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			if strings.Contains(strings.ToLower(line), mac) {
				parts := strings.Fields(line)
				// ip neigh format: 192.168.10.85 dev sst_tap lladdr 52:54:... REACHABLE
				if len(parts) >= 1 {
					return parts[0], nil
				}
			}
		}
	}

	// 4. macOS / Legacy Linux: Fallback to 'arp -a'
	if out, err := exec.Command("arp", "-a").Output(); err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			lowerLine := strings.ToLower(line)
			if strings.Contains(lowerLine, mac) || strings.Contains(lowerLine, macOSMac) {
				// arp -a format: ? (192.168.64.2) at 52:54:b2:b3:b9:9 on bridge100 ifscope [bridge]
				start := strings.Index(line, "(")
				end := strings.Index(line, ")")
				if start != -1 && end != -1 && end > start {
					return line[start+1 : end], nil
				}
			}
		}
	}

	return "", fmt.Errorf("MAC %s not found in DHCP leases or ARP cache", mac)
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
