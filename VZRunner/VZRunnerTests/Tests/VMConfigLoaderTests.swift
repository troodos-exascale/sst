import XCTest
@testable import VZRunner // Assumes module name

final class VMConfigLoaderTests: XCTestCase {
    func testDriveParsing() {
        let loader = ConfigLoader()
        let ro = loader.parseDrive("disk.img,ro")
        XCTAssertEqual(ro.path, "disk.img")
        XCTAssertTrue(ro.readOnly)
        
        let rw = loader.parseDrive("disk.img,rw")
        XCTAssertEqual(rw.path, "disk.img")
        XCTAssertFalse(rw.readOnly)
        
        let simple = loader.parseDrive("disk.img")
        XCTAssertEqual(simple.path, "disk.img")
        XCTAssertFalse(simple.readOnly)
    }
    
    func testMountParsing() {
        let loader = ConfigLoader()
        let mount = loader.parseMount("/Users/me:src,ro")
        XCTAssertNotNil(mount)
        XCTAssertEqual(mount?.host, "/Users/me")
        XCTAssertEqual(mount?.tag, "src")
        XCTAssertTrue(mount?.readOnly ?? false)
    }
}