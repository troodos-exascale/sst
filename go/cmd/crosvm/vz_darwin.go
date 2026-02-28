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
	//	"sync"

	"github.com/Code-Hex/vz/v3"
	// "github.com/swiss-software-tool/runtime-smart/go/internal/sst"
)

// --- CGO Glue for Sleep/Wake ---

// var globalSleepHandler func(bool)
// var globalSleepMu sync.Mutex

// //export goSleepCallback
// func goSleepCallback(isSleep C.int) {
// 	globalSleepMu.Lock()
// 	defer globalSleepMu.Unlock()
// 	if globalSleepHandler != nil {
// 		globalSleepHandler(isSleep != 0)
// 	}
// }

// func StartSleepObserver(log *sst.Logx, handler func(bool)) func() {
// 	globalSleepMu.Lock()
// 	globalSleepHandler = handler
// 	globalSleepMu.Unlock()

// 	C.startSleepObserver()

// 	return func() {
// 		C.stopSleepObserver()
// 		globalSleepMu.Lock()
// 		globalSleepHandler = nil
// 		globalSleepMu.Unlock()
// 	}
// }

// --- VM Wrapper ---

type VMWrapper struct {
	machine *vz.VirtualMachine
	config  *vz.VirtualMachineConfiguration
	errCh   chan error
	doneCh  chan struct{}
}

func NewVMWrapper(app *CrosvmApp) (*VMWrapper, error) {
	// 0. Bootloader
	bootLoader, err := createBootloader(app)
	if err != nil {
		return nil, err
	}

	config, err := vz.NewVirtualMachineConfiguration(bootLoader, uint(app.CPUCount), app.MemorySize)
	if err != nil {
		return nil, err
	}

	// 1. Platform & Nested Virtualization
	platformConfig, err := vz.NewGenericPlatformConfiguration()
	if err != nil {
		return nil, fmt.Errorf("failed to create platform config: %w", err)
	}

	if app.NestedVirt {
		if !vz.IsNestedVirtualizationSupported() {
			return nil, fmt.Errorf("nested virtualization is not supported on this host")
		}
		if err := platformConfig.SetNestedVirtualizationEnabled(true); err != nil {
			return nil, fmt.Errorf("failed to enable nested virtualization: %w", err)
		}
		app.log.Info(app.ctx, "Enabled nested virtualization")
	}
	config.SetPlatformVirtualMachineConfiguration(platformConfig)

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

	// 3. Storage
	var storageDevices []vz.StorageDeviceConfiguration

	for _, path := range app.RwDiskPaths {
		cfg, err := createDiskConfig(path, false)
		if err != nil {
			return nil, err
		}
		storageDevices = append(storageDevices, cfg)
	}

	for _, path := range app.DiskPaths {
		cfg, err := createDiskConfig(path, true)
		if err != nil {
			return nil, err
		}
		storageDevices = append(storageDevices, cfg)
	}

	for _, path := range app.CdromPaths {
		cfg, err := createUSBCDROMConfig(path)
		if err != nil {
			return nil, err
		}
		storageDevices = append(storageDevices, cfg)
	}
	config.SetStorageDevicesVirtualMachineConfiguration(storageDevices)

	// 4. Network (Strict Ordering Enforced)
	// Step A: Apply Defaults
	if len(app.Networks) == 0 {
		app.Networks = []string{"nat"}
	}

	// Step B: Build Device List
	var networkDevices []*vz.VirtioNetworkDeviceConfiguration
	for _, netStr := range app.Networks {
		cfg, err := createNetworkConfig(netStr)
		if err != nil {
			return nil, err
		}
		networkDevices = append(networkDevices, cfg)
	}
	// Step C: Attach to Config (Last mutation)
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

	// 7. Validation (The "Freeze" Point)
	// No config mutations are allowed after this line.
	validated, err := config.Validate()
	if !validated || err != nil {
		return nil, fmt.Errorf("invalid VM config: %w", err)
	}

	// 8. Instantiation
	vm, err := vz.NewVirtualMachine(config)
	if err != nil {
		return nil, err
	}

	wrapper := &VMWrapper{
		machine: vm, 
		config:  config, 
		errCh:   make(chan error, 1),
		doneCh:  make(chan struct{}),
	}
	return wrapper, nil
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
	return v.doneCh
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

func createBootloader(app *CrosvmApp) (vz.BootLoader, error) {
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
	att, err := vz.NewDiskImageStorageDeviceAttachmentWithCacheAndSync(
		path,
		ro,
		vz.DiskImageCachingModeCached,
		vz.DiskImageSynchronizationModeFsync,
	)
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
	macStr := ""

	// Parse standard crosvm format (e.g. "mac=52:54:00:11:22:33,tap-name=sst_tap")
	parts := strings.Split(netStr, ",")
	for _, p := range parts {
		kv := strings.SplitN(p, "=", 2)
		if len(kv) == 2 {
			if kv[0] == "mac" {
				macStr = kv[1]
			}
			// tap-name is intentionally ignored on macOS; 
			// Virtualization.framework natively routes via shared/NAT.
		} else if len(kv) == 1 && kv[0] != "" {
            // Fallback for edge cases where just a mac or type is passed
            if strings.Contains(kv[0], ":") {
                macStr = kv[0]
            }
        }
	}

	// 1. Create Attachment (Native macOS NAT)
	attachment, err := vz.NewNATNetworkDeviceAttachment()
	if err != nil {
		return nil, fmt.Errorf("failed to create NAT attachment: %w", err)
	}

	// 2. Create Device
	dev, err := vz.NewVirtioNetworkDeviceConfiguration(attachment)
	if err != nil {
		return nil, fmt.Errorf("failed to create virtio network device: %w", err)
	}

	// 3. Set MAC (if provided)
	if macStr != "" {
		hw, err := net.ParseMAC(macStr)
		if err != nil {
			return nil, fmt.Errorf("invalid MAC %q: %w", macStr, err)
		}
		mac, err := vz.NewMACAddress(hw)
		if err != nil {
			return nil, fmt.Errorf("failed to set MAC %q: %w", macStr, err)
		}
		dev.SetMACAddress(mac)
	}
	
	return dev, nil
}

// // Fix: Robust parsing logic
// func createNetworkConfig(netStr string) (*vz.VirtioNetworkDeviceConfiguration, error) {
// 	macStr := ""
// 	typ := "nat"

// 	parts := strings.Split(netStr, "@")
// 	switch len(parts) {
// 	case 1:
// 		typ = parts[0]
// 	case 2:
// 		macStr = parts[0]
// 		typ = parts[1]
// 	default:
// 		return nil, fmt.Errorf("invalid network spec %q", netStr)
// 	}

// 	if typ != "nat" {
// 		return nil, fmt.Errorf("unsupported network type %q (only nat supported here)", typ)
// 	}

// 	// 1. Create Attachment
// 	att, err := vz.NewNATNetworkDeviceAttachment()
// 	if err != nil {
// 		return nil, fmt.Errorf("failed to create NAT attachment: %w", err)
// 	}

// 	// 2. Create Device
// 	dev, err := vz.NewVirtioNetworkDeviceConfiguration(att)
// 	if err != nil {
// 		return nil, fmt.Errorf("failed to create virtio network device: %w", err)
// 	}

// 	// 3. Set MAC (if provided)
// 	if macStr != "" {
// 		hw, err := net.ParseMAC(macStr)
// 		if err != nil {
// 			return nil, fmt.Errorf("invalid MAC %q: %w", macStr, err)
// 		}
// 		mac, err := vz.NewMACAddress(hw)
// 		if err != nil {
// 			return nil, fmt.Errorf("failed to set MAC %q: %w", macStr, err)
// 		}
// 		dev.SetMACAddress(mac)
// 	}
// 	return dev, nil
// }

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
