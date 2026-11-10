// Update coordination tests with disposable children and stubbed HTTP.
// No production service, disk images, Keychain or network.
import AppKit
import Foundation
import Darwin
import CryptoKit
import Sparkle
import WebKit

/// Stub HTTP readiness/release independently of the owned subprocess. This
/// reproduces startup with a live child whose listener is not accepting yet.
final class StartupResponse: URLProtocol {
    static var ready = false
    static var busy = false
    override class func canInit(with request: URLRequest) -> Bool { request.url?.host == "startup-test.invalid" }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        if (request.url?.path == "/api/ready" && !Self.ready) || (request.url?.path == "/api/release" && !Self.busy) {
            client?.urlProtocol(self, didFailWithError: URLError(.cannotConnectToHost))
            return
        }
        let status = request.url?.path == "/api/release" ? 503 : 200
        let response = HTTPURLResponse(url: request.url!, statusCode: status, httpVersion: "HTTP/1.1", headerFields: nil)!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: Data("{}".utf8))
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() {}
}

/// Exercise resumed install cancellation without launching Sparkle's helper,
/// checking a feed, or signing an app. The actual user driver supplies the reply.
@MainActor final class StagedUpdater: SPUUpdater {
    var active = false
    var checks = 0
    var resume: ((StagedUpdater) -> Void)?
    override var sessionInProgress: Bool { active }
    override func checkForUpdates() {
        checks += 1
        active = true
        resume?(self)
    }
}

@MainActor func until(_ message: String, timeout: TimeInterval = 5, _ condition: () -> Bool) {
    let deadline = Date().addingTimeInterval(timeout)
    while !condition() {
        if Date() >= deadline { fatalError(message) }
        RunLoop.current.run(mode: .default, before: deadline)
    }
}

func expect(_ condition: @autoclosure () -> Bool, _ message: String) {
    if !condition() { fatalError(message) }
}
func refuses(_ work: () throws -> Void, _ message: String) {
    do { try work(); fatalError(message) } catch { }
}

/// Records WebKit's native sender metadata and forwards the real messages to
/// the production entry points. The pages use an injected picker and request no
/// successful clipboard write, update installation, eject or real window movement.
@MainActor final class BridgeMessages: NSObject, WKScriptMessageHandler, WKScriptMessageHandlerWithReply {
    let coordinator: ArchiveWebView.Coordinator
    var results: [Bool: [String: String]] = [:]
    var senders: [(String, URL?, Bool)] = []
    var replies: [Bool: Int] = [:]

    init(_ coordinator: ArchiveWebView.Coordinator) { self.coordinator = coordinator }

    func userContentController(_ controller: WKUserContentController, didReceive message: WKScriptMessage) {
        if message.name == "bridgeTestResults" {
            results[message.frameInfo.isMainFrame] = message.body as? [String: String]
            return
        }
        senders.append((message.name, message.frameInfo.request.url, message.frameInfo.isMainFrame))
        coordinator.userContentController(controller, didReceive: message)
    }

    func userContentController(
        _ controller: WKUserContentController, didReceive message: WKScriptMessage,
        replyHandler: @escaping (Any?, String?) -> Void
    ) {
        let mainFrame = message.frameInfo.isMainFrame
        senders.append((message.name, message.frameInfo.request.url, mainFrame))
        coordinator.userContentController(controller, didReceive: message) { value, error in
            self.replies[mainFrame, default: 0] += 1
            replyHandler(value, error)
        }
    }
}

/// A retained completion can fire synchronously during cancellation or arrive late.
final class FakeFolderPicker: ArchiveFolderPicker {
    var completion: ((String?) -> Void)?
    var cancellations = 0
    var onBegin: (() -> Void)?
    func begin(_ completion: @escaping (String?) -> Void) {
        self.completion = completion
        onBegin?()
    }
    func cancel() {
        cancellations += 1
        completion?(nil)
    }
}

@main struct UpdateCheckHarness {
    static func nativeSenderTests() {
        let origin = URL(string: "http://localhost:18765/")!
        let cases: [(String, URL?, Bool, Bool)] = [
            ("exact origin", origin, true, true),
            ("path/query/fragment", URL(string: "http://localhost:18765/library?q=test#row"), true, true),
            ("different port", URL(string: "http://localhost:18766/"), true, false),
            ("missing explicit port", URL(string: "http://localhost/"), true, false),
            ("invalid port", URL(string: "http://localhost:65536/"), true, false),
            ("different hostname", URL(string: "http://127.0.0.1:18765/"), true, false),
            ("different scheme", URL(string: "https://localhost:18765/"), true, false),
            ("missing URL", nil, true, false),
            ("invalid URL", URL(string: "http://[invalid"), true, false),
            ("relative URL", URL(string: "/library"), true, false),
            ("missing host", URL(string: "http:/library"), true, false),
            ("same-origin subframe", origin, false, false),
            ("about document", URL(string: "about:blank"), true, false),
            ("blob document", URL(string: "blob:http://localhost:18765/id"), true, false),
            ("data document", URL(string: "data:text/html,hello"), true, false),
            ("file document", URL(fileURLWithPath: "/tmp/bridge-test.html"), true, false),
        ]
        for (label, sender, mainFrame, allowed) in cases {
            expect(NativeBridgeSender.allows(expectedOrigin: origin, senderURL: sender, isMainFrame: mainFrame) == allowed, label)
            for handler in NativeBridgeSender.Handler.allCases {
                for replying in [false, true] {
                    var actions: [NativeBridgeSender.Handler] = []
                    var errors: [String] = []
                    NativeBridgeSender.dispatch(
                        name: handler.rawValue, expectedOrigin: origin, senderURL: sender, isMainFrame: mainFrame,
                        replying: replying, reject: { errors.append($0) }, receive: { actions.append($0) }
                    )
                    let dispatched = allowed && replying == handler.replies
                    expect(actions == (dispatched ? [handler] : []), "\(label): only the registered delivery reaches \(handler)")
                    expect(errors.count == (dispatched ? 0 : 1) && errors.allSatisfy { !$0.isEmpty }, "\(label): a rejection finishes once with a clear error")
                }
            }
        }
        for url in ["about:blank", "blob:http://localhost:18765/id", "data:text/html,hello", "file:///tmp/page"] {
            let special = URL(string: url)!
            expect(!NativeBridgeSender.allows(expectedOrigin: special, senderURL: special, isMainFrame: true), "non-application schemes cannot authorize themselves")
        }
        let invalidPort = URL(string: "http://localhost:65536/")!
        expect(!NativeBridgeSender.allows(expectedOrigin: invalidPort, senderURL: invalidPort, isMainFrame: true), "an invalid application port cannot authorize itself")
        let https = URL(string: "https://localhost:18765/")!
        expect(NativeBridgeSender.allows(expectedOrigin: https, senderURL: https, isMainFrame: true), "an HTTPS application origin is allowed")
        let defaultPort = URL(string: "http://localhost/")!
        expect(!NativeBridgeSender.allows(expectedOrigin: defaultPort, senderURL: URL(string: "http://localhost:80/"), isMainFrame: true), "an explicit default port remains distinct")
        for replying in [false, true] {
            var errors = 0
            NativeBridgeSender.dispatch(name: "unknown", expectedOrigin: origin, senderURL: origin, isMainFrame: true, replying: replying, reject: { _ in errors += 1 }) { _ in
                fatalError("an unknown name reached native effects")
            }
            expect(errors == 1, "unknown handlers are rejected once")
        }
        // Deferred actions retain their reply after dispatch returns. Rejected
        // folder/eject calls never acquire a callback or begin a native action.
        for mainFrame in [false, true] {
            for action in ["chooseFolder", "eject"] {
                var completion: (() -> Void)?
                var replies = 0
                NativeBridgeSender.dispatch(
                    name: "pharosLibrary", expectedOrigin: origin, senderURL: origin, isMainFrame: mainFrame,
                    replying: true, reject: { _ in replies += 1 }
                ) { handler in
                    expect(handler == .library, "deferred \(action) routes to the library")
                    completion = { replies += 1 }
                }
                expect((completion != nil) == mainFrame, "only an allowed \(action) begins")
                expect(replies == (mainFrame ? 0 : 1), "rejected \(action) replies immediately")
                completion?()
                expect(replies == 1, "\(action) completes exactly once")
            }
        }
        print("Native sender predicate and recorded dispatch checks passed")
    }

    @MainActor static func nativeFolderPickerTests() {
        let origin = URL(string: "http://localhost:18765/")!
        for selected in ["/tmp/chosen", nil] as [String?] {
            var pickers: [FakeFolderPicker] = []
            var purposes: [String] = []
            let coordinator = ArchiveWebView.Coordinator(origin: origin) { purpose in
                let picker = FakeFolderPicker()
                pickers.append(picker)
                purposes.append(purpose)
                return picker
            }
            var replies: [(String?, String?)] = []
            coordinator.library(["action": "chooseFolder", "purpose": "source"]) { value, error in
                replies.append((value as? String, error))
            }
            var refused: [(Any?, String?)] = []
            coordinator.library(["action": "chooseFolder", "purpose": "export"]) { refused.append(($0, $1)) }
            expect(pickers.count == 1 && purposes == ["source"], "concurrent requests must not replace the first picker")
            expect(refused.count == 1 && refused[0].0 == nil && refused[0].1?.isEmpty == false, "the concurrent caller gets its own clear error")
            expect(replies.isEmpty, "refusing another request must leave the first pending")
            pickers[0].completion?(selected)
            pickers[0].completion?("/tmp/duplicate")
            expect(replies.count == 1 && replies[0].0 == selected && replies[0].1 == nil, "selection and user cancellation each reply once with the existing payload")
            expect(pickers[0].cancellations == 0, "a completed picker needs no cancellation")
            // Completion clears ownership before user code can start the next request.
            coordinator.library(["action": "chooseFolder", "purpose": "export"]) { _, _ in
                coordinator.library(["action": "chooseFolder"]) { _, _ in }
            }
            pickers[1].completion?(nil)
            expect(purposes == ["source", "export", "library"], "all purposes pass through, including the default and a reentrant request")
            coordinator.cancelFolderPicker()
        }
        for event in ["navigation", "termination", "dismantle"] {
            let page = ArchiveWKWebView(frame: .zero, configuration: WKWebViewConfiguration())
            var pickers: [FakeFolderPicker] = []
            let coordinator = ArchiveWebView.Coordinator(origin: origin) { _ in
                let picker = FakeFolderPicker()
                pickers.append(picker)
                return picker
            }
            var oldReplies = 0, freshReplies = 0
            coordinator.library(["action": "chooseFolder"]) { value, error in
                oldReplies += 1
                expect(value == nil && error == nil, "\(event) replies as cancellation")
                // Invalidation clears ownership before invoking the original reply.
                coordinator.library(["action": "chooseFolder"]) { value, error in
                    freshReplies += 1
                    expect(value as? String == "/tmp/fresh" && error == nil, "the fresh request retains its own result")
                }
            }
            switch event {
            case "navigation": coordinator.webView(page, didStartProvisionalNavigation: nil)
            case "termination": coordinator.webViewWebContentProcessDidTerminate(page)
            default: ArchiveWebView.dismantleNSView(page, coordinator: coordinator)
            }
            expect(oldReplies == 1 && pickers[0].cancellations == 1, "\(event) dismisses and settles the owned picker once")
            expect(pickers.count == 2 && freshReplies == 0, "\(event) admits a fresh request after invalidation")
            pickers[0].completion?("/tmp/obsolete")
            pickers[0].completion?(nil)
            expect(oldReplies == 1 && freshReplies == 0, "late callbacks must neither duplicate the old reply nor finish the new request")
            pickers[1].completion?("/tmp/fresh")
            coordinator.cancelFolderPicker()
            expect(freshReplies == 1 && pickers[1].cancellations == 0, "the fresh picker completes normally")
        }
        print("Owned native folder picker lifecycle checks passed")
    }

    @MainActor static func nativeWebKitSenderTests(root: URL) throws {
        _ = NSApplication.shared
        let pages = root.appendingPathComponent("bridge-pages")
        try FileManager.default.createDirectory(at: pages, withIntermediateDirectories: true)
        let portFile = pages.appendingPathComponent("port")
        // Serve only disposable fixtures on an OS-assigned loopback port.
        let server = Process()
        server.executableURL = URL(fileURLWithPath: "/usr/bin/python3")
        server.arguments = ["-c", """
        import http.server, pathlib, signal, sys
        signal.alarm(45)
        class Handler(http.server.SimpleHTTPRequestHandler):
            def __init__(self, *args, **kwargs):
                super().__init__(*args, directory=sys.argv[1], **kwargs)
            def log_message(self, *args): pass
        server = http.server.HTTPServer(('127.0.0.1', 0), Handler)
        pathlib.Path(sys.argv[2]).write_text(str(server.server_port))
        server.serve_forever()
        """, pages.path, portFile.path]
        try server.run()
        defer { if server.isRunning { server.terminate() }; server.waitUntilExit() }
        until("native bridge fixture server did not start") { FileManager.default.fileExists(atPath: portFile.path) || !server.isRunning }
        let port = try String(contentsOf: portFile, encoding: .utf8)
        let origin = URL(string: "http://127.0.0.1:\(port)/")!
        let script = """
        <script>
        (async () => {
          const main = window === window.top, handlers = window.webkit.messageHandlers;
          const result = {};
          const post = async (name, body, key = name) => {
            try { await handlers[name].postMessage(body); result[key] = 'ok'; }
            catch (error) { result[key] = String(error); }
          };
          handlers.pharosWindow.postMessage({contextTarget: {reference: main ? 'main' : 'iframe'}, contextFile: {path: main ? '/tmp/main' : '/tmp/iframe'}, pageActions: main});
          handlers.pharosWindow.postMessage({headerCenter: main ? 26 : 200});
          handlers.pharosWindow.postMessage({background: main ? 'rgb(10, 20, 30)' : 'rgb(200, 200, 200)'});
          handlers.pharosMenuBar.postMessage(null);
          await post('pharosUpdates', {action: 'status'});
          await post('pharosLibrary', {action: 'invalid'});
          await post('pharosLibrary', {action: 'chooseFolder', purpose: 'source'}, 'chooseFolder');
          // Invalid payloads keep this integration fixture harmless even if
          // sender validation regresses; iframe errors must precede parsing.
          await post('pharosClipboard', {});
          await post('pharosDictation', {action: 'invalid'});
          if (!main) {
            handlers.pharosWindow.postMessage('drag');
            handlers.pharosWindow.postMessage('doubleClick');
          }
          handlers.bridgeTestResults.postMessage(result);
        })();
        </script>
        """
        try ("<!doctype html>" + script + "<iframe src='/frame.html'></iframe>").write(to: pages.appendingPathComponent("index.html"), atomically: true, encoding: .utf8)
        try ("<!doctype html>" + script).write(to: pages.appendingPathComponent("frame.html"), atomically: true, encoding: .utf8)
        var pickerCount = 0
        let coordinator = ArchiveWebView.Coordinator(origin: origin) { purpose in
            expect(purpose == "source", "the allowed main-document picker preserves its purpose")
            pickerCount += 1
            let picker = FakeFolderPicker()
            picker.onBegin = { [weak picker] in picker?.completion?(nil) }
            return picker
        }
        let updates = UpdateNotice()
        coordinator.updates = updates
        let recorder = BridgeMessages(coordinator)
        let configuration = WKWebViewConfiguration()
        configuration.websiteDataStore = .nonPersistent()
        let controller = configuration.userContentController
        for handler in NativeBridgeSender.Handler.allCases {
            if handler.replies {
                controller.addScriptMessageHandler(recorder, contentWorld: .page, name: handler.rawValue)
            } else {
                controller.add(recorder, name: handler.rawValue)
            }
        }
        controller.add(recorder, name: "bridgeTestResults")
        defer {
            for handler in NativeBridgeSender.Handler.allCases { controller.removeScriptMessageHandler(forName: handler.rawValue) }
            controller.removeScriptMessageHandler(forName: "bridgeTestResults")
        }
        let page = ArchiveWKWebView(frame: NSRect(x: 0, y: 0, width: 640, height: 480), configuration: configuration)
        coordinator.webView = page
        page.load(URLRequest(url: origin))
        until("real WKWebView main/iframe bridge messages did not complete", timeout: 20) { recorder.results.count == 2 }
        expect(recorder.results[true]?["pharosUpdates"] == "ok", "the main document reaches update status")
        expect(recorder.results[true]?["pharosLibrary"] == "Error: Unknown library action.", "the main document keeps library payload validation")
        expect(recorder.results[true]?["pharosClipboard"] == "Error: Clipboard content must be text.", "the main document keeps clipboard payload validation without writing")
        expect(recorder.results[true]?["pharosDictation"] == "Error: Unknown dictation action.", "the main document keeps dictation payload validation without recording")
        for error in recorder.results[false]!.values {
            expect(error.contains("main window document"), "every iframe reply reports a native sender rejection: \(error)")
        }
        expect(recorder.results[true]?["chooseFolder"] == "ok" && pickerCount == 1, "only the real main-document sender creates a picker")
        expect(recorder.replies[true] == 5 && recorder.replies[false] == 5, "each real WebKit reply finishes once")
        for mainFrame in [true, false] {
            let senders = recorder.senders.filter { $0.2 == mainFrame }
            expect(Set(senders.map { $0.0 }) == Set(NativeBridgeSender.Handler.allCases.map(\.rawValue)), "both frames exercise every registered name")
            expect(senders.allSatisfy { $0.1?.host == origin.host && $0.1?.port == origin.port }, "sender URLs are extracted from actual same-origin WebKit requests")
        }
        expect(page.contextTarget?.reference == "main" && page.contextFile?.url.path == "/tmp/main" && page.pageActions, "iframe messages cannot change main-document context state")
        expect(page.underPageBackgroundColor == ArchiveWebView.Coordinator.color(css: "rgb(10, 20, 30)"), "iframe messages cannot change the page background")

        func evaluate(_ script: String) {
            var finished = false
            page.evaluateJavaScript(script + "\n; true;") { _, error in
                expect(error == nil, "context menu fixture script succeeds: \(String(describing: error))")
                finished = true
            }
            until("context menu fixture script did not finish") { finished }
        }
        evaluate("""
        document.body.innerHTML = '<header><div class="brand"><svg><path id="mark"></path></svg></div><nav class="tabs"><button data-view="home"><span id="home">Home</span></button><button id="other">Search</button></nav></header>';
        window.loadingIndicator = true;
        window.pharosLighthouseLoadingIndicator = () => window.loadingIndicator;
        window.pharosPageAction = action => { if (action === 'lighthouse-loading') window.loadingIndicator = !window.loadingIndicator; };
        """)
        evaluate(ArchiveWebView.windowChromeScript)
        func rightClick(_ selector: String) {
            let count = recorder.senders.count
            evaluate("document.querySelector('\(selector)').dispatchEvent(new MouseEvent('contextmenu', {bubbles:true, button:2}))")
            until("context menu target was not reported") { recorder.senders.count > count }
        }
        let event = NSEvent.mouseEvent(with: .rightMouseDown, location: .zero, modifierFlags: [], timestamp: 0, windowNumber: 0, context: nil, eventNumber: 0, clickCount: 1, pressure: 0)!
        for selector in ["#mark", "#home"] {
            rightClick(selector)
            expect(page.contextLighthouseLoading == true, "\(selector) reports the enabled loading indicator")
            let menu = NSMenu()
            page.willOpenMenu(menu, with: event)
            guard let item = menu.item(withTitle: "Lighthouse Loading Indicator") else { fatalError("the Lighthouse menu item is missing") }
            expect(item.state == .on && item.isEnabled, "the Lighthouse menu item is checked and enabled")
            _ = NSApp.sendAction(item.action!, to: item.target, from: item)
            evaluate("void 0")
            rightClick(selector)
            expect(page.contextLighthouseLoading == false, "the Lighthouse action updates the next menu")
            let disabledMenu = NSMenu()
            page.willOpenMenu(disabledMenu, with: event)
            let disabledItem = disabledMenu.item(withTitle: "Lighthouse Loading Indicator")!
            expect(disabledItem.state == .off, "the menu shows the disabled indicator")
            _ = NSApp.sendAction(disabledItem.action!, to: disabledItem.target, from: disabledItem)
            evaluate("void 0")
        }
        rightClick("#other")
        expect(page.contextLighthouseLoading == nil, "other navigation items do not offer the Lighthouse setting")
        let otherMenu = NSMenu()
        page.willOpenMenu(otherMenu, with: event)
        expect(otherMenu.item(withTitle: "Lighthouse Loading Indicator") == nil, "the Lighthouse menu item is limited to its targets")
        page.didCloseMenu(otherMenu, with: event)
        evaluate("window.attention = true; window.pharosView = {hasAttention: () => window.attention, clearAttention: () => { window.attention = false; }}")
        rightClick("#other")
        expect(page.contextHasAttention, "page reports attention even when the clicked element has no target")
        let attentionMenu = NSMenu()
        page.willOpenMenu(attentionMenu, with: event)
        guard let clear = attentionMenu.item(withTitle: "Clear Highlights"), let action = clear.action else { fatalError("the clear highlights action is missing") }
        _ = NSApp.sendAction(action, to: clear.target, from: clear)
        evaluate("void 0")
        rightClick("#other")
        expect(!page.contextHasAttention, "the clear highlights action updates the page")
        let clearedMenu = NSMenu()
        page.willOpenMenu(clearedMenu, with: event)
        expect(clearedMenu.item(withTitle: "Clear Highlights") == nil, "clear highlights is offered only when attention exists")
        page.didCloseMenu(clearedMenu, with: event)
        page.stopLoading()
        print("Real WKWebView main-document and same-origin iframe sender checks passed")
    }

    @MainActor static func preparedHandoffFailureTests(service: ArchiveService, updater: SPUUpdater, item: SUAppcastItem) throws {
        let lease = AppUpdateLock.current()
        let marker = lease.base.appendingPathExtension("pending")
        let lockPath = lease.base.appendingPathExtension("lock").path
        for abortFirst in [true, false] {
            var userDriver: UpdateUserDriver!
            let updates = UpdateNotice { driver, delegate in
                userDriver = driver
                return StagedUpdater(hostBundle: .main, applicationBundle: .main, userDriver: driver, delegate: delegate)
            }
            updates.prepareInstallation = { completion in
                service.prepareToTerminate(installing: true, completion: completion)
            }
            var recoveries = 0
            updates.cancelInstallationPreparation = {
                recoveries += 1
                service.cancelUpdatePreparation()
            }
            updates.check() // Creates the injected updater without network or helpers.
            updates.updater(updater, willInstallUpdate: item)
            var prepared = false
            userDriver.prepareInstallation? { error in
                expect(error == nil, "the installation handoff must succeed before testing a Sparkle failure")
                prepared = true
            }
            until("installation handoff did not complete") { prepared }
            expect(updates.installationPrepared && updates.installationPending, "successful handoff arms Sparkle termination")
            expect(FileManager.default.fileExists(atPath: marker.path), "successful handoff retains its pending marker")
            let blocked = open(lockPath, O_RDONLY)
            expect(blocked >= 0 && flock(blocked, LOCK_SH | LOCK_NB) != 0, "successful handoff excludes CLI connections")
            close(blocked)
            updates.updater(updater, didFinishUpdateCycleFor: .updates, error: nil)
            expect(updates.installationPrepared, "a successful cycle must preserve the installation handoff")

            let failure = UpdateFailure(message: "installer XPC launch failed")
            if abortFirst { updates.updater(updater, didAbortWithError: failure) }
            updates.updater(updater, didFinishUpdateCycleFor: .updates, error: failure)
            updates.updater(updater, didAbortWithError: failure)
            expect(recoveries == 1, "abort and cycle completion unwind the handoff exactly once")
            expect(!updates.installationPrepared && !updates.installationPending, "a failed install cannot bypass ordinary Quit")
            expect(!FileManager.default.fileExists(atPath: marker.path), "failed install clears its marker immediately")
            let reconnected = open(lockPath, O_RDONLY)
            expect(reconnected >= 0 && flock(reconnected, LOCK_SH | LOCK_NB) == 0, "failed install releases the exclusive CLI lease")
            close(reconnected)
            try lease.recover(version: Bundle.main.object(forInfoDictionaryKey: "CFBundleVersion") as? String ?? "")
            expect(service.url == nil && service.error?.contains("The service has stopped.") == true, "a stopped service shows recovery instructions instead of a dead loading screen")
            var deferralRequested = false
            service.deferUpdate = { completion in
                deferralRequested = true
                completion(UpdateFailure(message: "test eject cancellation"))
            }
            service.ejectLibrary { reason in
                expect(reason == "test eject cancellation", "the shutdown flag must be cleared after Sparkle fails")
            }
            expect(deferralRequested, "the recovered app can process Eject again")
        }
        service.deferUpdate = { $0(nil) }
    }

    @MainActor static func applicationLifecycleTests(root: URL) throws {
        _ = NSApplication.shared
        URLProtocol.registerClass(StartupResponse.self)
        defer { URLProtocol.unregisterClass(StartupResponse.self) }

        // The header shares Sparkle's reply, then carries the user's install
        // choice through download readiness without a second confirmation.
        let headerDriver = UpdateUserDriver(hostBundle: .main, delegate: nil)
        var nativeReply: ((SPUUserUpdateChoice) -> Void)?
        var downloadChoices: [SPUUserUpdateChoice] = []
        headerDriver.offerUpdate(version: "0.8.0", installing: false, reply: { downloadChoices.append($0) }, present: { nativeReply = $0 })
        expect(headerDriver.headerState.version == "0.8.0" && headerDriver.headerState.phase == "available", "an installable release is offered in the header")
        expect(headerDriver.installFromHeader(), "the header starts the verified download")
        expect(!headerDriver.installFromHeader(), "a second click cannot start a second download")
        nativeReply?(.install)
        expect(downloadChoices == [.install], "native and header controls share a single-use reply")
        var preparation: ((Error?) -> Void)?
        var installChoices: [SPUUserUpdateChoice] = []
        headerDriver.prepareInstallation = { preparation = $0 }
        headerDriver.showReady(toInstallAndRelaunch: { installChoices.append($0) })
        expect(preparation != nil && installChoices.isEmpty, "one-click installation waits for Pharos's process handoff")
        preparation?(nil)
        expect(installChoices == [.install], "readiness continues to install and relaunch after one click")
        headerDriver.dismissUpdateInstallation()
        expect(headerDriver.headerState.version.isEmpty && !headerDriver.installFromHeader(), "session completion removes the action")
        for deferred in [false, true] {
            headerDriver.deferring = false
            headerDriver.offerUpdate(version: "0.9.0", installing: true, reply: { installChoices.append($0) }, present: { nativeReply = $0 })
            headerDriver.deferring = deferred
            if deferred {
                expect(!headerDriver.installFromHeader(), "eject prevents a header installation")
                headerDriver.cancellation?()
            } else {
                expect(headerDriver.installFromHeader(), "a staged release also installs from the header")
                preparation?(UpdateFailure(message: "test writer still running"))
            }
            expect(installChoices.last == .skip && headerDriver.headerState.version.isEmpty, "failed preparation or eject safely cancels the action")
        }
        headerDriver.deferring = false
        headerDriver.offerUpdate(version: "", installing: false, reply: { _ in }, present: { nativeReply = $0 })
        expect(!headerDriver.installFromHeader(), "information-only releases cannot start an install")
        nativeReply?(.dismiss)

        // Exercise the actual update controller: dismissing a staged update
        // preserves install-on-quit, while Skip cancels without an error callback.
        let updates = UpdateNotice()
        let controller = SPUStandardUpdaterController(startingUpdater: false, updaterDelegate: nil, userDriverDelegate: nil)
        let item = SUAppcastItem(dictionary: ["sparkle:version": "0.7.2", "enclosure": ["url": "https://example.invalid/Pharos.dmg"]])!
        var staged: StagedUpdater!
        var cancelReceived = false
        let coordinated = UpdateNotice { driver, delegate in
            staged = StagedUpdater(hostBundle: .main, applicationBundle: .main, userDriver: driver, delegate: delegate)
            staged.resume = { session in
                // Resuming must reach the installing choice, rather than
                // cancelling the check and leaving the installer armed.
                driver.showUserInitiatedUpdateCheck(cancellation: { fatalError("cancelled resume before obtaining the installer reply") })
                driver.showReady(toInstallAndRelaunch: { choice in
                    expect(choice == .skip, "resumed install cancellation uses Sparkle's Skip reply")
                    cancelReceived = true
                    session.active = false
                    (delegate as! UpdateNotice).updater(session, didFinishUpdateCycleFor: .updates, error: nil)
                })
            }
            return staged
        }
        _ = coordinated.updater(controller.updater, willInstallUpdateOnQuit: item, immediateInstallationBlock: {})
        var deferred = false
        coordinated.deferInstallation { error in
            expect(error == nil && cancelReceived, "Quit/Eject waits for cancellation to finish")
            expect(!coordinated.installationPending, "confirmed cancellation clears pending state")
            deferred = true
        }
        until("staged installer deferral did not complete") { deferred }
        expect(staged.checks == 1, "the staged installer was resumed once")
        _ = coordinated.updater(controller.updater, willInstallUpdateOnQuit: item, immediateInstallationBlock: {})
        staged.resume = { session in
            session.active = false
            coordinated.updater(session, didFinishUpdateCycleFor: .updates, error: UpdateFailure(message: "installer unavailable"))
        }
        deferred = false
        coordinated.deferInstallation { error in
            expect(error != nil && coordinated.installationPending, "an unconfirmed cancellation cannot silently permit replacement during eject")
            deferred = true
        }
        until("failed cancellation did not report its error") { deferred }
        _ = updates.updater(controller.updater, willInstallUpdateOnQuit: item, immediateInstallationBlock: {})
        expect(updates.installationPending, "automatic staging requires an installation handoff")
        updates.userDidChooseUpdate(.dismiss)
        expect(updates.installationPending, "Later preserves the staged installation")
        updates.userDidChooseUpdate(.skip)
        expect(!updates.installationPending, "Skip cancels the pending installation without an error")
        let driver = UpdateUserDriver(hostBundle: .main, delegate: nil)
        driver.deferring = true
        var cancelledInstallation = false
        driver.showReady(toInstallAndRelaunch: { choice in
            expect(choice == .skip, "deferring must cancel the installer, not dismiss its window")
            cancelledInstallation = true
        })
        expect(cancelledInstallation, "a ready installer receives its cancellation reply")
        var cancelledDownload = false
        driver.showDownloadInitiated(cancellation: { cancelledDownload = true })
        expect(cancelledDownload, "eject also cancels an update download")
        let unstarted = ArchiveService(plan: .library(root.appendingPathComponent("no-library")))
        var cancelBeforeEject: ((Error?) -> Void)?
        unstarted.deferUpdate = { cancelBeforeEject = $0 }
        var ejectReplied = false
        unstarted.ejectLibrary { reason in
            expect(reason == "installer unavailable", "Eject reports cancellation failures before touching the drive")
            ejectReplied = true
        }
        expect(cancelBeforeEject != nil && !ejectReplied && !unstarted.ejecting, "Eject waits for updater cancellation")
        cancelBeforeEject?(UpdateFailure(message: "installer unavailable"))
        expect(ejectReplied && !unstarted.ejecting, "a cancellation failure leaves the drive mounted")
        var quitFinished = false
        unstarted.prepareToTerminate(installing: updates.installationPending) { error in
            expect(error == nil, "quitting after Skip needs no update handoff")
            quitFinished = true
        }
        until("quit after Skip did not finish") { quitFinished }
        let lease = AppUpdateLock.current()
        expect(!FileManager.default.fileExists(atPath: lease.base.appendingPathExtension("pending").path), "Skip must not leave a marker that delays reopening")
        try lease.recover(version: "0.7.2")

        // A service stand-in reports its PID once signal handling is installed.
        // It never opens a catalog or socket; HTTP is stubbed above.
        for refusesTermination in [false, true] {
            let directory = root.appendingPathComponent(refusesTermination ? "stubborn" : "starting")
            try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
            let pidFile = directory.appendingPathComponent("pid")
            let signalFile = directory.appendingPathComponent("signalled")
            let executable = directory.appendingPathComponent("service")
            let script = """
            #!/usr/bin/python3
            import os, signal
            from pathlib import Path
            if \(refusesTermination ? "True" : "False"):
                signal.signal(signal.SIGTERM, lambda *_: Path(\(String(reflecting: signalFile.path))).touch())
            signal.alarm(30)
            Path(\(String(reflecting: pidFile.path))).write_text(str(os.getpid()))
            while True:
                signal.pause()
            """
            try script.write(to: executable, atomically: true, encoding: .utf8)
            try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: executable.path)
            try """
            api_token = "test"
            host = "startup-test.invalid"
            port = 18765
            executable = "\(executable.path)"
            """.write(to: directory.appendingPathComponent("library.toml"), atomically: true, encoding: .utf8)
            StartupResponse.ready = false
            StartupResponse.busy = false
            var workBusy = false
            let refusal = "The update waits for test work, which cannot stop in time."
            let service = ArchiveService(plan: .library(directory), releaseService: { _, _, forUpdate in
                if forUpdate && workBusy { return .stopping(#"{"released":false,"error":"\#(refusal)"}"#) }
                return StartupResponse.busy ? .stopping("test write in progress") : .notRunning
            })
            until("startup stand-in did not start") { FileManager.default.fileExists(atPath: pidFile.path) }
            let pid = Int32(try String(contentsOf: pidFile, encoding: .utf8))!
            defer { if refusesTermination { _ = kill(pid, SIGKILL) } } // Only this disposable test child.
            expect(service.url == nil, "Quit is requested before readiness")
            var finished = false
            if !refusesTermination {
                try preparedHandoffFailureTests(service: service, updater: controller.updater, item: item)
            }
            service.prepareToTerminate(installing: refusesTermination, timeout: refusesTermination ? 0.1 : 2) { error in
                expect((error != nil) == refusesTermination, "ordinary quit succeeds; installation waits for the child (stubborn: \(refusesTermination), error: \(String(describing: error)))")
                finished = true
            }
            until("startup quit did not finish") { finished }
            if refusesTermination {
                expect(FileManager.default.fileExists(atPath: signalFile.path), "an unopened listener must still signal its owned child")
                StartupResponse.ready = true
                until("readiness polling did not resume after cancelled quit") { service.url != nil }
                expect(service.error == nil, "cancelled startup quit can recover to a ready service")
                // Broker readiness alone cannot dismiss the startup beacon:
                // the page's initial request can still be opening its library.
                expect(service.pageLoading, "readiness must keep the beacon until a page loads")
                let page = ArchiveWKWebView(frame: .zero, configuration: WKWebViewConfiguration())
                let coordinator = ArchiveWebView.Coordinator(origin: service.url!)
                coordinator.service = service
                coordinator.webView(page, didFinish: nil)
                expect(!service.pageLoading, "a finished page dismisses the startup beacon")
                coordinator.webView(page, didStartProvisionalNavigation: nil)
                expect(!service.pageLoading, "later navigation preserves the visible page without a startup beacon")
                coordinator.webView(page, didFinish: nil)
                for error in [
                    NSError(domain: NSURLErrorDomain, code: NSURLErrorCancelled),
                    NSError(domain: "WebKitErrorDomain", code: 102),
                    NSError(domain: "WebKitErrorDomain", code: 204),
                ] {
                    for provisional in [true, false] {
                        // Each case starts at the initial navigation. A cancelled
                        // or diverted load need not have a replacement or finish.
                        service.pageLoading = true
                        coordinator.webView(page, didStartProvisionalNavigation: nil)
                        if provisional {
                            coordinator.webView(page, didFailProvisionalNavigation: nil, withError: error)
                        } else {
                            coordinator.webView(page, didFail: nil, withError: error)
                        }
                        expect(!service.pageLoading, "an ignored navigation error dismisses the startup beacon (\(error), provisional: \(provisional))")
                        expect(service.pageError == nil && !service.pageWaiting, "an ignored navigation keeps the working page available")
                        coordinator.webView(page, didStartProvisionalNavigation: nil)
                        expect(!service.pageLoading, "navigation after an ignored error does not rearm the startup beacon")
                    }
                }
                // A live broker whose routing probe is busy must enter the
                // waiting screen on the first timeout, then restore the exact
                // failed page once catalog initialization lets routing answer.
                StartupResponse.ready = false
                let target = URL(string: "/library?nav_collapsed=1", relativeTo: service.url!)!.absoluteURL
                coordinator.webView(page, didStartProvisionalNavigation: nil)
                coordinator.webView(page, didFailProvisionalNavigation: nil, withError: NSError(
                    domain: NSURLErrorDomain, code: NSURLErrorTimedOut,
                    userInfo: [NSURLErrorFailingURLErrorKey: target]
                ))
                until("busy routing did not enter page recovery") { service.pageWaiting }
                expect(service.pageError == nil, "catalog initialization must not become a terminal page error")
                StartupResponse.ready = true
                until("page recovery did not resume after catalog initialization") { !service.pageWaiting }
                expect(service.pageError == nil && service.takePageTarget() == target, "recovery preserves the failed route and its view state")
                // An explicit install asks first and stops nothing while
                // work cannot stop in time; it reports what it waits for.
                try FileManager.default.removeItem(at: signalFile)
                workBusy = true
                finished = false
                service.prepareToTerminate(installing: true, unlessBusy: true) { error in
                    expect(error?.localizedDescription == refusal, "an explicit install reports the work it waits for: \(String(describing: error))")
                    finished = true
                }
                until("refused explicit install did not finish") { finished }
                expect(!FileManager.default.fileExists(atPath: signalFile.path), "a refused explicit install must not signal the service")
                expect(service.url != nil && service.error == nil, "a refused explicit install keeps the ready service")
                workBusy = false
                StartupResponse.busy = true
                finished = false
                service.prepareToTerminate(installing: true) { error in
                    expect(error != nil, "busy release defers installation")
                    finished = true
                }
                until("busy shutdown did not finish") { finished }
                expect(service.url != nil, "cancelled quit preserves an already ready service")
                finished = false
                service.prepareToTerminate(installing: false) { error in
                    expect(error == nil, "ordinary Quit never vetoes logout for a busy or stubborn service")
                    finished = true
                }
                until("ordinary quit remained blocked by deferred installation") { finished }
            } else {
                until("ordinary Quit did not signal its owned startup child") { kill(pid, 0) != 0 }
            }
        }
    }

    @MainActor
    static func main() throws {
        // Disposable keys exist only in memory. Never use release credentials
        // or Keychain access to test signature verification on a development Mac.
        let key = Curve25519.Signing.PrivateKey()
        let message = Data("archive bytes".utf8)
        let signature = try key.signature(for: message)
        try UpdateSignature.verify(message, publicKey: key.publicKey.rawRepresentation, signature: signature)
        refuses({ try UpdateSignature.verify(Data("changed archive".utf8), publicKey: key.publicKey.rawRepresentation, signature: signature) }, "tampered archive accepted")
        let prefix = Data("<?xml version=\"1.0\"?><rss version=\"2.0\"><channel/></rss>".utf8)
        let signedPrefix = try key.signature(for: prefix).base64EncodedString()
        let footer = Data("<!-- sparkle-signatures:\nedSignature: \(signedPrefix)\nlength: \(prefix.count)\n-->".utf8)
        try UpdateSignature.verify(prefix + footer, publicKey: key.publicKey.rawRepresentation)
        refuses({ try UpdateSignature.verify(prefix + Data("<unsigned/>".utf8) + footer, publicKey: key.publicKey.rawRepresentation) }, "unsigned XML was accepted before the footer")
        // A fixture produced by the pinned upstream sign_update proves format
        // compatibility. Its unused test key was deleted, and is not trusted by
        // release configuration; only its public key is retained here.
        let fixture = try Data(contentsOf: URL(fileURLWithPath: CommandLine.arguments[1]))
        let fixtureKey = Data(base64Encoded: "4PwGZ3BsnsL55GDP4prYQ6P63fEaOrU76OTX4LPsseA=")!
        try UpdateSignature.verify(fixture, publicKey: fixtureKey)
        var changed = fixture
        changed[changed.startIndex + 50] ^= 1
        refuses({ try UpdateSignature.verify(changed, publicKey: fixtureKey) }, "tampered upstream signed feed was accepted")
        let root = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: root) }
        setenv("PHAROS_SUPPORT_DIR", root.appendingPathComponent("support").path, 1)
        nativeSenderTests()
        nativeFolderPickerTests()
        try nativeWebKitSenderTests(root: root)
        try applicationLifecycleTests(root: root)
        let app = root.appendingPathComponent("Library with spaces/Pharos.app")
        let cli = canonicalPath(app.appendingPathComponent("Contents/MacOS/pharos"))
        let support = root.appendingPathComponent("support")
        let lease = AppUpdateLock(app: app, support: support)
        try lease.begin(version: "0.7.2")
        let shared = open(lease.base.appendingPathExtension("lock").path, O_RDWR)
        expect(shared >= 0 && flock(shared, LOCK_SH | LOCK_NB) == 0, "running commands can finish before exclusivity")
        refuses({ try lease.finish() }, "installation must wait for CLI leases")
        close(shared)
        try lease.finish()
        let competitor = open(lease.base.appendingPathExtension("lock").path, O_RDWR)
        expect(flock(competitor, LOCK_SH | LOCK_NB) != 0, "installer excludes new CLI commands")
        close(competitor)
        lease.allowTermination(installing: true)
        expect(FileManager.default.fileExists(atPath: lease.base.appendingPathExtension("pending").path), "update marker survives application exit")
        // Simulate the old app exiting without removing its marker.
        let next = AppUpdateLock(app: app, support: support)
        refuses({ try next.recover(version: "0.7.3") }, "even a new version cannot bypass an exclusive installer")
        lease.cancel()
        try lease.begin(version: "0.7.2")
        // A different installed version wins even when the recorded PID is
        // reused by this very process. Recovery still requires exclusivity.
        try next.recover(version: "0.7.3", alive: { _, _ in true })
        lease.cancel()
        try lease.begin(version: "0.7.2")
        refuses({ try next.recover(version: "0.7.2", now: Date().addingTimeInterval(301), alive: { _, _ in true }) }, "a live owner of the same version remains protected")
        // No active CLI lease: a failed handoff is recoverable after five minutes.
        try next.recover(version: "0.7.2", now: Date().addingTimeInterval(301))
        // The test process has a reused PID but a different executable path.
        lease.cancel()
        try lease.begin(version: "0.7.2")
        lease.cancel()
        expect(!FileManager.default.fileExists(atPath: lease.base.appendingPathExtension("pending").path), "cancellation unblocks reconnects")

        let domain = "pharos-update-test-\(UUID().uuidString)"
        let defaults = UserDefaults(suiteName: domain)!
        defer { defaults.removePersistentDomain(forName: domain) }
        UpdatePreferences.migrate(defaults)
        expect(defaults.object(forKey: "SUEnableAutomaticChecks") == nil, "new installations retain Sparkle's permission prompt")
        defaults.removePersistentDomain(forName: domain)
        defaults.set(false, forKey: "checkForUpdatesOnLaunch")
        UpdatePreferences.migrate(defaults)
        expect(defaults.object(forKey: "SUEnableAutomaticChecks") as? Bool == false, "an explicit opt-out survives migration")
        defaults.set(true, forKey: "SUEnableAutomaticChecks")
        UpdatePreferences.migrate(defaults)
        expect(defaults.bool(forKey: "SUEnableAutomaticChecks"), "migration does not reset a later preference")

        var processes: [(pid: pid_t, path: String)] = [(901, cli), (902, cli), (903, "/another/Pharos.app/Contents/MacOS/pharos")]
        var stopped = false
        try UpdateProcesses.prepare(app: app, servicePID: 901, stopService: {
            stopped = true; processes.removeAll { $0.pid == 901 }
        }, processes: { processes }, arguments: { _ in ["pharos", "--config", "a path with spaces", "mcp"] }, signal: { pid in
            processes.removeAll { $0.pid == pid }
        }, wait: { $0() })
        expect(stopped && processes.map(\.pid) == [903], "service and own MCP stop; another installation stays running")
        processes = [(901, cli), (902, cli), (905, cli)]
        try UpdateProcesses.prepare(app: app, servicePID: 901, stopService: {
            processes.removeAll { [901, 902].contains($0.pid) }
        }, processes: { processes }, arguments: { pid in
            pid == 902 ? ["pharos", "serve", "--dynamic", "--application", app.path] : ["pharos", "mcp-app", "--application", app.path]
        }, parentPID: { $0 == 902 ? 901 : nil }, signal: { pid in processes.removeAll { $0.pid == pid } }, wait: { $0() })
        expect(processes.isEmpty, "update waits for broker-owned library services and managed MCP")
        processes = [(904, cli)]
        stopped = false
        refuses({ try UpdateProcesses.prepare(app: app, servicePID: nil, stopService: { stopped = true },
            processes: { processes }, arguments: { _ in ["pharos", "backup"] }, signal: { _ in fatalError("writer was signalled") }, wait: { $0() }) },
            "a writer must defer installation")
        expect(!stopped, "a writer prevents stopping the service in the first place")
        refuses({ try UpdateProcesses.prepare(app: app, servicePID: nil, stopService: {},
            processes: { processes }, arguments: { _ in ["pharos", "mcp"] }, signal: { _ in }, wait: { $0() }) },
            "an MCP process that does not exit must defer installation without SIGKILL")
        print("Update coordination harness passed")
    }
}
