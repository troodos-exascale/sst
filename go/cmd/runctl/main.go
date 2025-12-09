package main

import (
	"fmt"
	"gopkg.in/yaml.v3"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"path/filepath"
	"runtime"
	"runtime-smart/internal"
	"strconv"
	"strings"
	"syscall"
)

var (
	debug bool
	force bool
)



// -----------------------------------------------------------------------------
// Types (runtime.yaml)
// -----------------------------------------------------------------------------



func instanceIP(cfg *internal.RuntimeConfig) (string, error) {
	mac := strings.ToLower(cfg.Network.MAC)

	// First try ARP (IPv4)
	out, err := exec.Command("arp", "-a").CombinedOutput()
	if err == nil {
		lines := strings.Split(string(out), "\n")
		for _, ln := range lines {
			low := strings.ToLower(ln)
			if strings.Contains(low, mac) {
				// extract "(IP)" portion
				start := strings.Index(ln, "(")
				end := strings.Index(ln, ")")
				if start > -1 && end > start {
					return ln[start+1 : end], nil
				}
			}
		}
	}

	// Try IPv6 neighbor table (ndp for macOS, ip -6 neigh for Linux)
	// macOS: ndp -a
	out6, err6 := exec.Command("ndp", "-a").CombinedOutput()
	if err6 == nil {
		lines := strings.Split(string(out6), "\n")
		for _, ln := range lines {
			low := strings.ToLower(ln)
			if strings.Contains(low, mac) {
				fields := strings.Fields(ln)
				if len(fields) > 0 {
					return fields[0], nil // first field is IPv6 address
				}
			}
		}
	}

	// Linux fallback: ip neigh
	out6b, err6b := exec.Command("ip", "neigh").CombinedOutput()
	if err6b == nil {
		lines := strings.Split(string(out6b), "\n")
		for _, ln := range lines {
			low := strings.ToLower(ln)
			if strings.Contains(low, mac) {
				fields := strings.Fields(ln)
				if len(fields) > 0 {
					return fields[0], nil // first field is IP
				}
			}
		}
	}

	return "", fmt.Errorf("no IP found for MAC %s", cfg.Network.MAC)
}


// -----------------------------------------------------------------------------
// writeCIData - create cidata/{user-data,meta-data} for cloud-init NoCloud
// -----------------------------------------------------------------------------
func writeCIData(instDir string, cfg *internal.RuntimeConfig) error {

	cidataDir := filepath.Join(instDir, "cidata")
	if err := os.MkdirAll(cidataDir, 0755); err != nil {
		return fmt.Errorf("cannot create cidata directory: %w", err)
	}

	// --- Resolve user information ---
	u, err := user.Current()
	if err != nil {
		return fmt.Errorf("cannot determine current user: %w", err)
	}
	username := u.Username
	gecos := u.Name
	if gecos == "" {
		gecos = username
	}

	// --- Find a public SSH key ---
	sshDir := filepath.Join(u.HomeDir, ".ssh")
	pubCandidates := []string{
		filepath.Join(sshDir, "id_ed25519.pub"),
		filepath.Join(sshDir, "id_rsa.pub"),
		filepath.Join(sshDir, "id_ecdsa.pub"),
		filepath.Join(sshDir, "id_dsa.pub"),
	}

	var pubKey string
	for _, p := range pubCandidates {
		b, err := os.ReadFile(p)
		if err == nil && len(strings.TrimSpace(string(b))) > 0 {
			pubKey = strings.TrimSpace(string(b))
			break
		}
	}

	if pubKey == "" {
		return fmt.Errorf("no usable SSH public key found in %s", sshDir)
	}

	// --- Write user-data ---
	// Include directory creation for /Users/<username>
	userData := fmt.Sprintf(`#cloud-config
hostname: %s

ssh_pwauth: false
disable_root: true

users:
  - name: %s
    gecos: %s
    sudo: ALL=(ALL) NOPASSWD:ALL
    shell: /bin/bash
    ssh_authorized_keys:
      - %s

runcmd:
  - mkdir -p /Users/%s
  - chown %s:%s /Users/%s
`,
		cfg.Network.HostName, // hostname
		username,             // user
		gecos,                // gecos
		pubKey,               // ssh key
		username,             // /Users/<user>
		username,             // owner
		username,             // group
		username,             // /Users/<user>
	)

	if err := os.WriteFile(filepath.Join(cidataDir, "user-data"),
		[]byte(userData), 0644); err != nil {
		return fmt.Errorf("writing user-data failed: %w", err)
	}

	// --- Write meta-data ---
	meta := fmt.Sprintf("instance-id: %s\nlocal-hostname: %s\n",
		cfg.Name, cfg.Name)

	if err := os.WriteFile(filepath.Join(cidataDir, "meta-data"),
		[]byte(meta), 0644); err != nil {
		return fmt.Errorf("writing meta-data failed: %w", err)
	}

	return nil
}

// -----------------------------------------------------------------------------
// load/save runtime config
// -----------------------------------------------------------------------------

func loadConfig(instDir string) (*internal.RuntimeConfig, error) {
	data, err := os.ReadFile(filepath.Join(instDir, "runtime.yaml"))
	if err != nil {
		return nil, err
	}
	var cfg internal.RuntimeConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// -----------------------------------------------------------------------------
// Memory parsing (to MiB)
// -----------------------------------------------------------------------------

func parseMemorySizeMiB(s string) (uint64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		// Default: 4G
		return 4096, nil
	}

	orig := s
	s = strings.ToUpper(s)

	fmt.Printf("Convervting memory %s to MB\n", s)
	// separate numeric and suffix
	i := 0
	for i < len(s) && (s[i] >= '0' && s[i] <= '9') {
		i++
	}
	numStr := s[:i]
	suffix := strings.TrimSpace(s[i:])

	if numStr == "" {
		return 0, fmt.Errorf("invalid memory_size %q", orig)
	}
	n, err := strconv.ParseUint(numStr, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid memory_size %q: %w", orig, err)
	}

	switch suffix {
	case "":
		// no suffix: treat as MiB
		return n, nil
	case "K", "KB", "KIB":
		return n / 1024, nil
	case "M", "MB", "MIB":
		return n, nil
	case "G", "GB", "GIB":
		return n * 1024, nil
	default:
		return 0, fmt.Errorf("unsupported memory_size suffix in %q", orig)
	}
}

func startInstance(name string) error {
	if err := internal.EnsureInstanceNotBusy(name); err != nil {
		return err
	}

	instDir, _ := filepath.Abs(internal.InstanceDir(name))
	cfg, err := loadConfig(instDir)
	if err != nil {
		return err
	}

	// CPU defaults
	cpu := cfg.CPUCount
	if cpu <= 0 {
		cpu = 4
	}

	// ----------------------------------------------------------------------
	// Recreate cidata.iso if cidata/ directory exists
	// ----------------------------------------------------------------------
	if err := writeCIData(instDir, cfg); err != nil {
		return err
	}
	cidataDir := filepath.Join(instDir, "cidata")
	cidataISO := filepath.Join(instDir, "cidata.iso")

	if st, err := os.Stat(cidataDir); err == nil && st.IsDir() {
		internal.LogDebug(debug, "Rebuilding cidata.iso from %s", cidataDir)
		internal.LogDebug(debug, "cmd: hdiutil makehybrid -ov -iso -joliet -default-volume-name cidata -o %s %s", cidataISO, cidataDir)

		cmd := exec.Command(
			"hdiutil", "makehybrid", "-ov",
			"-iso", "-joliet",
			"-default-volume-name", "cidata",
			"-o", cidataISO, cidataDir,
		)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr

		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to create cidata.iso: %w", err)
		}
	}

	// Memory defaults and parsing (to MiB)
	memMiB, err := parseMemorySizeMiB(cfg.MemorySize)
	if err != nil {
		return fmt.Errorf("invalid memory_size in runtime.yaml: %w", err)
	}

	// common arguments
	args := []string{
		"--name", cfg.Name,
		"--net", fmt.Sprintf("%s@%s", cfg.Network.MAC, cfg.Network.Mode),
		"--cpu-count", strconv.Itoa(cpu),
		"--memory-size", strconv.FormatUint(memMiB, 10),
	}
	// CLOUD-INIT SEED (always RO)
	if cfg.CIDataDisk != "" {
		args = append(args, "--disk", cfg.CIDataDisk)
	}

	if cfg.Boot == "efi" {
		// SYSTEM DISK (must be RW)
		args = append(args, "--bootloader", "efi")
		args = append(args, "--efi-vars", filepath.Join(instDir, "efi-vars"))
		args = append(args, "--rwdisk", cfg.SystemDisk)

		// DATA DISKS (array, mixed RW/RO)
		for _, d := range cfg.DataDisks {
			if d.Mode == "rw" {
				args = append(args, "--rwdisk", d.Path)
			} else {
				args = append(args, "--disk", d.Path)
			}
		}
	} else {
		if cfg.Kernel == "" {
			return fmt.Errorf("kernel boot requires 'kernel' field in runtime.yaml")
		}
		args = append(args,
			"--bootloader", "linux",
			"--kernel", cfg.Kernel,
		)
		if cfg.Initrd != "" {
			args = append(args, "--initrd", cfg.Initrd)
		}

		// root/system disk must be rw
		args = append(args, "--rwdisk", cfg.SystemDisk)

		if cfg.Params != "" {
			args = append(args, "--params", cfg.Params)
		}

		// Attach extra data disks (same logic as EFI branch)
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

	// Symlink for process naming
	exeName := cfg.Name
	targetBin := "/usr/local/bin/runvm"
	exePath := filepath.Join(instDir, exeName)
	_ = os.Remove(exePath)
	if err := os.Symlink(targetBin, exePath); err != nil {
		return err
	}

	if debug {
		fmt.Fprintf(os.Stderr, "\n[runctl debug] launching runvm\n")
		fmt.Fprintf(os.Stderr, "[runctl debug]   workdir: %s\n", instDir)
		fmt.Fprintf(os.Stderr, "[runctl debug]   executable: %s\n", exePath)
		fmt.Fprintf(os.Stderr, "[runctl debug]   args:\n")
		for i, a := range args {
			fmt.Fprintf(os.Stderr, "    [%d] %q\n", i, a)
		}
		fmt.Fprintf(os.Stderr, "[runctl debug] END arguments\n\n")
	}

	cmd := exec.Command("./"+exeName, args...)
	cmd.Dir = instDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin

	if err := cmd.Start(); err != nil {
		return err
	}
	child := cmd.Process.Pid
	fmt.Printf("Started %s (pid %d)\n", name, child)

	lp := internal.LockFilePath(name)
	_ = internal.WriteLock(lp, cfg.Name, child)

	return nannyWait(cmd, lp, child)
}

// -----------------------------------------------------------------------------
// nannyWait
// -----------------------------------------------------------------------------

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
			switch s {
			case syscall.SIGINT, syscall.SIGTERM:
				_ = syscall.Kill(pid, s.(syscall.Signal))
				_ = exec.Command("reset").Run()
			case syscall.SIGHUP, syscall.SIGTSTP:
				_ = syscall.Kill(pid, syscall.SIGSTOP)
				_ = exec.Command("reset").Run()
			case syscall.SIGCONT:
				_ = syscall.Kill(pid, syscall.SIGCONT)
			}
		}
	}
}

// -----------------------------------------------------------------------------
// functions: ssh, stop, start, pause, create
// -----------------------------------------------------------------------------

func sshToInstance(name string) error {
	instDir := internal.InstanceDir(name)

	cfg, err := loadConfig(instDir)
	if err != nil {
		return fmt.Errorf("failed to load config for %s: %w", name, err)
	}

	ip, err := instanceIP(cfg)
	if err != nil {
		return fmt.Errorf("cannot determine IP for %s: %w", name, err)
	}

	// Username: default to the current user
	u, _ := user.Current()
	usr := u.Username

	fmt.Printf("Connecting to %s (%s) as %s...\n", name, ip, usr)

	sshArgs := []string{
		fmt.Sprintf("%s@%s", usr, ip),
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
	}

	cmd := exec.Command("ssh", sshArgs...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}

func stopInstance(name string) error {
	lp := internal.LockFilePath(name)
	info, err := internal.ReadLock(lp)
	if err != nil {
		fmt.Printf("Instance %s not running\n", name)
		return nil
	}
	if internal.IsPidAlive(info.Pid) {
		return syscall.Kill(info.Pid, syscall.SIGTERM)
	}
	_ = os.Remove(lp)
	fmt.Printf("Instance %s (stale lock removed)\n", name)
	return nil
}

func pauseInstance(name string) error {
	lp := internal.LockFilePath(name)
	info, err := internal.ReadLock(lp)
	if err != nil {
		fmt.Printf("Instance %s not running\n", name)
		return nil
	}
	if internal.IsPidAlive(info.Pid) {
		return syscall.Kill(info.Pid, syscall.SIGSTOP)
	}
	_ = os.Remove(lp)
	fmt.Printf("Instance %s (stale lock removed)\n", name)
	return nil
}

func resumeInstance(name string) error {
	lp := internal.LockFilePath(name)
	info, err := internal.ReadLock(lp)
	if err != nil {
		fmt.Printf("Instance %s not running\n", name)
		return nil
	}
	if internal.IsPidAlive(info.Pid) {
		return syscall.Kill(info.Pid, syscall.SIGCONT)
	}
	_ = os.Remove(lp)
	fmt.Printf("Instance %s (stale lock removed)\n", name)
	return nil
}

func listInstances() error {
	root := internal.InstanceRoot()
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		lp := internal.LockFilePath(name)
		info, err := internal.ReadLock(lp)
		if err != nil {
			fmt.Printf("%s\tstopped (no lock)\n", name)
			continue
		}
		state := "stopped"
		if internal.IsPidAlive(info.Pid) {
			state = "running"
		}
		cfg, _ := loadConfig(internal.InstanceDir(name))
		ip := ""
		if cfg != nil {
			if v, err := instanceIP(cfg); err == nil {
				ip = v
			}
		}

		if ip != "" {
			fmt.Printf("%s\t%s\tpid=%d\tip=%s\n", name, state, info.Pid, ip)
		} else {
			fmt.Printf("%s\t%s\tpid=%d\tip=unknown\n", name, state, info.Pid)
		}
	}
	return nil
}

// -----------------------------------------------------------------------------
// CLI
// -----------------------------------------------------------------------------

func usage() {
	fmt.Fprintf(os.Stderr, `usage:
  runctl [--debug] [--force] create --efi <image-file> <name>
  runctl [--debug] start <name>
  runctl [--debug] stop <name>
  runctl [--debug] pause <name>
  runctl [--debug] resume <name>
  runctl [--debug] list
  runctl [--debug] ssh <name>

`)
}

func main() {
	runtime.GOMAXPROCS(runtime.NumCPU())

	args := os.Args[1:]
	if len(args) == 0 {
		usage()
		os.Exit(1)
	}

	// Global flags: --debug, --force
	for len(args) > 0 && strings.HasPrefix(args[0], "-") {
		switch args[0] {
		case "--debug", "-debug":
			debug = true
		case "--force", "-force", "-f":
			force = true
		default:
			fmt.Fprintf(os.Stderr, "unknown global flag: %s\n", args[0])
			usage()
			os.Exit(2)
		}
		args = args[1:]
	}

	if len(args) == 0 {
		usage()
		os.Exit(1)
	}

	cmd := args[0]
	rest := args[1:]

	var err error

	switch cmd {
	case "ssh":
		if len(rest) != 1 {
			usage()
			os.Exit(1)
		}
		err = sshToInstance(rest[0])
	case "start":
		if len(rest) != 1 {
			usage()
			os.Exit(1)
		}
		err = startInstance(rest[0])

	case "stop":
		if len(rest) != 1 {
			usage()
			os.Exit(1)
		}
		err = stopInstance(rest[0])

	case "pause":
		if len(rest) != 1 {
			usage()
			os.Exit(1)
		}
		err = pauseInstance(rest[0])

	case "resume":
		if len(rest) != 1 {
			usage()
			os.Exit(1)
		}
		err = resumeInstance(rest[0])

	case "list":
		if len(rest) != 0 {
			usage()
			os.Exit(1)
		}
		err = listInstances()

	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", cmd)
		usage()
		os.Exit(1)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "runctl error: %v\n", err)
		os.Exit(1)
	}
}
