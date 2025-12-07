package main

import (
	"crypto/rand"
	"errors"
	"fmt"
	"gopkg.in/yaml.v3"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

var (
	debug bool
	force bool
)

// -----------------------------------------------------------------------------
// Logging
// -----------------------------------------------------------------------------

func logDebug(format string, args ...interface{}) {
	if debug {
		fmt.Fprintf(os.Stderr, "[runctl] "+format+"\n", args...)
	}
}

// -----------------------------------------------------------------------------
// Types (runtime.yaml)
// -----------------------------------------------------------------------------

type Share struct {
	Host  string `yaml:"host"`
	Guest string `yaml:"guest"`
}

type RootConfig struct {
	Base string `yaml:"base"`
}

type DataDisk struct {
    Path string `yaml:"path"`
    Mode string `yaml:"mode"` // "rw" or "ro"
}

type RuntimeConfig struct {
    Name        string     `yaml:"name"`
    Boot        string     `yaml:"boot"` // "efi" or "kernel"`

	Kernel string `yaml:"kernel,omitempty"`
	Initrd string `yaml:"initrd,omitempty"`

    SystemDisk  string     `yaml:"system_disk"`  // must be --rwdisk
    CIDataDisk  string     `yaml:"cidata_disk"`  // must be --disk
    DataDisks   []DataDisk `yaml:"data_disks"`  // array of mixed rw/ro

    EFIVars     string     `yaml:"efi_vars"`
    Params      string     `yaml:"params"`
    CPUCount    int        `yaml:"cpu_count"`
    MemorySize  string     `yaml:"memory_size"` //friendly: 4G, 4096=4096M

    Shares      []Share    `yaml:"shares"`

    Network struct {
        MAC  string `yaml:"mac"`
        Mode string `yaml:"mode"`
		HostName string `yaml:"hostname"`
    } `yaml:"network"`
}


// -----------------------------------------------------------------------------
// Paths
// -----------------------------------------------------------------------------

func home() string {
	u, err := user.Current()
	if err != nil {
		panic(err)
	}
	return u.HomeDir
}

func instanceRoot() string           { return filepath.Join(home(), ".runtimes", "instances") }
func instanceDir(name string) string { return filepath.Join(instanceRoot(), name) }
func lockFilePath(name string) string {
	return filepath.Join(instanceDir(name), "lockfile")
}

// -----------------------------------------------------------------------------
// Sparse file helper
// -----------------------------------------------------------------------------

func createSparse(path string, size int64) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := f.Truncate(size); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// -----------------------------------------------------------------------------
// Lockfile handling
// -----------------------------------------------------------------------------

type BusyError struct {
	Instance string
	Pid      int
}

func (e *BusyError) Error() string {
	return fmt.Sprintf("instance %q is busy (pid %d active)", e.Instance, e.Pid)
}

type LockInfo struct {
	Name string
	Pid  int
}

func readLock(path string) (*LockInfo, error) {
	data, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(string(data), "\n")
	info := &LockInfo{}
	for _, ln := range lines {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		parts := strings.SplitN(ln, "=", 2)
		if len(parts) != 2 {
			continue
		}
		switch strings.TrimSpace(parts[0]) {
		case "name":
			info.Name = strings.TrimSpace(parts[1])
		case "pid":
			p, _ := strconv.Atoi(strings.TrimSpace(parts[1]))
			info.Pid = p
		}
	}
	if info.Pid == 0 {
		return nil, errors.New("invalid lockfile (no pid)")
	}
	return info, nil
}

func writeLock(path, name string, pid int) error {
	content := fmt.Sprintf("name=%s\npid=%d\ncreated_at=%s\n",
		name, pid, time.Now().UTC().Format(time.RFC3339))
	return ioutil.WriteFile(path, []byte(content), 0644)
}

// -----------------------------------------------------------------------------
// Process helpers
// -----------------------------------------------------------------------------

// -----------------------------------------------------------------------------
// instanceIP - discover VM IP by matching ARP/NDP MAC entries to runtime config
// -----------------------------------------------------------------------------

func instanceIP(cfg *RuntimeConfig) (string, error) {
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


func isPidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	// Send signal 0 → check existence without affecting the process.
	err := syscall.Kill(pid, 0)
	return err == nil
}


func ensureInstanceNotBusy(name string) error {
	lp := lockFilePath(name)
	_, err := os.Stat(lp)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}

	info, err := readLock(lp)
	if err != nil {
		return fmt.Errorf("cannot parse lockfile %s: %w", lp, err)
	}

	if isPidAlive(info.Pid) {
		return &BusyError{Instance: name, Pid: info.Pid}
	}

	_ = os.Remove(lp)
	return nil
}

// -----------------------------------------------------------------------------
// MAC address
// -----------------------------------------------------------------------------

func generateMAC() (string, error) {
	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	buf[0] = 0x52
	buf[1] = 0x54
	buf[2] = (buf[2] & 0x7F) | 0x80
	return fmt.Sprintf("%02x:%02x:%02x:%02x:%02x:%02x",
		buf[0], buf[1], buf[2], buf[3], buf[4], buf[5]), nil
}

// -----------------------------------------------------------------------------
// writeCIData - create cidata/{user-data,meta-data} for cloud-init NoCloud
// -----------------------------------------------------------------------------
func writeCIData(instDir string, cfg *RuntimeConfig) error {

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
		cfg.Network.HostName,     // hostname
		username,     // user
		gecos,        // gecos
		pubKey,       // ssh key
		username,     // /Users/<user>
		username,     // owner
		username,     // group
		username,     // /Users/<user>
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

func loadConfig(instDir string) (*RuntimeConfig, error) {
	data, err := ioutil.ReadFile(filepath.Join(instDir, "runtime.yaml"))
	if err != nil {
		return nil, err
	}
	var cfg RuntimeConfig
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

// -----------------------------------------------------------------------------
// Image handling helpers
// -----------------------------------------------------------------------------

func isQCOW2(path string) bool {
	l := strings.ToLower(path)
	return strings.HasSuffix(l, ".qcow2") || strings.HasSuffix(l, ".qcow")
}

// sparseCopyRaw copies a raw image in a sparse way (simple zero-block detection).
func sparseCopyRaw(srcPath, dstPath string) error {
	src, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer src.Close()

	dst, err := os.Create(dstPath)
	if err != nil {
		return err
	}
	defer dst.Close()

	buf := make([]byte, 1024*1024) // 1 MiB blocks

	for {
		n, readErr := src.Read(buf)
		if n > 0 {
			block := buf[:n]
			allZero := true
			for _, b := range block {
				if b != 0 {
					allZero = false
					break
				}
			}
			if allZero {
				if _, err := dst.Seek(int64(n), io.SeekCurrent); err != nil {
					return err
				}
			} else {
				if _, err := dst.Write(block); err != nil {
					return err
				}
			}
		}

		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return readErr
		}
	}

	return nil
}



// -----------------------------------------------------------------------------
// start (EFI or kernel)
// -----------------------------------------------------------------------------

func startInstance(name string) error {
	if err := ensureInstanceNotBusy(name); err != nil {
		return err
	}

	instDir, _ := filepath.Abs(instanceDir(name))
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
		logDebug("Rebuilding cidata.iso from %s", cidataDir)
		logDebug("cmd: hdiutil makehybrid -ov -iso -joliet -default-volume-name cidata -o %s %s", cidataISO, cidataDir)

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
		args = append(args,"--efi-vars", filepath.Join(instDir,"efi-vars"))
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
			}  else {
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

	lp := lockFilePath(name)
	_ = writeLock(lp, cfg.Name, child)

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
				_ =exec.Command("reset").Run()
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
	instDir := instanceDir(name)

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
	lp := lockFilePath(name)
	info, err := readLock(lp)
	if err != nil {
		fmt.Printf("Instance %s not running\n", name)
		return nil
	}
	if isPidAlive(info.Pid) {
		return syscall.Kill(info.Pid, syscall.SIGTERM)
	}
	_ = os.Remove(lp)
	fmt.Printf("Instance %s (stale lock removed)\n", name)
	return nil
}

func pauseInstance(name string) error {
	lp := lockFilePath(name)
	info, err := readLock(lp)
	if err != nil {
		fmt.Printf("Instance %s not running\n", name)
		return nil
	}
	if isPidAlive(info.Pid) {
		return syscall.Kill(info.Pid, syscall.SIGSTOP)
	}
	_ = os.Remove(lp)
	fmt.Printf("Instance %s (stale lock removed)\n", name)
	return nil
}

func resumeInstance(name string) error {
	lp := lockFilePath(name)
	info, err := readLock(lp)
	if err != nil {
		fmt.Printf("Instance %s not running\n", name)
		return nil
	}
	if isPidAlive(info.Pid) {
		return syscall.Kill(info.Pid, syscall.SIGCONT)
	}
	_ = os.Remove(lp)
	fmt.Printf("Instance %s (stale lock removed)\n", name)
	return nil
}

func listInstances() error {
	root := instanceRoot()
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
		lp := lockFilePath(name)
		info, err := readLock(lp)
		if err != nil {
			fmt.Printf("%s\tstopped (no lock)\n", name)
			continue
		}
		state := "stopped"
		if isPidAlive(info.Pid) {
			state = "running"
		}
		cfg, _ := loadConfig(instanceDir(name))
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
