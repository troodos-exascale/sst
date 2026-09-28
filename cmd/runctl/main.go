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
	"text/tabwriter"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	rtsst "github.com/troodos-exascale/sst/internal/rtsst"
	sst "github.com/troodos-exascale/sst/internal/sst"
	runctlpb "github.com/troodos-exascale/sst/proto/yaml/runctl/v1"
)

var (
	debug bool
	force bool
)

var rootCmd = &cobra.Command{
	Use:           "runctl",
	Short:         "Runtime VM controller",
	SilenceUsage:  true,
	SilenceErrors: true,
}

func main() {
	runtime.GOMAXPROCS(runtime.NumCPU())

	rc := NewRunctl()

	rootCmd.PersistentFlags().BoolVar(&debug, "debug", false, "enable debug output")
	rootCmd.PersistentFlags().BoolVar(&force, "force", false, "force operation")

	rootCmd.AddCommand(
		rc.startCmd(),
		rc.stopCmd(),
		rc.pauseCmd(),
		rc.resumeCmd(),
		rc.listCmd(),
		rc.sshCmd(),
		rc.setupHostCmd(),
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

func (r *Runctl) syncWorkspace() {
	if cwd, err := os.Getwd(); err == nil {
		rtsst.SyncCatalog(r.Ctx, r.Log, cwd)
	}
}

func (r *Runctl) runtimeCompletion(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) != 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	// Updated to look at runtimes, falling back to instances
	names, err := rtsst.ListCatalogNames("runtimes")
	if err != nil {
		names, _ = rtsst.ListCatalogNames("instances")
	}
	var matches []string
	for _, name := range names {
		if strings.HasPrefix(name, toComplete) {
			matches = append(matches, name)
		}
	}
	return matches, cobra.ShellCompDirectiveNoFileComp
}

func (r *Runctl) startCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "start <name>",
		Short:             "Start a runtime",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: r.runtimeCompletion,
		RunE: func(cmd *cobra.Command, args []string) error {
			r.syncWorkspace()
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

	// Dynamic Branching based on manifest Bootloader type
	if inst.Bootloader == "container" {
		return r.startContainer(inst, instDir)
	}

	_ = rtsst.EnsureCIDataISO(r.Ctx, r.Log, instDir)

	memMiB, _ := rtsst.ParseMemorySizeMiB(inst.MemorySize, inst.MemorySizeSuffix)

	args := []string{
		"run",
		"--cpus", inst.CpuCount,
		"--mem", strconv.FormatUint(memMiB, 10),
	}

	// NEW PROTOBUF NETWORKS ARRAY
	for _, net := range inst.Networks {
		args = append(args, "--net", fmt.Sprintf("mac=%s,tap-name=sst_tap", net.MacAddress))
	}
	// LEGACY NETWORK ARRAY
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
		args = append(args, inst.Kernel)
	} else if inst.Bootloader == "efi" {
		if inst.EfiVars != "" {
			args = append(args, "--pflash", inst.EfiVars)
		}
	}

	exePath := filepath.Join(instDir, inst.Name)
	_ = os.Remove(exePath)

	crosvmPath, err := exec.LookPath("crosvm")
	if err != nil {
		crosvmPath = "/usr/local/bin/crosvm"
	}

	if err := os.Symlink(crosvmPath, exePath); err != nil {
		return fmt.Errorf("failed to create process shim symlink: %w", err)
	}

	r.Log.Info(r.Ctx, "starting aligned vmm", "bin", "./"+inst.Name, "target", crosvmPath, "args", args)

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

func (r *Runctl) startContainer(inst *runctlpb.Instance, instDir string) error {
	rootfs := filepath.Join(instDir, "rootfs")
	if _, err := os.Stat(rootfs); os.IsNotExist(err) {
		return fmt.Errorf("container rootfs not found at %s", rootfs)
	}

	args := []string{
		"--quiet",
		"--machine", inst.Name,
		"--network-bridge=sst_br0",
		fmt.Sprintf("--directory=%s", rootfs),
	}

	hasSystemd := false
	if _, err := os.Stat(filepath.Join(rootfs, "lib/systemd/systemd")); err == nil {
		hasSystemd = true
	} else if _, err := os.Stat(filepath.Join(rootfs, "sbin/init")); err == nil {
		hasSystemd = true
	}

	if hasSystemd {
		args = append(args, "--boot")
	} else {
		args = append(args, "--as-pid2", "/.sst/init", "/bin/sh")
	}

	if len(inst.Networks) > 0 {
		args = append(args, fmt.Sprintf("--network-mac=%s", inst.Networks[0].MacAddress))
	} else if len(inst.Network) > 0 {
		mac := strings.Split(inst.Network[0], "@")[0]
		args = append(args, fmt.Sprintf("--network-mac=%s", mac))
	}

	for _, shareStr := range inst.SharedDir {
		parts := strings.Split(shareStr, ":")
		if len(parts) >= 2 {
			args = append(args, fmt.Sprintf("--bind=%s:%s", parts[0], parts[1]))
		}
	}

	exePath := filepath.Join(instDir, inst.Name)
	_ = os.Remove(exePath)

	nspawnPath, err := exec.LookPath("systemd-nspawn")
	if err != nil {
		return fmt.Errorf("systemd-nspawn not found in PATH")
	}

	if err := os.Symlink(nspawnPath, exePath); err != nil {
		return fmt.Errorf("failed to create process shim symlink: %w", err)
	}

	r.Log.Info(r.Ctx, "starting container executor", "mode", map[bool]string{true: "System (init)", false: "App (daemonless)"}[hasSystemd], "bin", "./"+inst.Name, "args", args)

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
		return fmt.Errorf("failed to start container process: %w", err)
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
	TARGET_USER=${SUDO_USER:-$(whoami)}
	
	echo "Configuring TAP device for VMs: $TARGET_USER"
	ip link show sst_tap &>/dev/null || ip tuntap add mode tap user "$TARGET_USER" vnet_hdr sst_tap
	ip addr show sst_tap | grep -q "192.168.10.1/24" || ip addr add 192.168.10.1/24 dev sst_tap
	ip link set sst_tap up

	echo "Configuring Bridge device for Containers..."
	ip link show sst_br0 &>/dev/null || ip link add name sst_br0 type bridge
	ip addr show sst_br0 | grep -q "192.168.11.1/24" || ip addr add 192.168.11.1/24 dev sst_br0
	ip link set sst_br0 up

	sysctl -w net.ipv4.ip_forward=1 > /dev/null

	iptables -C INPUT -i sst_tap -j ACCEPT 2>/dev/null || iptables -I INPUT -i sst_tap -j ACCEPT
	iptables -C INPUT -i sst_br0 -j ACCEPT 2>/dev/null || iptables -I INPUT -i sst_br0 -j ACCEPT

	HOST_DEV=$(ip route get 8.8.8.8 | awk '{print $5}' | head -n 1)
	if [ -n "$HOST_DEV" ]; then
		iptables -t nat -C POSTROUTING -o "$HOST_DEV" -j MASQUERADE 2>/dev/null || iptables -t nat -A POSTROUTING -o "$HOST_DEV" -j MASQUERADE
		for IFACE in sst_tap sst_br0; do
			iptables -C FORWARD -i "$HOST_DEV" -o $IFACE -m state --state RELATED,ESTABLISHED -j ACCEPT 2>/dev/null || iptables -A FORWARD -i "$HOST_DEV" -o $IFACE -m state --state RELATED,ESTABLISHED -j ACCEPT
			iptables -C FORWARD -i $IFACE -o "$HOST_DEV" -j ACCEPT 2>/dev/null || iptables -A FORWARD -i $IFACE -o "$HOST_DEV" -j ACCEPT
		done
	fi

	if command -v dnsmasq &>/dev/null; then
		if [ ! -f /var/run/sst_tap_dnsmasq.pid ] || ! kill -0 $(cat /var/run/sst_tap_dnsmasq.pid) 2>/dev/null; then
			dnsmasq --conf-file=/dev/null --user=root --pid-file=/var/run/sst_tap_dnsmasq.pid \
			--dhcp-leasefile=/var/run/sst_tap_dnsmasq.leases --port=0 --interface=sst_tap \
			--listen-address=192.168.10.1 --bind-interfaces --dhcp-range=192.168.10.50,192.168.10.200,12h \
			--dhcp-option=option:router,192.168.10.1 --dhcp-option=option:dns-server,8.8.8.8 --except-interface=lo --clear-on-reload
		fi
		if [ ! -f /var/run/sst_br0_dnsmasq.pid ] || ! kill -0 $(cat /var/run/sst_br0_dnsmasq.pid) 2>/dev/null; then
			dnsmasq --conf-file=/dev/null --user=root --pid-file=/var/run/sst_br0_dnsmasq.pid \
			--dhcp-leasefile=/var/run/sst_br0_dnsmasq.leases --port=0 --interface=sst_br0 \
			--listen-address=192.168.11.1 --bind-interfaces --dhcp-range=192.168.11.50,192.168.11.200,12h \
			--dhcp-option=option:router,192.168.11.1 --dhcp-option=option:dns-server,8.8.8.8 --except-interface=lo --clear-on-reload
		fi
	fi
	`
	_, err := sst.ExecShell(r.Ctx, r.Log, "bash", "-c", script)
	return err
}

func (r *Runctl) stopCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "stop <name>",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: r.runtimeCompletion,
		RunE: func(cmd *cobra.Command, args []string) error {
			r.syncWorkspace()
			return r.Stop(args[0])
		},
	}
}

func (r *Runctl) Stop(name string) error {
	lp := rtsst.LockFilePath(name)
	info, err := rtsst.ReadLock(lp)
	if err != nil {
		return nil
	}
	if !rtsst.IsPidAlive(info.Pid) {
		return nil
	}

	r.Log.Info(r.Ctx, "stopping instance", "instance", name, "pid", info.Pid)
	return syscall.Kill(info.Pid, syscall.SIGTERM)
}

func (r *Runctl) pauseCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "pause <name>",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: r.runtimeCompletion,
		RunE: func(cmd *cobra.Command, args []string) error {
			r.syncWorkspace()
			lp := rtsst.LockFilePath(args[0])
			info, _ := rtsst.ReadLock(lp)
			if rtsst.IsPidAlive(info.Pid) {
				return syscall.Kill(info.Pid, syscall.SIGSTOP)
			}
			return nil
		},
	}
}

func (r *Runctl) resumeCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "resume <name>",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: r.runtimeCompletion,
		RunE: func(cmd *cobra.Command, args []string) error {
			r.syncWorkspace()
			lp := rtsst.LockFilePath(args[0])
			info, _ := rtsst.ReadLock(lp)
			if rtsst.IsPidAlive(info.Pid) {
				return syscall.Kill(info.Pid, syscall.SIGCONT)
			}
			return nil
		},
	}
}

func (r *Runctl) listCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List all registered runtimes",
		RunE: func(cmd *cobra.Command, args []string) error {
			r.syncWorkspace()

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
			fmt.Fprintln(w, "NAME\tSTATE\tIP\tIMAGE\tLOCATION")

			// Scan both instances and runtimes to support smooth migration
			catalogs := []string{"runtimes", "instances"}
			homeDir := rtsst.Home()

			for _, catalogName := range catalogs {
				baseDir := filepath.Join(rtsst.RuntimeDir(), catalogName)
				ents, err := os.ReadDir(baseDir)
				if err != nil {
					continue
				}

				for _, e := range ents {
					if strings.HasPrefix(e.Name(), ".") {
						continue
					}

					linkPath := filepath.Join(baseDir, e.Name())
					realPath := linkPath

					info, err := os.Lstat(linkPath)
					if err != nil {
						continue
					}

					if info.Mode()&os.ModeSymlink != 0 {
						resolved, err := filepath.EvalSymlinks(linkPath)
						if err != nil {
							fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", e.Name(), "dangling", "-", "-", linkPath)
							continue
						}
						realPath = resolved
					} else if !info.IsDir() {
						continue
					}

					lp := filepath.Join(realPath, "lockfile")
					state := "stopped"
					ip := "-"
					imageName := "-"

					inst, loadErr := r.loadInstanceFromDir(realPath)
					if loadErr != nil {
						continue // If neither runtime.yaml nor instance.yaml exists, skip
					}

					if inst.BaseImage != "" {
						imageName = inst.BaseImage
					}

					if linfo, err := rtsst.ReadLock(lp); err == nil && rtsst.IsPidAlive(linfo.Pid) {
						state = "running"
						if loadErr == nil {
							var mac string
							if len(inst.Networks) > 0 {
								mac = inst.Networks[0].MacAddress
							} else if len(inst.Network) > 0 {
								mac = strings.Split(inst.Network[0], "@")[0]
							}
							if mac != "" {
								if resolvedIP, err := r.resolveIP(mac); err == nil {
									ip = resolvedIP
								}
							}
						}
					}

					displayLoc := realPath
					if strings.HasPrefix(realPath, homeDir) {
						displayLoc = "~" + strings.TrimPrefix(realPath, homeDir)
					}

					fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", e.Name(), state, ip, imageName, displayLoc)
				}
			}

			w.Flush()
			return nil
		},
	}
}

func (r *Runctl) sshCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "ssh <[user@]name>",
		Short:             "SSH into a running runtime",
		Args:              cobra.MinimumNArgs(1),
		ValidArgsFunction: r.runtimeCompletion,
		RunE: func(cmd *cobra.Command, args []string) error {
			r.syncWorkspace()

			target := args[0]
			instName := target
			sshUser := ""

			if strings.Contains(target, "@") {
				parts := strings.SplitN(target, "@", 2)
				sshUser = parts[0]
				instName = parts[1]
			}

			inst, err := r.loadInstance(instName)
			if err != nil {
				return fmt.Errorf("failed to load runtime: %w", err)
			}

			u, err := user.Current()
			if err != nil {
				return err
			}

			if sshUser == "" {
				sshUser = u.Username
			}

			sshBin, err := exec.LookPath("ssh")
			if err != nil {
				return fmt.Errorf("ssh binary not found in PATH")
			}

			sshArgs := []string{
				"ssh",
				"-i", filepath.Join(u.HomeDir, ".ssh", "id_ed25519"),
				"-o", "StrictHostKeyChecking=no",
				"-o", "UserKnownHostsFile=/dev/null",
				"-o", "LogLevel=ERROR",
			}

			// Strictly rely on IP Resolution
			var mac string
			if len(inst.Networks) > 0 {
				mac = inst.Networks[0].MacAddress
			} else if len(inst.Network) > 0 {
				mac = strings.Split(inst.Network[0], "@")[0]
			}

			if mac == "" {
				return fmt.Errorf("instance has no network configured")
			}

			ip, err := r.resolveIP(mac)
			if err != nil {
				return fmt.Errorf("could not resolve IP for instance %s (MAC: %s): %w\nWait a few seconds after boot or ensure it acquired a DHCP lease", instName, mac, err)
			}

			fmt.Printf("Connecting to %s@%s...\n", sshUser, ip)

			if term.IsTerminal(int(os.Stdin.Fd())) {
				sshArgs = append(sshArgs, "-t")
			}
			sshArgs = append(sshArgs, fmt.Sprintf("%s@%s", sshUser, ip))

			if len(args) > 1 {
				sshArgs = append(sshArgs, args[1:]...)
			}

			return syscall.Exec(sshBin, sshArgs, os.Environ())
		},
	}
}

func (r *Runctl) resolveIP(mac string) (string, error) {
	mac = strings.ToLower(mac)

	var macOSMacParts []string
	for _, p := range strings.Split(mac, ":") {
		trimmed := strings.TrimLeft(p, "0")
		if trimmed == "" {
			trimmed = "0"
		}
		macOSMacParts = append(macOSMacParts, trimmed)
	}
	macOSMac := strings.Join(macOSMacParts, ":")

	// Check VM Bridge Leases
	if data, err := os.ReadFile("/var/run/sst_tap_dnsmasq.leases"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			parts := strings.Fields(line)
			if len(parts) >= 3 && strings.ToLower(parts[1]) == mac {
				return parts[2], nil
			}
		}
	}

	// Check Container Bridge Leases
	if data, err := os.ReadFile("/var/run/sst_br0_dnsmasq.leases"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			parts := strings.Fields(line)
			if len(parts) >= 3 && strings.ToLower(parts[1]) == mac {
				return parts[2], nil
			}
		}
	}

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

	if out, err := exec.Command("ip", "neigh").Output(); err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			if strings.Contains(strings.ToLower(line), mac) {
				parts := strings.Fields(line)
				if len(parts) >= 1 {
					return parts[0], nil
				}
			}
		}
	}

	if out, err := exec.Command("sudo", "arp", "-a").Output(); err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			lowerLine := strings.ToLower(line)
			if strings.Contains(lowerLine, mac) || strings.Contains(lowerLine, macOSMac) {
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

func (r *Runctl) instanceDir(name string) string {
	// Try the new runtimes catalog first
	globalPath := filepath.Join(rtsst.Home(), ".runtimes", "runtimes", name)
	if realPath, err := filepath.EvalSymlinks(globalPath); err == nil {
		return realPath
	}
	if _, err := os.Stat(globalPath); err == nil {
		return globalPath
	}

	// Fallback to legacy instances catalog
	legacyPath := filepath.Join(rtsst.Home(), ".runtimes", "instances", name)
	if realPath, err := filepath.EvalSymlinks(legacyPath); err == nil {
		return realPath
	}
	return legacyPath
}

func (r *Runctl) loadInstanceFromDir(dir string) (*runctlpb.Instance, error) {
	inst := &runctlpb.Instance{}

	yamlPath := filepath.Join(dir, "runtime.yaml")
	if _, err := os.Stat(yamlPath); os.IsNotExist(err) {
		yamlPath = filepath.Join(dir, "instance.yaml")
	}

	yo := &sst.YamlObject{
		Path:   yamlPath,
		Schema: "sst.yaml.runctl.v1.Instance",
		Msg:    inst,
	}
	if diags := yo.Load(r.Ctx, r.Log); diags != nil {
		return nil, diags
	}
	return inst, nil
}

func (r *Runctl) loadInstance(name string) (*runctlpb.Instance, error) {
	return r.loadInstanceFromDir(r.instanceDir(name))
}
