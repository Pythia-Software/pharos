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

    @Published var url: URL?
    @Published var error: String?
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

    init() {
        // Here rather than on a view: with its window closed, Pharos keeps
        // running, and a view's observer would be gone when it quits.
        terminationObserver = NotificationCenter.default.addObserver(
            forName: NSApplication.willTerminateNotification, object: nil, queue: .main
        ) { [weak self] _ in self?.stop() }
        switch LaunchPlan.resolve(bundle: Bundle.main.bundleURL) {
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

    var installationFolder: URL { Bundle.main.bundleURL.deletingLastPathComponent() }

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
        let answer = ServiceClient.release(endpoint.url, token: endpoint.token)
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
        guard let volume else {
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
                    self.url = nil
                    self.disconnected = Disconnection(volume: volume.name, state: .released,
                                                      note: "\(volume.name) could not be ejected: \(error.localizedDescription)")
                }
            }
        }
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
                guard let self else { return }
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
        guard process?.isRunning == true else { return }
        guard let request = healthRequest(serviceURL, token: token) else {
            error = "Could not construct the local archive health URL."
            process?.terminate()
            return
        }
        URLSession.shared.dataTask(with: request) { [weak self] _, response, _ in
            DispatchQueue.main.async {
                guard let self, self.process?.isRunning == true else { return }
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

    func stop() {
        monitor?.stop()
        stopService()
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

    /// Native actions for the page. `pharosLibrary` takes {"action": "eject"};
    /// the page offers Eject only when this handler exists and otherwise says
    /// how to eject in Finder.
    static let libraryHandler = "pharosLibrary"

    // WKWebView has no `-webkit-app-region`, so the page reports presses on
    // the app header's empty space and the window drags or zooms natively.
    // Only the app header (`body > header`) acts as the title bar; the page's
    // own <header>s, such as a workspace's heading and the drawers' and
    // panels' heads, are content, so pressing them never moves the window.
    // The header is trimmed to `WindowChrome.titlebarHeight` so the traffic
    // lights sit centered in it, and its left edge clears them except in
    // full screen, where macOS hides them. The page also reports its
    // background whenever the theme changes, so the area revealed by
    // rubber-band scrolling past either end matches it instead of showing
    // black.
    static let windowChromeScript = """
    (() => {
      const style = document.createElement('style');
      style.textContent = `
        html.pharos-window header .bar{padding:5px 22px;padding-left:max(22px,calc(84px - max(0px,(100vw - 1180px)/2)))}
        html.pharos-window.pharos-fullscreen header .bar{padding-left:22px}`;
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

        func webView(_ webView: WKWebView, didFinish navigation: WKNavigation!) {
            setFullScreenClass(webView.window?.styleMask.contains(.fullScreen) == true)
        }

        func userContentController(_ userContentController: WKUserContentController, didReceive message: WKScriptMessage) {
            guard let webView else { return }
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
            if message.name == ArchiveWebView.libraryHandler {
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
        let configuration = WKWebViewConfiguration()
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
        configuration.userContentController.add(context.coordinator, name: Self.windowMessageName)
        configuration.userContentController.addUserScript(
            WKUserScript(source: Self.windowChromeScript, injectionTime: .atDocumentEnd, forMainFrameOnly: true)
        )
        let view = ArchiveWKWebView(frame: .zero, configuration: configuration)
        context.coordinator.webView = view
        view.onMoveToWindow = { [weak coordinator = context.coordinator] in coordinator?.observeFullScreen(of: $0) }
        view.uiDelegate = context.coordinator
        view.navigationDelegate = context.coordinator
        // Show the window's harbor green, not white, until the page paints.
        if view.responds(to: NSSelectorFromString("setDrawsBackground:")) {
            view.setValue(false, forKey: "drawsBackground")
        }
        view.load(URLRequest(url: url))
        return view
    }
    func updateNSView(_ view: ArchiveWKWebView, context: Context) {}

    static func dismantleNSView(_ view: ArchiveWKWebView, coordinator: Coordinator) {
        view.configuration.userContentController.removeScriptMessageHandler(forName: windowMessageName)
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
    @State private var copied = false

    var body: some View {
        VStack(spacing: 18) {
            Image(nsImage: NSApp.applicationIconImage)
                .resizable()
                .frame(width: 88, height: 88)
            Text("Pharos couldn’t start")
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

@main struct PharosApp: App {
    @StateObject private var service = ArchiveService()
    @StateObject private var updates = UpdateNotice()
    @AppStorage("checkForUpdatesOnLaunch") private var checkForUpdatesOnLaunch = false
    var body: some Scene {
        WindowGroup("Pharos") {
            Group {
                if let disconnection = service.disconnected {
                    LibraryDisconnectedView(disconnection: disconnection, ejecting: service.ejecting,
                                            reopen: service.reopen, eject: { service.ejectLibrary() })
                }
                else if let url = service.url { ArchiveWebView(url: url, service: service).ignoresSafeArea() }
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
            .sheet(isPresented: Binding(get: { updates.status != nil }, set: { if !$0 { updates.status = nil } })) {
                UpdateNoticeSheet(notice: updates)
            }
            .task {
                if !service.setupNeeded {
                    updates.checkOnLaunchIfEnabled(checkForUpdatesOnLaunch)
                }
            }
        }
        .windowStyle(.hiddenTitleBar)
        .defaultSize(width: 1180, height: 780)
        .commands {
            CommandGroup(after: .appInfo) {
                Button("Check for Updates…") { updates.check() }
                    .disabled(updates.isChecking)
                Toggle("Check for Updates on Launch", isOn: $checkForUpdatesOnLaunch)
            }
        }
    }
}
