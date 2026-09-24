// swift-tools-version: 6.0
import PackageDescription

let package = Package(
    name: "t2bfm",
    platforms: [.macOS("26.0")],
    targets: [
        .executableTarget(name: "t2bfm", path: "Sources/t2bfm")
    ]
)
