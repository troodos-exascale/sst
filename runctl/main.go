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

type RuntimeConfig struct {
	Name    string `yaml:"name"`
	Boot    string `yaml:"boot"` // "kernel" or "efi"
	Disk    string `yaml:"disk"`
	EFIVars string `yaml:"efi_vars"`
	Params  string `yaml:"params"`

	Shares []Share `yaml:"shares"`

	Network struct {
		MAC  string `yaml:"mac"`
		Mode string `yaml:"mode"`
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

func instanceRoot() string { return filepath.Join(home(), ".runtimes", "instances") }
func instanceDir(name string) string { return filepath.Join(instanceRoot(), name) }
func lockFilePath(name string) string { return filepath.Join(instanceDir(name), "lockfile") }

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

func (e *BusyError) Error() string { return fmt.Sprintf("instance %q is busy (pid %d active)", e.Instance, e.Pid) }

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
	return comm == wantName
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

	if isPidAlive(info.Pid, info.Name) {
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
// runctl create --efi <image-file> <name>
// -----------------------------------------------------------------------------

func createInstanceEFI(imageFile, name string) error {
	if err := ensureInstanceNotBusy(name); err != nil {
		return err
	}

	instDir := instanceDir(name)
	if st, err := os.Stat(instDir); err == nil && st.IsDir() {
		if !force {
			return fmt.Errorf("instance %s exists (use --force)", name)
		}
		_ = os.RemoveAll(instDir)
	}
	if err := os.MkdirAll(instDir, 0755); err != nil {
		return err
	}

	// 1) Copy disk.raw (sparse)
	dstDisk := filepath.Join(instDir, "disk.raw")
	src, err := os.Open(imageFile)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.Create(dstDisk)
	if err != nil {
		return err
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		return err
	}
	dst.Close()

	// 2) Create empty EFI vars
	efiVars := filepath.Join(instDir, "efi-vars")
	_ = ioutil.WriteFile(efiVars, []byte{}, 0644)

	// 3) Create 30GB sparse data.img
	dataImg := filepath.Join(instDir, "data.img")
	if err := createSparse(dataImg, 30*1024*1024*1024); err != nil {
		return err
	}

	// 4) Add share /Users/$USER -> /Users/$USER
	u, _ := user.Current()
	hostShare := filepath.Join("/Users", u.Username)
	guestShare := filepath.Join("/Users", u.Username)

	// 5) Verbose kernel params (still stored even if EFI mode)
	params := "console=hvc0 loglevel=7 systemd.log_level=debug systemd.log_target=console"

	cfg := RuntimeConfig{
		Name:    name,
		Boot:    "efi",
		Disk:    dstDisk,
		EFIVars: efiVars,
		Params:  params,
		Shares: []Share{
			{Host: hostShare, Guest: guestShare},
		},
	}
	cfg.Network.MAC, _ = generateMAC()
	cfg.Network.Mode = "nat"

	data, err := yaml.Marshal(&cfg)
	if err != nil {
		return err
	}
	return ioutil.WriteFile(filepath.Join(instDir, "runtime.yaml"), data, 0644)
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

	args := []string{
		"--name", cfg.Name,
		"--net", fmt.Sprintf("%s@%s", cfg.Network.MAC, cfg.Network.Mode),
	}

	if cfg.Boot == "efi" {
		args = append(args,
			"--bootloader", "efi",
			"--efi-vars", cfg.EFIVars,
			"--rwdisk", cfg.Disk,
			"--rwdisk", filepath.Join(instDir, "data.img"),
		)
	} else {
		args = append(args,
			"--bootloader", "linux",
			"--kernel", cfg.Disk,
			"--params", cfg.Params,
		)
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
			case syscall.SIGHUP, syscall.SIGTSTP:
				_ = syscall.Kill(pid, syscall.SIGSTOP)
			case syscall.SIGCONT:
				_ = syscall.Kill(pid, syscall.SIGCONT)
			}
		}
	}
}

// -----------------------------------------------------------------------------
// stop/pause/resume/list
// -----------------------------------------------------------------------------

func stopInstance(name string) error {
	lp := lockFilePath(name)
	info, err := readLock(lp)
	if err != nil {
		fmt.Printf("Instance %s not running\n", name)
		return nil
	}
	if isPidAlive(info.Pid, info.Name) {
		return syscall.Kill(info.Pid, syscall.SIGTERM)
	}
	_ = os.Remove(lp)
	fmt.Printf("Instance %s (stale lock removed)\n", name)
	return nil
}

func pauseInstance(name string) error {
	lp := lockFilePath
