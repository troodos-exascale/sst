package rtsst

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	sst "github.com/swiss-software-tool/sst/internal/sst"
	runctlpb "github.com/swiss-software-tool/sst/proto/yaml/runctl/v1"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func Home() string {
	if h := os.Getenv("HOME"); h != "" {
		return h
	}
	u, err := user.Current()
	if err != nil {
		panic(err)
	}
	return u.HomeDir
}

func RuntimeDir() string {
	return filepath.Join(Home(), ".runtimes")
}

func InstanceRoot() string {
	return filepath.Join(RuntimeDir(), "instances")
}

// InstanceDir resolves the absolute path to a runtime instance.
// It checks the current working directory first, then falls back to the global ~/.runtimes catalog.
func InstanceDir(name string) string {
	// 1. Local CWD check
	if cwd, err := os.Getwd(); err == nil {
		// Check if we are at the context root (./instances/<name>)
		localPath := filepath.Join(cwd, "instances", name)
		if _, err := os.Stat(filepath.Join(localPath, "instance.yaml")); err == nil {
			return localPath
		}
		// Check if we are already sitting directly inside the instance folder
		if _, err := os.Stat(filepath.Join(cwd, "instance.yaml")); err == nil {
			if filepath.Base(cwd) == name {
				return cwd
			}
		}
	}

	// 2. Global Catalog check (~/.runtimes/instances/<name> -> symlink to host context)
	globalPath := filepath.Join(Home(), ".runtimes", "instances", name)
	if info, err := os.Lstat(globalPath); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			if realPath, err := filepath.EvalSymlinks(globalPath); err == nil {
				return realPath
			}
		} else {
			return globalPath // Legacy fallback if it's an actual directory
		}
	}

	// Fallback to CWD to allow standard error surfacing
	cwd, _ := os.Getwd()
	return filepath.Join(cwd, "instances", name)
}

func LockFilePath(name string) string {
	return filepath.Join(InstanceDir(name), "lockfile")
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
		// Corrected: added the "=" separator back in
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

func GetLocalPublicKey() (string, error) {
	if pk := os.Getenv("SST_HOST_PUBKEY"); pk != "" {
		return pk, nil
	}

	u, err := user.Current()
	if err != nil {
		return "", fmt.Errorf("cannot determine current user: %w", err)
	}

	sshDir := filepath.Join(u.HomeDir, ".ssh")

	pubCandidates := []string{
		filepath.Join(sshDir, "id_ed25519.pub"),
		filepath.Join(sshDir, "id_rsa.pub"),
		filepath.Join(sshDir, "id_ecdsa.pub"),
		filepath.Join(sshDir, "id_dsa.pub"),
	}

	for _, p := range pubCandidates {
		b, err := os.ReadFile(p)
		if err == nil {
			key := strings.TrimSpace(string(b))
			if len(key) > 0 {
				return key, nil
			}
		}
	}

	return "", nil
}

func WriteCIData(ctx context.Context, log *sst.Logx, instanceDir string, inst *runctlpb.Instance, username string) error {
	if username == "" || username == "root" {
		return fmt.Errorf("invalid username '%s' for cloud-init: cannot be empty or root", username)
	}

	cidataDir := filepath.Join(instanceDir, "cidata")
	if err := os.MkdirAll(cidataDir, 0755); err != nil {
		return fmt.Errorf("failed to create cidata dir: %w", err)
	}

	pubKey, err := GetLocalPublicKey()
	if err != nil {
		return fmt.Errorf("failed to get user's local public key: %w", err)
	}

	type mountInfo struct {
		tag       string
		guestPath string
		options   string
	}
	var mounts []mountInfo

	for _, shareStr := range inst.SharedDir {
		parts := strings.Split(shareStr, ":")
		if len(parts) == 0 || parts[0] == "" {
			continue
		}

		hostDir := parts[0]
		tag := hostDir
		if len(parts) > 1 && parts[1] != "" {
			tag = parts[1]
		}

		guestPath := tag
		opts := "rw"
		if len(parts) > 2 && parts[2] == "ro" {
			opts = "ro"
		}
		mounts = append(mounts, mountInfo{tag: tag, guestPath: guestPath, options: opts})
	}

	var sb strings.Builder

	sb.WriteString("#cloud-config\n")
	sb.WriteString("# syntax=yaml\n\n")

	fmt.Fprintf(&sb, "hostname: %s\n", inst.Hostname)
	fmt.Fprintf(&sb, "local-hostname: %s\n", inst.Hostname)
	sb.WriteString("lock_passwd: false\n")
	sb.WriteString("ssh_pwauth: false\n")
	sb.WriteString("disable_root: true\n\n")

	sb.WriteString("\nusers:\n")
	sb.WriteString("  - default\n")
	fmt.Fprintf(&sb, "  - name: %s\n", username)
	sb.WriteString("    shell: /bin/bash\n")
	sb.WriteString("    sudo: ALL=(ALL) NOPASSWD:ALL\n")
	if pubKey != "" {
		sb.WriteString("    ssh_authorized_keys:\n")
		sb.WriteString("      - " + pubKey + "\n")
	}

	// NEW: bootcmd runs on EVERY boot, ensuring tmpfs directories survive reboots
	sb.WriteString("\nbootcmd:\n")
	sb.WriteString("  - mkdir -p /run/sshd\n")
	sb.WriteString("  - chmod 0755 /run/sshd\n")

	sb.WriteString("\nruncmd:\n")
	sb.WriteString("  - |\n")

	sb.WriteString(fmt.Sprintf(`    if [ -f /etc/alpine-release ]; then
      mkdir -p /etc/conf.d
      echo 'TTY="hvc0"' > /etc/conf.d/ttyAMA0
      sed -i 's/^ttyAMA0/#ttyAMA0/' /etc/inittab
      
      # Ensure shell is set using sed since bash/usermod are removed
      sed -i -e "s#^\(%s:.*:\)[^:]*\$#\1/bin/sh#" /etc/passwd
      
      # Explicit passwordless doas configuration
      mkdir -p /etc/doas.d
      echo "permit nopass %s as root" > /etc/doas.d/%s.conf
      
      # Force Hostname
      hostname %s
      echo "%s" > /etc/hostname
      
      kill -HUP 1
    fi

    # Proactively announce presence to populate the host's ARP table
    GW=$(ip route | awk '/default/ {print $3}' | head -n 1)
    if [ -n "$GW" ]; then ping -c 3 "$GW" >/dev/null 2>&1 || true; fi
`, username, username, username, inst.Hostname, inst.Hostname))

	for _, m := range mounts {
		sb.WriteString("  - |\n")
		fmt.Fprintf(&sb, "    mkdir -p %s\n", m.guestPath)
		fmt.Fprintf(&sb, "    mount -t virtiofs %s %s -o %s\n", m.tag, m.guestPath, m.options)
	}

	userData := sb.String()
	metaData := fmt.Sprintf("instance-id: %s\nlocal-hostname: %s\n", inst.Hostname, inst.Hostname)

	if err := os.WriteFile(filepath.Join(cidataDir, "user-data"), []byte(userData), 0644); err != nil {
		return fmt.Errorf("failed to write user-data: %w", err)
	}
	if err := os.WriteFile(filepath.Join(cidataDir, "meta-data"), []byte(metaData), 0644); err != nil {
		return fmt.Errorf("failed to write meta-data: %w", err)
	}

	log.Info(ctx, "cidata.written", "dir", cidataDir)
	return nil
}

type Lock struct {
	Path string
}

func CreateLock(lockPath string) (*Lock, error) {
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("lock exists at %s (use --force to override)", lockPath)
	}

	pid := os.Getpid()
	hostname, _ := os.Hostname()
	timestamp := time.Now().UTC().Format(time.RFC3339)

	fmt.Fprintf(f, "pid=%d\nhost=%s\ntime=%s\n", pid, hostname, timestamp)
	f.Close()

	return &Lock{Path: lockPath}, nil
}

func (l *Lock) Remove() {
	_ = os.Remove(l.Path)
}

func EnsureCIDataISO(ctx context.Context, log *sst.Logx, instDir string) error {
	cidataDir := filepath.Join(instDir, "cidata")
	isoPath := filepath.Join(instDir, "cidata.iso")
	userDataPath := filepath.Join(cidataDir, "user-data")
	metaDataPath := filepath.Join(cidataDir, "meta-data")

	needsRebuild := false

	isoInfo, err := os.Stat(isoPath)
	if os.IsNotExist(err) {
		needsRebuild = true
		log.Info(ctx, "cidata.iso missing, generating")
	} else if err != nil {
		return fmt.Errorf("stat cidata.iso: %w", err)
	} else {
		if ui, err := os.Stat(userDataPath); err == nil && ui.ModTime().After(isoInfo.ModTime()) {
			needsRebuild = true
			log.Info(ctx, "user-data newer than cidata.iso, regenerating")
		}
		if mi, err := os.Stat(metaDataPath); err == nil && mi.ModTime().After(isoInfo.ModTime()) {
			needsRebuild = true
			log.Info(ctx, "meta-data newer than cidata.iso, regenerating")
		}
	}

	if !needsRebuild {
		return nil
	}

	instanceID := "iid-" + time.Now().UTC().Format("2006-01-02-15-04-05.000000000")
	hostname := filepath.Base(instDir)

	metaData := fmt.Sprintf(
		"instance-id: %s\nlocal-hostname: %s\n",
		instanceID,
		hostname,
	)

	if err := os.WriteFile(metaDataPath, []byte(metaData), 0644); err != nil {
		return fmt.Errorf("write meta-data: %w", err)
	}

	log.Info(ctx, "Updated instance-id", "instance_id", instanceID)

	_ = os.Remove(isoPath)
	if err := UpdateCIDATAISO(instDir); err != nil {
		log.Error(ctx, "hdiutil/geniso makehybrid failed", "output")
		return fmt.Errorf("Update cidata.iso: %w", err)
	}

	log.Info(ctx, "cidata.iso regenerated successfully")
	return nil
}

func UpdateCIDATAISO(instDir string) error {
	iso := filepath.Join(instDir, "cidata.iso")
	src := filepath.Join(instDir, "cidata")

	if _, err := exec.LookPath("hdiutil"); err == nil {
		cmd := exec.Command(
			"hdiutil", "makehybrid",
			"-o", iso,
			src,
			"-joliet", "-iso",
			"-default-volume-name", "cidata",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("hdiutil failed: %v\nOutput: %s", err, string(out))
		}
		return nil
	}

	if _, err := exec.LookPath("genisoimage"); err == nil {
		cmd := exec.Command(
			"genisoimage",
			"-output", iso,
			"-volid", "cidata",
			"-joliet",
			"-rock",
			src,
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("genisoimage failed: %v\nOutput: %s", err, string(out))
		}
		return nil
	}

	return fmt.Errorf("neither 'hdiutil' nor 'genisoimage' found in PATH")
}
