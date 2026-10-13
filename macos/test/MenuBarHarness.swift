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
                "library": ["activities": Self.activity ? [["kind": "index", "label": "Indexing captures", "progress": 0.42]] : [], "drive": ["ejectable": true, "name": "Archive SSD"]],
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
        let indexed = visible.firstIndex { $0.hasPrefix("Indexed ") }!
        precondition(visible[indexed + 1] == "Index changes now")
        precondition(visible.suffix(2) == ["Eject Archive SSD…", "Quit Pharos"])
        let metrics = visible.filter { ["Words typed", "Tokens used", "Estimated CO₂e", "Estimated API cost", "Last backup", "Library size"].contains($0) }
        precondition(metrics == ["Words typed", "Tokens used", "Estimated API cost", "Estimated CO₂e", "Last backup", "Library size"])
        controller.applyPreferences(["words": false, "tokens": true, "carbon": true])
        precondition(menu.items.first { $0.title.hasPrefix("Words typed") }!.isHidden)
        precondition(!menu.items.first { $0.title.hasPrefix("Tokens used") }!.isHidden)
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
        precondition(reopenedMenu.items.first { $0.title == "Usage unavailable" }!.isHidden)
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
        print("Native menu: fixed order, visibility, status precedence/recovery, closed window reopening, index/eject actions, template icon and formatting passed")
    }
}
