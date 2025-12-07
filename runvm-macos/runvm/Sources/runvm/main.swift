import Foundation
import AppKit // Required for NSWorkspace (Sleep/Wake notifications)
import ArgumentParser
import Virtualization

var origStdinTerm : termios? = nil
var origStdoutTerm : termios? = nil

// The global VM instance
var vm : VZVirtualMachine? = nil

// mask TERM signals so we can perform clean up
signal(SIGPIPE, SIG_IGN)
signal(SIGHUP, SIG_IGN)
signal(SIGINT, SIG_IGN)
signal(SIGTERM, SIG_IGN)
let sigSrcs = [
    SIGPIPE: DispatchSource.makeSignalSource(signal: SIGPIPE, queue: .main),
    SIGHUP: DispatchSource.makeSignalSource(signal: SIGHUP, queue: .main),
    SIGINT: DispatchSource.makeSignalSource(signal: SIGINT, queue: .main),
    SIGTERM: DispatchSource.makeSignalSource(signal: SIGTERM, queue: .main),
]
for (_, sigSrc) in sigSrcs {
    sigSrc.setEventHandler {
        quit(1)
    }
    sigSrc.resume()
}

func setupTty() {
    if isatty(0) != 0 {
        origStdinTerm = termios()
        var term = termios()
        tcgetattr(0, &origStdinTerm!)
        tcgetattr(0, &term)
        cfmakeraw(&term)
        tcsetattr(0, TCSANOW, &term)
    }
}

func resetTty() {
    if origStdinTerm != nil {
        tcsetattr(0, TCSANOW, &origStdinTerm!)
    }
    if origStdoutTerm != nil {
        tcsetattr(1, TCSANOW, &origStdoutTerm!)
    }
}

func tryGracefulShutdown(timeout: Double) {
    if vm != nil && vm!.canRequestStop {
        do {
            try vm!.requestStop()
            // Wait for 'graceful' shutdown, but quit immediately if timeout reached
            DispatchQueue.main.asyncAfter(deadline: .now() + timeout) {
                FileHandle.standardError.write("Shutdown timeout expired, exiting immediately.\r\n".data(using: .utf8)!)
                quit(1)
            }
        } catch {
            FileHandle.standardError.write("Failed to request stop.\r\n".data(using: .utf8)!)
            quit(1)
        }
    } else {
        quit(1)
    }
}

func quit(_ code: Int32) -> Never {
    resetTty()
    exit(code)
}

func openDisk(path: String, readOnly: Bool) throws -> VZVirtioBlockDeviceConfiguration {
    let vmDiskURL = URL(fileURLWithPath: path)
    let vmDisk: VZDiskImageStorageDeviceAttachment
    do {
        vmDisk = try VZDiskImageStorageDeviceAttachment(url: vmDiskURL, readOnly: readOnly)
    } catch {
        throw error
    }
    let vmBlockDevCfg = VZVirtioBlockDeviceConfiguration(attachment: vmDisk)
    return vmBlockDevCfg
}

@available(macOS 12, *)
func openFolder(path: String, tag: String, readOnly: Bool) throws -> VZDirectorySharingDeviceConfiguration {
    let sharedDirectory = VZSharedDirectory(url: URL(fileURLWithPath: path), readOnly: readOnly)
    let vzDirShare = VZVirtioFileSystemDeviceConfiguration(tag: tag)
    vzDirShare.share = VZSingleDirectoryShare(directory: sharedDirectory)
    return vzDirShare
}

class OccurrenceCounter {
    let pattern: Data
    var i = 0
    init(_ pattern: Data) {
        self.pattern = pattern
    }

    func process(_ data: Data) -> Int {
        if pattern.count == 0 {
            return 0
        }
        var occurrences = 0
        for byte in data {
            if byte == pattern[i] {
                i += 1
                if i >= pattern.count {
                    occurrences += 1
                    i = 0
                }
            } else {
                i = 0
            }
        }
        return occurrences
    }
}

// MARK: - Sleep/Wake Observer
class VMAppStateObserver: NSObject {
    func startObserving() {
        let center = NSWorkspace.shared.notificationCenter
        center.addObserver(self, selector: #selector(willSleep(_:)), name: NSWorkspace.willSleepNotification, object: nil)
        center.addObserver(self, selector: #selector(didWake(_:)), name: NSWorkspace.didWakeNotification, object: nil)
    }

    @objc func willSleep(_ notification: Notification) {
        // Pause the VM to prevent guest kernel watchdog panic due to time jump
        if let v = vm, v.state == .running {
            v.pause { result in
                if case .failure(let error) = result {
                    FileHandle.standardError.write("Failed to pause VM on sleep: \(error)\r\n".data(using: .utf8)!)
                }
            }
        }
    }

    @objc func didWake(_ notification: Notification) {
        // Resume execution
        if let v = vm, v.state == .paused {
            v.resume { result in
                if case .failure(let error) = result {
                    FileHandle.standardError.write("Failed to resume VM on wake: \(error)\r\n".data(using: .utf8)!)
                }
            }
        }
    }
}

class RunVMDelegate: NSObject, VZVirtualMachineDelegate {
    func guestDidStop(_ virtualMachine: VZVirtualMachine) {
        quit(0)
    }
    func virtualMachine(_ virtualMachine: VZVirtualMachine, didStopWithError error: Error) {
        quit(1)
    }
}

let delegate = RunVMDelegate()
let vmCfg = VZVirtualMachineConfiguration()
let platform = VZGenericPlatformConfiguration()
platform.machineIdentifier = VZGenericMachineIdentifier()
vmCfg.platform = platform


enum BootLoader: String, ExpressibleByArgument {
    case linux
    @available(macOS 13, *)
    case efi
}

enum SizeSuffix: UInt64, ExpressibleByArgument {
    case none = 1,
         KB = 1000, KiB = 0x400,
         MB = 1000000, MiB = 0x100000,
         GB = 1000000000, GiB = 0x40000000
}

struct RunVM: ParsableCommand {

    @Option(name: .customLong("name"), help: "Instance name (process name)")
    var name: String?

    @Option(name: .shortAndLong, help: "CPU count")
    var cpuCount: Int = 1

    @Option(name: .shortAndLong, help: "Memory Bytes")
    var memorySize: UInt64 = 512 // 512 MiB default

    @Option(name: .long, help: "Memory Size Suffix")
    var memorySizeSuffix: SizeSuffix = .MiB

    @Option(name: [.customLong("disk")],
            help: "Read-only disk images (crosvm: --disk PATH)")
    var roDisks: [String] = []

    @Option(name: [.customLong("rwdisk")],
            help: "Writable disk images (crosvm: --rwdisk PATH)")
    var rwDisks: [String] = []

    @Option(name: [ .customLong("cdrom") ], help: "CD-ROMs to use (legacy)")
    var cdroms: [String] = []

    @Option(name: [ .short, .customLong("folder")], help: "Folders to share")
    var folders: [String] = []

    @Option(name: [.customLong("shared-dir")], help: "Folders to share (crosvm: --shared-dir)")
    var sharedDirs: [String] = []

    @Option(name: [ .short, .customLong("network"), .customLong("net") ], help: "Networks to use")
    var networks: [String] = [ "nat" ]

    @Option(help: "Enable / Disable Memory Ballooning")
    var balloon: Bool = true

    @Option(name: .shortAndLong, help: "Bootloader to use")
    var bootloader: BootLoader = .linux

    @Option(name: .customLong("efi-vars"), help: "EFI variable store location")
    var efiVars: String?

    @Option(name: .shortAndLong, help: "Kernel to use (Linux bootloader only)")
    var kernel: String?

    @Option(help: "Initrd to use (Linux bootloader only)")
    var initrd: String?

    @Option(name: [.customLong("params"), .customShort("p")], help: "Kernel cmdline (crosvm: --params)")
    var params: String?

    @Option(help: "Escape Sequence, when using a tty")
    var escapeSequence: String = "q"

    @Option(help: "Timeout in seconds for graceful shutdown")
    var shutdownTimeout: Double = 120.0

    // Legacy support
    @Option(help: "Kernel cmdline to use (legacy)")
    var cmdline: String?

    mutating func run() throws {
        // CPU / memory
        vmCfg.cpuCount = cpuCount
        vmCfg.memorySize = memorySize * memorySizeSuffix.rawValue
        
        // bootloader
        switch bootloader {
        case .linux:
            if kernel == nil {
                throw ValidationError("Kernel not specified")
            }
            if efiVars != nil {
                throw ValidationError("EFI variable store cannot be used with Linux bootloader")
            }
            let vmKernelURL = URL(fileURLWithPath: kernel!)
            let vmBootLoader = VZLinuxBootLoader(kernelURL: vmKernelURL)
            if initrd != nil {
                vmBootLoader.initialRamdiskURL = URL(fileURLWithPath: initrd!)
            }
            // Prefer --params, fallback to --cmdline
            if let p = params {
                vmBootLoader.commandLine = p
            } else if let c = cmdline {
                vmBootLoader.commandLine = c
            }
            vmCfg.bootLoader = vmBootLoader

        case .efi:
            if #available(macOS 13, *) {
                if efiVars == nil {
                    throw ValidationError("EFI variable store must be specified if using EFI bootloader")
                }
                if kernel != nil || initrd != nil  {
                    throw ValidationError("Kernel, initrd options cannot be used with EFI bootloader")
                }
                let efiVarStoreURL = URL(fileURLWithPath: efiVars!)
                let efiVarStore: VZEFIVariableStore
                if FileManager.default.fileExists(atPath: efiVars!) {
                    efiVarStore = VZEFIVariableStore(url: efiVarStoreURL)
                } else {
                    efiVarStore = try VZEFIVariableStore(creatingVariableStoreAt: efiVarStoreURL)
                }
                let vmBootLoader = VZEFIBootLoader()
                vmBootLoader.variableStore = efiVarStore
                vmCfg.bootLoader = vmBootLoader
            } else {
                throw ValidationError("EFI bootloader is only available on macOS 13 and later versions")
            }
        }

        // set up tty console
        let vmSerialIn = Pipe()
        let vmSerialOut = Pipe()

        let vmConsoleCfg = VZVirtioConsoleDeviceSerialPortConfiguration()
        let vmSerialPort = VZFileHandleSerialPortAttachment(
            fileHandleForReading: vmSerialIn.fileHandleForReading,
            fileHandleForWriting: vmSerialOut.fileHandleForWriting
        )
        vmConsoleCfg.attachment = vmSerialPort
        vmCfg.serialPorts = [ vmConsoleCfg ]

        //
        // STORAGE: use rwDisks for RW, roDisks (and cdroms) for RO
        //
        vmCfg.storageDevices = []

        for path in rwDisks {
            try vmCfg.storageDevices.append(openDisk(path: path, readOnly: false))
        }
        for path in roDisks {
            try vmCfg.storageDevices.append(openDisk(path: path, readOnly: true))
        }
        for path in cdroms {
            try vmCfg.storageDevices.append(openDisk(path: path, readOnly: true))
        }

        //
        // Shared folders
        //
        if #available(macOS 12, *) {
            var allFolders = folders
            // also parse --shared-dir host:tag:ro or host:tag
            for sd in sharedDirs {
                 allFolders.append(sd)
            }
            
            for folder in allFolders {
                let parts = folder.split(separator: ":")
                if parts.count == 0 { continue }
                
                let path = String(parts[0])
                var tag = String(parts[0])
                var readOnly = false
                
                if parts.count > 1 {
                    tag = String(parts[1])
                }
                // Check for ro flag in 3rd position
                if parts.count > 2 && parts[2] == "ro" {
                    readOnly = true
                }
                
                try vmCfg.directorySharingDevices.append(openFolder(path: path, tag: tag, readOnly: readOnly))
            }
        }

        if #available(macOS 13, *) {
            do {
                try VZVirtioFileSystemDeviceConfiguration.validateTag("rosetta")
                let rosettaDirectoryShare = try VZLinuxRosettaDirectoryShare()
                let fileSystemDevice = VZVirtioFileSystemDeviceConfiguration(tag: "rosetta")
                fileSystemDevice.share = rosettaDirectoryShare

                vmCfg.directorySharingDevices.append(fileSystemDevice)
            } catch VZError.invalidVirtualMachineConfiguration {
                // Rosetta is unavailable.
            }
        }

        //
        // Networking
        //
        vmCfg.networkDevices = []
        for network in networks {
            let netCfg = VZVirtioNetworkDeviceConfiguration()
            let parts = network.split(separator: "@")
            var device = String(parts[0])
            if parts.count > 1 {
                netCfg.macAddress = VZMACAddress(string: String(parts[0]))!
                device = String(parts[1])
            }
            switch device {
            case "nat":
                netCfg.attachment = VZNATNetworkDeviceAttachment()
            default:
                for iface in VZBridgedNetworkInterface.networkInterfaces {
                    if iface.identifier == network {
                        netCfg.attachment = VZBridgedNetworkDeviceAttachment(interface: iface)
                        break
                    }
                }
                if netCfg.attachment == nil {
                    throw ValidationError("Cannot find network: \(network)")
                }
            }
            vmCfg.networkDevices.append(netCfg)
        }

        // memory balloon
        let balloonCfg = VZVirtioTraditionalMemoryBalloonDeviceConfiguration()
        vmCfg.memoryBalloonDevices = [ balloonCfg ]

        vmCfg.entropyDevices = [ VZVirtioEntropyDeviceConfiguration() ]

        try vmCfg.validate()

        // disable stdin echo, disable stdin line buffer, disable ^C
        setupTty()

        // set up piping.
        var fullEscapeSequence = Data([0x1b]) // escape sequence always starts with ESC
        if let escData = escapeSequence.data(using: .nonLossyASCII) {
            fullEscapeSequence.append(escData)
        } else {
             // Fallback if encoding fails
             fullEscapeSequence.append(contentsOf: [0x71]) // 'q'
        }

        let escapeSequenceCounter = OccurrenceCounter(fullEscapeSequence)
        FileHandle.standardInput.waitForDataInBackgroundAndNotify()
        NotificationCenter.default.addObserver(forName: NSNotification.Name.NSFileHandleDataAvailable, object: FileHandle.standardInput, queue: nil)
        {[shutdownTimeout] _ in
            let data = FileHandle.standardInput.availableData
            if origStdinTerm != nil && escapeSequenceCounter.process(data) > 0 {
                FileHandle.standardError.write("Escape sequence detected, exiting.\r\n".data(using: .utf8)!)
                tryGracefulShutdown(timeout: shutdownTimeout)
            }
            vmSerialIn.fileHandleForWriting.write(data)
            if data.count > 0 {
                FileHandle.standardInput.waitForDataInBackgroundAndNotify()
            }
        }

        // Create and run VM
        vm = VZVirtualMachine(configuration: vmCfg)
        vm!.delegate = delegate
        vm!.start { result in
            switch result {
            case .success:
                break
            case .failure(let error):
                FileHandle.standardError.write("Failed to start VM: \(error)\r\n".data(using: .utf8)!)
                quit(1)
            }
        }

        let outHandle = vmSerialOut.fileHandleForReading
        outHandle.readabilityHandler = { handle in
            let data = handle.availableData
            if data.count > 0 {
                FileHandle.standardOutput.write(data)
            }
        }
        
        // --- NEW: Observe Sleep/Wake ---
        let sleepObserver = VMAppStateObserver()
        sleepObserver.startObserving()
        // -------------------------------

        RunLoop.main.run()
    }
}

RunVM.main()
