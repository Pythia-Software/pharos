import AppKit
import Sparkle
import SwiftUI

struct HeaderUpdateState: Equatable {
    var version = ""
    /// available, downloading, preparing, waiting (for an idle Mac) or installing.
    var phase = ""
    var progress: Int? = nil
    /// The person chose to install once this Mac is idle, not at once.
    var whenIdle = false

    var dictionary: [String: Any] {
        var value: [String: Any] = ["version": version, "phase": phase]
        if let progress { value["progress"] = progress }
        if whenIdle { value["whenIdle"] = true }
        return value
    }
}

/// Keep Sparkle's native UI, retaining only its public cancellation/reply
/// callbacks. A dismissed install-on-quit is resumed through checkForUpdates
/// before replying Skip; merely clearing our flag would leave it armed.
/// Install When Idle holds Sparkle's ready-to-install reply, with no native
/// alert, until UpdateNotice finds this Mac idle; the header can still
/// install at once or stop waiting.
@MainActor final class UpdateUserDriver: SPUStandardUserDriver {
    var deferring = false
    var resumingInstallation = false
    var cancellation: (() -> Void)?
    var didCancel: (() -> Void)?
    /// Prepares Pharos's processes for replacement. The Bool is true for an
    /// install the idle watcher started, which reports no failure: it waits.
    var prepareInstallation: ((Bool, @escaping (Error?) -> Void) -> Void)?
    var stateChanged: ((HeaderUpdateState) -> Void)?
    private(set) var headerState = HeaderUpdateState() {
        didSet { if headerState != oldValue { stateChanged?(headerState) } }
    }
    private var headerInstall: (() -> Void)?
    private var headerWhenIdle: (() -> Void)?
    private var idleInstall: (() -> Void)?
    private var installInOneClick = false
    private var installWhenIdle = false
    private var expectedLength: UInt64 = 0
    private var receivedLength: UInt64 = 0
    private var generation = UUID()

    /// A downloaded update waits for an idle Mac.
    var waitingForIdle: Bool { idleInstall != nil && !deferring }

    private func choice(installing: Bool, offerInHeader: Bool = false, reply: @escaping (SPUUserUpdateChoice) -> Void,
                        present: (@escaping (SPUUserUpdateChoice) -> Void) -> Void) {
        let token = UUID()
        generation = token
        let answer: (SPUUserUpdateChoice) -> Void = { [weak self] choice in
            guard let self, self.generation == token else { return }
            self.generation = UUID()
            self.cancellation = nil
            self.headerInstall = nil
            self.headerWhenIdle = nil
            self.idleInstall = nil
            if choice != .install { self.clearHeaderUpdate() }
            else { self.headerState.phase = installing ? "installing" : "downloading" }
            if choice == .skip { self.didCancel?() }
            reply(choice)
        }
        cancellation = { answer(installing ? .skip : .dismiss) }
        if deferring { cancellation?(); return }
        weak var owner = self
        func current() -> UpdateUserDriver? {
            guard let driver = owner, driver.generation == token else { return nil }
            return driver
        }
        // Install now, or (before download) start downloading. `idle`: the
        // idle watcher asked; a refusal keeps waiting instead of skipping.
        func install(idle: Bool) {
            guard let driver = current() else { return }
            driver.headerInstall = nil
            driver.headerWhenIdle = nil
            driver.idleInstall = nil
            driver.headerState.phase = installing ? "installing" : "downloading"
            guard installing, let prepare = driver.prepareInstallation else { answer(.install); return }
            // Do not ask Sparkle to terminate until replacement is safe.
            prepare(idle) { error in
                guard let error else { answer(.install); return }
                NSLog("Pharos update deferred: %@", error.localizedDescription)
                if idle, let driver = current(), !driver.deferring { waitForIdle() }
                else { answer(.skip) }
            }
        }
        func offer() {
            guard let driver = current() else { return }
            driver.headerInstall = {
                guard let driver = current() else { return }
                driver.installInOneClick = true
                driver.installWhenIdle = false
                driver.headerState.whenIdle = false
                install(idle: false)
            }
            driver.headerWhenIdle = {
                guard let driver = current() else { return }
                driver.installWhenIdle = true
                driver.headerState.whenIdle = true
                if installing { waitForIdle() } else { install(idle: false) }
            }
        }
        func waitForIdle() {
            guard let driver = current() else { return }
            offer()
            driver.idleInstall = { install(idle: true) }
            driver.headerState.progress = nil
            driver.headerState.phase = "waiting"
            // Sparkle's status window would otherwise sit at "Ready to Install".
            driver.closeNativeUpdateUI()
        }
        let respond: (SPUUserUpdateChoice) -> Void = { choice in
            guard current() != nil else { return }
            if choice == .install { install(idle: false) } else { answer(choice) }
        }
        if installing && installInOneClick {
            install(idle: false)
        } else if installing && installWhenIdle && !headerState.version.isEmpty {
            waitForIdle()
        } else {
            if offerInHeader {
                offer()
                headerState.phase = "available"
            }
            present(respond)
        }
    }

    private func closeNativeUpdateUI() { super.dismissUpdateInstallation() }

    @discardableResult func installFromHeader() -> Bool {
        guard !deferring, let install = headerInstall else { return false }
        // The header answers Sparkle directly, bypassing the native alert's
        // button. Close that alert before download progress replaces it.
        // Calling the superclass clears only UI, preserving our guarded reply.
        super.dismissUpdateInstallation()
        install()
        return true
    }

    /// Download now if needed, then wait for UpdateNotice's idle watcher.
    @discardableResult func installWhenIdleFromHeader() -> Bool {
        guard !deferring, let whenIdle = headerWhenIdle else { return false }
        if headerState.phase == "waiting" { return true }
        super.dismissUpdateInstallation()
        whenIdle()
        return true
    }

    /// Keeps the downloaded update offered in the header, without waiting.
    @discardableResult func stopWaitingForIdle() -> Bool {
        guard !deferring, idleInstall != nil else { return false }
        idleInstall = nil
        installWhenIdle = false
        headerState.whenIdle = false
        headerState.phase = "available"
        return true
    }

    /// The idle watcher found this Mac idle: install the held update.
    @discardableResult func installHeldUpdate() -> Bool {
        guard !deferring, let install = idleInstall else { return false }
        install()
        return true
    }

    func clearHeaderUpdate() {
        generation = UUID()
        cancellation = nil
        headerInstall = nil
        headerWhenIdle = nil
        idleInstall = nil
        installInOneClick = false
        installWhenIdle = false
        headerState = HeaderUpdateState()
    }

    override func showUpdateFound(with item: SUAppcastItem, state: SPUUserUpdateState,
                                  reply: @escaping (SPUUserUpdateChoice) -> Void) {
        offerUpdate(version: item.isInformationOnlyUpdate ? "" : item.displayVersionString,
                    installing: state.stage == .installing, reply: reply) { response in
            super.showUpdateFound(with: item, state: state, reply: response)
        }
    }

    // Shares Sparkle's guarded reply with the header and native release notes.
    func offerUpdate(version: String, installing: Bool, reply: @escaping (SPUUserUpdateChoice) -> Void,
                     present: (@escaping (SPUUserUpdateChoice) -> Void) -> Void) {
        headerState = HeaderUpdateState(version: version)
        choice(installing: installing, offerInHeader: !version.isEmpty, reply: reply, present: present)
    }

    override func showReady(toInstallAndRelaunch reply: @escaping (SPUUserUpdateChoice) -> Void) {
        if installInOneClick { headerState.phase = "installing" }
        choice(installing: true, offerInHeader: !headerState.version.isEmpty && !installInOneClick, reply: reply) { response in
            super.showReady(toInstallAndRelaunch: response)
        }
    }

    override func showUserInitiatedUpdateCheck(cancellation: @escaping () -> Void) {
        if resumingInstallation {
            self.cancellation = nil // Wait for the staged install's choice.
            return
        }
        self.cancellation = cancellation
        if deferring { cancellation(); return }
        super.showUserInitiatedUpdateCheck(cancellation: cancellation)
    }

    override func showDownloadInitiated(cancellation: @escaping () -> Void) {
        expectedLength = 0
        receivedLength = 0
        headerState.phase = "downloading"
        headerState.progress = nil
        self.cancellation = cancellation
        if deferring { cancellation(); return }
        super.showDownloadInitiated(cancellation: cancellation)
    }

    override func showDownloadDidStartExtractingUpdate() {
        headerState.phase = "preparing"
        headerState.progress = nil
        cancellation = nil // Download cancellation is no longer valid.
        super.showDownloadDidStartExtractingUpdate()
    }

    override func showDownloadDidReceiveExpectedContentLength(_ expectedContentLength: UInt64) {
        expectedLength = expectedContentLength
        super.showDownloadDidReceiveExpectedContentLength(expectedContentLength)
    }

    override func showDownloadDidReceiveData(ofLength length: UInt64) {
        receivedLength += length
        if expectedLength > 0 { headerState.progress = Int(min(100, Double(receivedLength) / Double(expectedLength) * 100)) }
        super.showDownloadDidReceiveData(ofLength: length)
    }

    override func dismissUpdateInstallation() {
        generation = UUID()
        cancellation = nil
        clearHeaderUpdate()
        super.dismissUpdateInstallation()
    }
}

/// Sparkle owns checking, downloading, signature verification, installation,
/// settings persistence and native update UI. Pharos coordinates its processes.
@MainActor final class UpdateNotice: NSObject, ObservableObject, SPUUpdaterDelegate {
    @Published private(set) var canCheck = false
    @Published private(set) var automaticChecks = false
    @Published private(set) var automaticDownloads = false
    @Published private(set) var headerState = HeaderUpdateState()
    private(set) var installationPending = false
    private(set) var installationPrepared = false
    private(set) var preparingInstallation = false
    /// Prepares Pharos's processes for replacement; true for an install the
    /// idle watcher started, whose failure is not shown (it keeps waiting).
    var prepareInstallation: ((Bool, @escaping (Error?) -> Void) -> Void)?
    var cancelInstallationPreparation: (() -> Void)?
    /// What an update would interrupt now (ArchiveService.updateReadiness).
    var updateReadiness: ((@escaping (UpdateReadiness?) -> Void) -> Void)?
    var userAwaySeconds: () -> TimeInterval = IdleUpdatePolicy.userAwaySeconds
    private var idleTimer: Timer?
    private var checkingIdle = false
    private var started = false
    private var observations: [NSKeyValueObservation] = []
    private var deferrals: [(Error?) -> Void] = []
    private var cancellationSent = false
    private var resuming = false
    private var deferralID = UUID()
    private var preparationWaiters: [() -> Void] = []
    private let makeUpdater: @MainActor (UpdateUserDriver, SPUUpdaterDelegate) -> SPUUpdater

    init(makeUpdater: (@MainActor (UpdateUserDriver, SPUUpdaterDelegate) -> SPUUpdater)? = nil) {
        self.makeUpdater = makeUpdater ?? {
            SPUUpdater(hostBundle: .main, applicationBundle: .main, userDriver: $0, delegate: $1)
        }
        super.init()
    }
    private lazy var driver: UpdateUserDriver = {
        let driver = UpdateUserDriver(hostBundle: .main, delegate: nil)
        driver.stateChanged = { [weak self] state in
            self?.headerState = state
            self?.watchForIdle(state.phase == "waiting")
        }
        driver.didCancel = { [weak self] in self?.cancellationSent = true }
        driver.prepareInstallation = { [weak self] idle, completion in
            guard let self, let prepare = self.prepareInstallation else {
                completion(UpdateFailure(message: "Pharos cannot prepare the update right now.")); return
            }
            self.preparingInstallation = true
            prepare(idle) { error in
                self.preparingInstallation = false
                self.installationPrepared = error == nil
                completion(error)
                let callbacks = self.preparationWaiters
                self.preparationWaiters = []
                callbacks.forEach { $0() }
            }
        }
        return driver
    }()
    private lazy var updater = makeUpdater(driver, self)

    func startIfReady(_ ready: Bool) {
        guard ready, !started,
              Bundle.main.object(forInfoDictionaryKey: "PharosUpdatesEnabled") as? Bool == true else { return }
        started = true
        UpdatePreferences.migrate(UserDefaults.standard)
        let updater = self.updater
        observations = [
            updater.observe(\.canCheckForUpdates, options: [.initial, .new]) { [weak self] updater, _ in
                MainActor.assumeIsolated {
                    self?.canCheck = updater.canCheckForUpdates
                    DispatchQueue.main.async { self?.continueDeferral() }
                }
            },
            updater.observe(\.automaticallyChecksForUpdates, options: [.initial, .new]) { [weak self] updater, _ in
                MainActor.assumeIsolated { self?.automaticChecks = updater.automaticallyChecksForUpdates }
            },
            updater.observe(\.automaticallyDownloadsUpdates, options: [.initial, .new]) { [weak self] updater, _ in
                MainActor.assumeIsolated { self?.automaticDownloads = updater.automaticallyDownloadsUpdates }
            },
        ]
        do { try updater.start() }
        catch { NSLog("Could not start Pharos updates: %@", error.localizedDescription) }
    }

    func check() {
        guard deferrals.isEmpty else { return }
        driver.deferring = false
        updater.checkForUpdates()
    }
    func installFromHeader() -> Bool { driver.installFromHeader() }
    func installWhenIdleFromHeader() -> Bool { driver.installWhenIdleFromHeader() }
    func stopWaitingForIdle() -> Bool { driver.stopWaitingForIdle() }

    /// While a downloaded update waits for an idle Mac, look once a minute.
    private func watchForIdle(_ waiting: Bool) {
        guard waiting else { idleTimer?.invalidate(); idleTimer = nil; return }
        guard idleTimer == nil else { return }
        let timer = Timer(timeInterval: IdleUpdatePolicy.interval, repeats: true) { [weak self] _ in
            MainActor.assumeIsolated { self?.checkIdle() }
        }
        timer.tolerance = 15
        RunLoop.main.add(timer, forMode: .common)
        idleTimer = timer
    }

    /// Installs the waiting update when IdleUpdatePolicy allows. Asks the
    /// service only once the person has been away long enough.
    func checkIdle(completion: (() -> Void)? = nil) {
        guard driver.waitingForIdle, deferrals.isEmpty, !checkingIdle, !preparingInstallation,
              userAwaySeconds() >= IdleUpdatePolicy.away, let probe = updateReadiness else { completion?(); return }
        checkingIdle = true
        probe { [weak self] readiness in
            guard let self else { return }
            self.checkingIdle = false
            // Ask again: the person may have come back while the service answered.
            if self.driver.waitingForIdle, self.deferrals.isEmpty,
               IdleUpdatePolicy.mayInstall(awaySeconds: self.userAwaySeconds(), readiness: readiness) {
                self.driver.installHeldUpdate()
            }
            completion?()
        }
    }
    func setAutomaticChecks(_ enabled: Bool) { updater.automaticallyChecksForUpdates = enabled }
    func setAutomaticDownloads(_ enabled: Bool) { updater.automaticallyDownloadsUpdates = enabled }

    func resumeAfterFailedEject() {
        if deferrals.isEmpty { driver.deferring = false }
    }

    func afterPreparation(_ completion: @escaping () -> Void) {
        if preparingInstallation { preparationWaiters.append(completion) }
        else { completion() }
    }

    func deferInstallation(completion: @escaping (Error?) -> Void) {
        guard started || installationPending else { completion(nil); return }
        driver.deferring = true
        deferrals.append(completion)
        let token = UUID()
        deferralID = token
        continueDeferral()
        DispatchQueue.main.asyncAfter(deadline: .now() + 30) { [weak self] in
            guard let self, self.deferralID == token, !self.deferrals.isEmpty else { return }
            self.finishDeferral(UpdateFailure(message: "Pharos could not stop the update it is preparing. Wait a moment, then try again."))
        }
    }

    private func continueDeferral() {
        guard !deferrals.isEmpty, !resuming else { return }
        if let cancellation = driver.cancellation {
            driver.cancellation = nil
            cancellation()
        } else if !updater.sessionInProgress {
            if installationPending {
                resuming = true
                driver.resumingInstallation = true
                // A silent staged installer has no UI callback. Resume it to
                // obtain a public user-driver reply, then cancel with Skip.
                updater.checkForUpdates()
            } else { finishDeferral(nil) }
        }
    }

    private func finishDeferral(_ error: Error?) {
        let callbacks = deferrals
        deferrals = []
        resuming = false
        driver.resumingInstallation = false
        cancellationSent = false
        if error != nil { driver.deferring = false }
        callbacks.forEach { $0(error) }
    }

    func updater(_ updater: SPUUpdater, mayPerform updateCheck: SPUUpdateCheck) throws {
        if resuming { return } // Only obtaining a cancellation reply, not installing.
        if driver.deferring {
            throw UpdateFailure(message: "Pharos is closing or ejecting its library.")
        }
        try Self.validateInstallation()
    }

    static func validateInstallation(app: URL = Bundle.main.bundleURL) throws {
        let values = try app.resourceValues(forKeys: [.volumeIsReadOnlyKey, .volumeIsLocalKey])
        guard values.volumeIsReadOnly != true, values.volumeIsLocal == true,
              !canonicalPath(app).contains("/AppTranslocation/") else {
            throw UpdateFailure(message: "Pharos cannot update this copy. Move Pharos.app to Applications, or to a folder on a drive attached to this Mac, and open it from there. Then choose Pharos → Check for Updates…")
        }
        var info = statfs()
        guard statfs(app.path, &info) == 0 else {
            throw UpdateFailure(message: "The drive Pharos is installed on is unavailable. Reconnect it before updating.")
        }
        let filesystem = withUnsafePointer(to: &info.f_fstypename) {
            $0.withMemoryRebound(to: CChar.self, capacity: Int(MFSTYPENAMELEN)) { String(cString: $0) }
        }
        guard filesystem == "apfs" || filesystem == "hfs" else {
            throw UpdateFailure(message: "Pharos can update itself only on an APFS or Mac OS Extended drive, which keeps the app’s frameworks and permissions intact. Move Pharos.app to Applications or to such a drive, then check for updates again.")
        }
    }

    func updater(_ updater: SPUUpdater, willInstallUpdate item: SUAppcastItem) { installationPending = true }
    func updater(_ updater: SPUUpdater, willInstallUpdateOnQuit item: SUAppcastItem,
                 immediateInstallationBlock immediateInstallHandler: @escaping () -> Void) -> Bool {
        installationPending = true
        return false // Keep Sparkle's scheduler and gentle reminders running.
    }
    func updater(_ updater: SPUUpdater, userDidMake choice: SPUUserUpdateChoice,
                 forUpdate item: SUAppcastItem, state: SPUUserUpdateState) {
        userDidChooseUpdate(choice)
    }

    func userDidChooseUpdate(_ choice: SPUUserUpdateChoice) {
        // Skip cancels even a staged installer without reporting an error.
        // Dismiss keeps installation on quit, so it must preserve this flag.
        if choice == .skip { installationPending = false }
    }
    func updater(_ updater: SPUUpdater, didAbortWithError error: Error) {
        driver.clearHeaderUpdate()
        recoverPreparedInstallation()
        if !driver.deferring { installationPending = false }
    }

    private func recoverPreparedInstallation() {
        guard installationPrepared else { return }
        installationPrepared = false
        installationPending = false
        cancelInstallationPreparation?()
    }

    func updater(_ updater: SPUUpdater, didFinishUpdateCycleFor updateCheck: SPUUpdateCheck, error: Error?) {
        // Sparkle can fail after our handoff but before terminating the app.
        // Both callbacks may report the same failure; unwind the handoff once.
        if error != nil || cancellationSent { recoverPreparedInstallation() }
        resuming = false
        driver.resumingInstallation = false
        if cancellationSent {
            installationPending = false
            installationPrepared = false
            cancellationSent = false
        }
        guard !deferrals.isEmpty else { return }
        if let error, installationPending { finishDeferral(error) }
        else { DispatchQueue.main.async { self.continueDeferral() } }
    }
}

struct UpdateCommands: Commands {
    @ObservedObject var updates: UpdateNotice
    var body: some Commands {
        CommandGroup(after: .appInfo) {
            Button("Check for Updates…") { updates.check() }.disabled(!updates.canCheck)
            Toggle("Automatically Check for Updates", isOn: Binding(get: { updates.automaticChecks }, set: updates.setAutomaticChecks))
                .disabled(!updates.canCheck)
            Toggle("Automatically Download and Install Updates", isOn: Binding(get: { updates.automaticDownloads }, set: updates.setAutomaticDownloads))
                .disabled(!updates.canCheck || !updates.automaticChecks)
        }
    }
}

/// All quit paths, including Sparkle's install-on-quit, pass through this
/// asynchronous gate. A relaunch-only Sparkle hook would miss ordinary quits.
@MainActor final class PharosDelegate: NSObject, NSApplicationDelegate {
    let service = ArchiveService()
    let updates = UpdateNotice()
    private var terminating = false

    override init() {
        super.init()
        service.deferUpdate = { [weak updates] completion in
            guard let updates else { completion(nil); return }
            updates.deferInstallation(completion: completion)
        }
        service.resumeUpdates = { [weak updates] in updates?.resumeAfterFailedEject() }
        updates.prepareInstallation = { [weak self, weak service] idle, completion in
            guard let service else { completion(UpdateFailure(message: "Pharos is closing.")); return }
            // An explicit install waits for work that cannot stop in time,
            // as an eject does, and says why it did not install. One that
            // waits for an idle Mac keeps waiting instead.
            service.prepareToTerminate(installing: true, unlessBusy: true) { error in
                completion(error)
                if let error, !idle {
                    DispatchQueue.main.async {
                        self?.showFailure(error, title: "The update was not installed",
                                          next: "To try again, choose Pharos → Check for Updates…")
                    }
                }
            }
        }
        updates.cancelInstallationPreparation = { [weak service] in service?.cancelUpdatePreparation() }
        updates.updateReadiness = { [weak service] completion in
            guard let service else { completion(nil); return }
            service.updateReadiness(completion: completion)
        }
    }

    func applicationShouldTerminate(_ sender: NSApplication) -> NSApplication.TerminateReply {
        if service.ejecting || updates.installationPrepared { return .terminateNow }
        guard !terminating else { return .terminateLater }
        terminating = true
        if updates.preparingInstallation {
            updates.afterPreparation { [weak self] in
                guard let self else { return }
                if self.updates.installationPrepared {
                    self.terminating = false
                    sender.reply(toApplicationShouldTerminate: true)
                } else { self.finishOrdinaryQuit(sender) }
            }
            return .terminateLater
        }
        let installing = updates.installationPending
        // A failed installation handoff cancels Sparkle before falling back to
        // ordinary Quit. In particular, a writer must not veto logout/restart.
        if !installing {
            finishOrdinaryQuit(sender)
            return .terminateLater
        }
        do { try UpdateNotice.validateInstallation() }
        catch { finishOrdinaryQuit(sender); return .terminateLater }
        service.prepareToTerminate(installing: installing) { [weak self] error in
            guard let self else { return }
            if error != nil { self.finishOrdinaryQuit(sender) }
            else {
                self.terminating = false
                sender.reply(toApplicationShouldTerminate: true)
            }
        }
        return .terminateLater
    }

    private func finishOrdinaryQuit(_ sender: NSApplication) {
        updates.deferInstallation { [weak self] error in
            guard let self else { return }
            if let error {
                self.terminating = false
                sender.reply(toApplicationShouldTerminate: false)
                self.showFailure(error)
                return
            }
            self.service.prepareToTerminate(installing: false) { _ in
                self.terminating = false
                sender.reply(toApplicationShouldTerminate: true)
            }
        }
    }

    private func showFailure(_ error: Error, title: String = "Pharos cannot quit yet", next: String? = nil) {
        let alert = NSAlert()
        alert.messageText = title
        alert.informativeText = [error.localizedDescription, next].compactMap { $0 }.joined(separator: " ")
        alert.addButton(withTitle: "OK")
        alert.runModal()
    }
}
