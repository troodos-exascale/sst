package rtsst

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	sst "github.com/swiss-software-tool/runtime-smart/go/internal/sst"
	"github.com/swiss-software-tool/runtime-smart/go/proto/yaml/runctl/v1"
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

// GetLocalPublicKey attempts to find a valid SSH public key in the current user's ~/.ssh directory.
// It checks standard key names in priority order (ed25519, rsa, ecdsa, dsa).
func GetLocalPublicKey() (string, error) {
	u, err := user.Current()
	if err != nil {
		return "", fmt.Errorf("cannot determine current user: %w", err)
	}

	sshDir := filepath.Join(u.HomeDir, ".ssh")

	// Priority order matches standard ssh preferences
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

	return "", fmt.Errorf("no usable SSH public key found in %s", sshDir)
}

func WriteCIData(ctx context.Context, log *sst.Logx, instanceDir string, inst *runctlpb.Instance) error {
	cidataDir := filepath.Join(instanceDir, "cidata")
	if err := os.MkdirAll(cidataDir, 0755); err != nil {
		return fmt.Errorf("failed to create cidata dir: %w", err)
	}

	pubKey, err := GetLocalPublicKey()
	if err != nil {
		return fmt.Errorf("failed to get user's local public key: %w", err)
	}

	user, err := user.Current()
	if err != nil {
		return fmt.Errorf("failed to get current user: %w", err)
	}

	// ---- Prepare mounts ----
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

		tag := parts[0]
		guestPath := tag
		if len(parts) > 1 && parts[1] != "" {
			guestPath = parts[1]
		}

		opts := "rw"
		if len(parts) > 2 && parts[2] == "ro" {
			opts = "ro"
		}

		mounts = append(mounts, mountInfo{
			tag:       tag,
			guestPath: guestPath,
			options:   opts,
		})
	}

	// ---- Build user-data ----
	var sb strings.Builder

	sb.WriteString("#cloud-config\n")
	sb.WriteString("# syntax=yaml\n\n")

	fmt.Fprintf(&sb, "hostname: %s\n", inst.Hostname)
	sb.WriteString("lock_passwd: false\n")
	sb.WriteString("ssh_pwauth: false\n")
	sb.WriteString("disable_root: true\n\n")

	// 1. Boot Commands: Fix the GPT Table
	// This runs before filesystems are mounted read-write, using built-in tools if available.
	// Most cloud images include sgdisk/partprobe. If missing, this might fail harmlessly on first boot.
	sb.WriteString("bootcmd:\n")
	sb.WriteString("  - [ cloud-init-per, once, move-gpt-header, sgdisk, -e, /dev/vda ]\n")
	sb.WriteString("  - [ cloud-init-per, once, reread-pt, partprobe ]\n")

	// 2. Configure Users
	sb.WriteString("\nusers:\n")
	sb.WriteString("  - default\n")
	fmt.Fprintf(&sb, "  - name: %s\n", user.Username)
	if user.Name != "" {
		fmt.Fprintf(&sb, "    gecos: %s\n", user.Name)
	}
	sb.WriteString("    sudo: ALL=(ALL) NOPASSWD:ALL\n")
	sb.WriteString("    shell: /bin/bash\n")
	if pubKey != "" {
		sb.WriteString("    ssh_authorized_keys:\n")
		sb.WriteString("      - " + pubKey + "\n")
	}

	// 3. Set Passwords
	sb.WriteString("\nchpasswd:\n")
	sb.WriteString("  list: |\n")
	fmt.Fprintf(&sb, "    %s:abcd1234\n", user.Username)
	sb.WriteString("  expire: False\n")

	// 4. Mounts
	if len(mounts) > 0 {
		sb.WriteString("\n# Mount virtiofs shares\n")
		sb.WriteString("mounts:\n")
		for _, m := range mounts {
			fmt.Fprintf(
				&sb,
				"  - [ \"%s\", \"%s\", \"virtiofs\", \"%s\", \"0\", \"0\" ]\n",
				m.tag,
				m.guestPath,
				m.options,
			)
		}
	}

	// 5. Run Commands (Polyglot Install + Resize)
	sb.WriteString("\nruncmd:\n")

	// --- C. Alpine setup script ---
	sb.WriteString("  - |\n")
	sb.WriteString(fmt.Sprintf(`    if [ -f /etc/alpine-release ]; then
      mkdir -p /etc/conf.d
      echo 'TTY="hvc0"' > /etc/conf.d/ttyAMA0
      sed -i 's/^ttyAMA0/#ttyAMA0/' /etc/inittab
      apk add --no-cache sudo bash shadow
      usermod -s /bin/bash %s
      kill -HUP 1
    fi
`, user.Username))

	// --- D. Ensure mountpoints ---
	for _, m := range mounts {
		sb.WriteString("  - |\n")
		fmt.Fprintf(&sb, "    mkdir -p %s\n", m.guestPath)
	}

	userData := sb.String()

	// ---- meta-data ----
	metaData := fmt.Sprintf(
		"instance-id: %s\nlocal-hostname: %s\n",
		inst.Hostname,
		inst.Hostname,
	)

	// ---- Write files ----
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

// CreateLock attempts to create a lock file exclusively.
// Returns an error if the lock already exists.
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

// Remove deletes the lock file.
func (l *Lock) Remove() {
	_ = os.Remove(l.Path)
}

// EnsureCIDataISO ensures that cidata.iso exists and is up to date.
// If user-data or meta-data is newer than the ISO, it regenerates the ISO
// and writes a new instance-id into meta-data so cloud-init re-runs.
//
// The instance-id is human-readable and collision-free.
func EnsureCIDataISO(ctx context.Context, log *sst.Logx, instDir string) error {
	cidataDir := filepath.Join(instDir, "cidata")
	isoPath := filepath.Join(instDir, "cidata.iso")
	userDataPath := filepath.Join(cidataDir, "user-data")
	metaDataPath := filepath.Join(cidataDir, "meta-data")

	// --- decide whether rebuild is needed ---
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

	// --- write fresh meta-data with new instance-id ---
	// Cloud-init accepts any string; use readable + monotonic format.
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

	// --- rebuild cidata.iso ---
	_ = os.Remove(isoPath) // ignore error; hdiutil will fail if it matters
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

	// Try macOS tool first
	if _, err := exec.LookPath("hdiutil"); err == nil {
		cmd := exec.Command(
			"hdiutil", "makehybrid",
			"-o", iso,
			src,
			"-joliet", "-iso",
			"-default-volume-name", "cidata",
		)
		// Capture output to prevent log spam unless error
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("hdiutil failed: %v\nOutput: %s", err, string(out))
		}
		return nil
	}

	// Fallback to Linux tool
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
