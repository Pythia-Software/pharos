// Exercises the app's library-volume monitor, release, and Eject without the
// GUI. Built and driven by macos/test/swift-harness.sh; compiled together with
// macos/LibraryVolume.swift and macos/Launch.swift.
import AppKit
import Foundation

var failures = 0
func check(_ condition: Bool, _ message: String) {
    print(condition ? "ok: \(message)" : "FAIL: \(message)")
    if !condition { failures += 1 }
}

@discardableResult
func run(_ executable: String, _ arguments: String...) -> (status: Int32, output: String) {
    let process = Process()
    process.executableURL = URL(fileURLWithPath: executable)
    process.arguments = arguments
    let pipe = Pipe()
    process.standardOutput = pipe
    process.standardError = pipe
    try! process.run()
    let output = String(decoding: pipe.fileHandleForReading.readDataToEndOfFile(), as: UTF8.self)
    process.waitUntilExit()
    return (process.terminationStatus, output.trimmingCharacters(in: .whitespacesAndNewlines))
}

func wait(_ seconds: Double, until condition: () -> Bool) -> Bool {
    let deadline = Date().addingTimeInterval(seconds)
    while Date() < deadline {
        if condition() { return true }
        RunLoop.current.run(until: Date().addingTimeInterval(0.05))
    }
    return condition()
}

/// service STOPPING_SERVICE_URL
func serviceTests(_ args: [String]) {
    if case .stopping(let body) = ServiceClient.release(URL(string: args[0])!, token: "token") {
        check(body.contains("finishing a write"), "a 503 answer to a release means the service is stopping: \(body)")
    } else {
        check(false, "a 503 answer to a release is not reported as stopping")
    }

    // The app's release state. An eject asks for a release; the service's
    // answer decides the verdict, and later what its exit means.
    let (declined, declinedVerdict) = ReleaseState.after(.failed("HTTP 401: authentication required"))
    check(declined == .none && declinedVerdict != .approve, "a declined release dissents and leaves the library in use")
    check(declined.afterExit(status: 2, libraryPresent: true) == .failed, "a later crash after a declined release is an error, not \"released\"")
    let (finishing, finishingVerdict) = ReleaseState.after(.stopping("{}"))
    check(finishing == .finishing && finishingVerdict == .dissent(ReleaseState.finishingMessage), "a release that ran out of time says Pharos is finishing a write")
    check(finishing.afterExit(status: 0, libraryPresent: true) == .released, "and the library shows as released once the service exits")
    let (released, releasedVerdict) = ReleaseState.after(.released)
    check(released == .released && releasedVerdict == .approve && released.afterExit(status: 0, libraryPresent: true) == .released, "a release lets the eject go ahead")
    check(ReleaseState.after(.notRunning) == (.released, .approve), "with no service there is nothing to release")
    check(released.afterExit(status: 1, libraryPresent: false) == .unplugged && ReleaseState.none.afterExit(status: 1, libraryPresent: false) == .unplugged,
          "a service that exits once its drive is gone shows the library unplugged")
    check(ReleaseState.none.afterExit(status: 0, libraryPresent: true) == .released, "a clean exit closed the catalog: the library can be reopened")

    // The service's stderr is drained as it is written.
    let script = "i=0; while [ $i -lt 2000 ]; do echo 'a line of service diagnostics, 46 bytes long.' >&2; i=$((i+1)); done; echo last-line >&2"
    func shell(_ stderr: Any) -> Process {
        let process = Process()
        process.executableURL = URL(fileURLWithPath: "/bin/sh")
        process.arguments = ["-c", script]
        process.standardError = stderr
        try! process.run()
        return process
    }
    let undrained = Pipe()
    let blocked = shell(undrained)
    check(!wait(3) { !blocked.isRunning }, "control: a process whose stderr nobody reads blocks after 64 KB")
    blocked.terminate()
    let log = ServiceLog(limit: 4096)
    let drained = shell(log.pipe)
    check(wait(10) { !drained.isRunning }, "a service writing 92 KB to a drained stderr runs to the end")
    let text = log.text()
    check(text.hasSuffix("last-line\n") && text.utf8.count <= 4096, "the log keeps the last \(text.utf8.count) bytes, ending \(text.suffix(10).debugDescription)")
    // Its memory stays bounded however much the service writes.
    let before = footprint()
    let flood = ServiceLog()
    let flooding = Process()
    flooding.executableURL = URL(fileURLWithPath: "/bin/sh")
    flooding.arguments = ["-c", "head -c 268435456 /dev/zero >&2; echo end >&2"]
    flooding.standardError = flood.pipe
    try! flooding.run()
    flooding.waitUntilExit()
    let tail = flood.text(waiting: 10)
    let grown = (Int64(footprint()) - Int64(before)) / 1_000_000
    check(tail.hasSuffix("end\n") && tail.utf8.count <= 64 * 1024 && grown < 48, "after 256 MB of stderr the log holds \(tail.utf8.count) bytes and the process grew \(grown) MB")
}

/// This process's memory footprint, as Activity Monitor reports it.
func footprint() -> UInt64 {
    var info = task_vm_info_data_t()
    var count = mach_msg_type_number_t(MemoryLayout<task_vm_info_data_t>.size / MemoryLayout<natural_t>.size)
    let result = withUnsafeMutablePointer(to: &info) {
        $0.withMemoryRebound(to: integer_t.self, capacity: Int(count)) { task_info(mach_task_self_, task_flavor_t(TASK_VM_INFO), $0, &count) }
    }
    return result == KERN_SUCCESS ? info.phys_footprint : 0
}

final class Events {
    private let lock = NSLock()
    private var items: [String] = []
    func add(_ item: String) { lock.withLock { items.append(item) } }
    func all() -> [String] { lock.withLock { items } }
    func count(_ prefix: String) -> Int { all().filter { $0.hasPrefix(prefix) }.count }
}

/// Starts `pharos serve` on a library and waits until it answers.
func serveLibrary(_ pharos: String, config: String, service: URL, token: String) -> Process {
    let process = Process()
    process.executableURL = URL(fileURLWithPath: pharos)
    process.arguments = ["--config", config, "serve"]
    process.standardOutput = FileHandle.nullDevice
    process.standardError = FileHandle(forWritingAtPath: "/dev/null")
    try! process.run()
    let ready = wait(20) {
        var request = URLRequest(url: service.appendingPathComponent("api/health"))
        request.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
        let done = DispatchSemaphore(value: 0)
        var ok = false
        URLSession.shared.dataTask(with: request) { _, response, _ in
            ok = (response as? HTTPURLResponse)?.statusCode == 200
            done.signal()
        }.resume()
        done.wait()
        return ok
    }
    check(ready, "service is serving the library on the test image")
    return process
}

/// volume UUID MOUNT VOLUME_DEVICE PHAROS CONFIG PORT TOKEN IMAGE_DEVICE
func volumeTests(_ args: [String]) {
    let uuid = args[0], mount = args[1], device = args[2], pharos = args[3], config = args[4]
    let service = URL(string: "http://127.0.0.1:\(args[5])/")!, token = args[6]
    let events = Events()
    var releaseToken = token
    var lastRelease: ServiceClient.Release?

    func startMonitor() -> LibraryVolumeMonitor {
        let monitor = LibraryVolumeMonitor(volumeUUID: uuid)!
        // Release, then approve or dissent: the monitor and release pieces
        // the app's Eject uses. (The app itself refuses Finder's ejects.)
        monitor.approveUnmount = {
            events.add("approval")
            let result = ServiceClient.release(service, token: releaseToken)
            lastRelease = result
            return ReleaseState.after(result).1
        }
        monitor.onUnmount = { events.add("unmount") }
        monitor.onMount = { events.add("mount \($0.path)") }
        monitor.start()
        return monitor
    }
    func serve() -> Process { serveLibrary(pharos, config: config, service: service, token: token) }
    let volume = LibraryVolume(containing: URL(fileURLWithPath: mount).appendingPathComponent("Pharos"))
    check(volume?.uuid == uuid.uppercased() && volume?.relativePath == "Pharos", "LibraryVolume finds the image's UUID and the library within it")

    var monitor = startMonitor()
    check(wait(3) { events.count("mount \(mount)") == 1 }, "monitor reports the mounted volume at start")
    var process = serve()

    // A release that fails makes the eject fail with Pharos's message.
    releaseToken = "wrong-token"
    let refused = run("/usr/sbin/diskutil", "unmount", mount)
    check(refused.status != 0 && refused.output.contains("Pharos could not release its library (HTTP 401"), "failed release dissents: \(refused.output.split(separator: "\n").first ?? "")")
    check(events.count("approval") == 1, "approval callback ran once")
    if case .failed(let reason)? = lastRelease { check(reason.contains("401"), "release hook reported the failure (\(reason.prefix(40)))") } else { check(false, "release hook did not fail: \(String(describing: lastRelease))") }
    check(process.isRunning && FileManager.default.fileExists(atPath: config), "service and volume are untouched")

    // A release that succeeds lets the eject through.
    releaseToken = token
    let started = Date()
    let unmounted = run("/usr/sbin/diskutil", "unmount", mount)
    check(unmounted.status == 0, "released volume unmounts (\(String(format: "%.1f", Date().timeIntervalSince(started))) s): \(unmounted.output)")
    check(lastRelease == .released, "release hook released the catalog")
    check(wait(3) { !process.isRunning } && process.terminationStatus == 0, "service exited 0 after the release")
    check(wait(3) { events.count("unmount") >= 1 }, "monitor reports the unmount")

    // The volume comes back.
    let mounted = run("/usr/sbin/diskutil", "mount", device)
    check(mounted.status == 0 && wait(5) { events.count("mount ") >= 2 }, "monitor reports the volume mounting again: \(events.all().last ?? "")")
    let back = events.all().last(where: { $0.hasPrefix("mount ") }).map { String($0.dropFirst(6)) } ?? ""
    check(volume.map { FileManager.default.fileExists(atPath: $0.directory(at: URL(fileURLWithPath: back)).appendingPathComponent("library.toml").path) } == true,
          "the library is found again at \(back)")

    // With no service running there is nothing to release.
    let quiet = run("/usr/sbin/diskutil", "unmount", mount)
    check(quiet.status == 0 && lastRelease == .notRunning, "eject without a running service is approved (\(String(describing: lastRelease)))")
    _ = run("/usr/sbin/diskutil", "mount", device)
    check(wait(5) { events.count("mount ") >= 3 }, "mounted again")

    // Once stopped, the monitor no longer holds up an unmount.
    monitor.stop()
    let approvals = events.count("approval")
    let unwatched = run("/usr/sbin/diskutil", "unmount", mount)
    check(unwatched.status == 0 && events.count("approval") == approvals, "a stopped monitor is not consulted")
    _ = run("/usr/sbin/diskutil", "mount", device)
    monitor = startMonitor()
    check(wait(5) { events.count("mount \(mount)") >= 1 }, "restarted monitor sees the volume")

    // A forced detach, the nearest a test gets to pulling the drive: it may
    // ask for approval but goes ahead whatever the answer. With the release
    // failing, the service still holds the catalog when the volume vanishes,
    // as after a real yank, and must stop by itself.
    process = serve()
    releaseToken = "wrong-token"
    let unmounts = events.count("unmount")
    let yank = run("/usr/bin/hdiutil", "detach", "-force", args[7])
    check(yank.status == 0, "a forced detach goes ahead despite a dissent (approval asked: \(events.count("approval") > approvals))")
    check(wait(5) { events.count("unmount") > unmounts }, "monitor reports the volume gone")
    check(wait(5) { !process.isRunning } && process.terminationStatus == 1, "service exited 1 on its own after its drive vanished (\(process.terminationStatus))")
    monitor.stop()
}

/// eject MOUNT APP CONFIG PORT TOKEN
/// The Eject button's path, with APP the library's Pharos.app on the image:
/// release, stop what runs from the app, then eject once the app has exited.
func ejectTests(_ args: [String]) {
    let mount = URL(fileURLWithPath: args[0]), app = URL(fileURLWithPath: args[1]), config = args[2]
    let service = URL(string: "http://127.0.0.1:\(args[3])/")!, token = args[4]
    let name = mount.lastPathComponent
    let pharos = app.appendingPathComponent("Contents/MacOS/pharos").path

    // Which processes run from the app.
    let listed: [(pid: pid_t, path: String)] = [(1, "/usr/bin/true"), (2, canonicalPath(app) + "/Contents/MacOS/pharos"),
                                                (getpid(), canonicalPath(app) + "/Contents/MacOS/PharosApp"), (3, canonicalPath(app) + " 2/Contents/MacOS/pharos")]
    check(DriveProcesses.running(from: app, in: listed) == [2], "only other processes inside the app count: \(DriveProcesses.running(from: app, in: listed))")

    // The service, and an agent's MCP server, both running from the image.
    let process = serveLibrary(pharos, config: config, service: service, token: token)
    let agent = Process()
    agent.executableURL = URL(fileURLWithPath: pharos)
    agent.arguments = ["mcp"]
    let input = Pipe()
    agent.standardInput = input
    agent.standardOutput = FileHandle.nullDevice
    agent.standardError = FileHandle.nullDevice
    try! agent.run()
    check(wait(3) { DriveProcesses.running(from: app).contains(agent.processIdentifier) }, "the MCP server runs from the app on the image")

    check(ServiceClient.release(service, token: token) == .released && wait(5) { !process.isRunning }, "the service released the library and exited \(process.terminationStatus)")
    let busy = run("/usr/sbin/diskutil", "eject", mount.path)
    check(busy.status != 0 && FileManager.default.fileExists(atPath: mount.path), "an MCP server running from the drive keeps it from ejecting: \(busy.output.split(separator: "\n").last ?? "")")

    let stopped = DriveProcesses.stop(runningFrom: app)
    check(stopped == [agent.processIdentifier] && wait(3) { !agent.isRunning }, "Eject stops it: \(stopped)")

    // Standing in for the app, which quits once the eject is arranged.
    let quitting = Process()
    quitting.executableURL = URL(fileURLWithPath: "/bin/sleep")
    quitting.arguments = ["2"]
    try! quitting.run()
    try! VolumeEject.afterExit(of: quitting.processIdentifier, mount: mount, name: name)
    Thread.sleep(forTimeInterval: 1)
    check(quitting.isRunning && FileManager.default.fileExists(atPath: mount.path), "the drive waits for the app to exit")
    check(wait(30) { !FileManager.default.fileExists(atPath: mount.path) }, "then it ejects")
}

@main struct Harness {
    static func main() {
        guard ProcessInfo.processInfo.environment["PHAROS_SUPPORT_DIR"] != nil else {
            print("Set PHAROS_SUPPORT_DIR: the services this starts write there.")
            exit(2)
        }
        let args = Array(CommandLine.arguments.dropFirst())
        switch args.first {
        case "volume": volumeTests(Array(args.dropFirst()))
        case "service": serviceTests(Array(args.dropFirst()))
        case "eject": ejectTests(Array(args.dropFirst()))
        default:
            print("usage: harness service … | volume … | eject …")
            exit(2)
        }
        print(failures == 0 ? "PASS" : "\(failures) FAILED")
        exit(failures == 0 ? 0 : 1)
    }
}
