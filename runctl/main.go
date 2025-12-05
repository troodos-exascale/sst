package main

import (
	"crypto/rand"
	"errors"
	"fmt"
	"gopkg.in/yaml.v3"
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
	Base    string `yaml:"base"`
	Overlay string `yaml:"overlay"`
}

type CloudInitConfig struct {
	UserData string `yaml:"user-data"`
	MetaData string `yaml:"meta-data"`
	CIDATA   string `yaml:"cidata"`
}

type NetworkConfig struct {
	MAC  string `yaml:"mac"`
	Mode string `yaml:"mode"`
}

type RuntimeConfig struct {
	Name      string          `yaml:"name"`
	Image     string          `yaml:"image"`
	Kernel    string          `yaml:"kernel"`
	Initrd    string          `yaml:"initrd"`
	Root      RootConfig      `yaml:"root"`
	Network   NetworkConfig   `yaml:"network"`
	CloudInit CloudInitConfig `yaml:"cloudinit"`
	Shares    []Share         `yaml:"shares"`
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

func instanceRoot() string {
	return filepath.Join(home(), ".runtimes", "instances")
}

func imageRoot() string {
	return filepath.Join(home(), ".runtimes", "images")
}

func instanceDir(name string) string {
	return filepath.Join(instanceRoot(), name)
}

func lockFilePath(name string) string {
	return filepath.Join(instanceDir(name), "lockfile")
}

func loadConfig(instDir string) (*RuntimeConfig, error) {
	path := filepath.Join(instDir, "runtime.yaml")
	logDebug("Loading runtime config: %s", path)

	data, err := ioutil.ReadFile(path)
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
// MAC utilities
// -----------------------------------------------------------------------------

func generateMAC() (string, error) {
	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}

	buf[0] = 0x52
	buf[1] = 0x54
	buf[2] = (buf[2] & 0x7F) | 0x80

	mac := fmt.Sprintf("%02x:%02x:%02x:%02x:%02x:%02x",
		buf[0], buf[1], buf[2], buf[3], buf[4], buf[5])
	logDebug("Generated MAC address: %s", mac)
	return mac, nil
}

// -----------------------------------------------------------------------------
// Cloud-init ISO builder
// -----------------------------------------------------------------------------

func makeCIDATISO(cidataDir, isoPath string) error {
	logDebug("Building CIDATA ISO from %s → %s", cidataDir, isoPath)

	if runtime.GOOS == "darwin" {
		_ = os.Remove(isoPath)
		cmd := exec.Command(
			"hdiutil", "makehybrid",
			"-o", isoPath,
			cidataDir,
			"-iso", "-joliet", 
			"-default-volume-name", "CIDATA",
		)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		logDebug("Running: %s", strings.Join(cmd.Args, " "))
		return cmd.Run()
	}

	cmd := exec.Command(
		"genisoimage", "-output", isoPath,
		"-volid", "CIDATA", "-joliet", "-rock",
		cidataDir,
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	logDebug("Running: %s", strings.Join(cmd.Args, " "))
	return cmd.Run()
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
		k := strings.TrimSpace(parts[0])
		v := strings.TrimSpace(parts[1])
		switch k {
		case "name":
			info.Name = v
		case "pid":
			pid, _ := strconv.Atoi(v)
			info.Pid = pid
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
	return ioutil.WriteFile(path, []byte(content), 0o644)
}

// isPidAlive uses `ps` to check both existence and name match.
func isPidAlive(pid int, wantName string) bool {
	if pid <= 0 {
		return false
	}
	cmd := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "comm=")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return false
	}
	comm := strings.TrimSpace(string(out))
	if comm == "" {
		return false
	}
	if wantName == "" {
		return true
	}
	return comm == wantName
}

// ensureInstanceNotBusy enforces lock semantics.
// - If no lock → OK.
// - If lock with live PID → BusyError.
// - If lock with dead PID → remove stale lock, continue.
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

	if isPidAlive(info.Pid, info.Name) {
		fmt.Fprintf(os.Stderr,
			"runctl: instance %q appears to be busy (pid %d is active)\n", name, info.Pid)
		fmt.Fprintf(os.Stderr,
			"refusing to proceed to avoid filesystem corruption (EBUSY)\n")
		return &BusyError{Instance: name, Pid: info.Pid}
	}

	fmt.Fprintf(os.Stderr,
		"runctl: stale lockfile for instance %q (pid %d not running) — removing and continuing\n",
		name, info.Pid)
	_ = os.Remove(lp)
	return nil
}

// -----------------------------------------------------------------------------
// createInstance
// -----------------------------------------------------------------------------

func createInstance(name, image string) error {
	if err := ensureInstanceNotBusy(name); err != nil {
		return err
	}

	instDir := instanceDir(name)

	// Handle existing instance dir with --force (preserve cidata/)
	if st, err := os.Stat(instDir); err == nil && st.IsDir() {
		if !force {
			return fmt.Errorf("instance directory %s already exists (use --force to recreate)", instDir)
		}
		logDebug("Recreating instance %s with --force (preserving cidata/)", name)

		entries, err := os.ReadDir(instDir)
		if err != nil {
			return fmt.Errorf("failed to read instance dir %s: %w", instDir, err)
		}
		for _, e := range entries {
			if e.Name() == "cidata" {
				logDebug("Preserving %s", filepath.Join(instDir, e.Name()))
				continue
			}
			path := filepath.Join(instDir, e.Name())
			logDebug("Removing %s", path)
			_ = os.RemoveAll(path)
		}
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}

	if err := os.MkdirAll(instDir, 0o755); err != nil {
		return err
	}
	logDebug("Instance directory: %s", instDir)

	imgDir := filepath.Join(imageRoot(), image, "cloud")
	logDebug("Image directory: %s", imgDir)

	kernel := filepath.Join(imgDir, "kernel")
	initrd := filepath.Join(imgDir, "initrd")
	base := filepath.Join(imgDir, "rootfs.raw")
	overlay := filepath.Join(instDir, "rootfs.overlay")

	logDebug("Kernel:  %s", kernel)
	logDebug("Initrd:  %s", initrd)
	logDebug("Base:    %s", base)
	logDebug("Overlay: %s", overlay)

	// Create overlay file if not exists, and give it a sparse size (1 GiB)
	if _, err := os.Stat(overlay); os.IsNotExist(err) {
		logDebug("Creating overlay file: %s", overlay)

		f, err := os.Create(overlay)
		if err != nil {
			return fmt.Errorf("failed to create overlay file %s: %w", overlay, err)
		}
		if err := f.Close(); err != nil {
			return fmt.Errorf("failed to close overlay file after creation %s: %w", overlay, err)
		}

		logDebug("Truncating overlay to 1 GiB: %s", overlay)
		if err := os.Truncate(overlay, 1<<30); err != nil {
			return fmt.Errorf("failed to truncate overlay file %s to 1 GiB: %w", overlay, err)
		}
	} else if err != nil {
		return fmt.Errorf("failed to stat overlay file %s: %w", overlay, err)
	}

	// Cloud-init directory
	cidataDir := filepath.Join(instDir, "cidata")
	logDebug("Ensuring cidata directory: %s", cidataDir)
	if err := os.MkdirAll(cidataDir, 0o755); err != nil {
		return err
	}

	userDataPath := filepath.Join(cidataDir, "user-data")
	metaDataPath := filepath.Join(cidataDir, "meta-data")

	// Default user-data/meta-data, if missing
	if _, err := os.Stat(userDataPath); os.IsNotExist(err) {
		logDebug("Writing default user-data: %s", userDataPath)
		_ = ioutil.WriteFile(userDataPath, []byte("#cloud-config\n"), 0o644)
	}
	if _, err := os.Stat(metaDataPath); os.IsNotExist(err) {
		logDebug("Writing default meta-data: %s", metaDataPath)
		_ = ioutil.WriteFile(metaDataPath, []byte{}, 0o644)
	}

	isoPath := filepath.Join(instDir, "cidata.iso")
	if err := makeCIDATISO(cidataDir, isoPath); err != nil {
		return fmt.Errorf("failed to generate cidata.iso: %w", err)
	}

	// Generate MAC and store it in YAML
	mac, err := generateMAC()
	if err != nil {
		return fmt.Errorf("failed generating MAC: %w", err)
	}

	cfg := RuntimeConfig{
		Name:   name,
		Image:  image,
		Kernel: kernel,
		Initrd: initrd,
		Root: RootConfig{
			Base: filepath.Join(instDir, "rootfs.raw"),
		},
		Network: NetworkConfig{
			MAC:  mac,
			Mode: "nat",
		},
		CloudInit: CloudInitConfig{
			UserData: userDataPath,
			MetaData: metaDataPath,
			CIDATA:   isoPath,
		},
		Shares: []Share{},
	}

	data, err := yaml.Marshal(&cfg)
	if err != nil {
		return err
	}

	runtimePath := filepath.Join(instDir, "runtime.yaml")
	logDebug("Writing runtime.yaml: %s", runtimePath)
	if err := ioutil.WriteFile(runtimePath, data, 0o644); err != nil {
		return err
	}

	fmt.Println("Created instance", name, "using image", image)
	return nil
}

// -----------------------------------------------------------------------------
// startInstance with name + lock + signals
// -----------------------------------------------------------------------------

func startInstance(name string) error {
	if err := ensureInstanceNotBusy(name); err != nil {
		return err
	}

	instDir := instanceDir(name)
	logDebug("Starting instance: %s (dir: %s)", name, instDir)

	cfg, err := loadConfig(instDir)
	if err != nil {
		return err
	}

	// Rebuild CIDATA iso in case user-data/meta-data changed
	cidataDir := filepath.Dir(cfg.CloudInit.UserData)
	logDebug("Regenerating CIDATA ISO from dir: %s", cidataDir)
	if err := makeCIDATISO(cidataDir, cfg.CloudInit.CIDATA); err != nil {
		return fmt.Errorf("failed regenerating CIDATA ISO: %w", err)
	}

	netArg := fmt.Sprintf("%s@%s", cfg.Network.MAC, cfg.Network.Mode)
	logDebug("Network argument: %s", netArg)

	// Arguments to runvm (or future crosvm on Linux)
	args := []string{
		"--name", cfg.Name,
		"--kernel", cfg.Kernel,
		"--initrd", cfg.Initrd,
		"--rwdisk", cfg.Root.Base,
		//		"--rwdisk", cfg.Root.Overlay,
		"--disk", cfg.CloudInit.CIDATA,
		"--net", netArg,
		"--params", "console=hvc0 root=/dev/vda rootfstype=ext4 rw rootwait",
	}

	for _, s := range cfg.Shares {
		arg := fmt.Sprintf("%s:%s", s.Host, s.Guest)
		logDebug("Adding shared-dir (folder): %s", arg)
		args = append(args, "--folder", arg)
	}

	instDirAbs, err := filepath.Abs(instDir)
	if err != nil {
		return err
	}

	// Symlink for process name semantics
	targetBin := "/usr/local/bin/runvm"
	if runtime.GOOS != "darwin" {
		// placeholder for future crosvm; for now, still runvm if present
		targetBin = "/usr/local/bin/runvm"
	}

	exeName := cfg.Name
	exePath := filepath.Join(instDirAbs, exeName)

	_ = os.Remove(exePath)
	if err := os.Symlink(targetBin, exePath); err != nil {
		return fmt.Errorf("failed to symlink %s → %s: %w", exePath, targetBin, err)
	}

	cmd := exec.Command("./"+exeName, args...)
	cmd.Dir = instDirAbs
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin

	logDebug("Executing in %s: ./%s %s", instDirAbs, exeName, strings.Join(args, " "))

	if err := cmd.Start(); err != nil {
		return err
	}
	childPid := cmd.Process.Pid
	fmt.Printf("Started %s (pid %d)\n", cfg.Name, childPid)

	// Write lockfile with VM PID
	lp := lockFilePath(name)
	if err := writeLock(lp, cfg.Name, childPid); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to write lockfile: %v\n", err)
	}

	// Nanny: handle signals and wait for child
	err = nannyWait(cmd, lp, childPid)
	return err
}

func nannyWait(cmd *exec.Cmd, lockPath string, childPid int) error {
	waitCh := make(chan error, 1)
	go func() {
		waitCh <- cmd.Wait()
	}()

	sigCh := make(chan os.Signal, 8)
	signal.Notify(sigCh,
		syscall.SIGINT,
		syscall.SIGTERM,
		syscall.SIGHUP,
		syscall.SIGTSTP,
		syscall.SIGCONT,
	)

	for {
		select {
		case err := <-waitCh:
			_ = os.Remove(lockPath)
			if err != nil {
				if exitErr, ok := err.(*exec.ExitError); ok {
					if status, ok := exitErr.Sys().(syscall.WaitStatus); ok {
						if status.Signaled() {
							return fmt.Errorf("vm exited on signal %s", status.Signal())
						}
						return fmt.Errorf("vm exited with status %d", status.ExitStatus())
					}
				}
				return err
			}
			return nil

		case s := <-sigCh:
			logDebug("Received signal: %s", s.String())
			switch s {
			case syscall.SIGINT, syscall.SIGTERM:
				_ = syscall.Kill(childPid, s.(syscall.Signal))
			case syscall.SIGHUP:
				_ = syscall.Kill(childPid, syscall.SIGSTOP)
			case syscall.SIGTSTP:
				_ = syscall.Kill(childPid, syscall.SIGSTOP)
				_ = syscall.Kill(os.Getpid(), syscall.SIGSTOP)
			case syscall.SIGCONT:
				_ = syscall.Kill(childPid, syscall.SIGCONT)
			}
		}
	}
}

// -----------------------------------------------------------------------------
// Stop / Pause / Resume / List
// -----------------------------------------------------------------------------

func stopInstance(name string) error {
	lp := lockFilePath(name)
	info, err := readLock(lp)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Println("Instance", name, "not running (no lockfile)")
			return nil
		}
		fmt.Fprintf(os.Stderr, "warning: cannot read lockfile: %v\n", err)
		return nil
	}

	if isPidAlive(info.Pid, info.Name) {
		logDebug("Stopping instance %s (pid %d)", name, info.Pid)
		return syscall.Kill(info.Pid, syscall.SIGTERM)
	}

	fmt.Printf("Instance %s not running (stale lockfile); removing lock\n", name)
	_ = os.Remove(lp)
	return nil
}

func pauseInstance(name string) error {
	lp := lockFilePath(name)
	info, err := readLock(lp)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Println("Instance", name, "not running (no lockfile)")
			return nil
		}
		return err
	}

	if isPidAlive(info.Pid, info.Name) {
		logDebug("Pausing instance %s (pid %d)", name, info.Pid)
		return syscall.Kill(info.Pid, syscall.SIGSTOP)
	}

	fmt.Printf("Instance %s not running (stale lockfile); removing lock\n", name)
	_ = os.Remove(lp)
	return nil
}

func resumeInstance(name string) error {
	lp := lockFilePath(name)
	info, err := readLock(lp)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Println("Instance", name, "not running (no lockfile)")
			return nil
		}
		return err
	}

	if isPidAlive(info.Pid, info.Name) {
		logDebug("Resuming instance %s (pid %d)", name, info.Pid)
		return syscall.Kill(info.Pid, syscall.SIGCONT)
	}

	fmt.Printf("Instance %s not running (stale lockfile); removing lock\n", name)
	_ = os.Remove(lp)
	return nil
}

func listInstances() error {
	root := instanceRoot()
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Println("No instances.")
			return nil
		}
		return err
	}

	fmt.Printf("%-20s %-10s %-8s\n", "NAME", "STATE", "PID")
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		lp := lockFilePath(name)
		info, err := readLock(lp)
		if err != nil {
			if os.IsNotExist(err) {
				fmt.Printf("%-20s %-10s %-8s\n", name, "stopped", "-")
				continue
			}
			fmt.Printf("%-20s %-10s %-8s\n", name, "error", "-")
			continue
		}
		if isPidAlive(info.Pid, info.Name) {
			fmt.Printf("%-20s %-10s %-8d\n", name, "running", info.Pid)
		} else {
			fmt.Printf("%-20s %-10s %-8s\n", name, "stopped", "-")
		}
	}
	return nil
}

// -----------------------------------------------------------------------------
// CLI
// -----------------------------------------------------------------------------

func usage() {
	fmt.Println("usage:")
	fmt.Println("  runctl [--debug] [--force] create <instance> --image <image-name>")
	fmt.Println("  runctl [--debug]          start  <instance>")
	fmt.Println("  runctl [--debug]          stop   <instance>")
	fmt.Println("  runctl [--debug]          pause  <instance>")
	fmt.Println("  runctl [--debug]          resume <instance>")
	fmt.Println("  runctl [--debug]          list")
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	args := os.Args[1:]
	for len(args) > 0 && strings.HasPrefix(args[0], "-") {
		switch args[0] {
		case "--debug":
			debug = true
		case "--force":
			force = true
		default:
			fmt.Fprintf(os.Stderr, "unknown global option: %s\n", args[0])
			usage()
			os.Exit(1)
		}
		args = args[1:]
	}

	if len(args) < 1 {
		usage()
		os.Exit(1)
	}

	cmd := args[0]
	args = args[1:]

	var err error

	switch cmd {
	case "create":
		if len(args) < 3 {
			fmt.Println("missing arguments for create")
			usage()
			os.Exit(1)
		}
		name := args[0]
		if args[1] != "--image" {
			fmt.Println("missing --image for create")
			usage()
			os.Exit(1)
		}
		image := args[2]
		err = createInstance(name, image)

	case "start":
		if len(args) < 1 {
			fmt.Println("missing instance for start")
			usage()
			os.Exit(1)
		}
		name := args[0]
		err = startInstance(name)

	case "stop":
		if len(args) < 1 {
			fmt.Println("missing instance for stop")
			usage()
			os.Exit(1)
		}
		name := args[0]
		err = stopInstance(name)

	case "pause":
		if len(args) < 1 {
			fmt.Println("missing instance for pause")
			usage()
			os.Exit(1)
		}
		name := args[0]
		err = pauseInstance(name)

	case "resume":
		if len(args) < 1 {
			fmt.Println("missing instance for resume")
			usage()
			os.Exit(1)
		}
		name := args[0]
		err = resumeInstance(name)

	case "list":
		err = listInstances()

	default:
		fmt.Println("unknown command:", cmd)
		usage()
		os.Exit(1)
	}

	if err != nil {
		if be, ok := err.(*BusyError); ok {
			fmt.Fprintln(os.Stderr, "error:", be.Error())
			os.Exit(16) // EBUSY
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
