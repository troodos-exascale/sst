import XCTest
import Virtualization
@testable import VZRunner

final class VZMachineBuilderTests: XCTestCase {
    func testConfigBuilder() throws {
        let vmCfg = VMConfig(
            kernel: "/tmp/vmlinuz",
            initrd: "/tmp/initrd",
            cmdline: "console=hvc0",
            cpus: 2,
            memory_mb: 2048,
            drives: [],
            mounts: [],
            ports: [],
            mac_nat: nil,
            mac_bridge: nil
        )
        
        // We can't fully validate VZ objects without entitlements in unit tests usually,
        // but we can check if the builder throws on bad paths.
        // Since /tmp/vmlinuz likely doesn't exist, this might throw or fail validation.
        let builder = VZMachineBuilder(config: vmCfg)
        
        // Expect error due to missing files
        XCTAssertThrowsError(try builder.build())
    }
}