import Foundation

// Root structure matching the YAML format
public struct SSTConfig: Codable {
    public let vm: VMConfig
}

public struct VMConfig: Codable {
    public let kernel: String
    public let initrd: String
    public let cmdline: String
    public let cpus: Int
    public let memory_mb: UInt64
    
    // Drives in format: "path,ro" or "path,rw" or just "path"
    public let drives: [String]
    
    // Mounts in format: "hostPath:tag,ro"
    public let mounts: [String]
    
    // Ports in format: "host:guest"
    public let ports: [String]?
    
    // Optional explicit MACs
    public let mac_nat: String?
    public let mac_bridge: String?
}