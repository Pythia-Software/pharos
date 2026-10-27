import CryptoKit
import Darwin
import Foundation

struct UpdateFailure: LocalizedError {
    let message: String
    var errorDescription: String? { message }
}

enum UpdatePreferences {
    static func migrate(_ defaults: UserDefaults) {
        guard defaults.object(forKey: "pharosSparklePreferencesMigrated") == nil else { return }
        // Absence means Sparkle should ask. Preserve an explicit yes or no.
        if let legacy = defaults.object(forKey: "checkForUpdatesOnLaunch") as? Bool,
           defaults.object(forKey: "SUEnableAutomaticChecks") == nil {
            defaults.set(legacy, forKey: "SUEnableAutomaticChecks")
        }
        defaults.set(true, forKey: "pharosSparklePreferencesMigrated")
    }
}

/// Matches internal/archive/app_update.go. State lives on this Mac, separately
/// for each installed copy; no lock or temporary update data enters the library.
final class AppUpdateLock {
    private struct Pending: Codable {
        let pid: Int32
        let version: String
        let created: Date
    }
    let base: URL
    private let executable: String
    private var descriptor: Int32 = -1
    private var marked = false

    init(app: URL, support: URL) {
        executable = canonicalPath(app.appendingPathComponent("Contents/MacOS/PharosApp"))
        let digest = SHA256.hash(data: Data(canonicalPath(app).utf8)).map { String(format: "%02x", $0) }.joined()
        base = support.appendingPathComponent("updates").appendingPathComponent(digest)
    }

    deinit { if descriptor >= 0 { close(descriptor) } }

    static func current(app: URL = Bundle.main.bundleURL) -> AppUpdateLock {
        let support = ProcessInfo.processInfo.environment["PHAROS_SUPPORT_DIR"].map {
            URL(fileURLWithPath: ($0 as NSString).expandingTildeInPath)
        } ?? FileManager.default.homeDirectoryForCurrentUser.appendingPathComponent("Library/Application Support/Pharos")
        return AppUpdateLock(app: app, support: support)
    }

    private var pendingURL: URL { base.appendingPathExtension("pending") }

    /// A new version clears the handoff marker before starting its service.
    /// After a crash/failed installation, the old version can recover after five
    /// minutes. PID reuse alone is not evidence of a live updating app.
    func recover(version: String, now: Date = Date(), alive: ((Int32, Date) -> Bool)? = nil) throws {
        let data: Data
        do { data = try Data(contentsOf: pendingURL) }
        catch where (error as? CocoaError)?.code == .fileReadNoSuchFile { return }
        let pending = try? JSONDecoder().decode(Pending.self, from: data)
        if let pending {
            guard pending.version != version ||
                    (!(alive?(pending.pid, pending.created) ?? ownerIsAlive(pending.pid, created: pending.created)) && now.timeIntervalSince(pending.created) >= 300) else {
                throw UpdateFailure(message: "Pharos is installing an update. Wait for it to finish. If installation was interrupted, reopen Pharos in five minutes.")
            }
        } else {
            let modified: Date
            do { modified = try pendingURL.resourceValues(forKeys: [.contentModificationDateKey]).contentModificationDate ?? now }
            catch where (error as? CocoaError)?.code == .fileReadNoSuchFile { return }
            if now.timeIntervalSince(modified) < 300 {
                throw UpdateFailure(message: "Pharos's last update was interrupted. Reopen Pharos in five minutes to recover.")
            }
        }
        try openLock()
        guard flock(descriptor, LOCK_EX | LOCK_NB) == 0 else {
            cancel()
            // A newly installed CLI may have removed this stale marker while
            // acquiring its shared lease. Its live connection need not block
            // the new GUI from opening the same, already replaced bundle.
            if !FileManager.default.fileExists(atPath: pendingURL.path) { return }
            throw UpdateFailure(message: "Another Pharos process is still using this app. Quit its Pharos connection and reopen the app.")
        }
        if FileManager.default.fileExists(atPath: pendingURL.path) {
            try FileManager.default.removeItem(at: pendingURL)
        }
        cancel()
    }

    private func ownerIsAlive(_ pid: Int32, created: Date) -> Bool {
        var path = [CChar](repeating: 0, count: 4 * Int(MAXPATHLEN))
        guard proc_pidpath(pid, &path, UInt32(path.count)) > 0,
              canonicalPath(URL(fileURLWithPath: String(cString: path))) == executable else { return false }
        var info = proc_bsdinfo()
        guard proc_pidinfo(pid, PROC_PIDTBSDINFO, 0, &info, Int32(MemoryLayout.size(ofValue: info))) > 0 else {
            return true // Same executable, but identity unavailable: recover conservatively.
        }
        let started = Double(info.pbi_start_tvsec) + Double(info.pbi_start_tvusec) / 1_000_000
        return started <= created.timeIntervalSince1970
    }

    func begin(version: String) throws {
        try openLock()
        // Serialize marker creation without blocking existing shared CLI leases.
        // Only this GUI owns the marker; its PID distinguishes another copy.
        if FileManager.default.fileExists(atPath: pendingURL.path) {
            throw UpdateFailure(message: "An update is already in progress for this copy of Pharos.")
        }
        let data = try JSONEncoder().encode(Pending(pid: getpid(), version: version, created: Date()))
        try data.write(to: pendingURL, options: .withoutOverwriting)
        marked = true
    }

    func finish() throws {
        guard descriptor >= 0, flock(descriptor, LOCK_EX | LOCK_NB) == 0 else {
            throw UpdateFailure(message: "A Pharos command is still running. Let it finish, then try installing again.")
        }
    }

    /// On successful update termination leave the marker for the new app and
    /// keep the exclusive lease until this process exits. On an ordinary quit
    /// remove only the marker; the lease still prevents late CLI starts.
    func allowTermination(installing: Bool) {
        if !installing, marked {
            try? FileManager.default.removeItem(at: pendingURL)
            marked = false
        }
    }

    func cancel() {
        if marked { try? FileManager.default.removeItem(at: pendingURL) }
        marked = false
        if descriptor >= 0 { close(descriptor); descriptor = -1 }
    }

    private func openLock() throws {
        guard descriptor < 0 else { return }
        try FileManager.default.createDirectory(at: base.deletingLastPathComponent(), withIntermediateDirectories: true,
                                               attributes: [.posixPermissions: 0o700])
        descriptor = open(base.appendingPathExtension("lock").path, O_CREAT | O_RDWR | O_CLOEXEC, 0o600)
        guard descriptor >= 0 else { throw UpdateFailure(message: "Could not open Pharos's update lock: \(String(cString: strerror(errno))).") }
    }
}

/// Runs off the main thread. Never kills a writer or a process from another app.
enum UpdateProcesses {
    static func prepare(app: URL, servicePID: Int32?, stopService: () throws -> Void,
                        processes: @escaping () -> [(pid: pid_t, path: String)] = runningExecutables,
                        arguments: (pid_t) -> [String]? = processArguments,
                        parentPID: (pid_t) -> pid_t? = processParentPID,
                        signal: (pid_t) -> Void = { _ = kill($0, SIGTERM) },
                        wait: (@escaping () -> Bool) -> Bool = { condition in
                            let deadline = Date().addingTimeInterval(10)
                            while !condition() {
                                if Date() >= deadline { return false }
                                Thread.sleep(forTimeInterval: 0.05)
                            }
                            return true
                        }) throws {
        let cli = canonicalPath(app.appendingPathComponent("Contents/MacOS/pharos"))
        let wrapper = canonicalPath(app.appendingPathComponent("Contents/MacOS/PharosApp"))
        let candidates = processes().filter { $0.pid != getpid() && ($0.path == cli || $0.path == wrapper) }
        var mcp: [pid_t] = []
        for process in candidates {
            if process.pid == servicePID { continue }
            if process.path == cli, let argv = arguments(process.pid), DriveProcesses.command(in: argv) == "serve",
               let servicePID, parentPID(process.pid) == servicePID,
               let flag = argv.firstIndex(of: "--application"), flag + 1 < argv.count,
               canonicalPath(URL(fileURLWithPath: argv[flag + 1])) == canonicalPath(app) { continue }
            guard process.path == cli, let argv = arguments(process.pid), ["mcp", "mcp-app"].contains(DriveProcesses.command(in: argv) ?? "") else {
                throw UpdateFailure(message: "Another Pharos command or app is running from this copy (PID \(process.pid)). Let it finish before installing the update.")
            }
            mcp.append(process.pid)
        }
        try stopService()
        mcp.forEach(signal)
        let watched = Set(candidates.map(\.pid))
        guard wait({ !processes().contains { watched.contains($0.pid) && ($0.path == cli || $0.path == wrapper) } }) else {
            throw UpdateFailure(message: "Pharos is still finishing work. The update has been deferred; try again after its processes stop. Agent clients may need to reconnect.")
        }
    }
}
