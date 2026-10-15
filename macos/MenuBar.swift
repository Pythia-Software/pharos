import AppKit
import Combine
import WebKit

struct MenuBarPreferences {
    static let key = "pharos-menu-bar"
    static let defaults: [String: Bool] = [
        "enabled": true, "indexed": true, "words": true, "tokens": true, "carbon": true,
        "cost": false, "backup": false, "space": false, "index": false, "eject": false,
    ]
    var values: [String: Bool]

    init(_ saved: [String: Any] = [:]) {
        values = Self.defaults
        for key in values.keys {
            // JSON booleans only; a numeric or corrupt preference uses its default.
            if let value = saved[key] as? NSNumber, CFGetTypeID(value) == CFBooleanGetTypeID() {
                values[key] = value.boolValue
            }
        }
    }
    subscript(_ key: String) -> Bool { values[key] == true }
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
    private var pendingPath: String?
    private var item: NSStatusItem?
    private let dot = MenuBarDot(frame: NSRect(x: 25, y: 7, width: 8, height: 8))
    private let menu = NSMenu()
    private var rows: [String: NSMenuItem] = [:]
    private var metricText: [String: (label: String, value: String)] = [:]
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
        defaults.set(preferences.values, forKey: cacheKey)
        update()
        if !fromServer { refresh() }
    }

    private func makeMenu() {
        func row(_ key: String, _ title: String, path: String? = nil) {
            let row = NSMenuItem(title: title, action: path == nil ? nil : #selector(navigate(_:)), keyEquivalent: "")
            row.target = self
            row.representedObject = path
            row.isEnabled = path != nil
            menu.addItem(row)
            rows[key] = row
        }
        func separator(_ key: String) { let row = NSMenuItem.separator(); menu.addItem(row); rows[key] = row }
        row("open", "Open Pharos", path: "")
        separator("status-rule")
        row("status", "Pharos · Starting")
        row("detail", "Starting local archive…")
        row("indexed", "Not indexed yet")
        let index = NSMenuItem(title: "Index changes now", action: #selector(indexNow), keyEquivalent: "")
        index.target = self; menu.addItem(index); rows["index"] = index
        row("error", "View error…", path: "/settings/sync")
        separator("usage-rule")
        row("usage-heading", "Last 24 hours")
        row("words", "Words typed", path: "/usage?usage=writing")
        row("tokens", "Tokens used", path: "/usage?usage=tokens")
        row("cost", "Estimated API cost", path: "/usage?usage=tokens")
        row("carbon", "Estimated CO₂e", path: "/usage?usage=carbon")
        row("usage-note", "Loading usage…")
        separator("library-rule")
        row("library-heading", "Library")
        row("backup", "Last backup", path: "/settings")
        row("space", "Library size", path: "/settings")
        row("free", "Calculating free space…")
        separator("settings-rule")
        row("settings", "Settings…", path: "/settings/preferences")
        separator("quit-rule")
        let eject = NSMenuItem(title: "Eject library…", action: #selector(ejectLibrary), keyEquivalent: "")
        eject.target = self; menu.addItem(eject); rows["eject"] = eject
        // Cmd-Q remains in the app's standard menu. Repeating it here makes
        // AppKit reserve a shortcut column beside every status-menu row.
        let quit = NSMenuItem(title: "Quit Pharos", action: #selector(quitApp), keyEquivalent: "")
        quit.target = self; menu.addItem(quit)
    }

    func refresh() {
        guard let service else { return }
        if connectedURL != service.url {
            generation += 1
            connectedURL = service.url
            statusTask?.cancel(); statusTask = nil
            usageTask?.cancel(); usageTask = nil
            snapshot = [:]; usage = nil; usageUpdated = .distantPast
            statusError = nil; usageError = nil; actionError = nil
        }
        update()
        guard statusTask == nil, let request = service.localRequest("/api/menu-bar/status") else { return }
        let origin = service.url
        let generation = self.generation
        let actionErrorVersion = self.actionErrorVersion
        statusTask = fetch(request) { [weak self] result in
            guard let self, self.generation == generation, self.service?.url == origin else { return }
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
        guard preferences["enabled"], ["words", "tokens", "carbon", "cost"].contains(where: { preferences[$0] }),
              usageTask == nil, Date().timeIntervalSince(usageUpdated) >= 60,
              let request = service?.localRequest("/api/menu-bar/usage") else { return }
        let origin = service?.url
        let generation = self.generation
        usageTask = fetch(request) { [weak self] result in
            guard let self, self.generation == generation, self.service?.url == origin else { return }
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
        let busy = service.ejecting || service.settingUp || service.disconnected?.state == .finishing || service.url == nil || !activities.isEmpty || indexing || snapshot["library"] == nil
        let title: String
        let detail: String
        if let problem {
            title = "Pharos · Needs attention"
            detail = String(problem.split(separator: "\n").first ?? "Unknown error")
            dot.color = .systemRed
        } else if busy {
            title = "Pharos · Working"
            if service.ejecting || service.disconnected?.state == .finishing { detail = "Releasing the library for eject…" }
            else if service.url == nil { detail = service.settingUp ? "Setting up Pharos…" : service.status }
            else if let activity = activities.first {
                var parts = [activity["label"] as? String ?? "Working"]
                if let progress = activity["progress"] as? Double { parts.append("\(Int(progress * 100))%") }
                if let completion = Self.date(activity["estimated_completion_at"]), completion > Date() {
                    parts.append("about \(max(1, Int(ceil(completion.timeIntervalSinceNow / 60)))) min left")
                }
                detail = parts.joined(separator: " · ")
            } else { detail = indexing ? "Starting indexing…" : "Checking the library…" }
            dot.color = .systemYellow
        } else {
            title = "Pharos · Idle"
            detail = ""
            dot.color = .systemGreen
        }
        rows["status"]?.title = title
        rows["detail"]?.title = detail.count > 75 ? String(detail.prefix(72)) + "…" : detail
        rows["detail"]?.isHidden = detail.isEmpty
        rows["detail"]?.toolTip = detail
        item?.button?.toolTip = detail.isEmpty ? title : "\(title) — \(detail)"
        item?.button?.setAccessibilityLabel(detail.isEmpty ? title : "\(title). \(detail)")
        rows["error"]?.isHidden = problem == nil
        rows["error"]?.representedObject = snapshot["error_path"] as? String ?? "/settings/sync"
        rows["indexed"]?.title = Self.date(snapshot["indexed_at"]).map { "Indexed \(Self.ago($0))" } ?? "Not indexed yet"
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
        layoutMetrics()
        rows["free"]?.title = (storage["free_bytes"] as? NSNumber).map { "\(Self.bytes($0.int64Value)) free on \(drive["name"] as? String ?? "library drive")" } ?? "Checking free space…"
        rows["free"]?.isHidden = !preferences["space"]
        let hasUsage = ["words", "tokens", "carbon", "cost"].contains { preferences[$0] }
        rows["usage-heading"]?.isHidden = !hasUsage; rows["usage-rule"]?.isHidden = !hasUsage
        rows["usage-note"]?.title = usageError == nil ? "Loading usage…" : "Usage unavailable"
        rows["usage-note"]?.isHidden = !hasUsage || usage != nil
        rows["usage-note"]?.toolTip = usageError
        let hasLibrary = preferences["backup"] || preferences["space"]
        rows["library-heading"]?.isHidden = !hasLibrary; rows["library-rule"]?.isHidden = !hasLibrary
        let ejectable = drive["ejectable"] as? Bool == true
        rows["eject"]?.isHidden = !preferences["eject"] || !ejectable
        rows["eject"]?.title = "Eject \(drive["name"] as? String ?? "library")…"
        rows["eject"]?.isEnabled = ejectable && !service.ejecting && service.url != nil
        rows["index"]?.isEnabled = service.url != nil && !service.ejecting && !indexing && !activities.contains { ["sync", "index", "capture", "capture-other", "maintenance"].contains($0["kind"] as? String ?? "") }
        for key in ["words", "tokens", "carbon", "cost", "backup", "space"] { rows[key]?.isEnabled = service.url != nil }
        menu.update()
    }

    private func metric(_ key: String, _ label: String, _ value: String?) {
        metricText[key] = (label, value ?? (usageError == nil ? "…" : "—"))
    }

    private func layoutMetrics() {
        let font = NSFont.menuFont(ofSize: 13)
        let attributes: [NSAttributedString.Key: Any] = [.font: font]
        let visible = metricText.filter { preferences[$0.key] }.map(\.value)
        let labelWidth = visible.map { ($0.label as NSString).size(withAttributes: attributes).width }.max() ?? 0
        let valueWidth = visible.map { ($0.value as NSString).size(withAttributes: attributes).width }.max() ?? 0
        let paragraph = NSMutableParagraphStyle()
        paragraph.tabStops = [NSTextTab(textAlignment: .right, location: ceil(labelWidth + 22 + valueWidth))]
        for (key, text) in metricText {
            rows[key]?.attributedTitle = NSAttributedString(string: "\(text.label)\t\(text.value)", attributes: [.paragraphStyle: paragraph, .font: font])
        }
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
        _ = fetch(request) { [weak self] result in
            guard let self else { return }
            self.indexing = false
            if case .failure(let error) = result { self.actionError = error.localizedDescription }
            self.refresh()
        }
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
