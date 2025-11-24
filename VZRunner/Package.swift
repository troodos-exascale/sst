// swift-tools-version: 5.9
import PackageDescription

let package = Package(
    name: "VZRunner",
    platforms: [
        .macOS(.v13)
    ],
    dependencies: [
        .package(url: "https://github.com/jpsim/Yams.git", from: "5.0.0"),
        .package(url: "https://github.com/apple/swift-argument-parser", from: "1.2.0"),
    ],
    targets: [
        .executableTarget(
            name: "VZRunner",
            dependencies: [
                .product(name: "Yams", package: "Yams"),
                .product(name: "ArgumentParser", package: "swift-argument-parser"),
            ],
            path: "Sources/VZRunner"
        ),
        .testTarget(
            name: "VZRunnerTests",
            dependencies: ["VZRunner"],
            path: "VZRunnerTests"
        ),
    ]
)