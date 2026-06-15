package probe

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/troodos-exascale/sst/internal/sst"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"golang.org/x/crypto/ssh"
	"gopkg.in/yaml.v3"
)

// RunRemoteProbe connects to a host via SSH...
// UPDATED: logger is now *sst.Logx
func RunRemoteProbe(ctx context.Context, logger *sst.Logx, hostname string, sshKey []byte, tempDir string) error {
	logger.Info(ctx, "Preparing to probe remote host", "host", hostname)

	signer, err := ssh.ParsePrivateKey(sshKey)
	if err != nil {
		return fmt.Errorf("failed to parse SSH private key: %w", err)
	}

	config := &ssh.ClientConfig{
		User:            "root",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	}

	addr := fmt.Sprintf("%s:22", hostname)
	client, err := ssh.Dial("tcp", addr, config)
	if err != nil {
		return fmt.Errorf("failed to dial SSH host %s: %w", addr, err)
	}
	defer func() {
		if err := client.Close(); err != nil {
			logger.WithError(err).Warn(ctx, "Failed to close SSH client cleanly")
		}
	}()

	logger.Info(ctx, "SSH connection established", "host", hostname)

	commands := map[string]string{
		"lscpu.json":    "lscpu --json",
		"lsblk.json":    "lsblk --json --bytes --output NAME,MODEL,TYPE,SIZE",
		"meminfo.txt":   "cat /proc/meminfo",
		"ip_addr.json":  "ip --json addr",
		"dmidecode.txt": "dmidecode -s system-serial-number",
		"pci.txt":       "lspci -mm",
	}

	propagator := otel.GetTextMapPropagator()
	carrier := make(propagation.MapCarrier)
	propagator.Inject(ctx, carrier)
	traceparent, hasTrace := carrier["traceparent"]

	for filename, cmdStr := range commands {
		_, spanEnd := sst.StartSpan(ctx, fmt.Sprintf("remote-cmd:%s", filename))

		session, err := client.NewSession()
		if err != nil {
			spanEnd(err)
			return fmt.Errorf("failed to create SSH session for command '%s': %w", cmdStr, err)
		}

		if hasTrace {
			if err := session.Setenv("TRACEPARENT", traceparent); err != nil {
				logger.Warn(ctx, "Failed to set TRACEPARENT env var over SSH", "error", err)
			}
		}

		var stdout, stderr bytes.Buffer
		session.Stdout = &stdout
		session.Stderr = &stderr

		err = session.Run(cmdStr)
		if closeErr := session.Close(); closeErr != nil {
			// A close error is usually not fatal, so we log it as a warning
			// and allow the main loop to continue.
			logger.WithError(closeErr).Warn(ctx, "Failed to close SSH session cleanly", "command", cmdStr)
		}

		if err != nil {
			logger.Error(ctx, "Remote command failed", "command", cmdStr, "stderr", stderr.String())
			spanEnd(err)
			continue
		}

		outputPath := filepath.Join(tempDir, filename)
		if err := os.WriteFile(outputPath, stdout.Bytes(), 0644); err != nil {
			logger.WithError(err).Error(ctx, "Failed to write raw probe output", "file", outputPath)
			spanEnd(err)
			return err
		}
		spanEnd(nil)
	}

	logger.Info(ctx, "Finished gathering all raw data from host", "host", hostname)
	return nil
}

// Normalize reads the raw probe data...
// UPDATED: logger is now *sst.Logx
func Normalize(ctx context.Context, logger *sst.Logx, probeDataDir string) ([]byte, error) {
	probe := HardwareProbe{
		APIVersion: "s2.io/v1",
		Kind:       "HardwareProbe",
		Metadata: ProbeMetadata{
			Timestamp: time.Now().UTC(),
		},
	}

	cpuSpec, err := parseLscpu(filepath.Join(probeDataDir, "lscpu.json"))
	if err != nil {
		logger.WithError(err).Warn(ctx, "Could not parse CPU data")
	}
	probe.Spec.CPU = cpuSpec

	storageSpec, err := parseLsblk(filepath.Join(probeDataDir, "lsblk.json"))
	if err != nil {
		logger.WithError(err).Warn(ctx, "Could not parse block device data")
	}
	probe.Spec.Storage = storageSpec

	memSpec, err := parseMeminfo(filepath.Join(probeDataDir, "meminfo.txt"))
	if err != nil {
		logger.WithError(err).Warn(ctx, "Could not parse memory data")
	}
	probe.Spec.Memory = memSpec

	netSpec, primaryMAC, err := parseIpAddr(filepath.Join(probeDataDir, "ip_addr.json"))
	if err != nil {
		logger.WithError(err).Warn(ctx, "Could not parse network data")
	}
	probe.Spec.Network = netSpec
	probe.Metadata.Identifiers.MACAddress = primaryMAC

	serial, err := parseDmidecode(filepath.Join(probeDataDir, "dmidecode.txt"))
	if err != nil {
		logger.WithError(err).Warn(ctx, "Could not parse serial number")
	}

	if serial == "" {
		logger.Warn(ctx, "System serial number is empty, falling back to primary MAC address for unique identifier.")
		probe.Metadata.Identifiers.SerialNumber = primaryMAC
	} else {
		probe.Metadata.Identifiers.SerialNumber = serial
	}

	pciDevices, err := parseLspci(filepath.Join(probeDataDir, "pci.txt"))
	if err != nil {
		logger.WithError(err).Warn(ctx, "Could not parse PCI device data")
	}
	probe.Spec.PCIDevices = pciDevices

	yamlBytes, err := yaml.Marshal(&probe)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal probe data to YAML: %w", err)
	}

	return yamlBytes, nil
}

// ... (Rest of file: parsing helpers remain unchanged) ...
func parseLscpu(path string) (CPUSpec, error) {
	var spec CPUSpec
	bytes, err := os.ReadFile(path)
	if err != nil {
		return spec, err
	}

	var data struct {
		Lscpu []struct {
			Field string `json:"field"`
			Data  string `json:"data"`
		} `json:"lscpu"`
	}
	if err := json.Unmarshal(bytes, &data); err != nil {
		return spec, err
	}

	for _, field := range data.Lscpu {
		switch field.Field {
		case "Model name:":
			spec.Model = field.Data
		case "Socket(s):":
			spec.Sockets, _ = strconv.Atoi(field.Data)
		case "Core(s) per socket:":
			spec.CoresPerSocket, _ = strconv.Atoi(field.Data)
		case "Thread(s) per core:":
			spec.ThreadsPerCore, _ = strconv.Atoi(field.Data)
		case "NUMA node(s):":
			spec.NUMANodes, _ = strconv.Atoi(field.Data)
		}
	}
	return spec, nil
}

func parseLsblk(path string) (StorageSpec, error) {
	var spec StorageSpec
	bytes, err := os.ReadFile(path)
	if err != nil {
		return spec, err
	}

	var data struct {
		Blockdevices []struct {
			Name  string `json:"name"`
			Model string `json:"model"`
			Type  string `json:"type"`
			Size  uint64 `json:"size"`
		} `json:"blockdevices"`
	}
	if err := json.Unmarshal(bytes, &data); err != nil {
		return spec, err
	}

	for _, dev := range data.Blockdevices {
		if dev.Type == "disk" {
			spec.BlockDevices = append(spec.BlockDevices, BlockDevice{
				Name:      "/dev/" + dev.Name,
				Model:     dev.Model,
				Type:      "HDD/SSD",
				SizeBytes: dev.Size,
			})
		}
	}
	return spec, nil
}

func parseIpAddr(path string) (NetworkSpec, string, error) {
	var spec NetworkSpec
	var primaryMAC string
	bytes, err := os.ReadFile(path)
	if err != nil {
		return spec, "", err
	}

	var interfaces []struct {
		Ifname   string `json:"ifname"`
		Address  string `json:"address"`
		AddrInfo []struct {
			Family string `json:"family"`
			Local  string `json:"local"`
			Prefix uint   `json:"prefixlen"`
		} `json:"addr_info"`
	}
	if err := json.Unmarshal(bytes, &interfaces); err != nil {
		return spec, "", err
	}

	for _, iface := range interfaces {
		if iface.Ifname == "lo" {
			continue
		}
		if primaryMAC == "" {
			primaryMAC = iface.Address
		}
		ni := NetworkInterface{
			Name:       iface.Ifname,
			MACAddress: iface.Address,
			Type:       "Ethernet",
			SpeedMbps:  1000,
		}
		for _, addr := range iface.AddrInfo {
			if addr.Family == "inet" || addr.Family == "inet6" {
				ni.Addresses = append(ni.Addresses, fmt.Sprintf("%s/%d", addr.Local, addr.Prefix))
			}
		}
		spec.Interfaces = append(spec.Interfaces, ni)
	}
	return spec, primaryMAC, nil
}

func parseDmidecode(path string) (string, error) {
	bytes, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(bytes)), nil
}

func parseMeminfo(path string) (MemorySpec, error) {
	var spec MemorySpec
	bytes, err := os.ReadFile(path)
	if err != nil {
		return spec, err
	}
	lines := strings.Split(string(bytes), "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "MemTotal:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				kib, _ := strconv.ParseUint(fields[1], 10, 64)
				spec.TotalBytes = kib * 1024
				return spec, nil
			}
		}
	}
	return spec, fmt.Errorf("MemTotal not found in meminfo")
}

func parseLspci(path string) ([]PCIDevice, error) {
	var devices []PCIDevice
	bytes, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(string(bytes), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}
		parts := strings.SplitN(strings.ReplaceAll(line, "\"", ""), " ", 4)
		if len(parts) < 4 {
			continue
		}
		devices = append(devices, PCIDevice{
			Address: parts[0],
			Class:   parts[1],
			Vendor:  parts[2],
			Device:  parts[3],
		})
	}
	return devices, nil
}
