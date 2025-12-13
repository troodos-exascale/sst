package rtsst

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
	Name string `yaml:"name"`
	Boot string `yaml:"boot"` // "efi" or "kernel"`

	Kernel string `yaml:"kernel,omitempty"`
	Initrd string `yaml:"initrd,omitempty"`

	SystemDisk string     `yaml:"system_disk"` // must be --rwdisk
	CIDataDisk string     `yaml:"cidata_disk"` // must be --disk
	DataDisks  []DataDisk `yaml:"data_disks"`  // array of mixed rw/ro

	EFIVars    string `yaml:"efi_vars"`
	Params     string `yaml:"params"`
	CPUCount   int    `yaml:"cpu_count"`
	MemorySize string `yaml:"memory_size"` //friendly: 4G, 4096=4096M

	Shares []Share `yaml:"shares"`

	Network struct {
		MAC      string `yaml:"mac"`
		Mode     string `yaml:"mode"`
		HostName string `yaml:"hostname"`
	} `yaml:"network"`
}
