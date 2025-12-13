package rtsst

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"os/user"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func Home() string {
	u, err := user.Current()
	if err != nil {
		panic(err)
	}
	return u.HomeDir
}

type LockInfo struct {
	Name string
	Pid  int
}

func ReadLock(path string) (*LockInfo, error) {
	data, err := os.ReadFile(path)
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

func WriteLock(path, name string, pid int) error {
	content := fmt.Sprintf("name=%s\npid=%d\ncreated_at=%s\n",
		name, pid, time.Now().UTC().Format(time.RFC3339))
	return os.WriteFile(path, []byte(content), 0644)
}

func InstanceRoot() string {
	return filepath.Join(Home(), ".runtimes", "instances")
}

func InstanceDir(name string) string {
	return filepath.Join(InstanceRoot(), name)
}

func LockFilePath(name string) string {
	return filepath.Join(InstanceDir(name), "lockfile")
}

func EnsureInstanceNotBusy(name string) error {
	lp := LockFilePath(name)
	_, err := os.Stat(lp)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}

	info, err := ReadLock(lp)
	if err != nil {
		return fmt.Errorf("cannot parse lockfile %s: %w", lp, err)
	}

	if IsPidAlive(info.Pid) {
		return &BusyError{Instance: name, Pid: info.Pid}
	}

	_ = os.Remove(lp)
	return nil
}

func IsPidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	// Send signal 0 → check existence without affecting the process.
	err := syscall.Kill(pid, 0)
	return err == nil
}

type BusyError struct {
	Instance string
	Pid      int
}

func (e *BusyError) Error() string {
	return fmt.Sprintf("instance %q is busy (pid %d active)", e.Instance, e.Pid)
}

func IsQCOW2(path string) bool {
	l := strings.ToLower(path)
	return strings.HasSuffix(l, ".qcow2") || strings.HasSuffix(l, ".qcow")
}

func LogDebug(debug bool, format string, args ...interface{}) {
	if debug {
		fmt.Fprintf(os.Stderr, "[runctl] "+format+"\n", args...)
	}
}

func GenerateMAC() (string, error) {
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

func WriteCIData(instDir string, hostname string) error {

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
network:
  config: disabled
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
  - touch /etc/cloud/cloud-init.disabled
`,
		hostname, // hostname
		username, // user
		gecos,    // gecos
		pubKey,   // ssh key
		username, // /Users/<user>
		username, // owner
		username, // group
		username, // /Users/<user>
	)

	if err := os.WriteFile(filepath.Join(cidataDir, "user-data"),
		[]byte(userData), 0644); err != nil {
		return fmt.Errorf("writing user-data failed: %w", err)
	}

	// --- Write meta-data ---
	meta := fmt.Sprintf("instance-id: %s\nlocal-hostname: %s\n",
		hostname, hostname)

	if err := os.WriteFile(filepath.Join(cidataDir, "meta-data"),
		[]byte(meta), 0644); err != nil {
		return fmt.Errorf("writing meta-data failed: %w", err)
	}

	return nil
}

func UpdateCIDATAISO(instDir string) error {
	iso := filepath.Join(instDir, "cidata.iso")
	src := filepath.Join(instDir, "cidata")

	isoInfo, err := os.Stat(iso)
	isoTime := time.Time{}
	if err == nil {
		isoTime = isoInfo.ModTime()
	}

	needsBuild := false

	err = filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && info.ModTime().After(isoTime) {
			needsBuild = true
		}
		return nil
	})
	if err != nil {
		return err
	}

	if !needsBuild {
		return nil
	}

	cmd := exec.Command(
		"hdiutil", "makehybrid",
		"-o", iso,
		src,
		"-hfs", "-joliet", "-iso",
		"-default-volume-name", "cidata",
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}
