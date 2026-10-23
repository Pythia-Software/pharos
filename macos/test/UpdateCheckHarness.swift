// Update coordination tests with disposable children and stubbed HTTP.
// No production service, disk images, Keychain or network.
import AppKit
import Foundation
import Darwin
import CryptoKit
import Sparkle

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

@main struct UpdateCheckHarness {
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
            let service = ArchiveService(plan: .library(directory), releaseService: { _, _ in
                StartupResponse.busy ? .stopping("test write in progress") : .notRunning
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
