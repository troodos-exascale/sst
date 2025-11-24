import XCTest
import Virtualization
@testable import VZRunner

final class VZMachineBuilderTests: XCTestCase {
    func testConfigBuilderHandlesMissingFiles() throws {
        let vmCfg = VMConfig(
            kernel: "/tmp/vmlinuz_missing",
            initrd: "/tmp/initrd_missing",
            cmdline: "console=hvc0",
            cpus: 2,
            memory_mb: 2048,
            drives: ["/tmp/disk_missing.img"],
            mounts: [],
            ports: [],
            mac_nat: nil,
            mac_bridge: nil
        )
        
        let builder = VZMachineBuilder(config: vmCfg)
        
        // Expect error due to missing files (Builder checks file existence)
        XCTAssertThrowsError(try builder.build()) { error in
            // Verify it's our error, not a VZ crash
            let nsError = error as NSError
            XCTAssertEqual(nsError.domain, "SST")
        }
    }
}