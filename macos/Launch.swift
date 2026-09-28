import AppKit
import Darwin
import Foundation

// Pharos.app runs where it is. A library's app runs from the library's drive,
// and so do the MCP servers agent clients start from it: the drive holds the
// library, so neither is any use without it. Ejecting from Pharos stops them
// (see DriveProcesses and VolumeEject).

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

    /// Asks them to stop, and kills any still running after `grace`. Returns
    /// the ones that were running.
    @discardableResult
    static func stop(runningFrom directory: URL, grace: TimeInterval = 3) -> [pid_t] {
        let pids = running(from: directory)
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
