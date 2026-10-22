import AppKit
import Combine
import WebKit

// Minimal service stand-in: exercise the real native controller without
// opening a user's library, starting a service, or ejecting a real drive.
final class ArchiveService: ObservableObject {
    struct Disconnection {
        enum State { case finishing, released, unplugged }
        var state: State
        var volume: String
        var note: String?
    }
    @Published var url: URL? = URL(string: "http://menu-bar-test.invalid/")
    var error: String?
    var setupError: String?
    var disconnected: Disconnection?
    var setupNeeded = false
    var settingUp = false
    var ejecting = false
    var status = "Starting…"
    var ejectRequested = false
    var ejectFailure: String?
    func localRequest(_ path: String, method: String = "GET") -> URLRequest? {
        guard let url, let target = URL(string: path, relativeTo: url) else { return nil }
        var request = URLRequest(url: target); request.httpMethod = method
        request.setValue("Bearer fixture-token", forHTTPHeaderField: "Authorization")
        return request
    }
    func ejectLibrary(reply: @escaping (String?) -> Void) { ejectRequested = true; reply(ejectFailure) }
}
final class ArchiveWKWebView: WKWebView {}

final class MenuFixture: URLProtocol {
    static var activity = false
    static var activityLabel = "Indexing captures"
    static var ejectable = true
    static var failure = ""
    static var indexRequested = false
    static var usageFailure = false
    static var usageRequests = 0
    static var indexFailure = false
    static var statusFailure = false
    override class func canInit(with request: URLRequest) -> Bool { request.url?.host == "menu-bar-test.invalid" }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        precondition(request.value(forHTTPHeaderField: "Authorization") == "Bearer fixture-token")
        let path = request.url!.path
        let value: [String: Any]
        if path == "/api/menu-bar/usage" {
            Self.usageRequests += 1
            if Self.usageFailure {
                client?.urlProtocol(self, didFailWithError: NSError(domain: NSURLErrorDomain, code: NSURLErrorTimedOut))
                return
            }
            value = ["human_words": 3240, "tokens": 1200000, "co2e_g": 18.25, "cost_usd": 8.42]
        } else if path == "/api/sync/check" {
            Self.indexRequested = true
            if Self.indexFailure {
                client?.urlProtocol(self, didFailWithError: NSError(domain: NSURLErrorDomain, code: NSURLErrorCannotConnectToHost))
                return
            }
            value = ["accepted": true]
        } else {
            if Self.statusFailure {
                client?.urlProtocol(self, didFailWithError: NSError(domain: NSURLErrorDomain, code: NSURLErrorCannotConnectToHost))
                return
            }
            value = ["settings": MenuBarPreferences.defaults.merging(["cost": true, "backup": true, "space": true, "index": true, "eject": true]) { _, new in new },
                "error": Self.failure, "indexed_at": "2026-10-13T10:40:00Z", "backup_at": "2026-10-13T07:40:00Z",
                "library": ["library_dir": "/Volumes/Archive SSD/Pharos", "activities": Self.activity ? [["kind": "index", "label": Self.activityLabel, "progress": 0.42]] : [], "drive": ["ejectable": Self.ejectable, "name": "Archive SSD"]],
                "storage": ["sizes": ["library_bytes": 12400000000], "free_bytes": 286000000000]]
        }
        let response = HTTPURLResponse(url: request.url!, statusCode: 200, httpVersion: "HTTP/1.1", headerFields: ["Content-Type": "application/json"])!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: try! JSONSerialization.data(withJSONObject: value))
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() {}
}

@main struct MenuBarHarness {
    static func pump(_ predicate: () -> Bool) {
        let end = Date().addingTimeInterval(5)
        while !predicate() && Date() < end { RunLoop.main.run(until: Date().addingTimeInterval(0.02)) }
        precondition(predicate(), "Timed out waiting for native menu")
    }
    static func menu(_ controller: MenuBarController) -> NSMenu {
        let item = Mirror(reflecting: controller).children.first { $0.label == "item" }!.value as! NSStatusItem
        return item.menu!
    }
    static func main() {
        _ = NSApplication.shared
        NSApp.setActivationPolicy(.accessory)
        URLProtocol.registerClass(MenuFixture.self)
        let suite = "pharos-menu-test-\(UUID().uuidString)"
        let defaults = UserDefaults(suiteName: suite)!
        defer { defaults.removePersistentDomain(forName: suite) }
        let service = ArchiveService()
        let controller = MenuBarController(service: service, defaults: defaults)
        let menu = menu(controller)
        pump { menu.items.contains { $0.title == "Pharos · Idle" } && menu.items.contains { $0.attributedTitle?.string.contains("3,240") == true } }
        let visible = menu.items.filter { !$0.isHidden && !$0.isSeparatorItem }.map { String($0.title.split(separator: "\t")[0]) }
        precondition(visible.first == "Open Pharos")
        precondition(!visible.contains("No work running"))
        let words = menu.items.first { $0.title.hasPrefix("Words typed") }!
        let baselineWidth = menu.size.width
        let baselineValueEdge = words.attributedTitle!.size().width
        precondition(baselineWidth - baselineValueEdge < 40, "Values must reach the menu's trailing text edge")
        // A wider native item changes the menu's width without changing usage.
        let wideAction = NSMenuItem(title: String(repeating: "W", count: 40), action: nil, keyEquivalent: "")
        menu.addItem(wideAction)
        controller.refresh()
        let expandedWidth = menu.size.width
        let expandedValueEdge = words.attributedTitle!.size().width
        precondition(expandedWidth > baselineWidth)
        precondition(abs((expandedWidth - baselineWidth) - (expandedValueEdge - baselineValueEdge)) < 2, "Value alignment must follow the full menu width")
        menu.removeItem(wideAction)
        controller.refresh()
        precondition(abs(menu.size.width - baselineWidth) < 2, "Menus must shrink after a wider item goes away")
        let busyLabel = "Manual sync: evaluating findings while checking every captured conversation and its retained tool results"
        MenuFixture.activityLabel = busyLabel
        MenuFixture.activity = true
        controller.refresh()
        pump { menu.items.contains { $0.title.contains("Manual sync:") } }
        let detail = menu.items.first { $0.title.hasPrefix("Manual sync:") }!
        precondition(detail.title.contains("\n"), "Long status must wrap")
        precondition(detail.title.replacingOccurrences(of: "\n", with: " ") == busyLabel + " · stage 42%", "Wrapping must retain the complete status")
        precondition(menu.size.width <= baselineWidth + 2, "Status must not widen the menu")
        let font = NSFont.menuFont(ofSize: 13)
        for text in [String(repeating: "conversation/", count: 20), "👩🏽‍💻 日本語の会話を読み込み、ライブラリのインデックスを更新しています。"] {
            let wrapped = MenuBarController.wrappedMenuTitle(text, width: 160, font: font)
            precondition(wrapped.size().width <= 162, "Unbroken and multilingual text must fit")
            precondition(wrapped.string.replacingOccurrences(of: "\n", with: "").replacingOccurrences(of: " ", with: "") == text.replacingOccurrences(of: " ", with: ""))
        }
        if let output = ProcessInfo.processInfo.environment["PHAROS_MENU_SCREENSHOT"] {
            // Capture only this test's native menu; no service or user library.
            DispatchQueue.main.async {
                if let view = NSApp.windows.first(where: { $0.isVisible && $0.level == .popUpMenu })?.contentView,
                   let image = view.bitmapImageRepForCachingDisplay(in: view.bounds) {
                    view.displayIfNeeded()
                    view.cacheDisplay(in: view.bounds, to: image)
                    if let data = image.representation(using: .png, properties: [:]) { try! data.write(to: URL(fileURLWithPath: output)) }
                }
                menu.cancelTracking()
            }
            let screen = NSScreen.main!.visibleFrame
            menu.popUp(positioning: nil, at: NSPoint(x: screen.midX, y: screen.maxY - 80), in: nil)
        }
        MenuFixture.activityLabel = "Indexing captures"
        MenuFixture.activity = false
        controller.refresh()
        pump { menu.items.contains { $0.title == "Pharos · Idle" } }
        let indexed = visible.firstIndex { $0.hasPrefix("Indexed ") }!
        precondition(visible[indexed + 1] == "Index changes now")
        precondition(visible.suffix(2) == ["Eject Archive SSD…", "Quit Pharos"])
        let metrics = visible.filter { ["Words typed", "Tokens used", "Estimated CO₂e", "Estimated API cost", "Last backup", "Library size"].contains($0) }
        precondition(metrics == ["Words typed", "Tokens used", "Estimated API cost", "Estimated CO₂e", "Last backup", "Library size"])
        controller.applyPreferences(["words": false, "tokens": true, "carbon": true])
        precondition(!menu.items.contains { $0.title.hasPrefix("Words typed") && !$0.isHidden })
        precondition(!menu.items.first { $0.title.hasPrefix("Tokens used") }!.isHidden)
        // The same corrupt/legacy inputs are normalized by JS, Go, and Swift.
        let fixturesURL = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent().appendingPathComponent("testdata/menu-bar-layout.json")
        let fixtures = try! JSONSerialization.jsonObject(with: Data(contentsOf: fixturesURL)) as! [[String: Any]]
        for fixture in fixtures {
            let prefs = MenuBarPreferences(fixture["saved"] as! [String: Any])
            precondition(prefs.order == fixture["order"] as! [String], fixture["name"] as! String)
            precondition(prefs["enabled"] == fixture["enabled"] as! Bool)
            precondition(MenuBarPreferences(prefs.saved).order == prefs.order, "Order must survive the native cache")
        }
        let shortcuts = [("search", "Search conversations…", "/library"), ("analyze", "Analyze usage…", "/analyze"),
                         ("optimize", "Optimize…", "/findings"), ("capture", "Capture & index…", "/settings/sync"),
                         ("health", "Library health & backups…", "/settings"), ("sources", "Manage sources…", "/settings/sources")]
        precondition(shortcuts.allSatisfy { !MenuBarPreferences()[$0.0] } && !MenuBarPreferences()["finder"], "Shortcuts must not change the default menu")
        controller.applyPreferences(["layoutVersion": 2, "order": ["open"] + shortcuts.map { $0.0 } + ["finder", "status", "settings", "quit"]])
        var shortcutOpened = false
        controller.openWindow = { shortcutOpened = true }
        for (_, title, path) in shortcuts {
            let item = menu.items.first { $0.title == title }!
            shortcutOpened = false
            NSApp.sendAction(item.action!, to: item.target, from: item)
            precondition(shortcutOpened, "Shortcut must reopen a closed window")
            precondition(Mirror(reflecting: controller).children.first { $0.label == "pendingPath" }!.value as? String == path)
        }
        controller.openWindow = nil
        let finder = menu.items.first { $0.title == "Show library in Finder" }!
        var revealed: URL?
        controller.revealInFinder = { revealed = $0 }
        precondition(finder.isEnabled)
        NSApp.sendAction(finder.action!, to: finder.target, from: finder)
        precondition(revealed?.path == "/Volumes/Archive SSD/Pharos")
        service.ejecting = true
        controller.refresh()
        precondition(!finder.isEnabled, "Do not reveal a library while ejecting it")
        revealed = nil
        NSApp.sendAction(finder.action!, to: finder.target, from: finder)
        precondition(revealed == nil)
        service.ejecting = false
        controller.applyPreferences(["layoutVersion": 2, "order": ["quit", "tokens", "space", "open", "status", "settings", "words"]])
        let reordered = menu.items.filter { !$0.isHidden && !$0.isSeparatorItem }.map { String($0.title.split(separator: "\t")[0]) }
        precondition(reordered.first == "Quit Pharos")
        precondition(reordered.firstIndex(of: "Tokens used")! < reordered.firstIndex(of: "Library size")!)
        precondition(reordered.firstIndex(of: "Library size")! < reordered.firstIndex(of: "Open Pharos")!)
        precondition(reordered.last == "Words typed")
        precondition(defaults.dictionary(forKey: "menuBarPreferences")?["order"] as? [String] == ["quit", "tokens", "space", "open", "status", "settings", "words"])
        precondition(menu.items.filter { $0.isSeparatorItem }.isEmpty, "Explicit layouts must not gain automatic dividers")
        controller.applyPreferences(["layoutVersion": 2, "order": ["open", "separator:4", "status", "separator:1", "settings", "quit"]])
        precondition(menu.items.filter { $0.isSeparatorItem }.count == 2, "Render each saved divider")
        let openPosition = menu.items.firstIndex { $0.title == "Open Pharos" }!
        precondition(menu.items[openPosition + 1].isSeparatorItem)
        controller.applyPreferences(["layoutVersion": 2, "order": ["open", "status", "settings", "quit"]])
        precondition(menu.items.filter { $0.isSeparatorItem }.isEmpty, "All dividers can be removed")
        controller.applyPreferences(["order": ["eject", "open", "status", "settings", "quit"]])
        MenuFixture.ejectable = false
        controller.refresh()
        pump { !menu.items.contains { $0.title.hasPrefix("Eject ") } }
        precondition(menu.items.first?.isSeparatorItem == false, "Unavailable actions must not leave an empty section")
        MenuFixture.ejectable = true
        controller.applyPreferences(["enabled": false])
        precondition(defaults.dictionary(forKey: "menuBarPreferences")?["enabled"] as? Bool == false)
        controller.applyPreferences(MenuBarPreferences.defaults.merging(["cost": true, "backup": true, "space": true, "index": true, "eject": true]) { _, new in new })
        let reopenedMenu = Self.menu(controller)
        let dot = Mirror(reflecting: controller).children.first { $0.label == "dot" }!.value as! MenuBarDot
        MenuFixture.usageFailure = true
        controller.invalidateUsage()
        pump { reopenedMenu.items.contains { $0.title == "Usage unavailable" && !$0.isHidden } }
        precondition(reopenedMenu.items.contains { $0.title == "Pharos · Idle" })
        precondition(dot.color == .systemGreen)
        precondition(reopenedMenu.items.first { $0.title == "View error…" }!.isHidden)
        MenuFixture.activity = true
        controller.refresh()
        pump { reopenedMenu.items.contains { $0.title == "Pharos · Working" } }
        precondition(dot.color == .systemYellow)
        precondition(reopenedMenu.items.first { $0.title == "View error…" }!.isHidden)
        MenuFixture.activity = false
        controller.refresh()
        pump { reopenedMenu.items.contains { $0.title == "Pharos · Idle" } }
        controller.applyPreferences(["words": false, "tokens": false, "carbon": false, "cost": false])
        let usageRequests = MenuFixture.usageRequests
        controller.invalidateUsage()
        precondition(MenuFixture.usageRequests == usageRequests)
        precondition(dot.color == .systemGreen)
        precondition(!reopenedMenu.items.contains { $0.title == "Usage unavailable" && !$0.isHidden })
        precondition(reopenedMenu.items.first { $0.title == "View error…" }!.isHidden)
        MenuFixture.usageFailure = false
        controller.applyPreferences(MenuBarPreferences.defaults.merging(["cost": true, "backup": true, "space": true, "index": true, "eject": true]) { _, new in new })
        controller.invalidateUsage()
        pump { reopenedMenu.items.contains { $0.attributedTitle?.string.contains("3,240") == true } }
        MenuFixture.activity = true
        controller.refresh()
        pump { reopenedMenu.items.contains { $0.title == "Pharos · Working" } }
        precondition(!reopenedMenu.items.first { $0.title == "Index changes now" }!.isEnabled)
        MenuFixture.failure = "Source permission denied"
        controller.refresh()
        pump { reopenedMenu.items.contains { $0.title == "Pharos · Needs attention" } }
        precondition(!reopenedMenu.items.first { $0.title == "View error…" }!.isHidden)
        MenuFixture.activity = false; MenuFixture.failure = ""
        controller.refresh()
        pump { reopenedMenu.items.contains { $0.title == "Pharos · Idle" } }
        var opened = false
        controller.openWindow = { opened = true }
        controller.showWindow(path: "/settings/preferences")
        precondition(opened, "Menu must reopen a closed main window")
        // SwiftUI can retain an NSWindow after closing its scene. Opening
        // that stale window directly can leave it without its web content.
        let closedWindow = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 200, height: 200), styleMask: .titled, backing: .buffered, defer: false)
        closedWindow.isReleasedWhenClosed = false
        closedWindow.close()
        controller.window = closedWindow
        opened = false
        controller.showWindow()
        precondition(opened, "A retained closed window must reopen through SwiftUI")
        controller.window = nil
        let index = reopenedMenu.items.first { $0.title == "Index changes now" }!
        NSApp.sendAction(index.action!, to: index.target, from: index)
        pump { MenuFixture.indexRequested }
        let eject = reopenedMenu.items.first { $0.title.hasPrefix("Eject Archive") }!
        NSApp.sendAction(eject.action!, to: eject.target, from: eject)
        precondition(service.ejectRequested, "Eject must use the existing release flow")
        pump { index.isEnabled }
        for action in [index, eject] {
            MenuFixture.statusFailure = true
            MenuFixture.indexFailure = true
            service.ejectFailure = "Library is busy; cannot eject."
            NSApp.sendAction(action.action!, to: action.target, from: action)
            pump { Mirror(reflecting: controller).children.first { $0.label == "actionError" }!.value as? String != nil }
            precondition(dot.color == .systemRed)
            controller.applyPreferences(["index": false, "eject": false])
            MenuFixture.statusFailure = false
            pump {
                controller.refresh()
                return reopenedMenu.items.contains { $0.title == "Pharos · Idle" }
            }
            precondition(dot.color == .systemGreen)
            precondition(reopenedMenu.items.first { $0.title == "View error…" }!.isHidden)
            precondition(index.isHidden && eject.isHidden)
            MenuFixture.indexFailure = false
            service.ejectFailure = nil
            controller.applyPreferences(MenuBarPreferences.defaults.merging(["cost": true, "backup": true, "space": true, "index": true, "eject": true]) { _, new in new })
        }
        precondition(MenuBarPreferences(["enabled": 0])["enabled"], "Numbers are not booleans")
        precondition(MenuBarController.mass(1234) == "1.23 kg")
        for (input, expected) in [(999.0, "999"), (999.7, "1k"), (999400.0, "999k"), (999700.0, "1M"), (999700000.0, "1B"), (999700000000.0, "1T")] {
            precondition(MenuBarController.compact(input) == expected)
        }
        for (input, expected) in [(999.4, "999 g"), (999.7, "1 kg"), (999700.0, "1 t"), (999700000.0, "1000 t"), (0.0000001, "0.0000001 g")] {
            precondition(MenuBarController.mass(input) == expected)
        }
        precondition(MenuBarController.date("2026-10-13T10:40:00.123Z") != nil)
        if CommandLine.arguments.count > 1 {
            let image = MenuBarLighthouse.image()
            let data = NSBitmapImageRep(data: image.tiffRepresentation!)!.representation(using: .png, properties: [:])!
            try! data.write(to: URL(fileURLWithPath: CommandLine.arguments[1]))
        }
        print("Native menu: custom order, migration, visibility, status precedence/recovery, closed window reopening, index/eject actions, template icon, wrapping and trailing value alignment passed")
    }
}
