import Foundation
import Virtualization

final class VZExecutor: NSObject, VZVirtualMachineDelegate {
    private let vm: VZVirtualMachine
    private let ports: [String]?

    // Initializer matches the call in main.swift: 
    // let executor = VZExecutor(config: vzConfig, ports: vmConfig.ports)
    init(config: VZVirtualMachineConfiguration, ports: [String]?) {
        self.vm = VZVirtualMachine(configuration: config)
        self.ports = ports
        super.init()
        self.vm.delegate = self
    }

    func run() throws {
        let sem = DispatchSemaphore(value: 0)
        
        // Start the VM
        vm.start { result in
            switch result {
            case .failure(let err):
                print("Failed to start VM: \(err)")
                exit(1)
            case .success:
                print("VM successfully started.")
            }
            sem.signal()
        }
        
        sem.wait()
    }

    // Method matches the call in main.swift: executor.stop()
    func stop() {
        print("Stopping VM...")
        if vm.canRequestStop {
            do {
                try vm.requestStop()
            } catch {
                print("Request stop failed, forcing stop: \(error)")
                vm.stop { _ in }
            }
        } else {
            vm.stop { _ in }
        }
    }

    // MARK: - VZVirtualMachineDelegate
    
    func guestDidStop(_ virtualMachine: VZVirtualMachine) {
        print("Guest OS halted.")
        exit(0)
    }

    func virtualMachine(_ virtualMachine: VZVirtualMachine, didStopWithError error: Error) {
        print("VM stopped with error: \(error)")
        exit(1)
    }
}