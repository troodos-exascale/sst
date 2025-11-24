import Foundation
import Virtualization

public class VZMachineBuilder {
    let config: VMConfig
    
    public init(config: VMConfig) {
        self.config = config
    }
    
    public func build() throws -> VZVirtualMachineConfiguration {
        // 1. BootLoader (Linux)
        guard let _ = URL(string: config.kernel),
              let _ = URL(string: config.initrd) else {
            throw NSError(domain: "SST", code: 1, userInfo: [NSLocalizedDescriptionKey: "Invalid kernel/initrd paths"])
        }
        
        let cwd = FileManager.default.currentDirectoryPath
        let absKernel = URL(fileURLWithPath: config.kernel, relativeTo: URL(fileURLWithPath: cwd))
        let absInitrd = URL(fileURLWithPath: config.initrd, relativeTo: URL(fileURLWithPath: cwd))

        let bootLoader = VZLinuxBootLoader(kernelURL: absKernel)
        bootLoader.initialRamdiskURL = absInitrd
        bootLoader.commandLine = config.cmdline
        
        let vzConfig = VZVirtualMachineConfiguration()
        vzConfig.bootLoader = bootLoader
        vzConfig.cpuCount = config.cpus
        vzConfig.memorySize = config.memory_mb * 1024 * 1024
        
        // 2. Storage
        var storageDevices: [VZStorageDeviceConfiguration] = []
        for driveStr in config.drives {
            let (path, ro) = parseDrive(driveStr)
            let absPath = URL(fileURLWithPath: path, relativeTo: URL(fileURLWithPath: cwd))
            
            if !FileManager.default.fileExists(atPath: absPath.path) {
                 throw NSError(domain: "SST", code: 2, userInfo: [NSLocalizedDescriptionKey: "Drive not found: \(absPath.path)"])
            }

            let attachment = try VZDiskImageStorageDeviceAttachment(url: absPath, readOnly: ro)
            let blockDevice = VZVirtioBlockDeviceConfiguration(attachment: attachment)
            storageDevices.append(blockDevice)
        }
        vzConfig.storageDevices = storageDevices
        
        // 3. Directory Shares (VirtioFS)
        var fsDevices: [VZDirectorySharingDeviceConfiguration] = []
        for mountStr in config.mounts {
            if let (host, tag, ro) = parseMount(mountStr) {
                let absHost = URL(fileURLWithPath: host, relativeTo: URL(fileURLWithPath: cwd))
                
                if !FileManager.default.fileExists(atPath: absHost.path) {
                     throw NSError(domain: "SST", code: 3, userInfo: [NSLocalizedDescriptionKey: "Shared dir not found: \(absHost.path)"])
                }
                
                let share = VZSharedDirectory(url: absHost, readOnly: ro)
                let singleShare = VZSingleDirectoryShare(directory: share)
                
                let fs = VZVirtioFileSystemDeviceConfiguration(tag: tag)
                fs.share = singleShare
                fsDevices.append(fs)
            }
        }
        vzConfig.directorySharingDevices = fsDevices
        
        // 4. Network
        var netDevices: [VZNetworkDeviceConfiguration] = []
        
        // A. NAT
        let natAttachment = VZNATNetworkDeviceAttachment()
        let natDev = VZVirtioNetworkDeviceConfiguration()
        natDev.attachment = natAttachment
        
        if let macStr = config.mac_nat, let mac = VZMACAddress(string: macStr) {
            natDev.macAddress = mac
        } else {
            natDev.macAddress = VZMACAddress(string: "aa:bb:cc:dd:ee:01")!
        }
        netDevices.append(natDev)
        
        // B. Bridge
        if let bridgeIface = findBridgedInterface() {
            let bridgeAttachment = VZBridgedNetworkDeviceAttachment(interface: bridgeIface)
            let bridgeDev = VZVirtioNetworkDeviceConfiguration()
            bridgeDev.attachment = bridgeAttachment
            
            if let macStr = config.mac_bridge, let mac = VZMACAddress(string: macStr) {
                bridgeDev.macAddress = mac
            } else {
                bridgeDev.macAddress = VZMACAddress(string: "aa:bb:cc:dd:ee:02")!
            }
            netDevices.append(bridgeDev)
            print("[Net] Bridged to \(bridgeIface.identifier)")
        }
        
        vzConfig.networkDevices = netDevices
        
        // 5. Console
        let serial = VZVirtioConsoleDeviceSerialPortConfiguration()
        
        // Correct API: Uses FileHandle objects directly
        let attachment = VZFileHandleSerialPortAttachment(
            fileHandleForReading: FileHandle.standardInput,
            fileHandleForWriting: FileHandle.standardOutput
        )
        
        serial.attachment = attachment
        vzConfig.serialPorts = [serial]
        
        // 6. Entropy
        vzConfig.entropyDevices = [VZVirtioEntropyDeviceConfiguration()]
        
        return vzConfig
    }
    
    // MARK: - Private Helpers
    
    private func parseDrive(_ entry: String) -> (String, Bool) {
        let parts = entry.split(separator: ",")
        let path = String(parts[0])
        let ro = parts.count > 1 && parts[1].trimmingCharacters(in: .whitespaces) == "ro"
        return (path, ro)
    }
    
    private func parseMount(_ entry: String) -> (String, String, Bool)? {
        let parts = entry.split(separator: ",")
        let main = String(parts[0]).split(separator: ":")
        guard main.count == 2 else { return nil }
        let ro = parts.count > 1 && parts[1].trimmingCharacters(in: .whitespaces) == "ro"
        return (String(main[0]), String(main[1]), ro)
    }
    
    private func findBridgedInterface() -> VZBridgedNetworkInterface? {
        let interfaces = VZBridgedNetworkInterface.networkInterfaces
        for iface in interfaces {
            if iface.identifier.hasPrefix("en") {
                return iface
            }
        }
        return interfaces.first
    }
}