import Foundation
import Yams

public struct ConfigLoader {
    public init() {}

    public func load(from path: String) throws -> SSTConfig {
        let url = URL(fileURLWithPath: path)
        let data = try Data(contentsOf: url)
        
        let decoder = YAMLDecoder()
        return try decoder.decode(SSTConfig.self, from: data)
    }
    
    public func parseDrive(_ entry: String) -> (path: String, readOnly: Bool) {
        let parts = entry.split(separator: ",")
        let path = String(parts[0])
        var ro = false
        if parts.count > 1 && parts[1].trimmingCharacters(in: .whitespaces) == "ro" {
            ro = true
        }
        return (path, ro)
    }
    
    public func parseMount(_ entry: String) -> (host: String, tag: String, readOnly: Bool)? {
        // Format: host:tag,ro
        let commaParts = entry.split(separator: ",")
        let mainPart = String(commaParts[0])
        
        let components = mainPart.split(separator: ":")
        guard components.count == 2 else { return nil }
        
        let host = String(components[0])
        let tag = String(components[1])
        
        var ro = false
        if commaParts.count > 1 && commaParts[1].trimmingCharacters(in: .whitespaces) == "ro" {
            ro = true
        }
        
        return (host, tag, ro)
    }
}