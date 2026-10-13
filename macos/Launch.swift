import AppKit
import Darwin
import Foundation

// Pharos.app runs where it is. A library's app runs from the library's drive,
// and so do the MCP servers agent clients start from it: the drive holds the
// library, so neither is any use without it. Quitting or ejecting from Pharos
// stops them (see DriveProcesses and VolumeEject).

/// A simple `key = value` read of Pharos's TOML files, as the app needs only
/// top-level strings and numbers.
func tomlValue(_ name: String, in source: String) -> String? {
    for raw in source.split(separator: "\n") {
        let line = raw.split(separator: "#", maxSplits: 1).first.map(String.init) ?? ""
        guard let separator = line.firstIndex(of: "="),
              line[..<separator].trimmingCharacters(in: .whitespaces) == name else { continue }
        return line[line.index(after: separator)...].trimmingCharacters(in: CharacterSet(charactersIn: " \t\""))
    }
    return nil
}

/// The path with every symlink resolved, as the kernel reports executables.
/// (URL.resolvingSymlinksInPath drops /private from /private/var paths.)
func canonicalPath(_ url: URL) -> String {
    guard let resolved = realpath(url.path, nil) else { return url.standardizedFileURL.path }
    defer { free(resolved) }
    return String(cString: resolved)
}

/// Every running process and its executable's path, where it can be read.
func runningExecutables() -> [(pid: pid_t, path: String)] {
    let count = proc_listallpids(nil, 0)
    guard count > 0 else { return [] }
    var pids = [pid_t](repeating: 0, count: Int(count) + 64)
    let filled = pids.withUnsafeMutableBytes { proc_listallpids($0.baseAddress, Int32($0.count)) }
    var path = [CChar](repeating: 0, count: 4 * Int(MAXPATHLEN))
    return pids.prefix(Int(max(filled, 0))).compactMap { pid in
        proc_pidpath(pid, &path, UInt32(path.count)) > 0 ? (pid, String(cString: path)) : nil
    }
}

/// Reads argv without flattening paths or arguments containing spaces. The
/// kernel returns argc, the executable path, NUL padding, then argv strings.
private func processArguments(_ pid: pid_t) -> [String]? {
    var limit: Int32 = 0
    var size = MemoryLayout<Int32>.size
    var mib = [CTL_KERN, KERN_ARGMAX]
    guard sysctl(&mib, 2, &limit, &size, nil, 0) == 0, limit > 0 else { return nil }
    var bytes = [UInt8](repeating: 0, count: Int(limit))
    size = bytes.count
    mib = [CTL_KERN, KERN_PROCARGS2, pid]
    let result = bytes.withUnsafeMutableBytes { sysctl(&mib, 3, $0.baseAddress, &size, nil, 0) }
    guard result == 0, size > MemoryLayout<Int32>.size else { return nil }
    let count = bytes.withUnsafeBytes { $0.loadUnaligned(as: Int32.self) }
    guard count > 0 else { return nil }
    var offset = MemoryLayout<Int32>.size
    while offset < size, bytes[offset] != 0 { offset += 1 }
    while offset < size, bytes[offset] == 0 { offset += 1 }
    var arguments: [String] = []
    for _ in 0..<count {
        guard offset < size else { return nil }
        let start = offset
        while offset < size, bytes[offset] != 0 { offset += 1 }
        guard offset < size else { return nil }
        arguments.append(String(decoding: bytes[start..<offset], as: UTF8.self))
        offset += 1
    }
    return arguments
}

/// How this launch of Pharos.app should proceed.
enum LaunchPlan: Equatable {
    /// Serve the library in this directory.
    case library(URL)
    /// Serve this Mac's own configuration.
    case user

    /// An app with library.toml beside it serves that library.
    static func resolve(bundle: URL) -> LaunchPlan {
        let beside = bundle.deletingLastPathComponent()
        return FileManager.default.fileExists(atPath: beside.appendingPathComponent("library.toml").path) ? .library(beside) : .user
    }
}

/// Processes running from a directory, such as agents' MCP servers started
/// from the library's app: they keep its drive from ejecting.
enum DriveProcesses {
    /// Processes other than this one whose executable is inside `directory`.
    static func running(from directory: URL, in processes: [(pid: pid_t, path: String)] = runningExecutables()) -> [pid_t] {
        let prefix = canonicalPath(directory) + "/"
        return processes.filter { $0.pid != getpid() && $0.path.hasPrefix(prefix) }.map(\.pid)
    }

    /// The CLI accepts --config PATH before its command (see archive.Run).
    static func command(in arguments: [String]) -> String? {
        var index = 1 // argv[0] is the executable
        while index < arguments.count {
            if arguments[index] != "--config" { return arguments[index] }
            index += 2
        }
        return nil
    }

    /// A library app's Quit stops only its MCP servers. Its archive service
    /// has its own graceful shutdown, and other CLI commands keep their lifecycle.
    @discardableResult
    static func stopMCP(runningFrom app: URL, grace: TimeInterval = 3) -> [pid_t] {
        let executable = canonicalPath(app.appendingPathComponent("Contents/MacOS/pharos"))
        let candidates = running(from: app, in: runningExecutables().filter { $0.path == executable })
        let pids = candidates.filter { pid in
            guard let arguments = processArguments(pid) else { return false }
            return command(in: arguments) == "mcp"
        }
        return stop(pids, grace: grace)
    }

    /// Asks them to stop, and kills any still running after `grace`. Returns
    /// the ones that were running.
    @discardableResult
    static func stop(runningFrom directory: URL, grace: TimeInterval = 3) -> [pid_t] {
        stop(running(from: directory), grace: grace)
    }

    private static func stop(_ pids: [pid_t], grace: TimeInterval) -> [pid_t] {
        pids.forEach { kill($0, SIGTERM) }
        let deadline = Date().addingTimeInterval(grace)
        var left = pids
        while !left.isEmpty, Date() < deadline {
            Thread.sleep(forTimeInterval: 0.05)
            left = left.filter { kill($0, 0) == 0 }
        }
        left.forEach { kill($0, SIGKILL) }
        return pids
    }
}
