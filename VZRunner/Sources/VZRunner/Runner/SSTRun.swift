import Foundation
import ArgumentParser
import Virtualization
import Darwin // Required for exit()

@main
struct SSTRun: ParsableCommand {
    static let configuration = CommandConfiguration(
        abstract: "SST Runner for macOS (VZ)"
    )

    @Option(name: .shortAndLong, help: "Path to config file")
    var config: String = "sst-run.yaml"

    mutating func run() throws {
        // 1. Load Config
        print("Loading config from \(config)")
        let loader = ConfigLoader()
        let sstConfig = try loader.load(from: config)
        let vmConfig = sstConfig.vm

        // 2. Prepare VM
        print("Configuring VM (Kernel: \(vmConfig.kernel))...")
        let builder = VZMachineBuilder(config: vmConfig)
        
        do {
            let vzConfig = try builder.build()
            
            // 3. Validate
            try vzConfig.validate()
            
            // 4. Run
            // Note: Ensure VZExecutor has the updated init(config:ports:) signature
            let executor = VZExecutor(config: vzConfig, ports: vmConfig.ports)
            try executor.run()
            
            // 5. Block Main Thread (Handle Ctrl-C)
            let signalSource = DispatchSource.makeSignalSource(signal: SIGINT, queue: .main)
            signalSource.setEventHandler {
                print("\nCaught SIGINT. Stopping...")
                executor.stop()
                Darwin.exit(0) // Explicit call to system exit
            }
            signalSource.resume()
            
            RunLoop.main.run()
            
        } catch {
            print("Error: \(error)")
            print("Detailed: \((error as NSError).localizedFailureReason ?? "")")
            Darwin.exit(1) // Explicit call to system exit
        }
    }
}