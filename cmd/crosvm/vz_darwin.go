// Copyright 2025-2026 Peter Braam
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build darwin

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
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Code-Hex/vz/v3"
)

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

	// 4. Network
	if len(app.Networks) == 0 {
		app.Networks = []string{"nat"}
	}

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

	// 6. Devices (Balloon, Entropy, VSOCK)
	entropy, _ := vz.NewVirtioEntropyDeviceConfiguration()
	config.SetEntropyDevicesVirtualMachineConfiguration([]*vz.VirtioEntropyDeviceConfiguration{entropy})

	if app.Balloon {
		bal, _ := vz.NewVirtioTraditionalMemoryBalloonDeviceConfiguration()
		config.SetMemoryBalloonDevicesVirtualMachineConfiguration([]vz.MemoryBalloonDeviceConfiguration{bal})
	}

	// 6.5 VSOCK Configuration for Host-to-Guest SSH
	socketDev, err := vz.NewVirtioSocketDeviceConfiguration()
	if err != nil {
		return nil, fmt.Errorf("failed to create vsock config: %w", err)
	}
	config.SetSocketDevicesVirtualMachineConfiguration([]vz.SocketDeviceConfiguration{socketDev})

	// 7. Validation
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
	if err := v.machine.Start(); err != nil {
		return err
	}

	// Setup Host Unix Socket for VSOCK SSH Tunnel
	sockDir := "var"
	os.MkdirAll(sockDir, 0755)
	sockPath := filepath.Join(sockDir, "ssh.sock")
	os.Remove(sockPath)

	ln, err := net.Listen("unix", sockPath)
	if err == nil {
		go func() {
			defer ln.Close()
			for {
				hostConn, err := ln.Accept()
				if err != nil {
					return
				}
				go func(hc net.Conn) {
					defer hc.Close()
					socketDevices := v.machine.SocketDevices()
					if len(socketDevices) == 0 {
						return
					}
					// Connect to guest vsock port 2222
					guestConn, err := socketDevices[0].Connect(2222)
					if err != nil {
						return
					}
					defer guestConn.Close()

					errc := make(chan error, 2)
					go func() {
						_, err := io.Copy(hc, guestConn)
						errc <- err
					}()
					go func() {
						_, err := io.Copy(guestConn, hc)
						errc <- err
					}()
					<-errc
				}(hostConn)
			}
		}()
	}

	go func() {
		for {
			state := v.machine.State()
			if state == vz.VirtualMachineStateStopped || state == vz.VirtualMachineStateError {
				close(v.doneCh)
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(250 * time.Millisecond):
			}
		}
	}()

	return nil
}

// ... (keep RequestStop, Stop, Pause, Resume, Done, Err, and all Helpers exactly as they are) ...
func (v *VMWrapper) RequestStop() error {
	if v.machine.CanRequestStop() {
		_, err := v.machine.RequestStop()
		return err
	}
	return v.machine.Stop()
}

func (v *VMWrapper) Stop() error { return v.machine.Stop() }
func (v *VMWrapper) Pause() error { return v.machine.Pause() }
func (v *VMWrapper) Resume() error { return v.machine.Resume() }
func (v *VMWrapper) Done() <-chan struct{} { return v.doneCh }
func (v *VMWrapper) Err() error {
	select {
	case err := <-v.errCh:
		return err
	default:
		return nil
	}
}

func createBootloader(app *CrosvmApp) (vz.BootLoader, error) {
	if app.Bootloader == "linux" {
		return vz.NewLinuxBootLoader(app.Kernel, vz.WithCommandLine(app.Params), vz.WithInitrd(app.Initrd))
	}
	store, err := vz.NewEFIVariableStore(app.EfiVars, vz.WithCreatingEFIVariableStore())
	if err != nil {
		return nil, fmt.Errorf("failed to create EFI store: %w", err)
	}
	return vz.NewEFIBootLoader(vz.WithEFIVariableStore(store))
}

func createDiskConfig(path string, ro bool) (vz.StorageDeviceConfiguration, error) {
	att, err := vz.NewDiskImageStorageDeviceAttachmentWithCacheAndSync(path, ro, vz.DiskImageCachingModeCached, vz.DiskImageSynchronizationModeFsync)
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
	parts := strings.Split(netStr, ",")
	for _, p := range parts {
		kv := strings.SplitN(p, "=", 2)
		if len(kv) == 2 {
			if kv[0] == "mac" {
				macStr = kv[1]
			}
		} else if len(kv) == 1 && kv[0] != "" {
			if strings.Contains(kv[0], ":") {
				macStr = kv[0]
			}
		}
	}

	attachment, err := vz.NewNATNetworkDeviceAttachment()
	if err != nil {
		return nil, fmt.Errorf("failed to create NAT attachment: %w", err)
	}

	dev, err := vz.NewVirtioNetworkDeviceConfiguration(attachment)
	if err != nil {
		return nil, fmt.Errorf("failed to create virtio network device: %w", err)
	}

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
