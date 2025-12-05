package main

import (
	"crypto/rand"
	"fmt"
	"gopkg.in/yaml.v3"
	"io/ioutil"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
)

//
// ------------------------------------------------------------
// Global flags
// ------------------------------------------------------------
//

var (
	debug bool
	force bool
)

func logDebug(format string, args ...interface{}) {
	if debug {
		fmt.Printf("[runctl] "+format+"\n", args...)
	}
}

//
// ------------------------------------------------------------
// Types
// ------------------------------------------------------------
//

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

//
// ------------------------------------------------------------
// Runtime paths
// ------------------------------------------------------------
//

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

//
// ------------------------------------------------------------
// MAC utilities
// ------------------------------------------------------------
//

func generateMAC() (string, error) {
	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}

	// QEMU/crosvm-friendly MAC: 52:54:xx:xx:xx:xx (locally administered)
	buf[0] = 0x52
	buf[1] = 0x54
	buf[2] = (buf[2] & 0x7F) | 0x80

	mac := fmt.Sprintf("%02x:%02x:%02x:%02x:%02x:%02x",
		buf[0], buf[1], buf[2], buf[3], buf[4], buf[5])
	logDebug("Generated MAC address: %s", mac)
	return mac, nil
}

//
// ------------------------------------------------------------
// Cloud-init ISO builder
// ------------------------------------------------------------
//

func makeCIDATISO(cidataDir, isoPath string) error {
	logDebug("Building CIDATA ISO from %s → %s", cidataDir, isoPath)

	if runtime.GOOS == "darwin" {
		_ = os.Remove(isoPath)
		cmd := exec.Command(
			"hdiutil", "makehybrid",
			"-o", isoPath,
			cidataDir,
			"-hfs", "-joliet", "-iso",
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

//
// ------------------------------------------------------------
// createInstance
// ------------------------------------------------------------
//

func createInstance(name, image string) error {
	instDir := filepath.Join(instanceRoot(), name)

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

	if err := os.MkdirAll(instDir, 0755); err != nil {
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
		if err := os.Truncate(overlay, 1<<30); err != nil { // 1 GiB
			return fmt.Errorf("failed to truncate overlay file %s to 1 GiB: %w", overlay, err)
		}
	} else if err != nil {
		return fmt.Errorf("failed to stat overlay file %s: %w", overlay, err)
	}

	// Cloud-init directory
	cidataDir := filepath.Join(instDir, "cidata")
	logDebug("Ensuring cidata directory: %s", cidataDir)
	if err := os.MkdirAll(cidataDir, 0755); err != nil {
		return err
	}

	userDataPath := filepath.Join(cidataDir, "user-data")
	metaDataPath := filepath.Join(cidataDir, "meta-data")

	// Default user-data/meta-data, if missing
	if _, err := os.Stat(userDataPath); os.IsNotExist(err) {
		logDebug("Writing default user-data: %s", userDataPath)
		_ = ioutil.WriteFile(userDataPath, []byte("#cloud-config\n"), 0644)
	}
	if _, err := os.Stat(metaDataPath); os.IsNotExist(err) {
		logDebug("Writing default meta-data: %s", metaDataPath)
		_ = ioutil.WriteFile(metaDataPath, []byte{}, 0644)
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
			Base:    base,
			Overlay: overlay,
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
	if err := ioutil.WriteFile(runtimePath, data, 0644); err != nil {
		return err
	}

	fmt.Println("Created instance", name, "using image", image)
	return nil
}

//
// ------------------------------------------------------------
// startInstance
// ------------------------------------------------------------
//

func startInstance(name string) error {
	instDir := filepath.Join(instanceRoot(), name)
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

	// crosvm-style: --disk = RO, --rwdisk = RW
	args := []string{
		"--name", cfg.Name,
		"--kernel", cfg.Kernel,
		"--initrd", cfg.Initrd,

		"--disk", cfg.Root.Base, // RO base image
		"--rwdisk", cfg.Root.Overlay, // RW overlay
		"--disk", cfg.CloudInit.CIDATA, // RO CIDATA

		"--net", netArg,
		"--params", "console=hvc0 root=/dev/vda rootfstype=ext4 rootwait",
	}

	for _, s := range cfg.Shares {
		arg := fmt.Sprintf("%s:%s", s.Host, s.Guest)
		logDebug("Adding shared-dir: %s", arg)
		args = append(args, "--shared-dir", arg)
	}

	logDebug("Executing: runvm %s", strings.Join(args, " "))

	cmd := exec.Command("runvm", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return err
	}
	fmt.Printf("Started %s (pid %d)\n", cfg.Name, cmd.Process.Pid)
	return nil
}

//
// ------------------------------------------------------------
// Stop / Pause / Resume
// ------------------------------------------------------------
//

func stopInstance(name string) error {
	logDebug("Stopping instance: %s", name)
	cmd := exec.Command("killall", name)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func pauseInstance(name string) error {
	logDebug("Pausing instance: %s", name)
	cmd := exec.Command("killall", "-STOP", name)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func resumeInstance(name string) error {
	logDebug("Resuming instance: %s", name)
	cmd := exec.Command("killall", "-CONT", name)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

//
// ------------------------------------------------------------
// CLI entrypoint
// ------------------------------------------------------------
//

func usage() {
	fmt.Println("usage:")
	fmt.Println("  runctl [--debug] [--force] create <instance> --image <image-name>")
	fmt.Println("  runctl [--debug]          start  <instance>")
	fmt.Println("  runctl [--debug]          stop   <instance>")
	fmt.Println("  runctl [--debug]          pause  <instance>")
	fmt.Println("  runctl [--debug]          resume <instance>")
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	// Global options: --debug, --force
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

	if len(args) < 2 {
		usage()
		os.Exit(1)
	}

	cmd := args[0]
	name := args[1]
	args = args[2:]

	switch cmd {
	case "create":
		if len(args) < 2 || args[0] != "--image" {
			fmt.Println("missing --image for create")
			usage()
			os.Exit(1)
		}
		image := args[1]
		if err := createInstance(name, image); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}

	case "start":
		if err := startInstance(name); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}

	case "stop":
		if err := stopInstance(name); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}

	case "pause":
		if err := pauseInstance(name); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}

	case "resume":
		if err := resumeInstance(name); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}

	default:
		fmt.Println("unknown command:", cmd)
		usage()
		os.Exit(1)
	}
}
