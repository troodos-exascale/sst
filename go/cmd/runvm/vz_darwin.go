package main

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Foundation -framework AppKit

// Declarations only. Implementation is in sleep_observer.m
void startSleepObserver();
void stopSleepObserver();
*/
import "C"

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"

	"github.com/Code-Hex/vz/v3"
	"github.com/swiss-software-tool/runtime-smart/go/internal/sst"
)

// --- CGO Glue for Sleep/Wake ---

var globalSleepHandler func(bool)
var globalSleepMu sync.Mutex

//export goSleepCallback
func goSleepCallback(isSleep C.int) {
	globalSleepMu.Lock()
	defer globalSleepMu.Unlock()
	if globalSleepHandler != nil {
		globalSleepHandler(isSleep != 0)
	}
}

func StartSleepObserver(log *sst.Logx, handler func(bool)) func() {
	globalSleepMu.Lock()
	globalSleepHandler = handler
	globalSleepMu.Unlock()

	C.startSleepObserver()

	return func() {
		C.stopSleepObserver()
		globalSleepMu.Lock()
		globalSleepHandler = nil
		globalSleepMu.Unlock()
	}
}

// --- VM Wrapper ---

type VMWrapper struct {
	machine *vz.VirtualMachine
	config  *vz.VirtualMachineConfiguration
	errCh   chan error
}

func NewVMWrapper(app *RunVMApp) (*VMWrapper, error) {
	// 0. Bootloader
	bootLoader, err := createBootloader(app)
	if err != nil {
		return nil, err
	}

	config, err := vz.NewVirtualMachineConfiguration(bootLoader, uint(app.CPUCount), app.MemorySize)
	if err != nil {
		return nil, err
	}

	// ---------------------------------------------------------
	// 1. Platform & Nested Virtualization (THE FIX)
	// ---------------------------------------------------------
	// We must create a GenericPlatformConfiguration to enable nested virt.
	platformConfig, err := vz.NewGenericPlatformConfiguration()
	if err != nil {
		return nil, fmt.Errorf("failed to create platform config: %w", err)
	}

	if app.NestedVirt {
		// Check support first to avoid obscure errors
		if !vz.IsNestedVirtualizationSupported() {
			return nil, fmt.Errorf("nested virtualization is not supported on this host (requires M3 or newer / macOS 15+)")
		}
		if err := platformConfig.SetNestedVirtualizationEnabled(true); err != nil {
			return nil, fmt.Errorf("failed to enable nested virtualization: %w", err)
		}
		app.log.Info(app.ctx, "Enabled nested virtualization")
	}
	config.SetPlatformVirtualMachineConfiguration(platformConfig)
	// ---------------------------------------------------------

	// 2. Serial Ports
	attachment, err := vz.NewFileHandleSerialPortAttachment(os.Stdin, os.Stdout)
	if err != nil {
		return nil, fmt.Errorf("failed to create serial attachment: %w", err)
	}
	serialConf, err := vz.NewVirtioConsoleDeviceSerialPortConfiguration(attachment)
	if err != nil {
		return nil, fmt.Errorf("failed to create console config: %w", err)
	}
	config.SetSerialPortsVirtualMachineConfiguration([]*vz.VirtioConsoleDeviceSerialPortConfiguration{serialConf})

	// 3. Storage (Order Matters: RW First)
	var storageDevices []vz.StorageDeviceConfiguration

	// A. RW Disks
	for _, path := range app.RwDiskPaths {
		cfg, err := createDiskConfig(path, false)
		if err != nil {
			return nil, err
		}
		storageDevices = append(storageDevices, cfg)
	}

	// B. RO Disks
	for _, path := range app.DiskPaths {
		cfg, err := createDiskConfig(path, true)
		if err != nil {
			return nil, err
		}
		storageDevices = append(storageDevices, cfg)
	}

	// C. CD-ROMs
	for _, path := range app.CdromPaths {
		cfg, err := createUSBCDROMConfig(path)
		if err != nil {
			return nil, err
		}
		storageDevices = append(storageDevices, cfg)
	}
	config.SetStorageDevicesVirtualMachineConfiguration(storageDevices)

	// 4. Network
	var networkDevices []*vz.VirtioNetworkDeviceConfiguration
	for _, netStr := range app.Networks {
		cfg, err := createNetworkConfig(netStr)
		if err != nil {
			return nil, err
		}
		networkDevices = append(networkDevices, cfg)
	}
	config.SetNetworkDevicesVirtualMachineConfiguration(networkDevices)

	// 5. Shared Dirs
	var directoryShares []vz.DirectorySharingDeviceConfiguration
	for _, share := range app.SharedDirs {
		cfg, err := createShareConfig(share)
		if err != nil {
			return nil, err
		}
		directoryShares = append(directoryShares, cfg)
	}
	config.SetDirectorySharingDevicesVirtualMachineConfiguration(directoryShares)

	// 6. Devices (Balloon & Entropy)
	entropy, _ := vz.NewVirtioEntropyDeviceConfiguration()
	config.SetEntropyDevicesVirtualMachineConfiguration([]*vz.VirtioEntropyDeviceConfiguration{entropy})

	if app.Balloon {
		bal, _ := vz.NewVirtioTraditionalMemoryBalloonDeviceConfiguration()
		config.SetMemoryBalloonDevicesVirtualMachineConfiguration([]vz.MemoryBalloonDeviceConfiguration{bal})
	}

	validated, err := config.Validate()
	if !validated || err != nil {
		return nil, fmt.Errorf("invalid VM config: %w", err)
	}

	vm, err := vz.NewVirtualMachine(config)
	if err != nil {
		return nil, err
	}

	return &VMWrapper{machine: vm, config: config, errCh: make(chan error, 1)}, nil
}

func (v *VMWrapper) Start(ctx context.Context) error {
	return v.machine.Start()
}

func (v *VMWrapper) RequestStop() error {
	if v.machine.CanRequestStop() {
		_, err := v.machine.RequestStop()
		return err
	}
	return v.machine.Stop()
}

func (v *VMWrapper) Stop() error {
	return v.machine.Stop()
}

func (v *VMWrapper) Pause() error {
	return v.machine.Pause()
}

func (v *VMWrapper) Resume() error {
	return v.machine.Resume()
}

func (v *VMWrapper) Done() <-chan struct{} {
	return nil
}

func (v *VMWrapper) Err() error {
	select {
	case err := <-v.errCh:
		return err
	default:
		return nil
	}
}

// --- Helpers ---

func createBootloader(app *RunVMApp) (vz.BootLoader, error) {
	if app.Bootloader == "linux" {
		return vz.NewLinuxBootLoader(
			app.Kernel,
			vz.WithCommandLine(app.Params),
			vz.WithInitrd(app.Initrd),
		)
	}
	
	store, err := vz.NewEFIVariableStore(
		app.EfiVars, 
		vz.WithCreatingEFIVariableStore(),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create EFI store: %w", err)
	}

	return vz.NewEFIBootLoader(
		vz.WithEFIVariableStore(store),
	)
}

func createDiskConfig(path string, ro bool) (vz.StorageDeviceConfiguration, error) {
	att, err := vz.NewDiskImageStorageDeviceAttachment(path, ro)
	if err != nil {
		return nil, err
	}
	return vz.NewVirtioBlockDeviceConfiguration(att)
}

func createUSBCDROMConfig(path string) (vz.StorageDeviceConfiguration, error) {
	att, err := vz.NewDiskImageStorageDeviceAttachment(path, true)
	if err != nil {
		return nil, err
	}
	return vz.NewUSBMassStorageDeviceConfiguration(att)
}

func createNetworkConfig(netStr string) (*vz.VirtioNetworkDeviceConfiguration, error) {
	parts := strings.Split(netStr, "@")
	var macStr, typeStr string

	if len(parts) == 2 {
		macStr = parts[0]
		typeStr = parts[1]
	} else {
		typeStr = parts[0]
	}

	if typeStr != "nat" && typeStr != "" {
		return nil, fmt.Errorf("only 'nat' networking is supported (requested: %s)", typeStr)
	}

	attachment, err := vz.NewNATNetworkDeviceAttachment()
	if err != nil {
		return nil, err
	}

	devConfig, err := vz.NewVirtioNetworkDeviceConfiguration(attachment)
	if err != nil {
		return nil, err
	}

	if macStr != "" {
		hwAddr, err := net.ParseMAC(macStr)
		if err != nil {
			return nil, fmt.Errorf("invalid MAC address %q: %w", macStr, err)
		}
		
		mac, err := vz.NewMACAddress(hwAddr)
		if err == nil {
			devConfig.SetMACAddress(mac)
		}
	}
	return devConfig, nil
}

func createShareConfig(shareStr string) (vz.DirectorySharingDeviceConfiguration, error) {
	parts := strings.Split(shareStr, ":")
	if len(parts) < 1 {
		return nil, fmt.Errorf("invalid share format")
	}
	path := parts[0]
	tag := path
	if len(parts) > 1 {
		tag = parts[1]
	}

	sharedDir, err := vz.NewSharedDirectory(path, false)
	if err != nil {
		return nil, fmt.Errorf("failed to create shared directory for %s: %w", path, err)
	}

	share, err := vz.NewSingleDirectoryShare(sharedDir)
	if err != nil {
		return nil, fmt.Errorf("failed to create single directory share: %w", err)
	}

	cfg, err := vz.NewVirtioFileSystemDeviceConfiguration(tag)
	if err != nil {
		return nil, err
	}
	cfg.SetDirectoryShare(share)
	return cfg, nil
}
