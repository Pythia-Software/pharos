import AppKit
import SwiftUI
import WebKit

// The app icon's palette. `masthead` matches the top of the web header so the
// title bar and header read as one band.
enum Harbor {
    static let masthead = NSColor(srgbRed: 0x2b / 255, green: 0x52 / 255, blue: 0x41 / 255, alpha: 1)
    static let sea = Color(red: 0x10 / 255, green: 0x24 / 255, blue: 0x1b / 255)
    static let stone = Color(red: 0xf3 / 255, green: 0xeb / 255, blue: 0xdc / 255)
    static let brass = Color(red: 0xd4 / 255, green: 0xa8 / 255, blue: 0x57 / 255)
    static let backdrop = LinearGradient(colors: [Color(nsColor: masthead), sea], startPoint: .top, endPoint: .bottom)
}

// The content runs under the transparent title bar so the web header and the
// traffic lights share one band. AppKit keeps the lights 14pt from the top of
// a 28pt title bar and resets them whenever it lays the title bar out again,
// so they are re-centered in a header-height bar after each resize or focus
// change. An empty toolbar would center them too, but it swallows the clicks
// meant for the header's tabs and search.
struct WindowChrome: NSViewRepresentable {
    static let titlebarHeight: CGFloat = 52
    static let trafficLightInset: CGFloat = 19

    final class ChromeView: NSView {
        private var observers: [NSObjectProtocol] = []

        override func viewDidMoveToWindow() {
            super.viewDidMoveToWindow()
            observers.forEach(NotificationCenter.default.removeObserver)
            observers = []
            // `.windowStyle(.hiddenTitleBar)` makes the title bar transparent;
            // setting that here instead is undone by SwiftUI.
            guard let window else { return }
            window.titlebarSeparatorStyle = .none
            window.backgroundColor = Harbor.masthead
            let relayouts = [
                NSWindow.didResizeNotification, NSWindow.didExitFullScreenNotification,
                NSWindow.didBecomeKeyNotification, NSWindow.didResignKeyNotification,
            ]
            for name in relayouts {
                observers.append(NotificationCenter.default.addObserver(forName: name, object: window, queue: .main) { [weak self] _ in
                    self?.placeTrafficLights()
                })
            }
            placeTrafficLights()
        }

        deinit { observers.forEach(NotificationCenter.default.removeObserver) }

        private func placeTrafficLights() {
            // In full screen macOS owns the title bar that slides in on hover.
            guard let window, !window.styleMask.contains(.fullScreen),
                  let close = window.standardWindowButton(.closeButton),
                  let container = close.superview?.superview else { return }
            let height = WindowChrome.titlebarHeight
            container.frame = NSRect(x: container.frame.minX, y: window.frame.height - height, width: container.frame.width, height: height)
            let buttons: [NSWindow.ButtonType] = [.closeButton, .miniaturizeButton, .zoomButton]
            for (index, type) in buttons.enumerated() {
                guard let button = window.standardWindowButton(type), let bar = button.superview else { continue }
                button.setFrameOrigin(NSPoint(
                    x: WindowChrome.trafficLightInset + CGFloat(index) * 20,
                    y: (bar.frame.height - button.frame.height) / 2
                ))
            }
        }
    }

    func makeNSView(context: Context) -> NSView { ChromeView() }
    func updateNSView(_ view: NSView, context: Context) {}
}

extension View {
    func harborBackdrop() -> some View {
        padding(40)
            .frame(minWidth: 620, maxWidth: .infinity, minHeight: 420, maxHeight: .infinity)
            .background(Harbor.backdrop)
            .foregroundStyle(Harbor.stone)
            .environment(\.colorScheme, .dark)
    }
}

final class ArchiveService: ObservableObject {
    /// A library whose drive was ejected or unplugged, or which Pharos let go
    /// of (or is letting go of) so that its drive could be ejected.
    struct Disconnection: Equatable {
        enum State: Equatable {
            /// A release ran out of time: the service finishes a write, then exits.
            case finishing
            /// The catalog is closed; the drive is still there.
            case released
            case unplugged
        }
        let volume: String
        let state: State
        /// Why the drive could not be ejected, after the Eject button released it.
        var note: String? = nil
    }

    @Published var url: URL? {
        // A page error belongs to the service it came from.
        didSet { if url != oldValue { clearPageFailure(); pageTarget = nil } }
    }
    // The broker keeps one URL while the selected library can change.
    @Published private(set) var libraryConnection: String? {
        // Another library, or this one connected or lost: a page failure
        // belongs to the connection it happened on.
        didSet { if libraryConnection != oldValue { clearPageFailure(); pageTarget = nil } }
    }
    private var managedLibraryConnected = false
    private var managedSelectedLibrary = ""
    var hasConnectedLibrary: Bool { !managedApplication || managedLibraryConnected }
    // What the broker last said (see ManagedLibraryStatus in MenuBar.swift).
    private var managedStatus = ManagedLibraryStatus()
    private var managedStateRequestedAt = Date.distantPast
    private var currentManagedStatus: ManagedLibraryStatus {
        managedStatus.current(pendingFor: managedLibraryStateTask == nil ? nil : Date().timeIntervalSince(managedStateRequestedAt))
    }
    var libraryOperation: String? { managedApplication ? currentManagedStatus.activeOperation : nil }
    var noLibrarySelected: Bool { managedApplication && currentManagedStatus.noLibrary }
    @Published var error: String?
    /// Why the window's page could not load although the service answers;
    /// View → Reload tries again.
    @Published var pageError: String?
    /// The window's page could not load because the library's service does
    /// not answer yet (it may be opening a large catalog); it reloads by
    /// itself once the service answers.
    @Published var pageWaiting = false
    // The broker can answer before its library service has opened the catalog.
    // Keep the startup beacon until a page finishes or a navigation is cancelled
    // or diverted. Once dismissed, later navigations keep the old page visible.
    @Published var pageLoading = true
    private var pageWatch = 0
    /// The page that failed, which the web view loads when it comes back.
    private var pageTarget: URL?
    @Published var status = "Starting local archive…"
    @Published var disconnected: Disconnection?
    /// The Eject button's release and eject are under way.
    @Published var ejecting = false
    @Published var setupNeeded = false
    @Published var settingUp = false
    @Published var setupError: String?

    private var process: Process?
    private var serviceLog: ServiceLog?
    /// The directory holding library.toml, when serving a portable library.
    private var library: URL?
    private var volume: LibraryVolume?
    private var monitor: LibraryVolumeMonitor?
    // Shared with the volume monitor's queue, which releases the library
    // during an eject.
    private let lock = NSLock()
    private var endpoint: (url: URL, token: String)?
    private var service: Process?
    private var release = ReleaseState.none
    private var terminationObserver: NSObjectProtocol?
    private var shuttingDown = false
    private var updateLock: AppUpdateLock?
    private(set) var managedApplication = false
    private var managedPortable = false
    private var managedLibraryMonitors: [String: LibraryVolumeMonitor] = [:]
    private var managedLibraryStateTask: URLSessionDataTask?
    private var brokerOperationsTask: URLSessionDataTask?
    private let releaseService: (URL, String) -> ServiceClient.Release
    var deferUpdate: (@escaping (Error?) -> Void) -> Void = { $0(nil) }
    var resumeUpdates: () -> Void = {}
    private(set) var menuBar: MenuBarController!

    init(plan: LaunchPlan? = nil,
         releaseService: @escaping (URL, String) -> ServiceClient.Release = { ServiceClient.release($0, token: $1) }) {
        self.releaseService = releaseService
        defer { menuBar = MenuBarController(service: self) }
        // Here rather than on a view: with its window closed, Pharos keeps
        // running, and a view's observer would be gone when it quits.
        terminationObserver = NotificationCenter.default.addObserver(
            forName: NSApplication.willTerminateNotification, object: nil, queue: .main
        ) { [weak self] _ in self?.stop() }
        do {
            try AppUpdateLock.current().recover(version: Bundle.main.object(forInfoDictionaryKey: "CFBundleVersion") as? String ?? "")
        } catch {
            self.error = error.localizedDescription
            return
        }
        guard let plan else {
            managedApplication = true
            startApplication()
            return
        }
        switch plan {
        case .library(let directory):
            openLibrary(directory)
        case .user:
            let config = FileManager.default.homeDirectoryForCurrentUser
                .appendingPathComponent("Library/Application Support/Pharos/archive.toml")
            guard FileManager.default.fileExists(atPath: config.path) else {
                setupNeeded = true
                return
            }
            guard let contents = try? String(contentsOf: config, encoding: .utf8) else {
                error = "Could not read \(config.path)."
                return
            }
            open(config, contents: contents)
        }
    }

    /// The app owns a broker; every library service receives an allocated
    /// loopback listener. Neither app placement nor a historical port chooses
    /// which catalog a window or an integration reaches.
    private func startApplication() {
        let bundle = Bundle.main.bundleURL
        let cli = bundle.appendingPathComponent("Contents/MacOS/pharos")
        let runtime = FileManager.default.temporaryDirectory.appendingPathComponent("pharos-app-\(UUID().uuidString).json")
        let task = Process()
        task.executableURL = cli
        task.arguments = ["app", "--application", bundle.path, "--endpoint", runtime.path]
        let oldConfig: URL
        switch LaunchPlan.resolve(bundle: bundle) {
        case .library(let directory): oldConfig = directory.appendingPathComponent("library.toml")
        case .user: oldConfig = FileManager.default.homeDirectoryForCurrentUser.appendingPathComponent("Library/Application Support/Pharos/archive.toml")
        }
        if FileManager.default.fileExists(atPath: oldConfig.path) {
            task.arguments?.append(contentsOf: ["--initial-config", oldConfig.path])
        }
        var environment = ProcessInfo.processInfo.environment
        environment["PHAROS_PARENT_PID"] = String(getpid())
        task.environment = environment
        let log = ServiceLog()
        task.standardError = log.pipe
        serviceLog = log
        task.terminationHandler = { [weak self] process in
            try? FileManager.default.removeItem(at: runtime)
            DispatchQueue.main.async {
                guard let self, self.process === process, !self.shuttingDown else { return }
                self.process = nil
                self.url = nil
                self.error = "Pharos stopped. \(log.text())"
            }
        }
        process = task
        lock.withLock { service = task }
        do {
            try task.run()
            awaitApplicationEndpoint(runtime, task: task, deadline: Date().addingTimeInterval(120))
            watchManagedLibraries()
        } catch { self.error = "Could not start Pharos: \(error.localizedDescription)" }
        if let appVolume = LibraryVolume(containing: bundle), let watcher = LibraryVolumeMonitor(volumeUUID: appVolume.uuid) {
            managedPortable = true
            watcher.approveUnmount = { .dissent("Pharos is running from this drive. Use Eject in Pharos, or quit Pharos before ejecting it.") }
            watcher.onUnmount = { DispatchQueue.main.async { NSApplication.shared.terminate(nil) } }
            monitor = watcher
            watcher.start()
        }
    }

    private func awaitApplicationEndpoint(_ file: URL, task: Process, deadline: Date) {
        guard !shuttingDown, task.isRunning else { return }
        if let data = try? Data(contentsOf: file),
           let value = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
           let pid = value["pid"] as? Int, pid == Int(task.processIdentifier),
           let address = value["url"] as? String, let token = value["token"] as? String,
           var components = URLComponents(string: address) {
            components.queryItems = [URLQueryItem(name: "token", value: token)]
            if let target = components.url {
                lock.withLock { endpoint = (target, token) }
                waitForService(target, token: token)
                return
            }
        }
        guard Date() < deadline else { error = "Pharos did not become ready within 2 minutes."; task.terminate(); return }
        DispatchQueue.main.asyncAfter(deadline: .now() + 0.1) { [weak self] in self?.awaitApplicationEndpoint(file, task: task, deadline: deadline) }
    }

    /// Finder can release a data SSD while an app in Applications stays open.
    /// Only libraries on that volume are closed; local pending capture remains
    /// configured. The app's own volume still requires quitting the app.
    /// The broker's operations, which it answers without its main lock, so
    /// the menu can tell a library operation is running while /api/app/state
    /// waits for one to finish. An older broker without the endpoint leaves
    /// the state-based detection alone.
    private func watchBrokerOperations(_ broker: (url: URL, token: String)) {
        // Broker-only: no library's service is asked.
        guard brokerOperationsTask == nil, let request = localRequest("/api/app/operations?libraries=0") else { return }
        brokerOperationsTask = URLSession.shared.dataTask(with: request) { [weak self] data, _, _ in
            let document = data.flatMap { try? JSONSerialization.jsonObject(with: $0) as? [String: Any] }
            DispatchQueue.main.async {
                guard let self else { return }
                self.brokerOperationsTask = nil
                guard !self.shuttingDown, self.lock.withLock({ self.endpoint?.url == broker.url }) else { return }
                // An answer that is not an operations document (an older or
                // restarting broker) clears what an earlier one said.
                let running = document.flatMap(ManagedLibraryStatus.runningBrokerOperations) ?? []
                self.managedStatus = self.managedStatus.withBrokerOperations(running)
            }
        }
        brokerOperationsTask?.resume()
    }

    private func watchManagedLibraries() {
        guard managedApplication, !shuttingDown else { return }
        DispatchQueue.main.asyncAfter(deadline: .now() + 5) { [weak self] in self?.watchManagedLibraries() }
        // Its own schedule: /api/app/state can wait on the broker's lock for
        // minutes, which is exactly when this poll is needed.
        if let broker = lock.withLock({ endpoint }) { watchBrokerOperations(broker) }
        guard managedLibraryStateTask == nil, let request = localRequest("/api/app/state"), let broker = lock.withLock({ endpoint }) else { return }
        managedStateRequestedAt = Date()
        managedLibraryStateTask = URLSession.shared.dataTask(with: request) { [weak self] data, _, error in
            let timedOut = (error as? URLError)?.code == .timedOut
            let state = data.flatMap { try? JSONSerialization.jsonObject(with: $0) as? [String: Any] }
            DispatchQueue.main.async {
                guard let self else { return }
                self.managedLibraryStateTask = nil
                guard !self.shuttingDown, self.lock.withLock({ self.endpoint?.url == broker.url }) else { return }
                guard let state, let status = ManagedLibraryStatus.answered(state), let libraries = state["libraries"] as? [[String: Any]] else {
                    // A timeout means the broker may be holding its lock for a
                    // library operation; keep saying so until it answers.
                    // Other failures keep the last answer.
                    self.managedStatus = self.managedStatus.failedRequest(timedOut: timedOut)
                    return
                }
                self.managedStatus = status.withBrokerOperations(self.managedStatus.brokerOperation.map { [$0] } ?? [])
                let selected = status.selected
                let generation = (state["generation"] as? NSNumber)?.stringValue ?? "0"
                let connected = status.connected
                self.managedLibraryConnected = connected
                self.managedSelectedLibrary = selected
                let connection = "\(selected):\(generation):\(connected)"
                if self.libraryConnection != connection { self.libraryConnection = connection }
                let applicationVolume = LibraryVolume(containing: Bundle.main.bundleURL)?.uuid
                for library in libraries {
                    guard let path = library["config"] as? String,
                          let volume = LibraryVolume(containing: URL(fileURLWithPath: path).deletingLastPathComponent()),
                          volume.uuid != applicationVolume, self.managedLibraryMonitors[volume.uuid] == nil,
                          let watcher = LibraryVolumeMonitor(volumeUUID: volume.uuid) else { continue }
                    watcher.approveUnmount = {
                        let body = try? JSONSerialization.data(withJSONObject: ["volume": "uuid:" + volume.uuid])
                        switch ServiceClient.release(broker.url, token: broker.token, path: "/api/app/release-volume", body: body) {
                        case .released, .notRunning: return .approve
                        case .stopping(let reason), .failed(let reason): return .dissent("Pharos could not release this library yet: \(reason)")
                        }
                    }
                    self.managedLibraryMonitors[volume.uuid] = watcher
                    watcher.start()
                }
            }
        }
        managedLibraryStateTask?.resume()
    }

    var installationFolder: URL { Bundle.main.bundleURL.deletingLastPathComponent() }

    /// Native menu requests use the same authenticated loopback service as
    /// the web view, but do not depend on its cookies or window lifetime.
    func localRequest(_ path: String, method: String = "GET") -> URLRequest? {
        guard url != nil, let endpoint = lock.withLock({ self.endpoint }),
              let target = URL(string: path, relativeTo: endpoint.url) else { return nil }
        var request = URLRequest(url: target, cachePolicy: .reloadIgnoringLocalCacheData, timeoutInterval: 30)
        request.httpMethod = method
        request.setValue("Bearer \(endpoint.token)", forHTTPHeaderField: "Authorization")
        if managedApplication && !path.hasPrefix("/api/app/") && !managedSelectedLibrary.isEmpty {
            request.setValue(managedSelectedLibrary, forHTTPHeaderField: "X-Pharos-Library")
        }
        if method == "POST" { request.httpBody = Data("{}".utf8); request.setValue("application/json", forHTTPHeaderField: "Content-Type") }
        return request
    }

    /// Asks the library's service whether it answers, without the web
    /// view's cookies. In the managed app the request carries the selected
    /// library (localRequest), so the broker passes it on to that library's
    /// service rather than answering for itself.
    func checkReady(_ completion: @escaping (Bool) -> Void) {
        guard var request = localRequest("/api/ready") else { completion(false); return }
        // With no library connected the window shows the broker's own pages
        // (the chooser, its status): then the broker is what must answer.
        if !hasConnectedLibrary { request.setValue(nil, forHTTPHeaderField: "X-Pharos-Library") }
        request.timeoutInterval = 2
        URLSession.shared.dataTask(with: request) { _, response, _ in
            let code = (response as? HTTPURLResponse)?.statusCode ?? 0
            DispatchQueue.main.async { completion((200..<300).contains(code)) }
        }.resume()
    }

    /// The window's page did not load. While the service answers, that is an
    /// error to show with Reload. While it does not, wait: check again after
    /// 0.5 s, doubling up to every 10 s, and bring the page back (a new web
    /// view loads it) once the service answers.
    func pageFailed(_ reason: String, serviceAnswers: Bool, target: URL) {
        pageWatch += 1
        pageTarget = target
        if serviceAnswers {
            pageWaiting = false
            pageError = "\(reason)\n\nThe Pharos service is running, but the page still did not load. Choose View → Reload (⌘R) to try again."
            return
        }
        pageError = nil
        pageWaiting = true
        watchForService(pageWatch, delay: 0.5)
    }

    private func watchForService(_ watch: Int, delay: Double) {
        DispatchQueue.main.asyncAfter(deadline: .now() + delay) { [weak self] in
            guard let self, self.pageWatch == watch, self.pageWaiting, !self.shuttingDown else { return }
            self.checkReady { ready in
                guard self.pageWatch == watch, self.pageWaiting else { return }
                if ready { self.clearPageFailure() } else { self.watchForService(watch, delay: min(delay * 2, 10)) }
            }
        }
    }

    /// The page a new web view should load: the one that failed, if it is
    /// this service's, else the service's own.
    func takePageTarget() -> URL? {
        defer { pageTarget = nil }
        guard let target = pageTarget, let url, target.scheme == url.scheme, target.host == url.host, target.port == url.port else { return url }
        return target
    }

    private func clearPageFailure() {
        pageWatch += 1
        if pageError != nil { pageError = nil }
        if pageWaiting { pageWaiting = false }
    }

    /// View → Reload: reloads the page, or brings the web view back after a
    /// page that could not load.
    func reloadPage() {
        if pageError != nil || pageWaiting {
            clearPageFailure() // a new web view loads the service's page
            return
        }
        guard let webView = menuBar.webView else { return }
        if webView.url == nil, let url { webView.load(URLRequest(url: url)) } else { webView.reload() }
    }

    var canSetUpHere: Bool {
        let readOnly = (try? installationFolder.resourceValues(forKeys: [.volumeIsReadOnlyKey]))?.volumeIsReadOnly == true
        return !readOnly
    }

    var canCreatePortableLibrary: Bool {
        let path = installationFolder.standardizedFileURL.path
        let applications = FileManager.default.homeDirectoryForCurrentUser.appendingPathComponent("Applications").path
        let appFolder = ["/Applications", "/System/Applications", applications].contains {
            path == $0 || path.hasPrefix($0 + "/")
        }
        return canSetUpHere && !appFolder && FileManager.default.isWritableFile(atPath: installationFolder.path)
    }

    func setUpOnThisMac() {
        guard canSetUpHere else { return }
        let config = FileManager.default.homeDirectoryForCurrentUser
            .appendingPathComponent("Library/Application Support/Pharos/archive.toml")
        runSetup(["init", config.path]) { [weak self] in
            guard let self else { return }
            guard let contents = try? String(contentsOf: config, encoding: .utf8) else {
                self.setupError = "Could not read \(config.path) after setup."
                return
            }
            self.setupNeeded = false
            self.open(config, contents: contents)
        }
    }

    func setUpPortableLibrary() {
        guard canCreatePortableLibrary else { return }
        let directory = installationFolder
        runSetup(["init-library", directory.path]) { [weak self] in
            guard let self else { return }
            self.setupNeeded = false
            self.openLibrary(directory)
        }
    }

    private func runSetup(_ arguments: [String], onSuccess: @escaping () -> Void) {
        guard setupNeeded, !settingUp else { return }
        let cli = Bundle.main.bundleURL.appendingPathComponent("Contents/MacOS/pharos")
        guard FileManager.default.isExecutableFile(atPath: cli.path) else {
            setupError = "The Pharos command is missing from this app. Download a complete copy and try again."
            return
        }
        settingUp = true
        setupError = nil
        DispatchQueue.global(qos: .userInitiated).async { [weak self] in
            let task = Process()
            task.executableURL = cli
            task.arguments = arguments
            let errors = Pipe()
            task.standardError = errors
            let result: String?
            do {
                try task.run()
                task.waitUntilExit()
                result = task.terminationStatus == 0 ? nil : String(data: errors.fileHandleForReading.readDataToEndOfFile(), encoding: .utf8)
                    ?? "Setup exited with status \(task.terminationStatus)."
            } catch {
                result = error.localizedDescription
            }
            DispatchQueue.main.async {
                guard let self else { return }
                self.settingUp = false
                if let result { self.setupError = result.trimmingCharacters(in: .whitespacesAndNewlines) }
                else { onSuccess() }
            }
        }
    }

    private func openLibrary(_ directory: URL) {
        library = directory
        volume = LibraryVolume(containing: directory) ?? volume
        watchVolume()
        let config = directory.appendingPathComponent("library.toml")
        guard let contents = try? String(contentsOf: config, encoding: .utf8) else {
            if let volume, !FileManager.default.fileExists(atPath: directory.path) {
                disconnected = Disconnection(volume: volume.name, state: .unplugged)
            } else {
                error = "Could not read \(config.path)."
            }
            return
        }
        open(config, contents: contents)
    }

    private func watchVolume() {
        guard monitor == nil, let volume, let monitor = LibraryVolumeMonitor(volumeUUID: volume.uuid) else { return }
        // Pharos runs from the drive, so it cannot be unmounted until Pharos
        // quits. Say so, rather than leave Finder to blame "a program".
        let name = volume.name
        monitor.approveUnmount = { .dissent("Pharos is running from \(name). Use Eject in Pharos, or quit Pharos, to eject it.") }
        monitor.onUnmount = { [weak self] in DispatchQueue.main.async { self?.libraryWentAway() } }
        monitor.onMount = { [weak self] mount in DispatchQueue.main.async { self?.libraryCameBack(at: mount) } }
        self.monitor = monitor
        monitor.start()
    }

    private func open(_ config: URL, contents: String) {
        let name = config.lastPathComponent
        let token = tomlValue("api_token", in: contents)
        let host = tomlValue("host", in: contents) ?? "127.0.0.1"
        let port = tomlValue("port", in: contents) ?? "8765"
        guard let token else { error = "api_token is missing from \(name)"; return }
        guard let portNumber = Int(port) else { error = "port in \(name) is invalid"; return }
        var components = URLComponents()
        components.scheme = "http"
        components.host = host
        components.port = portNumber
        components.path = "/"
        components.queryItems = [URLQueryItem(name: "token", value: token)]
        guard let serviceURL = components.url else { error = "host or port in \(name) is invalid"; return }
        lock.withLock { endpoint = (serviceURL, token) }
        connectOrStart(serviceURL, token: token, config: config, contents: contents)
    }

    /// Asks the service to close the catalog before an eject. Blocks; call it
    /// off the main thread.
    private func releaseForEject() -> LibraryVolumeMonitor.Verdict {
        let (endpoint, service) = lock.withLock { () -> ((url: URL, token: String)?, Process?) in
            release = .asking
            return (self.endpoint, self.service)
        }
        guard let endpoint else { return .approve }
        let answer = releaseService(endpoint.url, endpoint.token)
        let (state, verdict) = ReleaseState.after(answer)
        lock.withLock { release = state }
        switch answer {
        case .released, .notRunning:
            // The service exits right after answering; let it go first.
            for _ in 0..<20 where service?.isRunning == true { Thread.sleep(forTimeInterval: 0.1) }
            DispatchQueue.main.async { [weak self] in self?.show(.released) }
        case .stopping(let reason):
            NSLog("Pharos is finishing a write before it releases its library: %@", reason)
            DispatchQueue.main.async { [weak self] in self?.show(.finishing) }
        case .failed(let reason):
            NSLog("Pharos could not release its library for an eject: %@", reason)
        }
        return verdict
    }

    private var releaseState: ReleaseState { lock.withLock { release } }

    /// The page's Eject button (see ArchiveWebView), and the one in the
    /// released window. Pharos runs from the library's drive, and so do the
    /// MCP servers agent clients start from it, so none of them may be left
    /// running: Pharos releases the library, stops those servers, and quits,
    /// and the drive is ejected once it has. `reply` gets nil once the library
    /// is released, or the reason Pharos kept it.
    func ejectLibrary(reply: @escaping (String?) -> Void = { _ in }) {
        guard !shuttingDown else {
            reply("Pharos is closing. Wait for it to finish before ejecting the drive.")
            return
        }
        deferUpdate { [weak self] error in
            if let error { reply(error.localizedDescription); return }
            self?.ejectAfterDeferringUpdate(reply: reply)
        }
    }

    private func ejectAfterDeferringUpdate(reply: @escaping (String?) -> Void) {
        if managedApplication {
            ejectManagedLibrary(reply: reply)
            return
        }
        guard let volume else {
            resumeUpdates()
            reply("This library is not on a drive that Pharos can eject.")
            return
        }
        guard !ejecting else {
            reply("Pharos is already ejecting \(volume.name).")
            return
        }
        ejecting = true
        let mount = library.flatMap { LibraryVolume(containing: $0)?.mount } ?? volume.mount
        let app = Bundle.main.bundleURL
        DispatchQueue.global(qos: .userInitiated).async { [weak self] in
            guard let self else { return reply("Pharos is closing.") }
            if case .dissent(let reason) = self.releaseForEject() {
                DispatchQueue.main.async {
                    self.ejecting = false
                    self.resumeUpdates()
                    reply(reason)
                }
                return
            }
            DispatchQueue.main.async { reply(nil) }
            DriveProcesses.stop(runningFrom: app)
            do {
                try VolumeEject.afterExit(of: getpid(), mount: mount, name: volume.name)
                DispatchQueue.main.async { NSApp.terminate(nil) }
            } catch {
                DispatchQueue.main.async {
                    self.ejecting = false
                    self.resumeUpdates()
                    self.url = nil
                    self.disconnected = Disconnection(volume: volume.name, state: .released,
                                                      note: "\(volume.name) could not be ejected: \(error.localizedDescription)")
                }
            }
        }
    }

    /// Resolve the current broker selection for each action; app placement
    /// and the previously displayed library cannot select the volume to eject.
    private func ejectManagedLibrary(reply: @escaping (String?) -> Void) {
        flushWebSession { [weak self] error in
            if let error { self?.resumeUpdates(); reply(error.localizedDescription); return }
            self?.ejectManagedLibraryAfterSessionSave(reply: reply)
        }
    }

    private func ejectManagedLibraryAfterSessionSave(reply: @escaping (String?) -> Void) {
        guard !ejecting else { reply("Pharos is already ejecting a drive."); return }
        guard let request = localRequest("/api/app/state"), let broker = lock.withLock({ endpoint }) else {
            resumeUpdates()
            reply("Pharos is not ready to release its library yet.")
            return
        }
        ejecting = true
        let app = Bundle.main.bundleURL
        let child = process
        let fail: (String) -> Void = { [weak self] reason in
            DispatchQueue.main.async {
                self?.ejecting = false
                self?.resumeUpdates()
                reply(reason)
            }
        }
        URLSession.shared.dataTask(with: request) { [weak self] data, response, error in
            guard let self else { return fail("Pharos is closing.") }
            do {
                if let error { throw error }
                guard (response as? HTTPURLResponse)?.statusCode == 200, let data else {
                    throw ManagedLibraryEject.Failure("Could not read the connected library. Try again once Pharos is ready.")
                }
                let volume = try ManagedLibraryEject.volume(in: data, application: app)
                switch ManagedLibraryEject.release(volume, service: broker.url, token: broker.token) {
                case .released: break
                case .notRunning: throw ManagedLibraryEject.Failure("Pharos stopped before confirming that its library was released.")
                case .stopping: throw ManagedLibraryEject.Failure(ReleaseState.finishingMessage)
                case .failed(let reason): throw ManagedLibraryEject.Failure("Could not release the library: \(reason)")
                }
                if LibraryVolume(containing: app)?.uuid == volume.uuid {
                    // The broker can also own libraries on other disks. Ask
                    // it to close those safely rather than killing its tree.
                    if let child, child.isRunning {
                        child.terminate()
                        let deadline = Date().addingTimeInterval(10)
                        while child.isRunning {
                            guard Date() < deadline else {
                                throw ManagedLibraryEject.Failure("Pharos is still closing. Try ejecting again in a moment.")
                            }
                            Thread.sleep(forTimeInterval: 0.05)
                        }
                    }
                    DriveProcesses.stopMCP(runningFrom: app)
                    try VolumeEject.afterExit(of: getpid(), mount: volume.mount, name: volume.name)
                    DispatchQueue.main.async { reply(nil); NSApp.terminate(nil) }
                } else {
                    // The app remains available with its local libraries.
                    try NSWorkspace.shared.unmountAndEjectDevice(at: volume.mount)
                    DispatchQueue.main.async {
                        self.ejecting = false
                        self.resumeUpdates()
                        self.url = broker.url
                        reply(nil)
                    }
                }
            } catch { fail(error.localizedDescription) }
        }.resume()
    }

    /// Shows a release in progress or done, unless the drive is already gone.
    private func show(_ state: Disconnection.State) {
        guard let volume, disconnected == nil || disconnected?.state == .finishing else { return }
        url = nil
        disconnected = Disconnection(volume: volume.name, state: state)
    }

    private func libraryWentAway() {
        guard let volume, disconnected?.state != .unplugged else { return }
        url = nil
        error = nil
        lock.withLock { release = .none }
        disconnected = Disconnection(volume: volume.name, state: .unplugged)
        // After a yank the service stops by itself within a second or two.
        stopService(killAfter: 5)
    }

    private func libraryCameBack(at mount: URL) {
        guard let volume, disconnected != nil else { return }
        disconnected = nil
        url = nil
        error = nil
        status = "Reopening the library on \(volume.name)…"
        lock.withLock { release = .none }
        openLibrary(volume.directory(at: mount))
    }

    /// Opens a library that was released for an eject that didn't happen.
    func reopen() {
        guard let volume else { return }
        libraryCameBack(at: library.flatMap { LibraryVolume(containing: $0)?.mount } ?? volume.mount)
    }

    private func connectOrStart(_ serviceURL: URL, token: String, config: URL, contents: String, attemptsRemaining: Int = 40) {
        guard let request = healthRequest(serviceURL, token: token) else {
            error = "Could not construct the local archive health URL."
            return
        }
        URLSession.shared.dataTask(with: request) { [weak self] _, response, _ in
            DispatchQueue.main.async {
                guard let self, !self.shuttingDown else { return }
                let code = (response as? HTTPURLResponse)?.statusCode ?? 0
                if (200..<300).contains(code) {
                    self.lock.withLock { self.release = .none }
                    self.error = nil
                    self.url = serviceURL
                } else if code == 503, attemptsRemaining > 1 {
                    // A service that is stopping, e.g. an older build's that
                    // just quit, still holds the port until it exits.
                    self.status = "Waiting for the previous Pharos service to stop…"
                    DispatchQueue.main.asyncAfter(deadline: .now() + 0.5) {
                        self.connectOrStart(serviceURL, token: token, config: config, contents: contents, attemptsRemaining: attemptsRemaining - 1)
                    }
                } else {
                    self.startService(serviceURL, token: token, config: config, contents: contents)
                }
            }
        }.resume()
    }

    private func startService(_ serviceURL: URL, token: String, config: URL, contents: String) {
        guard !shuttingDown else { return }
        let task = Process()
        let bundledExecutable = Bundle.main.executableURL?.deletingLastPathComponent().appendingPathComponent("pharos")
        if let bundledExecutable, FileManager.default.isExecutableFile(atPath: bundledExecutable.path) {
            task.executableURL = bundledExecutable
            task.arguments = ["--config", config.path, "serve"]
        } else if let executable = tomlValue("executable", in: contents) {
            task.executableURL = URL(fileURLWithPath: executable)
            task.arguments = ["--config", config.path, "serve"]
        } else {
            task.executableURL = URL(fileURLWithPath: "/usr/bin/env")
            task.arguments = ["pharos", "--config", config.path, "serve"]
        }
        // The service stops once Pharos has exited, even when Pharos could
        // not stop it (a force quit or a crash), so it never keeps the
        // library's drive busy by itself.
        var environment = ProcessInfo.processInfo.environment
        environment["PHAROS_PARENT_PID"] = String(getpid())
        task.environment = environment
        let log = ServiceLog()
        task.standardError = log.pipe
        serviceLog = log
        task.terminationHandler = { [weak self] process in
            let diagnostic = log.text().trimmingCharacters(in: .whitespacesAndNewlines)
            DispatchQueue.main.async {
                guard let self, self.process === process else { return }
                if self.shuttingDown { return }
                self.process = nil
                self.url = nil
                // Released for an eject, or its drive went away: not an error.
                if let volume = self.volume, let library = self.library {
                    switch self.releaseState.afterExit(status: process.terminationStatus, libraryPresent: FileManager.default.fileExists(atPath: library.path)) {
                    case .unplugged:
                        self.disconnected = Disconnection(volume: volume.name, state: .unplugged)
                        return
                    case .released:
                        self.show(.released)
                        return
                    case .failed:
                        break
                    }
                }
                let summary = "The local archive service exited with status \(process.terminationStatus)."
                self.error = diagnostic.isEmpty ? summary : "\(summary)\n\n\(diagnostic)"
            }
        }
        process = task
        lock.withLock {
            service = task
            release = .none
        }
        do {
            try task.run()
            waitForService(serviceURL, token: token)
        } catch {
            process = nil
            url = nil
            self.error = "Could not start the local archive service: \(error.localizedDescription)"
        }
    }

    private func healthRequest(_ serviceURL: URL, token: String) -> URLRequest? {
        var healthComponents = URLComponents(url: serviceURL, resolvingAgainstBaseURL: false)
        healthComponents?.path = "/api/ready"
        healthComponents?.queryItems = nil
        guard let healthURL = healthComponents?.url else { return nil }
        var request = URLRequest(url: healthURL)
        request.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
        request.timeoutInterval = 1
        return request
    }

    // A large catalog may need to checkpoint its WAL before the service can
    // listen. Keep the wrapper alive while that startup work completes.
    private func waitForService(_ serviceURL: URL, token: String, attemptsRemaining: Int = 600) {
        guard !shuttingDown, process?.isRunning == true else { return }
        guard let request = healthRequest(serviceURL, token: token) else {
            error = "Could not construct the local archive health URL."
            process?.terminate()
            return
        }
        URLSession.shared.dataTask(with: request) { [weak self] _, response, _ in
            DispatchQueue.main.async {
                guard let self, !self.shuttingDown, self.process?.isRunning == true else { return }
                if let http = response as? HTTPURLResponse, (200..<300).contains(http.statusCode) {
                    self.error = nil
                    self.url = serviceURL
                } else if attemptsRemaining > 1 {
                    DispatchQueue.main.asyncAfter(deadline: .now() + 0.2) {
                        self.waitForService(serviceURL, token: token, attemptsRemaining: attemptsRemaining - 1)
                    }
                } else {
                    self.url = nil
                    self.error = "The local archive service did not become ready within 2 minutes."
                    self.process?.terminate()
                }
            }
        }.resume()
    }

    /// Stops the service this app started; SIGTERM lets it close the catalog.
    private func stopService(killAfter seconds: Double? = nil) {
        let running = process
        process = nil
        serviceLog = nil
        guard let running, running.isRunning else { return }
        running.terminate()
        if let seconds {
            DispatchQueue.global().asyncAfter(deadline: .now() + seconds) {
                if running.isRunning { kill(running.processIdentifier, SIGKILL) }
            }
        }
    }

    /// Stop only this app's service, wait for catalog closure, and (for an
    /// update) exclude every bundled CLI until the new app has launched.
    func prepareToTerminate(installing: Bool, timeout: TimeInterval = 10, completion: @escaping (Error?) -> Void) {
        flushWebSession { [weak self] error in
            if let error { completion(error); return }
            self?.prepareAfterSessionSave(installing: installing, timeout: timeout, completion: completion)
        }
    }

    // WebKit's store remains temporary. Await the small library-backed session
    // writes before closing the service that saves them, including for updates.
    private func flushWebSession(completion: @escaping (Error?) -> Void) {
        guard managedApplication, let webView = menuBar?.webView else { completion(nil); return }
        var finished = false
        let finish: (Error?) -> Void = { error in
            guard !finished else { return }
            finished = true
            completion(error)
        }
        let failure = UpdateFailure(message: "Could not save the current UI session. Keep Pharos open and retry before quitting or ejecting.")
        DispatchQueue.main.asyncAfter(deadline: .now() + 5) { finish(failure) }
        webView.callAsyncJavaScript("await window.pharosSession?.flush(); await window.pharosPrefs?.flush?.(); return true;", arguments: [:], in: nil, in: .page) { result in
            switch result {
            case .success: finish(nil)
            case .failure: finish(failure)
            }
        }
    }

    private func prepareAfterSessionSave(installing: Bool, timeout: TimeInterval, completion: @escaping (Error?) -> Void) {
        shuttingDown = true
        let child = process
        let endpoint = lock.withLock { self.endpoint }
        let releaseService = self.releaseService
        let app = Bundle.main.bundleURL
        let version = Bundle.main.object(forInfoDictionaryKey: "CFBundleVersion") as? String ?? ""
        let lease = installing ? AppUpdateLock.current() : nil
        updateLock = lease
        DispatchQueue.global(qos: .userInitiated).async { [weak self] in
            do {
                try lease?.begin(version: version)
                let stopService = {
                    if let child, child.isRunning {
                        if self?.managedApplication == true { child.terminate(); return }
                        if let endpoint {
                            switch releaseService(endpoint.url, endpoint.token) {
                            case .released: break
                            case .notRunning:
                                // The owned child may still be initializing,
                                // before its HTTP listener has opened.
                                if child.isRunning { child.terminate() }
                            case .stopping: throw UpdateFailure(message: "Pharos is finishing a write. Wait a moment, then quit or install again.")
                            case .failed(let reason): throw UpdateFailure(message: "Could not close Pharos's catalog: \(reason)")
                            }
                        } else { child.terminate() }
                    }
                }
                if installing {
                    try UpdateProcesses.prepare(app: app, servicePID: child?.processIdentifier, stopService: stopService)
                    let deadline = Date().addingTimeInterval(timeout)
                    while child?.isRunning == true {
                        if Date() >= deadline { throw UpdateFailure(message: "Pharos is still closing its catalog. Try quitting again in a moment.") }
                        Thread.sleep(forTimeInterval: 0.05)
                    }
                    try lease?.finish()
                } else {
                    // Ordinary Quit/log out never waits on a busy catalog.
                    // SIGTERM asks the owned service to close safely; Go may
                    // finish that work independently after the wrapper exits.
                    if child?.isRunning == true { child?.terminate() }
                    if self?.library != nil || self?.managedPortable == true { DriveProcesses.stopMCP(runningFrom: app) }
                }
                lease?.allowTermination(installing: installing)
                DispatchQueue.main.async { completion(nil) }
            } catch {
                lease?.cancel()
                DispatchQueue.main.async {
                    self?.resumeAfterCancelledTermination()
                    completion(error)
                }
            }
        }
    }

    /// Sparkle aborted after we closed the service and handed over the bundle.
    /// Release the handoff immediately so reopening and CLI reconnects need no
    /// stale-marker timeout. The stopped service is shown as a recoverable error.
    func cancelUpdatePreparation() {
        guard let updateLock else { return }
        updateLock.cancel()
        resumeAfterCancelledTermination()
    }

    private func resumeAfterCancelledTermination() {
        shuttingDown = false
        updateLock = nil
        if process?.isRunning != true {
            process = nil
            serviceLog = nil
            url = nil
            error = "The service has stopped. Reopen Pharos to resume, or try installing the update again."
        } else if url == nil, let endpoint = lock.withLock({ self.endpoint }) {
            // Readiness callbacks stop while shutdown is pending.
            // A cancelled quit must resume observing the live child.
            waitForService(endpoint.url, token: endpoint.token)
        }
    }

    func stop() {
        monitor?.stop()
        managedLibraryMonitors.values.forEach { $0.stop() }
        managedLibraryMonitors.removeAll()
        stopService()
        if library != nil || managedPortable {
            DriveProcesses.stopMCP(runningFrom: Bundle.main.bundleURL)
        }
    }

    deinit {
        if let terminationObserver { NotificationCenter.default.removeObserver(terminationObserver) }
        stop()
    }
}

// Remembers the mouse-down that a header drag or double-click message refers
// to: the message arrives from the web process after the event has passed.
final class ArchiveWKWebView: WKWebView {
    private(set) var lastMouseDown: NSEvent?
    var onMoveToWindow: ((NSWindow) -> Void)?

    override func viewDidMoveToWindow() {
        super.viewDidMoveToWindow()
        if let window { onMoveToWindow?(window) }
    }

    override func mouseDown(with event: NSEvent) {
        lastMouseDown = event
        super.mouseDown(with: event)
    }
}

struct ArchiveWebView: NSViewRepresentable {
    let url: URL
    let service: ArchiveService
    @ObservedObject var updates: UpdateNotice

    /// Native actions for the page. `pharosLibrary` takes {"action": "eject"};
    /// the page offers Eject only when this handler exists and otherwise says
    /// how to eject in Finder.
    static let libraryHandler = "pharosLibrary"
    static let updateHandler = "pharosUpdates"

    // WKWebView has no `-webkit-app-region`, so the page reports presses on
    // the app header's empty space and the window drags or zooms natively.
    // Only the app header (`body > header`) acts as the title bar; the page's
    // own <header>s, such as a workspace's heading and the drawers' and
    // panels' heads, are content, so pressing them never moves the window.
    // The header is trimmed to `WindowChrome.titlebarHeight` so the traffic
    // lights sit centered in it. Reserve their space from the window's left
    // edge at every width; the web header spans the full window. In full
    // screen macOS hides them, so the gap is removed. The page also reports its
    // background whenever the theme changes, so the area revealed by
    // rubber-band scrolling past either end matches it instead of showing
    // black.
    static let windowChromeScript = """
    (() => {
      const style = document.createElement('style');
      style.textContent = `
        html.pharos-window body>header .bar{padding:5px 22px;padding-left:84px}
        html.pharos-window.pharos-fullscreen body>header .bar{padding-left:22px}`;
      document.head.append(style);
      document.documentElement.classList.add('pharos-window');
      const interactive = 'a,button,input,textarea,select,label,summary,[role=button],[contenteditable],[tabindex]';
      const inside = (rect, x, y) => x >= rect.left && x <= rect.right && y >= rect.top && y <= rect.bottom;
      // A modal's backdrop covers the header, so presses there land on the
      // backdrop (or, for a <dialog>, on the dialog outside its box). Treat
      // those like presses on the header so the window stays draggable.
      const backdropOverHeader = event => {
        const header = document.querySelector('body > header');
        if (!header) return false;
        const bar = header.getBoundingClientRect(), x = event.clientX, y = event.clientY;
        if (!inside(bar, x, y)) return false;
        const box = event.target.getBoundingClientRect();
        if (event.target instanceof HTMLDialogElement) return !inside(box, x, y);
        return box.left <= bar.left && box.right >= bar.right && box.top <= bar.top && box.bottom >= bar.bottom;
      };
      document.addEventListener('mousedown', event => {
        if (event.button !== 0 || !(event.target instanceof Element)) return;
        const onHeader = event.target.closest('body > header') && !event.target.closest(interactive);
        if (!onHeader && !backdropOverHeader(event)) return;
        event.preventDefault();
        window.webkit.messageHandlers.\(windowMessageName).postMessage(event.detail === 2 ? 'doubleClick' : 'drag');
      });
      const reportBackground = () => window.webkit.messageHandlers.\(windowMessageName).postMessage({background: getComputedStyle(document.body).backgroundColor});
      new MutationObserver(reportBackground).observe(document.documentElement, {attributes: true, attributeFilter: ['data-theme']});
      matchMedia('(prefers-color-scheme: dark)').addEventListener('change', reportBackground);
      reportBackground();
    })();
    """
    static let windowMessageName = "pharosWindow"

    final class Coordinator: NSObject, WKScriptMessageHandlerWithReply, WKScriptMessageHandler, WKUIDelegate, WKNavigationDelegate, WKDownloadDelegate {
        let origin: URL
        weak var webView: ArchiveWKWebView?
        weak var service: ArchiveService?
        weak var updates: UpdateNotice?
        private var publishedUpdateState: HeaderUpdateState?
        private var observers: [NSObjectProtocol] = []

        init(origin: URL) { self.origin = origin }

        deinit { observers.forEach(NotificationCenter.default.removeObserver) }

        func observeFullScreen(of window: NSWindow) {
            guard observers.isEmpty else { return }
            let center = NotificationCenter.default
            // Drop the traffic-light gap once they hide, and restore it before
            // they reappear.
            let changes = [(NSWindow.didEnterFullScreenNotification, true), (NSWindow.willExitFullScreenNotification, false)]
            for (name, fullScreen) in changes {
                observers.append(center.addObserver(forName: name, object: window, queue: .main) { [weak self] _ in
                    self?.setFullScreenClass(fullScreen)
                })
            }
        }

        func setFullScreenClass(_ fullScreen: Bool) {
            webView?.evaluateJavaScript("document.documentElement.classList.toggle('pharos-fullscreen', \(fullScreen))")
        }

        /// Failed loads in a row; a page that finishes resets it.
        private var failedLoads = 0
        /// Bumped by every navigation that starts, so a scheduled retry never
        /// replaces a newer one (a page opened from the menu meanwhile).
        private var navigations = 0

        func webView(_ webView: WKWebView, didStartProvisionalNavigation navigation: WKNavigation!) {
            navigations += 1
        }
        static let loadRetries = 4

        func webView(_ webView: WKWebView, didFailProvisionalNavigation navigation: WKNavigation!, withError error: Error) {
            loadFailed(webView, error: error)
        }

        func webView(_ webView: WKWebView, didFail navigation: WKNavigation!, withError error: Error) {
            if Self.ignorable(error) {
                service?.pageLoading = false
                return
            }
            if repeatedFailure(webView, reason: (error as NSError).localizedDescription) { return }
            loadFailed(webView, error: error)
        }

        func webViewWebContentProcessDidTerminate(_ webView: WKWebView) {
            if repeatedFailure(webView, reason: "The page stopped unexpectedly.") { return }
            loadFailed(webView, error: nil)
        }

        /// Errors that are not failures: another navigation replaced this one
        /// (cancelled), WebKitErrorFrameLoadInterruptedByPolicyChange (102: a
        /// download, or a link the policy opened elsewhere), and
        /// WebKitErrorPlugInWillHandleLoad (204).
        static func ignorable(_ error: Error) -> Bool {
            let nsError = error as NSError
            if nsError.domain == NSURLErrorDomain && nsError.code == NSURLErrorCancelled { return true }
            return nsError.domain == "WebKitErrorDomain" && (nsError.code == 102 || nsError.code == 204)
        }

        /// Failures of a page that had loaded (its web process ended, or it
        /// failed after it committed), over the last minute. A page that
        /// loads and then fails again each time would otherwise reload
        /// forever, since a load that finishes resets failedLoads; only time
        /// resets this.
        private var lateFailures: [Date] = []

        private func repeatedFailure(_ webView: WKWebView, reason: String) -> Bool {
            let now = Date()
            lateFailures = lateFailures.filter { now.timeIntervalSince($0) < 60 } + [now]
            guard lateFailures.count >= 3, let service else { return false }
            lateFailures = []
            failedLoads += 1 // drop any retry still scheduled
            service.pageFailed("\(reason) It failed three times within a minute.", serviceAnswers: true, target: webView.url ?? origin)
            return true
        }

        /// A page that did not load (the service was busy, e.g. folding a
        /// large catalog log at startup) or whose web process died would
        /// otherwise leave the window blank. While the service answers, load
        /// it again after 0.5, 1, 2 and 4 s, then show the error with Reload.
        /// While it does not answer, the window waits for it and reloads by
        /// itself (ArchiveService.pageFailed).
        private func loadFailed(_ webView: WKWebView, error: Error?) {
            let nsError = error as NSError?
            if let error, Self.ignorable(error) {
                service?.pageLoading = false
                return
            }
            // Retry only the service's own pages; a blob:, data: or about:
            // navigation that failed falls back to the page shown.
            let servicePage = { (url: URL?) -> URL? in
                guard let url, url.scheme == "http" || url.scheme == "https",
                      url.scheme == self.origin.scheme, url.host == self.origin.host, url.port == self.origin.port else { return nil }
                return url
            }
            let target = servicePage(nsError?.userInfo[NSURLErrorFailingURLErrorKey] as? URL) ?? servicePage(webView.url) ?? origin
            failedLoads += 1
            let attempt = failedLoads
            let navigation = navigations
            let reason = nsError?.localizedDescription ?? "The page stopped unexpectedly."
            guard let service else { return }
            service.checkReady { [weak self, weak webView] ready in
                guard let self, let webView, self.failedLoads == attempt, self.navigations == navigation else { return }
                if ready && attempt <= Self.loadRetries {
                    let delay = 0.5 * pow(2, Double(attempt - 1))
                    DispatchQueue.main.asyncAfter(deadline: .now() + delay) { [weak self, weak webView] in
                        guard let self, let webView, self.failedLoads == attempt, self.navigations == navigation else { return }
                        webView.load(URLRequest(url: target))
                    }
                } else {
                    service.pageFailed(reason, serviceAnswers: ready, target: target)
                }
            }
        }

        func webView(_ webView: WKWebView, didFinish navigation: WKNavigation!) {
            failedLoads = 0
            service?.pageLoading = false
            setFullScreenClass(webView.window?.styleMask.contains(.fullScreen) == true)
            service?.menuBar.navigationFinished()
            publishedUpdateState = nil
            publishUpdateState()
        }

        func publishUpdateState() {
            guard let webView, let updates, let page = webView.url,
                  page.scheme == origin.scheme, page.host == origin.host, page.port == origin.port,
                  publishedUpdateState != updates.headerState,
                  let data = try? JSONSerialization.data(withJSONObject: updates.headerState.dictionary),
                  let json = String(data: data, encoding: .utf8) else { return }
            publishedUpdateState = updates.headerState
            webView.evaluateJavaScript("window.dispatchEvent(new CustomEvent('pharos:app-update', {detail: \(json)}))")
        }

        func userContentController(_ userContentController: WKUserContentController, didReceive message: WKScriptMessage) {
            guard let webView else { return }
            if message.name == "pharosMenuBar" {
                guard message.frameInfo.isMainFrame, let page = message.frameInfo.request.url,
                      page.scheme == origin.scheme, page.host == origin.host, page.port == origin.port,
                      let settings = message.body as? [String: Any] else { return }
                if settings["refreshUsage"] as? Bool == true { service?.menuBar.invalidateUsage(); return }
                service?.menuBar.applyPreferences(settings)
                return
            }
            if let background = (message.body as? [String: Any])?["background"] as? String {
                if let color = Self.color(css: background) { webView.underPageBackgroundColor = color }
                return
            }
            guard let window = webView.window, let action = message.body as? String else { return }
            switch action {
            case "drag":
                if let event = webView.lastMouseDown { window.performDrag(with: event) }
            case "doubleClick":
                switch UserDefaults.standard.string(forKey: "AppleActionOnDoubleClick") {
                case "Minimize": window.performMiniaturize(nil)
                case "None": break
                default: window.performZoom(nil)
                }
            default: break
            }
        }

        // Parses a computed `rgb(r, g, b)` or `rgba(r, g, b, a)`; a
        // transparent background leaves the current color in place.
        static func color(css: String) -> NSColor? {
            guard css.hasPrefix("rgb") else { return nil }
            let parts = css.split { !"0123456789.".contains($0) }.compactMap { Double($0) }
            guard parts.count == 3 || (parts.count == 4 && parts[3] > 0) else { return nil }
            return NSColor(srgbRed: parts[0] / 255, green: parts[1] / 255, blue: parts[2] / 255, alpha: 1)
        }

        private func isArchivePage(_ url: URL) -> Bool {
            if url.scheme == "about" || url.scheme == "blob" || url.scheme == "data" { return true }
            return url.scheme == origin.scheme && url.host == origin.host && url.port == origin.port
        }

        func userContentController(
            _ userContentController: WKUserContentController,
            didReceive message: WKScriptMessage,
            replyHandler: @escaping (Any?, String?) -> Void
        ) {
            if message.name == ArchiveWebView.updateHandler {
                guard message.frameInfo.isMainFrame, let page = message.frameInfo.request.url,
                      page.scheme == origin.scheme, page.host == origin.host, page.port == origin.port,
                      let updates else {
                    replyHandler(nil, "Updates are available only from the Pharos app."); return
                }
                switch (message.body as? [String: Any])?["action"] as? String {
                case "status": replyHandler(updates.headerState.dictionary, nil)
                case "install":
                    guard updates.installFromHeader() else {
                        replyHandler(nil, "This update is no longer ready. Use Check for Updates in the Pharos menu."); return
                    }
                    replyHandler(updates.headerState.dictionary, nil)
                default: replyHandler(nil, "Unknown update action.")
                }
                return
            }
            if message.name == ArchiveWebView.libraryHandler {
                guard message.frameInfo.isMainFrame, let page = message.frameInfo.request.url,
                      page.scheme == origin.scheme, page.host == origin.host, page.port == origin.port else {
                    replyHandler(nil, "Library actions must come from the Pharos window.")
                    return
                }
                library(message.body, replyHandler: replyHandler)
                return
            }
            guard let text = message.body as? String else {
                replyHandler(nil, "Clipboard content must be text.")
                return
            }
            let pasteboard = NSPasteboard.general
            pasteboard.clearContents()
            if pasteboard.setString(text, forType: .string) {
                replyHandler(true, nil)
            } else {
                replyHandler(nil, "Could not write to the system clipboard.")
            }
        }

        private func library(_ body: Any, replyHandler: @escaping (Any?, String?) -> Void) {
            if let object = body as? [String: Any], object["action"] as? String == "chooseFolder" {
                let picker = NSOpenPanel()
                picker.canChooseDirectories = true
                picker.canChooseFiles = false
                picker.allowsMultipleSelection = false
                picker.canCreateDirectories = true
                picker.message = "Choose a Pharos library or a parent folder for a new library."
                picker.begin { response in replyHandler(response == .OK ? picker.url?.path : nil, nil) }
                return
            }
            guard (body as? [String: Any])?["action"] as? String == "eject" else {
                replyHandler(nil, "Unknown library action.")
                return
            }
            guard let service else {
                replyHandler(nil, "Pharos is closing.")
                return
            }
            // WebKit requires exactly one reply, even once the page is gone.
            service.ejectLibrary { reason in
                if let reason { replyHandler(nil, reason) } else { replyHandler(["released": true], nil) }
            }
        }

        func webView(
            _ webView: WKWebView,
            createWebViewWith configuration: WKWebViewConfiguration,
            for navigationAction: WKNavigationAction,
            windowFeatures: WKWindowFeatures
        ) -> WKWebView? {
            if let url = navigationAction.request.url { NSWorkspace.shared.open(url) }
            return nil
        }

        // Anything that would leave the archive (PR links, markdown links,
        // server redirects) opens in the system browser instead of in-app.
        func webView(
            _ webView: WKWebView,
            decidePolicyFor navigationAction: WKNavigationAction,
            decisionHandler: @escaping (WKNavigationActionPolicy) -> Void
        ) {
            guard let url = navigationAction.request.url, !isArchivePage(url) else {
                // A link with a `download` attribute, such as a conversation's Share.
                decisionHandler(navigationAction.shouldPerformDownload ? .download : .allow)
                return
            }
            if navigationAction.targetFrame?.isMainFrame ?? true {
                NSWorkspace.shared.open(url)
            }
            decisionHandler(.cancel)
        }

        // The archive marks files it wants saved, not shown, as attachments.
        func webView(
            _ webView: WKWebView,
            decidePolicyFor navigationResponse: WKNavigationResponse,
            decisionHandler: @escaping (WKNavigationResponsePolicy) -> Void
        ) {
            let disposition = (navigationResponse.response as? HTTPURLResponse)?
                .value(forHTTPHeaderField: "Content-Disposition")
            decisionHandler(disposition?.lowercased().hasPrefix("attachment") == true ? .download : .allow)
        }

        func webView(_ webView: WKWebView, navigationAction: WKNavigationAction, didBecome download: WKDownload) {
            download.delegate = self
        }

        func webView(_ webView: WKWebView, navigationResponse: WKNavigationResponse, didBecome download: WKDownload) {
            download.delegate = self
        }

        // Downloads ask where to save, then show the file in Finder.
        private var downloadDestinations: [ObjectIdentifier: URL] = [:]

        func download(
            _ download: WKDownload,
            decideDestinationUsing response: URLResponse,
            suggestedFilename: String,
            completionHandler: @escaping (URL?) -> Void
        ) {
            let panel = NSSavePanel()
            panel.nameFieldStringValue = suggestedFilename
            panel.canCreateDirectories = true
            panel.directoryURL = FileManager.default.urls(for: .downloadsDirectory, in: .userDomainMask).first
            let key = ObjectIdentifier(download)
            let finish: (NSApplication.ModalResponse) -> Void = { [weak self] result in
                guard result == .OK, let destination = panel.url else {
                    completionHandler(nil)
                    return
                }
                // The panel has confirmed any replacement, but WKDownload fails
                // rather than write over a file.
                try? FileManager.default.removeItem(at: destination)
                self?.downloadDestinations[key] = destination
                completionHandler(destination)
            }
            if let window = webView?.window {
                panel.beginSheetModal(for: window, completionHandler: finish)
            } else {
                finish(panel.runModal())
            }
        }

        func downloadDidFinish(_ download: WKDownload) {
            if let destination = downloadDestinations.removeValue(forKey: ObjectIdentifier(download)) {
                NSWorkspace.shared.activateFileViewerSelecting([destination])
            }
        }

        func download(_ download: WKDownload, didFailWithError error: Error, resumeData: Data?) {
            downloadDestinations.removeValue(forKey: ObjectIdentifier(download))
            // Declining the save panel cancels the download; that is not a failure.
            if (error as NSError).code == NSURLErrorCancelled { return }
            let alert = NSAlert()
            alert.messageText = "Could not save the file"
            alert.informativeText = error.localizedDescription
            alert.runModal()
        }
    }

    func makeCoordinator() -> Coordinator { Coordinator(origin: url) }

    func makeNSView(context: Context) -> ArchiveWKWebView {
        context.coordinator.service = service
        context.coordinator.updates = updates
        let configuration = WKWebViewConfiguration()
        if service.managedApplication { configuration.websiteDataStore = .nonPersistent() }
        configuration.userContentController.addScriptMessageHandler(
            context.coordinator,
            contentWorld: .page,
            name: "pharosClipboard"
        )
        configuration.userContentController.addScriptMessageHandler(
            context.coordinator,
            contentWorld: .page,
            name: Self.libraryHandler
        )
        configuration.userContentController.addScriptMessageHandler(
            context.coordinator,
            contentWorld: .page,
            name: Self.updateHandler
        )
        configuration.userContentController.add(context.coordinator, name: Self.windowMessageName)
        configuration.userContentController.add(context.coordinator, name: "pharosMenuBar")
        configuration.userContentController.addUserScript(
            WKUserScript(source: Self.windowChromeScript, injectionTime: .atDocumentEnd, forMainFrameOnly: true)
        )
        let view = ArchiveWKWebView(frame: .zero, configuration: configuration)
        context.coordinator.webView = view
        service.menuBar.webView = view
        view.onMoveToWindow = { [weak coordinator = context.coordinator] in coordinator?.observeFullScreen(of: $0) }
        view.uiDelegate = context.coordinator
        view.navigationDelegate = context.coordinator
        // Show the window's harbor green, not white, until the page paints.
        if view.responds(to: NSSelectorFromString("setDrawsBackground:")) {
            view.setValue(false, forKey: "drawsBackground")
        }
        view.load(URLRequest(url: service.takePageTarget() ?? url))
        return view
    }
    func updateNSView(_ view: ArchiveWKWebView, context: Context) {
        context.coordinator.publishUpdateState()
    }

    static func dismantleNSView(_ view: ArchiveWKWebView, coordinator: Coordinator) {
        view.configuration.userContentController.removeScriptMessageHandler(forName: updateHandler, contentWorld: .page)
        view.configuration.userContentController.removeScriptMessageHandler(forName: windowMessageName)
        view.configuration.userContentController.removeScriptMessageHandler(forName: "pharosMenuBar")
        view.configuration.userContentController.removeScriptMessageHandler(
            forName: "pharosClipboard",
            contentWorld: .page
        )
        view.configuration.userContentController.removeScriptMessageHandler(
            forName: libraryHandler,
            contentWorld: .page
        )
    }
}

// The header's lighthouse mark with its beam turning, drawn in the mark's SVG
// units. `turn` 0 points the beam right, .pi/2 at the viewer, .pi left and
// 3*.pi/2 away: the beam's length follows cos, its brightness and the lantern
// flash follow sin, matching `.brand.sweeping` in the web UI.
struct PharosBeacon: View {
    static let period = 4.0
    static let scale: CGFloat = 2.2
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    var body: some View {
        TimelineView(.animation(paused: reduceMotion)) { timeline in
            Canvas { context, size in
                let seconds = timeline.date.timeIntervalSinceReferenceDate
                let turn = reduceMotion ? 0 : seconds.truncatingRemainder(dividingBy: Self.period) / Self.period * 2 * .pi
                context.translateBy(x: size.width / 2 - 18 * Self.scale, y: (size.height - 43.5 * Self.scale) / 2 - 2 * Self.scale)
                context.scaleBy(x: Self.scale, y: Self.scale)
                Self.draw(in: context, turn: turn)
            }
        }
        .frame(width: 460, height: 120)
        .accessibilityLabel("Pharos lighthouse")
    }

    private static func draw(in context: GraphicsContext, turn: Double) {
        let lamp = Color(red: 0xff / 255, green: 0xe7 / 255, blue: 0xa6 / 255)
        let facing = (1 + sin(turn)) / 2
        let lantern = CGPoint(x: 18, y: 11.9)
        let reach = 95.0, spread = 13.8, lanternHalf = 1.65, lanternSide = 2.7
        let length = reach * cos(turn), side = lanternSide * cos(turn)

        var beam = Path()
        beam.move(to: CGPoint(x: lantern.x + side, y: lantern.y - lanternHalf))
        beam.addLine(to: CGPoint(x: lantern.x + length, y: lantern.y - spread))
        beam.addLine(to: CGPoint(x: lantern.x + length, y: lantern.y + spread))
        beam.addLine(to: CGPoint(x: lantern.x + side, y: lantern.y + lanternHalf))
        beam.closeSubpath()
        let fade = Gradient(stops: [
            .init(color: lamp.opacity(0.78), location: 0),
            .init(color: lamp.opacity(0.34), location: 0.12),
            .init(color: lamp.opacity(0.12), location: 0.6),
            .init(color: lamp.opacity(0), location: 1),
        ])
        let end = CGPoint(x: lantern.x + (abs(length) < 1 ? (length < 0 ? -1 : 1) : length), y: lantern.y)
        var beamContext = context
        beamContext.opacity = 0.3 + 0.7 * facing
        beamContext.fill(beam, with: .linearGradient(fade, startPoint: lantern, endPoint: end))

        let tower = Color(red: 0xea / 255, green: 0xdc / 255, blue: 0xbc / 255)
        let brass = Color(red: 0xd4 / 255, green: 0xa8 / 255, blue: 0x57 / 255)
        let door = Color(red: 0x1b / 255, green: 0x3a / 255, blue: 0x2c / 255)
        var body = Path()
        body.addLines([CGPoint(x: 10.4, y: 45), CGPoint(x: 25.6, y: 45), CGPoint(x: 24.1, y: 29.2), CGPoint(x: 11.9, y: 29.2)])
        body.closeSubpath()
        body.addLines([CGPoint(x: 13, y: 27.6), CGPoint(x: 23, y: 27.6), CGPoint(x: 22.2, y: 20.4), CGPoint(x: 13.8, y: 20.4)])
        body.closeSubpath()
        body.addRect(CGRect(x: 15.4, y: 15.2, width: 5.2, height: 4))
        body.move(to: CGPoint(x: 14.6, y: 9.7))
        body.addQuadCurve(to: CGPoint(x: 18, y: 5.3), control: CGPoint(x: 14.9, y: 5.9))
        body.addQuadCurve(to: CGPoint(x: 21.4, y: 9.7), control: CGPoint(x: 21.1, y: 5.9))
        body.closeSubpath()
        body.addEllipse(in: CGRect(x: 16.8, y: 2.7, width: 2.4, height: 2.4))
        context.fill(body, with: .color(tower))
        var bands = Path()
        bands.addRoundedRect(in: CGRect(x: 10.8, y: 27.4, width: 14.4, height: 2.4), cornerSize: CGSize(width: 0.8, height: 0.8))
        bands.addRoundedRect(in: CGRect(x: 13.2, y: 18.9, width: 9.6, height: 2.1), cornerSize: CGSize(width: 0.7, height: 0.7))
        bands.addRoundedRect(in: CGRect(x: 15.2, y: 9.2, width: 5.6, height: 5.4), cornerSize: CGSize(width: 0.9, height: 0.9))
        context.fill(bands, with: .color(brass))
        var doorway = Path()
        doorway.move(to: CGPoint(x: 15.9, y: 45))
        doorway.addLine(to: CGPoint(x: 15.9, y: 41.5))
        doorway.addArc(center: CGPoint(x: 18, y: 41.5), radius: 2.1, startAngle: .degrees(180), endAngle: .degrees(360), clockwise: false)
        doorway.addLine(to: CGPoint(x: 20.1, y: 45))
        doorway.closeSubpath()
        context.fill(doorway, with: .color(door))

        var lampContext = context
        lampContext.opacity = pow(facing, 3)
        lampContext.fill(Path(roundedRect: CGRect(x: 16.1, y: 10.1, width: 3.8, height: 3.6), cornerRadius: 0.4), with: .color(Color(red: 1, green: 0xf6 / 255, blue: 0xd8 / 255)))
        var haloContext = context
        haloContext.opacity = pow(facing, 6)
        let halo = Gradient(stops: [
            .init(color: Color(red: 1, green: 0xf6 / 255, blue: 0xd8 / 255), location: 0),
            .init(color: Color(red: 1, green: 0xd9 / 255, blue: 0x78 / 255).opacity(0.7), location: 0.3),
            .init(color: Color(red: 1, green: 0xd9 / 255, blue: 0x78 / 255).opacity(0), location: 1),
        ])
        haloContext.fill(Path(ellipseIn: CGRect(x: lantern.x - 9, y: lantern.y - 9, width: 18, height: 18)), with: .radialGradient(halo, center: lantern, startRadius: 0, endRadius: 9))
    }
}

struct ArchiveErrorView: View {
    let message: String
    var title = "Pharos couldn’t start"
    var retry: (() -> Void)? = nil
    @State private var copied = false

    var body: some View {
        VStack(spacing: 18) {
            Image(nsImage: NSApp.applicationIconImage)
                .resizable()
                .frame(width: 88, height: 88)
            Text(title)
                .font(.title2.weight(.semibold))
            ScrollView {
                Text(message)
                    .font(.system(.body, design: .monospaced))
                    .textSelection(.enabled)
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
            .frame(maxWidth: 700, maxHeight: 240)
            .padding(12)
            .background(.black.opacity(0.22))
            .clipShape(RoundedRectangle(cornerRadius: 8))
            HStack(spacing: 12) {
                if let retry {
                    Button(action: retry) {
                        Label("Reload", systemImage: "arrow.clockwise")
                            .foregroundStyle(Harbor.sea)
                    }
                    .buttonStyle(.borderedProminent)
                    .tint(Harbor.brass)
                }
                Button {
                    let pasteboard = NSPasteboard.general
                    pasteboard.clearContents()
                    copied = pasteboard.setString(message, forType: .string)
                } label: {
                    Label(copied ? "Copied" : "Copy Error", systemImage: copied ? "checkmark" : "doc.on.doc")
                        .foregroundStyle(Harbor.sea)
                }
                .buttonStyle(.borderedProminent)
                .tint(Harbor.brass)
            }
        }
        .harborBackdrop()
        .onChange(of: message) { _, _ in copied = false }
    }
}

struct LibraryDisconnectedView: View {
    let disconnection: ArchiveService.Disconnection
    let ejecting: Bool
    let reopen: () -> Void
    let eject: () -> Void

    var body: some View {
        VStack(spacing: 18) {
            Image(nsImage: NSApp.applicationIconImage)
                .resizable()
                .frame(width: 88, height: 88)
            switch disconnection.state {
            case .finishing:
                Text("Finishing a write")
                    .font(.title2.weight(.semibold))
                Text("Pharos is finishing a write before it lets go of its library. Try ejecting \(disconnection.volume) again in a moment.")
                    .opacity(0.8)
            case .released:
                Text("Library released")
                    .font(.title2.weight(.semibold))
                Text("Pharos closed its library so \(disconnection.volume) can be ejected safely.")
                    .opacity(0.8)
            case .unplugged:
                Text("Library disconnected")
                    .font(.title2.weight(.semibold))
                Text("Reconnect \(disconnection.volume) to continue.")
                    .opacity(0.8)
            }
            if let note = disconnection.note {
                Text(note)
                    .multilineTextAlignment(.center)
                    .frame(maxWidth: 520)
                    .textSelection(.enabled)
            }
            if disconnection.state == .released {
                HStack(spacing: 12) {
                    Button(action: eject) {
                        Label(ejecting ? "Ejecting \(disconnection.volume)…" : "Eject \(disconnection.volume)", systemImage: "eject")
                            .foregroundStyle(Harbor.sea)
                    }
                    .buttonStyle(.borderedProminent)
                    .tint(Harbor.brass)
                    .disabled(ejecting)
                    Button(action: reopen) {
                        Label("Reopen Library", systemImage: "arrow.clockwise")
                    }
                    .buttonStyle(.bordered)
                    .disabled(ejecting)
                }
            }
        }
        .harborBackdrop()
    }
}

struct FirstRunView: View {
    @ObservedObject var service: ArchiveService

    var body: some View {
        VStack(alignment: .leading, spacing: 22) {
            Text("Set up Pharos").font(.largeTitle.weight(.bold))
            Text("Choose where your library data should live. Put Pharos.app in its final location before continuing.")
                .font(.title3)
            if !service.canSetUpHere {
                Text("Copy Pharos.app out of the disk image first. Put it in a dedicated folder on your SSD, or in Applications, then open that copy.")
                    .foregroundStyle(Harbor.brass)
            }
            HStack(alignment: .top, spacing: 18) {
                VStack(alignment: .leading, spacing: 12) {
                    Label("SSD or portable drive", systemImage: "externaldrive")
                        .font(.title3.weight(.semibold))
                    Text("Create a folder such as SSD/Pharos and put Pharos.app inside it. The catalog, captures, and preserved files will live beside the app.")
                    Text("Current folder: \(service.installationFolder.path)")
                        .font(.callout.monospaced())
                        .textSelection(.enabled)
                        .opacity(0.8)
                    Spacer(minLength: 0)
                    Button("Create Library Beside App") { service.setUpPortableLibrary() }
                        .disabled(!service.canCreatePortableLibrary || service.settingUp)
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                VStack(alignment: .leading, spacing: 12) {
                    Label("Applications", systemImage: "apps.iphone")
                        .font(.title3.weight(.semibold))
                    Text("Put only Pharos.app in Applications. The catalog, captures, and preserved files will live in your Mac's Application Support folder.")
                    Text("~/Library/Application Support/Pharos")
                        .font(.callout.monospaced())
                        .textSelection(.enabled)
                        .opacity(0.8)
                    Spacer(minLength: 0)
                    Button("Set Up on This Mac") { service.setUpOnThisMac() }
                        .disabled(!service.canSetUpHere || service.settingUp)
                }
                .frame(maxWidth: .infinity, alignment: .leading)
            }
            .frame(minHeight: 235)
            if service.settingUp { ProgressView("Creating your library…") }
            if let setupError = service.setupError {
                Text(setupError).foregroundStyle(.red).textSelection(.enabled)
            }
        }
        .harborBackdrop()
    }
}

struct MenuBarWindowBridge: NSViewRepresentable {
    @Environment(\.openWindow) private var openWindow
    let service: ArchiveService
    final class WindowView: NSView {
        weak var controller: MenuBarController?
        override func viewDidMoveToWindow() {
            super.viewDidMoveToWindow()
            if let window { controller?.window = window }
        }
    }
    func makeNSView(context: Context) -> WindowView {
        let open = openWindow
        service.menuBar.openWindow = { open(id: "pharos") }
        let view = WindowView(); view.controller = service.menuBar; return view
    }
    func updateNSView(_ view: WindowView, context: Context) {}
}

struct PharosWindow: View {
    @ObservedObject var service: ArchiveService
    @ObservedObject var updates: UpdateNotice
    var body: some View {
        Group {
            if let disconnection = service.disconnected {
                LibraryDisconnectedView(disconnection: disconnection, ejecting: service.ejecting,
                                        reopen: service.reopen, eject: { service.ejectLibrary() })
            }
            else if service.pageWaiting, service.url != nil {
                VStack(spacing: 16) {
                    PharosBeacon()
                    Text(service.hasConnectedLibrary ? "Waiting for the library to answer… It may be busy opening a large catalog." : "Waiting for Pharos to answer…")
                        .opacity(0.8)
                }
                .harborBackdrop()
            }
            else if let pageError = service.pageError, service.url != nil {
                ArchiveErrorView(message: pageError, title: "Pharos couldn’t load this page", retry: service.reloadPage)
            }
            else if let url = service.url {
                ArchiveWebView(url: url, service: service, updates: updates)
                    .ignoresSafeArea()
                    .overlay {
                        if service.pageLoading {
                            VStack(spacing: 16) {
                                PharosBeacon()
                                Text(service.hasConnectedLibrary ? "Opening your library…" : "Opening Pharos…")
                                    .opacity(0.8)
                            }
                            .frame(maxWidth: .infinity, maxHeight: .infinity)
                            .harborBackdrop()
                        }
                    }
            }
            else if service.setupNeeded { FirstRunView(service: service) }
            else if let error = service.error { ArchiveErrorView(message: error) }
            else {
                VStack(spacing: 16) {
                    PharosBeacon()
                    Text(service.status)
                        .opacity(0.8)
                }
                .harborBackdrop()
            }
        }
        .background(WindowChrome())
        .background(MenuBarWindowBridge(service: service))
        .task {
            if !service.setupNeeded {
                updates.startIfReady(true)
            }
        }
        .onChange(of: service.setupNeeded) { _, needed in
            updates.startIfReady(!needed)
        }
    }
}

/// View → Reload (⌘R): reloads the window's page, also after one that
/// could not load.
struct ViewCommands: Commands {
    let service: ArchiveService
    var body: some Commands {
        CommandGroup(before: .toolbar) {
            Button("Reload") { service.reloadPage() }
                .keyboardShortcut("r", modifiers: .command)
            Divider()
        }
    }
}

#if !PHAROS_NATIVE_TEST
@main struct PharosApp: App {
    @NSApplicationDelegateAdaptor(PharosDelegate.self) private var delegate
    var body: some Scene {
        WindowGroup("Pharos", id: "pharos") {
            PharosWindow(service: delegate.service, updates: delegate.updates)
        }
        .windowStyle(.hiddenTitleBar)
        .defaultSize(width: 1180, height: 780)
        .commands {
            UpdateCommands(updates: delegate.updates)
            ViewCommands(service: delegate.service)
        }
    }
}
#endif
