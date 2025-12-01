// swift-tools-version:5.8
// The swift-tools-version declares the minimum version of Swift required to build this package.

import PackageDescription

// Add an extra #if checkable symbol on Big Sur, to work around a seeming Swift bug around
// #available version checks by using `#if EXTRA_WORKAROUND_FOR_BIG_SUR`.
// See eg https://developer.apple.com/forums/thread/688678 for other bug reports.
let swiftSettings : [SwiftSetting]
if #available(macOS 12, *) {
    swiftSettings = []
} else {
    swiftSettings = [ .define("EXTRA_WORKAROUND_FOR_BIG_SUR") ]
}

let package = Package(
    name: "dealer",
    platforms: [
        .macOS(.v12),
    ],
    products: [
        .executable(name: "runvm", targets: ["runvm"]),
    ],
    dependencies: [
        .package(url: "https://github.com/apple/swift-argument-parser", from: "1.0.0"),
    ],
    targets: [
        .executableTarget(name: "runvm",
                dependencies: [
                    .product(name: "ArgumentParser", package: "swift-argument-parser"),
                ],
                swiftSettings: swiftSettings),
    ]
)
