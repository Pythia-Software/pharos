import AppKit
import Combine
import WebKit

struct MenuBarPreferences {
    static let key = "pharos-menu-bar"
    static let defaults: [String: Bool] = [
        "enabled": true, "indexed": true, "words": true, "tokens": true, "carbon": true,
        "cost": false, "backup": false, "space": false, "index": false, "eject": false,
        "open": true, "status": true, "settings": true, "quit": true,
        "search": false, "analyze": false, "optimize": false, "capture": false, "health": false, "sources": false, "finder": false,
    ]
    static let defaultOrder = ["open", "status", "indexed", "index", "words", "tokens", "cost", "carbon", "backup", "space", "search", "analyze", "optimize", "capture", "health", "sources", "finder", "settings", "eject", "quit"]
    static let required = ["open", "status", "settings", "quit"]
    static func isSeparator(_ id: String) -> Bool {
        id.range(of: "^separator:[1-9][0-9]*$", options: .regularExpression) != nil
    }
    static func group(_ id: String) -> String {
        switch id {
        case "status", "indexed", "index": return "status"
        case "words", "tokens", "cost", "carbon": return "usage"
        case "backup", "space": return "library"
        case "search", "analyze", "optimize": return "navigation"
        case "capture", "health", "sources", "finder": return "manage"
        case "eject", "quit": return "quit"
        default: return id
        }
    }
    var values: [String: Bool]
    var order: [String]
    var saved: [String: Any] { values.reduce(into: ["order": order, "layoutVersion": 2] as [String: Any]) { $0[$1.key] = $1.value } }

    init(_ saved: [String: Any] = [:]) {
        values = Self.defaults
        order = []
        for key in values.keys {
            // JSON booleans only; a numeric or corrupt preference uses its default.
            if let value = saved[key] as? NSNumber, CFGetTypeID(value) == CFBooleanGetTypeID() {
                values[key] = value.boolValue
            }
        }
        let candidates = saved["order"] as? [Any] ?? Self.defaultOrder.filter { Self.required.contains($0) || values[$0] == true }
        for case let id as String in candidates where (Self.defaultOrder.contains(id) || Self.isSeparator(id)) && !order.contains(id) { order.append(id) }
        for id in Self.required where !order.contains(id) { order.append(id) }
        for id in Self.defaultOrder { values[id] = order.contains(id) }
        if (saved["layoutVersion"] as? NSNumber)?.doubleValue != 2 && !order.contains(where: Self.isSeparator) {
            var layout: [String] = []
            var previous: String?
            var separator = 0
            for id in order {
                let group = Self.group(id)
                if let previous, previous != group { separator += 1; layout.append("separator:\(separator)") }
                layout.append(id)
                previous = group
            }
            order = layout
        }
    }
    subscript(_ key: String) -> Bool { values[key] == true }
}

/// What the broker's /api/app/state says about the selected library and any
/// library operation, as the native menu shows it. Pure, for the harness.
struct ManagedLibraryStatus: Equatable {
    static let unanswered = "Finishing a library operation…"
    /// Whether the broker has answered at least once.
    var known = false
    var selected = ""
    /// The selected library's service is up.
    var connected = false
    /// A library is selected and available. One whose service is not up
    /// (starting, respawning, stopped for a delivery) is still a library.
    var selectedAvailable = false
    /// A library operation the broker reports (recover, transfer, delivery),
    /// or one it is too busy to report: creating a library holds the lock
    /// /api/app/state waits for.
    var operation: String?

    var noLibrary: Bool { known && !selectedAvailable && operation == nil }

    /// A successful answer; nil if it is not a state document.
    static func answered(_ state: [String: Any]) -> ManagedLibraryStatus? {
        guard let libraries = state["libraries"] as? [[String: Any]] else { return nil }
        let selected = state["selected"] as? String ?? ""
        let entry = selected.isEmpty ? nil : libraries.first { $0["id"] as? String == selected }
        let job = state["job"] as? [String: Any]
        let running = job?["state"] as? String == "running"
        let operating = state["busy"] as? Bool == true || running
        return ManagedLibraryStatus(known: true, selected: selected, connected: entry?["connected"] as? Bool == true,
            selectedAvailable: entry?["available"] as? Bool == true,
            operation: operating ? ((running ? job?["phase"] as? String : nil) ?? unanswered) : nil)
    }

    /// A request that failed. Only a timeout suggests the broker is holding
    /// its lock for a library operation; a connection or parse error keeps
    /// the last answer.
    func failedRequest(timedOut: Bool) -> ManagedLibraryStatus { timedOut ? unansweredRequest() : self }

    /// A request the broker did not answer in time: assume it is busy until a
    /// successful answer says nothing is running.
    func unansweredRequest() -> ManagedLibraryStatus {
        var status = self
        status.operation = status.operation ?? Self.unanswered
        return status
    }

    /// What to show while a request has been waiting pendingFor seconds.
    func current(pendingFor: TimeInterval?) -> ManagedLibraryStatus {
        if let pendingFor, pendingFor > 6 { return unansweredRequest() }
        return self
    }
}

// The template lighthouse and colored dot are separate views, so macOS can
// tint the mark for its menu bar appearance without tinting the status dot.
final class MenuBarDot: NSView {
    var color = NSColor.systemYellow { didSet { needsDisplay = true } }
    override func draw(_ dirtyRect: NSRect) {
        color.setFill()
        NSBezierPath(ovalIn: bounds.insetBy(dx: 1.5, dy: 1.5)).fill()
    }
}

enum MenuBarLighthouse {
    static func image() -> NSImage {
        let image = NSImage(size: NSSize(width: 18, height: 18), flipped: false) { _ in
            NSColor.black.setFill()

            // AppIcon.svg's stepped Alexandrian tower, simplified for 18 pt.
            let tower = NSBezierPath()
            tower.move(to: NSPoint(x: 5.5, y: 1))
            tower.line(to: NSPoint(x: 12.5, y: 1))
            tower.line(to: NSPoint(x: 12, y: 7.5))
            tower.line(to: NSPoint(x: 6, y: 7.5))
            tower.close()
            tower.fill()
            NSBezierPath(roundedRect: NSRect(x: 4.5, y: 0.5, width: 9, height: 1), xRadius: 0.3, yRadius: 0.3).fill()
            NSBezierPath(roundedRect: NSRect(x: 5.5, y: 7.5, width: 7, height: 1), xRadius: 0.3, yRadius: 0.3).fill()

            let middle = NSBezierPath()
            middle.move(to: NSPoint(x: 6.5, y: 8.5))
            middle.line(to: NSPoint(x: 11.5, y: 8.5))
            middle.line(to: NSPoint(x: 11, y: 11.5))
            middle.line(to: NSPoint(x: 7, y: 11.5))
            middle.close()
            middle.fill()
            NSBezierPath(roundedRect: NSRect(x: 6.5, y: 11, width: 5, height: 1), xRadius: 0.3, yRadius: 0.3).fill()
            NSBezierPath(rect: NSRect(x: 7.5, y: 12, width: 3, height: 1.5)).fill()
            NSBezierPath(roundedRect: NSRect(x: 7, y: 13, width: 4, height: 1), xRadius: 0.3, yRadius: 0.3).fill()
            NSBezierPath(rect: NSRect(x: 7.5, y: 14, width: 3, height: 1.5)).fill()

            // Rounded dome and finial echo the app icon's lantern crown.
            let dome = NSBezierPath()
            dome.move(to: NSPoint(x: 7, y: 15.5))
            dome.curve(to: NSPoint(x: 9, y: 17), controlPoint1: NSPoint(x: 7.1, y: 16.4), controlPoint2: NSPoint(x: 8, y: 17))
            dome.curve(to: NSPoint(x: 11, y: 15.5), controlPoint1: NSPoint(x: 10, y: 17), controlPoint2: NSPoint(x: 10.9, y: 16.4))
            dome.close()
            dome.fill()
            NSBezierPath(roundedRect: NSRect(x: 8.5, y: 16.5, width: 1, height: 1.5), xRadius: 0.5, yRadius: 0.5).fill()

            // Leave air around the lantern so the beams remain distinct.
            let beams = NSBezierPath()
            beams.move(to: NSPoint(x: 5.5, y: 14.5))
            beams.line(to: NSPoint(x: 1, y: 15.5))
            beams.line(to: NSPoint(x: 1, y: 13))
            beams.line(to: NSPoint(x: 5.5, y: 13.5))
            beams.close()
            beams.move(to: NSPoint(x: 12.5, y: 14.5))
            beams.line(to: NSPoint(x: 17, y: 16))
            beams.line(to: NSPoint(x: 17, y: 12.5))
            beams.line(to: NSPoint(x: 12.5, y: 13.5))
            beams.close()
            beams.fill()

            // Transparent openings preserve native template tinting.
            NSGraphicsContext.saveGraphicsState()
            NSGraphicsContext.current?.compositingOperation = .clear
            NSBezierPath(rect: NSRect(x: 8.5, y: 14, width: 1, height: 1.5)).fill()
            NSBezierPath(roundedRect: NSRect(x: 8.5, y: 9, width: 1, height: 1.5), xRadius: 0.5, yRadius: 0.5).fill()
            let doorway = NSBezierPath()
            doorway.move(to: NSPoint(x: 8.25, y: 0))
            doorway.line(to: NSPoint(x: 9.75, y: 0))
            doorway.appendArc(withCenter: NSPoint(x: 9, y: 2.25), radius: 0.75, startAngle: 0, endAngle: 180, clockwise: false)
            doorway.close()
            doorway.fill()
            NSGraphicsContext.restoreGraphicsState()
            return true
        }
        image.isTemplate = true
        image.accessibilityDescription = "Pharos lighthouse"
        return image
    }
}

final class MenuBarController: NSObject, NSMenuDelegate {
    private weak var service: ArchiveService?
    weak var webView: ArchiveWKWebView?
    weak var window: NSWindow?
    var openWindow: (() -> Void)?
    var revealInFinder: (URL) -> Void = { NSWorkspace.shared.activateFileViewerSelecting([$0]) }
    private var pendingPath: String?
    private var item: NSStatusItem?
    private let dot = MenuBarDot(frame: NSRect(x: 25, y: 7, width: 8, height: 8))
    private let menu = NSMenu()
    private var rows: [String: NSMenuItem] = [:]
    private var arrangedOrder: [String] = []
    private var metricText: [String: (label: String, value: String)] = [:]
    private var rowTitles: [String: String] = [:]
    private lazy var titleInsets = Self.menuTitleInsets(font: menu.font ?? NSFont.menuFont(ofSize: 13))
    private var preferences: MenuBarPreferences
    private var snapshot: [String: Any] = [:]
    private var usage: [String: Any]?
    private var statusError: String?
    private var usageError: String?
    private var actionErrorVersion = 0
    private var actionError: String? { didSet { actionErrorVersion += 1 } }
    private var statusTask: URLSessionDataTask?
    private var usageTask: URLSessionDataTask?
    private var usageUpdated = Date.distantPast
    private var connectedURL: URL?
    private var libraryConnection: String?
    private var generation = 0
    private var timer: Timer?
    private var observation: AnyCancellable?
    private var indexing = false
    private var preferencesChangedAt = Date.distantPast
    private let cacheKey = "menuBarPreferences"
    private let defaults: UserDefaults

    init(service: ArchiveService, defaults: UserDefaults = .standard) {
        self.service = service
        self.defaults = defaults
        preferences = MenuBarPreferences(defaults.dictionary(forKey: cacheKey) ?? [:])
        super.init()
        menu.delegate = self
        menu.autoenablesItems = false
        menu.font = NSFont.menuFont(ofSize: 13)
        makeMenu()
        update()
        observation = service.objectWillChange.sink { [weak self] _ in
            DispatchQueue.main.async { self?.refresh() }
        }
        let timer = Timer(timeInterval: 5, repeats: true) { [weak self] _ in self?.refresh() }
        RunLoop.main.add(timer, forMode: .common)
        self.timer = timer
        refresh()
    }

    func applyPreferences(_ saved: [String: Any], fromServer: Bool = false) {
        // A poll already in flight may precede the page's debounced save.
        if fromServer && Date().timeIntervalSince(preferencesChangedAt) < 2 { return }
        if !fromServer { preferencesChangedAt = Date() }
        preferences = MenuBarPreferences(saved)
        defaults.set(preferences.saved, forKey: cacheKey)
        arrangeMenu()
        update()
        if !fromServer { refresh() }
    }

    private func makeMenu() {
        func row(_ key: String, _ title: String, path: String? = nil) {
            let row = NSMenuItem(title: title, action: path == nil ? nil : #selector(navigate(_:)), keyEquivalent: "")
            row.target = self
            row.representedObject = path
            row.isEnabled = path != nil
            rows[key] = row
        }
        row("open", "Open Pharos", path: "")
        row("status", "Pharos · Starting")
        row("detail", "Starting local archive…")
        row("indexed", "Not indexed yet")
        let index = NSMenuItem(title: "Index changes now", action: #selector(indexNow), keyEquivalent: "")
        index.target = self; rows["index"] = index
        row("error", "View error…", path: "/settings/sync")
        row("words", "Words typed", path: "/analyze?analyze_topic=human")
        row("tokens", "Tokens used", path: "/analyze?analyze_topic=tokens")
        row("cost", "Estimated API cost", path: "/analyze?analyze_topic=tokens")
        row("carbon", "Estimated CO₂e", path: "/analyze?analyze_topic=carbon")
        row("usage-note", "Loading usage…")
        row("backup", "Last backup", path: "/settings/backups")
        row("space", "Library size", path: "/settings")
        row("free", "Calculating free space…")
        row("search", "Search conversations…", path: "/library")
        row("analyze", "Analyze usage…", path: "/analyze")
        row("optimize", "Optimize…", path: "/findings")
        row("capture", "Capture & index…", path: "/settings/sync")
        row("health", "Library health & backups…", path: "/settings")
        row("sources", "Manage sources…", path: "/settings/sources")
        let finder = NSMenuItem(title: "Show library in Finder", action: #selector(showLibraryInFinder), keyEquivalent: "")
        finder.target = self; rows["finder"] = finder
        row("settings", "Settings…", path: "/settings/preferences")
        let eject = NSMenuItem(title: "Eject library…", action: #selector(ejectLibrary), keyEquivalent: "")
        eject.target = self; rows["eject"] = eject
        // Cmd-Q remains in the app's standard menu. Repeating it here makes
        // AppKit reserve a shortcut column beside every status-menu row.
        let quit = NSMenuItem(title: "Quit Pharos", action: #selector(quitApp), keyEquivalent: "")
        quit.target = self; rows["quit"] = quit
        rowTitles = rows.mapValues(\.title)
        arrangeMenu()
    }

    private func arrangeMenu() {
        let drive = (snapshot["library"] as? [String: Any])?["drive"] as? [String: Any] ?? [:]
        var order: [String] = []
        for id in preferences.order {
            if id == "eject" && drive["ejectable"] as? Bool != true { continue }
            if MenuBarPreferences.isSeparator(id) && (order.isEmpty || MenuBarPreferences.isSeparator(order.last!)) { continue }
            order.append(id)
        }
        if let last = order.last, MenuBarPreferences.isSeparator(last) { order.removeLast() }
        guard arrangedOrder != order else { return }
        arrangedOrder = order
        menu.removeAllItems()
        var previousGroup: String?
        let lastUsage = order.last { MenuBarPreferences.group($0) == "usage" }
        for id in order {
            if MenuBarPreferences.isSeparator(id) { menu.addItem(.separator()); continue }
            let group = MenuBarPreferences.group(id)
            if previousGroup != group && ["usage", "library"].contains(group) {
                let heading = NSMenuItem(title: group == "usage" ? "Last 24 hours" : "Library", action: nil, keyEquivalent: "")
                heading.isEnabled = false
                menu.addItem(heading)
            }
            if let row = rows[id] { menu.addItem(row) }
            if id == "status" {
                for key in ["detail", "error"] { if let row = rows[key] { menu.addItem(row) } }
            }
            if id == "space", let free = rows["free"] { menu.addItem(free) }
            if id == lastUsage, let note = rows["usage-note"] { menu.addItem(note) }
            previousGroup = group
        }
    }

    func refresh() {
        guard let service else { return }
        if connectedURL != service.url || libraryConnection != service.libraryConnection {
            generation += 1
            connectedURL = service.url
            libraryConnection = service.libraryConnection
            indexing = false
            preferencesChangedAt = .distantPast
            statusTask?.cancel(); statusTask = nil
            usageTask?.cancel(); usageTask = nil
            snapshot = [:]; usage = nil; usageUpdated = .distantPast
            statusError = nil; usageError = nil; actionError = nil
        }
        update()
        guard service.hasConnectedLibrary, statusTask == nil, let request = service.localRequest("/api/menu-bar/status") else { return }
        let origin = service.url
        let connection = service.libraryConnection
        let generation = self.generation
        let actionErrorVersion = self.actionErrorVersion
        statusTask = fetch(request) { [weak self] result in
            guard let self, self.generation == generation, self.service?.url == origin, self.service?.libraryConnection == connection else { return }
            self.statusTask = nil
            switch result {
            case .success(let value):
                self.snapshot = value
                self.statusError = nil
                // Only a poll started after the action failure can clear it.
                if self.actionErrorVersion == actionErrorVersion { self.actionError = nil }
                if let saved = value["settings"] as? [String: Any] { self.applyPreferences(saved, fromServer: true) }
                self.refreshUsage()
            case .failure(let error): self.statusError = error.localizedDescription
            }
            self.update()
        }
    }

    private func refreshUsage() {
        guard service?.hasConnectedLibrary == true, preferences["enabled"], ["words", "tokens", "carbon", "cost"].contains(where: { preferences[$0] }),
              usageTask == nil, Date().timeIntervalSince(usageUpdated) >= 60,
              let request = service?.localRequest("/api/menu-bar/usage") else { return }
        let origin = service?.url
        let connection = service?.libraryConnection
        let generation = self.generation
        usageTask = fetch(request) { [weak self] result in
            guard let self, self.generation == generation, self.service?.url == origin, self.service?.libraryConnection == connection else { return }
            self.usageTask = nil
            self.usageUpdated = Date()
            switch result {
            case .success(let value): self.usage = value; self.usageError = nil
            case .failure(let error): self.usage = nil; self.usageError = error.localizedDescription
            }
            self.update()
        }
    }

    private func fetch(_ request: URLRequest, completion: @escaping (Result<[String: Any], Error>) -> Void) -> URLSessionDataTask {
        let task = URLSession.shared.dataTask(with: request) { data, response, error in
            let result: Result<[String: Any], Error>
            do {
                if let error { throw error }
                guard let http = response as? HTTPURLResponse, (200..<300).contains(http.statusCode), let data else {
                    throw NSError(domain: "Pharos", code: 1, userInfo: [NSLocalizedDescriptionKey: "Could not reach the local archive service."])
                }
                guard let value = try JSONSerialization.jsonObject(with: data) as? [String: Any] else {
                    throw NSError(domain: "Pharos", code: 2, userInfo: [NSLocalizedDescriptionKey: "The local archive returned an invalid response."])
                }
                result = .success(value)
            } catch { result = .failure(error) }
            DispatchQueue.main.async { completion(result) }
        }
        task.resume()
        return task
    }

    private func update() {
        guard let service else { return }
        arrangeMenu()
        if preferences["enabled"] && item == nil {
            let item = NSStatusBar.system.statusItem(withLength: 38)
            item.menu = menu
            if let button = item.button {
                let mark = NSImageView(frame: NSRect(x: 5, y: 2, width: 18, height: 18))
                mark.image = MenuBarLighthouse.image()
                mark.contentTintColor = .labelColor
                mark.setAccessibilityElement(false)
                button.addSubview(mark)
                button.addSubview(dot)
            }
            self.item = item
        } else if !preferences["enabled"], let item {
            NSStatusBar.system.removeStatusItem(item)
            self.item = nil
        }
        let library = snapshot["library"] as? [String: Any] ?? [:]
        let activities = library["activities"] as? [[String: Any]] ?? []
        let drive = library["drive"] as? [String: Any] ?? [:]
        var problem = actionError ?? service.error ?? service.setupError ?? statusError ?? nonempty(snapshot["error"])
        if let disconnected = service.disconnected, disconnected.state != .finishing { problem = disconnected.note ?? "Library unavailable: \(disconnected.volume)" }
        if service.setupNeeded && !service.settingUp { problem = problem ?? "Set up Pharos to connect a library." }
        // The managed app with no library selected (or the selected one
        // unavailable) has nothing to poll, so its snapshot stays empty: that
        // is idle, not a check in progress. A selected library whose service
        // is not up yet (starting, respawning, stopped by a delivery) is work.
        let noLibrary = service.url != nil && service.noLibrarySelected && !service.ejecting && !service.settingUp && service.disconnected?.state != .finishing
        let busy = service.ejecting || service.settingUp || service.disconnected?.state == .finishing || service.url == nil || !activities.isEmpty || indexing || (snapshot["library"] == nil && !noLibrary)
        let title: String
        let detail: String
        if let problem {
            title = "Pharos · Needs attention"
            detail = String(problem.split(separator: "\n").first ?? "Unknown error")
            dot.color = .systemRed
        } else if noLibrary {
            title = "Pharos · No library connected"
            detail = ""
            dot.color = .systemGray
        } else if busy {
            title = "Pharos · Working"
            if service.ejecting || service.disconnected?.state == .finishing { detail = "Releasing the library for eject…" }
            else if service.url == nil { detail = service.settingUp ? "Setting up Pharos…" : service.status }
            else if let activity = activities.first {
                var parts = [activity["label"] as? String ?? "Working"]
                if let progress = activity["progress"] as? Double { parts.append("stage \(Int(progress * 100))%") }
                if let completion = Self.date(activity["estimated_completion_at"]), completion > Date() {
                    // A whole-operation forecast covers the run; otherwise the estimate is the stage's.
                    let minutes = max(1, Int(ceil(completion.timeIntervalSinceNow / 60)))
                    parts.append(activity["estimated_completion_scope"] as? String == "operation" ? "about \(minutes) min left" : "about \(minutes) min for this stage")
                }
                detail = parts.joined(separator: " · ")
            } else if let operation = service.libraryOperation { detail = operation }
            else { detail = indexing ? "Starting indexing…" : !service.hasConnectedLibrary ? "Waiting for the library…" : "Checking the library…" }
            dot.color = .systemYellow
        } else {
            title = "Pharos · Idle"
            detail = ""
            dot.color = .systemGreen
        }
        setRowTitle("status", title)
        setRowTitle("detail", detail)
        rows["detail"]?.isHidden = detail.isEmpty
        rows["detail"]?.toolTip = problem ?? detail
        item?.button?.toolTip = detail.isEmpty ? title : "\(title) — \(detail)"
        item?.button?.setAccessibilityLabel(detail.isEmpty ? title : "\(title). \(detail)")
        rows["error"]?.isHidden = problem == nil
        rows["error"]?.representedObject = snapshot["error_path"] as? String ?? "/settings/sync"
        setRowTitle("indexed", Self.date(snapshot["indexed_at"]).map { "Indexed \(Self.ago($0))" } ?? "Not indexed yet")
        for key in ["indexed", "words", "tokens", "carbon", "cost", "backup", "space", "index", "eject"] {
            rows[key]?.isHidden = !preferences[key]
        }
        metric("words", "Words typed", (usage?["human_words"] as? NSNumber).map { NumberFormatter.localizedString(from: $0, number: .decimal) })
        metric("tokens", "Tokens used", (usage?["tokens"] as? NSNumber).map { Self.compact($0.doubleValue) })
        metric("carbon", "Estimated CO₂e", (usage?["co2e_g"] as? NSNumber).map { Self.mass($0.doubleValue) })
        metric("cost", "Estimated API cost", (usage?["cost_usd"] as? NSNumber).map { String(format: "$%.2f", $0.doubleValue) })
        metric("backup", "Last backup", Self.date(snapshot["backup_at"]).map(Self.ago) ?? "Never")
        let storage = snapshot["storage"] as? [String: Any] ?? [:]
        let sizes = storage["sizes"] as? [String: Any] ?? [:]
        metric("space", "Library size", (sizes["library_bytes"] as? NSNumber).map { Self.bytes($0.int64Value) } ?? "Calculating…")
        setRowTitle("free", (storage["free_bytes"] as? NSNumber).map { "\(Self.bytes($0.int64Value)) free on \(drive["name"] as? String ?? "library drive")" } ?? "Checking free space…")
        rows["free"]?.isHidden = !preferences["space"]
        let hasUsage = ["words", "tokens", "carbon", "cost"].contains { preferences[$0] }
        setRowTitle("usage-note", usageError == nil ? "Loading usage…" : "Usage unavailable")
        rows["usage-note"]?.isHidden = !hasUsage || usage != nil
        rows["usage-note"]?.toolTip = usageError
        let ejectable = drive["ejectable"] as? Bool == true
        rows["eject"]?.isHidden = !preferences["eject"] || !ejectable
        setRowTitle("eject", "Eject \(drive["name"] as? String ?? "library")…")
        rows["eject"]?.isEnabled = ejectable && !service.ejecting && service.url != nil
        rows["finder"]?.isEnabled = libraryDirectory != nil
        rows["index"]?.isEnabled = service.url != nil && service.hasConnectedLibrary && !service.ejecting && !indexing && !activities.contains { ["sync", "index", "capture", "capture-other", "maintenance"].contains($0["kind"] as? String ?? "") }
        for key in ["words", "tokens", "carbon", "cost", "backup", "space"] { rows[key]?.isEnabled = service.url != nil }
        layoutMenuText()
        menu.update()
    }

    private func setRowTitle(_ key: String, _ title: String) {
        // attributedTitle also changes title; retain the unwrapped source for
        // later reflow and for status updates while the menu is open.
        rowTitles[key] = title
        rows[key]?.title = title
    }

    private func metric(_ key: String, _ label: String, _ value: String?) {
        metricText[key] = (label, value ?? (usageError == nil ? "…" : "—"))
    }

    // Measure AppKit's own title margins instead of assuming a particular OS
    // menu gutter. The metrics retain native highlighting, actions and VoiceOver.
    static func menuTitleInsets(font: NSFont) -> CGFloat {
        let probe = NSMenu()
        probe.font = font
        let title = NSAttributedString(string: String(repeating: "M", count: 24), attributes: [.font: font])
        let row = NSMenuItem()
        row.attributedTitle = title
        probe.addItem(row)
        return probe.size.width - ceil(title.size().width)
    }

    static func wrappedMenuTitle(_ text: String, width: CGFloat, font: NSFont) -> NSAttributedString {
        let attributes: [NSAttributedString.Key: Any] = [.font: font]
        let storage = NSTextStorage(string: text, attributes: attributes)
        let layout = NSLayoutManager()
        let container = NSTextContainer(size: NSSize(width: max(1, width), height: .greatestFiniteMagnitude))
        container.lineFragmentPadding = 0
        container.lineBreakMode = .byWordWrapping
        layout.addTextContainer(container)
        storage.addLayoutManager(layout)
        let source = text as NSString
        var lines: [String] = []
        layout.enumerateLineFragments(forGlyphRange: layout.glyphRange(for: container)) { _, _, _, glyphs, _ in
            let characters = layout.characterRange(forGlyphRange: glyphs, actualGlyphRange: nil)
            lines.append(source.substring(with: characters).trimmingCharacters(in: .whitespacesAndNewlines))
        }
        // Explicit line breaks let a standard NSMenuItem grow vertically.
        return NSAttributedString(string: lines.joined(separator: "\n"), attributes: attributes)
    }

    private func layoutMenuText() {
        let font = menu.font ?? NSFont.menuFont(ofSize: 13)
        let attributes: [NSAttributedString.Key: Any] = [.font: font]
        let visible = metricText.filter { rows[$0.key]?.menu === menu && rows[$0.key]?.isHidden == false }.map(\.value)
        let labelWidth = visible.map { ($0.label as NSString).size(withAttributes: attributes).width }.max() ?? 0
        let valueWidth = visible.map { ($0.value as NSString).size(withAttributes: attributes).width }.max() ?? 0
        let metricsWidth = ceil(labelWidth + 22 + valueWidth)
        let screenWidth = (item?.button?.window?.screen ?? NSScreen.main)?.visibleFrame.width ?? 1024
        let availableWidth = max(1, screenWidth - 24)
        menu.minimumWidth = min(availableWidth, max(300, metricsWidth + titleInsets))
        let textWidth = max(1, menu.minimumWidth - titleInsets)
        for (key, row) in rows where metricText[key] == nil {
            row.attributedTitle = Self.wrappedMenuTitle(rowTitles[key] ?? row.title, width: textWidth, font: font)
        }
        func alignMetrics(to width: CGFloat) {
            let paragraph = NSMutableParagraphStyle()
            paragraph.tabStops = [NSTextTab(textAlignment: .right, location: width)]
            for (key, text) in metricText {
                rows[key]?.attributedTitle = NSAttributedString(string: "\(text.label)\t\(text.value)", attributes: [.paragraphStyle: paragraph, .font: font])
            }
        }
        // Clear the last layout's width first, so a menu can shrink again.
        alignMetrics(to: metricsWidth)
        menu.update()
        alignMetrics(to: max(metricsWidth, menu.size.width - titleInsets))
    }

    func menuWillOpen(_ menu: NSMenu) { refresh() }

    func invalidateUsage() { usageUpdated = .distantPast; refreshUsage() }

    @objc private func navigate(_ sender: NSMenuItem) { showWindow(path: sender.representedObject as? String ?? "") }

    func showWindow(path: String = "") {
        NSApp.activate(ignoringOtherApps: true)
        if let window = window ?? webView?.window, window.isVisible || window.isMiniaturized {
            window.deminiaturize(nil)
            window.makeKeyAndOrderFront(nil)
            if !path.isEmpty {
                if webView != nil { load(path) }
                else { pendingPath = path }
            }
        } else {
            pendingPath = path.isEmpty ? nil : path
            openWindow?()
        }
    }

    func navigationFinished() {
        if let path = pendingPath { pendingPath = nil; load(path) }
    }

    private func load(_ path: String) {
        guard let origin = service?.url, let target = URL(string: path, relativeTo: origin) else { return }
        webView?.load(URLRequest(url: target))
    }

    @objc private func indexNow() {
        guard let request = service?.localRequest("/api/sync/check", method: "POST"), !indexing else { return }
        indexing = true; actionError = nil; update()
        let generation = self.generation
        let connection = service?.libraryConnection
        _ = fetch(request) { [weak self] result in
            guard let self, self.generation == generation, self.service?.libraryConnection == connection else { return }
            self.indexing = false
            if case .failure(let error) = result { self.actionError = error.localizedDescription }
            self.refresh()
        }
    }

    private var libraryDirectory: URL? {
        guard let service, service.url != nil, !service.ejecting, service.disconnected == nil,
              let library = snapshot["library"] as? [String: Any],
              let path = library["library_dir"] as? String, path.hasPrefix("/") else { return nil }
        return URL(fileURLWithPath: path, isDirectory: true)
    }

    @objc private func showLibraryInFinder() {
        guard let directory = libraryDirectory else { return }
        revealInFinder(directory)
    }

    @objc private func ejectLibrary() {
        guard let service else { return }
        actionError = nil
        service.ejectLibrary { [weak self] error in self?.actionError = error; self?.refresh() }
    }
    @objc private func quitApp() { NSApp.terminate(nil) }

    private func nonempty(_ value: Any?) -> String? { guard let value = value as? String, !value.isEmpty else { return nil }; return value }
    static func date(_ value: Any?) -> Date? {
        guard let text = value as? String else { return nil }
        let formatter = ISO8601DateFormatter()
        formatter.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        if let date = formatter.date(from: text) { return date }
        formatter.formatOptions = [.withInternetDateTime]
        return formatter.date(from: text)
    }
    static func ago(_ date: Date) -> String {
        let formatter = RelativeDateTimeFormatter(); formatter.unitsStyle = .short
        return formatter.localizedString(for: date, relativeTo: Date())
    }
    static func compact(_ value: Double) -> String {
        scaled(value, units: ["", "k", "M", "B", "T"], separator: "")
    }
    static func mass(_ grams: Double) -> String {
        scaled(grams, units: ["g", "kg", "t"], separator: " ")
    }
    private static func scaled(_ value: Double, units: [String], separator: String) -> String {
        var amount = value, unit = 0
        // Three significant figures round 999.5 up to 1000. Promote before
        // formatting so a rounded boundary becomes 1M or 1 kg, not 1000k.
        while amount >= 999.5 && unit < units.count - 1 { amount /= 1000; unit += 1 }
        let formatter = NumberFormatter()
        formatter.locale = Locale(identifier: "en_US_POSIX")
        formatter.numberStyle = .decimal
        formatter.usesGroupingSeparator = false
        formatter.usesSignificantDigits = true
        formatter.minimumSignificantDigits = 1
        formatter.maximumSignificantDigits = 3
        formatter.roundingMode = .halfUp
        return (formatter.string(from: NSNumber(value: amount)) ?? "—") + separator + units[unit]
    }
    static func bytes(_ value: Int64) -> String { ByteCountFormatter.string(fromByteCount: value, countStyle: .decimal) }

    deinit { timer?.invalidate(); statusTask?.cancel(); usageTask?.cancel(); if let item { NSStatusBar.system.removeStatusItem(item) } }
}
