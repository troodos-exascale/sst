package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"golang.org/x/term"
	"time"

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
	Log *sst.Logx
}

func NewRunctl() *Runctl {
	ctx := context.Background()
	log := sst.LogxRoot().Subsystem("runctl")
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

	if diags := yo.Load(r.Ctx, r.Log); diags != nil {
		return nil, diags
	}
	return inst, nil
}

func parseMemorySizeMiB(sizeStr, suffix string) (uint64, error) {
	if sizeStr == "" {
		return 0, fmt.Errorf("memory size is empty")
	}

	size, err := strconv.ParseUint(sizeStr, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid memory size %q: %w", sizeStr, err)
	}

	switch strings.ToLower(suffix) {
	case "", "mib":
		// already MiB
		return size, nil
	case "mb":
		// decimal MB → MiB
		return size * 1000 / 1024, nil
	case "gib":
		return size * 1024, nil
	case "gb":
		return size * 1000 * 1000 / 1024 / 1024, nil
	case "kib":
		return size / 1024, nil
	case "kb":
		return size * 1000 / 1024 / 1024, nil
	default:
		return 0, fmt.Errorf("unknown memory size suffix %q", suffix)
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

	// Allow editing cloud-init user-data and restarting
	if err := rtsst.EnsureCIDataISO(r.Ctx, r.Log, instDir); err != nil {
		r.Log.Warn(r.Ctx, "failed to refresh cidata.iso (ignoring)", "error", err)
	}

	r.Log.Info(r.Ctx, "starting instance",
		"instance", inst.Name,
		"bootloader", inst.Bootloader,
		"kernel", inst.Kernel,
	)

	cpu, err := strconv.Atoi(inst.CpuCount)
	if err != nil {
		return err
	}
	if cpu <= 0 {
		cpu = 4
	}

	memMiB, err := parseMemorySizeMiB(inst.MemorySize, inst.MemorySizeSuffix)
	if err != nil {
		return err
	}

	args := []string{
		"--name", inst.Name,
		"--cpu-count", strconv.Itoa(cpu),
		"--memory-size", strconv.FormatUint(memMiB, 10),
	}

	// Flags
	if inst.Gui {
		args = append(args, "--gui")
	}
	if inst.Balloon {
		args = append(args, "--balloon", "true")
	} else {
		args = append(args, "--balloon", "false")
	}

	// Networking (repeated)
	for _, net := range inst.Network {
		args = append(args, "--network", net)
	}

	// Bootloader
	switch inst.Bootloader {
	case "efi":
		efiVars := inst.EfiVars
		if efiVars == "" {
			efiVars = "efi-vars"
		}
		args = append(args,
			"--bootloader", "efi",
			"--efi-vars", filepath.Join(instDir, efiVars),
		)

	case "linux":
		if inst.Kernel == "" {
			return fmt.Errorf("bootloader=linux requires kernel")
		}
		args = append(args,
			"--bootloader", "linux",
			"--kernel", inst.Kernel,
		)
		if inst.Initrd != "" {
			args = append(args, "--initrd", inst.Initrd)
		}
		if len(inst.Params) > 0 {
			args = append(args, "--params", strings.Join(inst.Params, " "))
		}

	default:
		return fmt.Errorf("invalid bootloader %q", inst.Bootloader)
	}

	// Storage
	for _, d := range inst.Rwdisk {
		args = append(args, "--rwdisk", d)
	}
	for _, d := range inst.Disk {
		args = append(args, "--disk", d)
	}
	for _, d := range inst.Cdrom {
		args = append(args, "--cdrom", d)
	}

	// Shared directories
	for _, s := range inst.SharedDir {
		args = append(args, "--shared-dir", s)
	}

	// Enable nested virtualization on VZ by default
	// Aargh - this only works on m3 and later macs
	// args = append(args, "--nested-virt")

	// Prepare executable shim
	exePath := filepath.Join(instDir, inst.Name)
	_ = os.Remove(exePath)
	if err := os.Symlink("/usr/local/bin/runvm", exePath); err != nil {
		return err
	}

    // --- Terminal Safety Net ---
    // Save terminal state before handing control to the raw-mode VM
    fd := int(os.Stdin.Fd())
    if term.IsTerminal(fd) {
        state, err := term.GetState(fd)
        if err == nil {
            // Restore terminal no matter what happens (panic, error, or child kill -9)
            defer term.Restore(fd, state)
        }
    }

	lockPath := rtsst.LockFilePath(inst.Name)
	lock, err := rtsst.CreateLock(lockPath)
	if err != nil {
		return err
	}
	defer lock.Remove()

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
        // If we can't read the lock, assume it's already stopped
		return nil
	}

	if !rtsst.IsPidAlive(info.Pid) {
        // Process is already gone
		return nil
	}

    r.Log.Info(r.Ctx, "sending SIGTERM", "instance", name, "pid", info.Pid)
	if err := syscall.Kill(info.Pid, syscall.SIGTERM); err != nil {
        return err
    }

    // --- Synchronous Wait ---
    // Poll for up to 10 seconds for the process to actually exit.
    // This prevents the "storage invalid" race condition on restart.
    timeout := time.After(10 * time.Second)
    ticker := time.NewTicker(100 * time.Millisecond)
    defer ticker.Stop()

    for {
        select {
        case <-timeout:
            r.Log.Warn(r.Ctx, "instance did not stop gracefully, sending SIGKILL", "pid", info.Pid)
            _ = syscall.Kill(info.Pid, syscall.SIGKILL)
            return nil // We forced it, so we consider it stopped
        case <-ticker.C:
            if !rtsst.IsPidAlive(info.Pid) {
                r.Log.Info(r.Ctx, "instance stopped", "instance", name)
                return nil
            }
        }
    }
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

func (r *Runctl) SSH(name string) error {
	inst, err := r.loadInstance(name)
	if err != nil {
		return err
	}

	if len(inst.Network) == 0 {
		return fmt.Errorf("instance %q has no network configuration", name)
	}

	// Network entries are "aa:bb:cc:dd:ee:ff@nat"
	net := inst.Network[0]
	mac := net
	if i := strings.IndexByte(net, '@'); i >= 0 {
		mac = net[:i]
	}

	log := r.Log.Subsystem("ssh")
	ip, err := instanceIP(r.Ctx, log, mac)
	if err != nil {
		return err
	}

	// Exec ssh in foreground
	_, err = sst.Exec(r.Ctx, r.Log, sst.ExecRequest{
		Cmd:  "ssh",
		Args: []string{ip},
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

// instanceIP attempts to find the IP address for a given MAC address.
func instanceIP(ctx context.Context, log *sst.Logx, mac string) (string, error) {
	mac = strings.ToLower(mac)
	log.Info(ctx, "Resolving IP", "mac", mac)

	// 1. Refresh ARP cache via Broadcast Ping
	// We ping the broadcast address to force neighbors to announce themselves.
	// This ensures the ARP table is populated even if the VM is idle.
	pingArgs := []string{"-c", "1", "-W", "1"}
	if runtime.GOOS == "darwin" {
		// Try specific gateway ping on macOS (vmnet framework usually uses .1)
		_, _ = sst.Exec(ctx, log, sst.ExecRequest{
			Cmd:  "ping",
			Args: append(pingArgs, "192.168.64.1"),
			Mode: sst.ExecCaptured,
		})
	}
	// Broadcast ping (works on many Linux setups + macOS)
	// We ignore errors because some networks block broadcast pings.
	_, _ = sst.Exec(ctx, log, sst.ExecRequest{
		Cmd:  "ping",
		Args: append(pingArgs, "255.255.255.255"),
		Mode: sst.ExecCaptured,
	})

	// 2. Read ARP Table
	entries, err := getARPTable(ctx, log)
	if err != nil {
		return "", fmt.Errorf("failed to read arp table: %w", err)
	}

	// 3. Search with Normalization
	// We normalize both the target MAC and the table entry to handle
	// cases where the OS drops leading zeros (e.g. 0:36 vs 00:36).
	targetNormalized := normalizeMAC(mac)

	for ip, entryMac := range entries {
		if normalizeMAC(entryMac) == targetNormalized {
			log.Info(ctx, "IP found", "ip", ip, "mac", mac)
			return ip, nil
		}
	}

	return "", fmt.Errorf("no IP found for MAC %s", mac)
}

// getARPTable returns a map of IP -> MAC from the system's ARP cache.
func getARPTable(ctx context.Context, log *sst.Logx) (map[string]string, error) {
	table := make(map[string]string)

	if runtime.GOOS == "linux" {
		// Fast path: Read /proc/net/arp directly
		data, err := os.ReadFile("/proc/net/arp")
		if err == nil {
			lines := strings.Split(string(data), "\n")
			for _, line := range lines {
				// Format: IP address  HW type  Flags  HW address  Mask  Device
				fields := strings.Fields(line)
				if len(fields) >= 4 {
					ip := fields[0]
					mac := fields[3]
					if mac != "00:00:00:00:00:00" { // Ignore incomplete entries
						table[ip] = mac
					}
				}
			}
			return table, nil
		}
		log.Warn(ctx, "Could not read /proc/net/arp, falling back to command", "error", err)
	}

	// Universal / macOS fallback: `arp -an`
	// -n is critical: prevents DNS lookups that slow down execution (fixing the 5s hang)
	res, err := sst.Exec(ctx, log, sst.ExecRequest{
		Cmd:  "arp",
		Args: []string{"-an"},
		Mode: sst.ExecCaptured,
	})
	if err != nil {
		return nil, err
	}

	for _, line := range strings.Split(res.Stdout, "\n") {
		// macOS format: ? (192.168.64.2) at 52:54:00:12:34:56 on bridge100 ifscope [ethernet]
		// Linux format (arp -an): ? (192.168.122.115) at 52:54:00:12:34:56 [ether] on virbr0
		if !strings.Contains(line, " at ") {
			continue
		}

		parts := strings.Fields(line)
		if len(parts) < 4 {
			continue
		}

		// Extract IP: usually part[1], wrapped in parens
		ipStr := strings.Trim(parts[1], "()")

		// Extract MAC: 'at' is usually parts[2], so MAC is parts[3]
		macStr := parts[3]

		// Sanity check basics
		if strings.Contains(ipStr, ".") && strings.Contains(macStr, ":") {
			table[ipStr] = macStr
		}
	}

	return table, nil
}

// normalizeMAC standardizes MAC addresses (adds leading zeros if missing, lowercase)
// e.g., "52:54:df:9b:0:36" -> "52:54:df:9b:00:36"
func normalizeMAC(mac string) string {
	parts := strings.Split(strings.ToLower(mac), ":")
	for i, p := range parts {
		if len(p) == 1 {
			parts[i] = "0" + p
		}
	}
	return strings.Join(parts, ":")
}