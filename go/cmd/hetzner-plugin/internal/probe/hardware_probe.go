package probe

import "time"

// HardwareProbe is the top-level struct for the normalized hardware manifest.
type HardwareProbe struct {
	APIVersion string        `yaml:"apiVersion"`
	Kind       string        `yaml:"kind"`
	Metadata   ProbeMetadata `yaml:"metadata"`
	Spec       HardwareSpec  `yaml:"spec"`
}

// ProbeMetadata contains the identifiers for the probed machine.
type ProbeMetadata struct {
	Identifiers ProbeIdentifiers `yaml:"identifiers"`
	Hostname    string           `yaml:"hostname,omitempty"`
	Timestamp   time.Time        `yaml:"timestamp"`
}

// ProbeIdentifiers contains stable, physical hardware identifiers.
type ProbeIdentifiers struct {
	SerialNumber string `yaml:"serialNumber"`
	MACAddress   string `yaml:"macAddress"`
}

// HardwareSpec contains the detailed specifications of the hardware.
type HardwareSpec struct {
	CPU        CPUSpec     `yaml:"cpu"`
	Memory     MemorySpec  `yaml:"memory"`
	Storage    StorageSpec `yaml:"storage"`
	Network    NetworkSpec `yaml:"network"`
	PCIDevices []PCIDevice `yaml:"pciDevices,omitempty"`
}

type CPUSpec struct {
	Model          string `yaml:"model"`
	Sockets        int    `yaml:"sockets"`
	CoresPerSocket int    `yaml:"coresPerSocket"`
	ThreadsPerCore int    `yaml:"threadsPerCore"`
	NUMANodes      int    `yaml:"numaNodes"`
}

type MemorySpec struct {
	TotalBytes uint64 `yaml:"totalBytes"`
}

type StorageSpec struct {
	BlockDevices []BlockDevice `yaml:"blockDevices"`
}

type BlockDevice struct {
	Name      string `yaml:"name"`
	Model     string `yaml:"model"`
	Type      string `yaml:"type"`
	SizeBytes uint64 `yaml:"sizeBytes"`
}

type NetworkSpec struct {
	Interfaces []NetworkInterface `yaml:"interfaces"`
}

type NetworkInterface struct {
	Name       string   `yaml:"name"`
	Type       string   `yaml:"type"` // e.g., Ethernet, InfiniBand
	MACAddress string   `yaml:"macAddress"`
	SpeedMbps  int      `yaml:"speedMbps"`
	Addresses  []string `yaml:"addresses"`
}

type PCIDevice struct {
	Address string `yaml:"address"`
	Class   string `yaml:"class"`
	Vendor  string `yaml:"vendor"`
	Device  string `yaml:"device"`
}
