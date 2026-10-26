import AppKit
import Sparkle
import SwiftUI

struct HeaderUpdateState: Equatable {
    var version = ""
    var phase = ""
    var progress: Int? = nil

    var dictionary: [String: Any] {
        var value: [String: Any] = ["version": version, "phase": phase]
        if let progress { value["progress"] = progress }
        return value
    }
}

/// Keep Sparkle's native UI, retaining only its public cancellation/reply
/// callbacks. A dismissed install-on-quit is resumed through checkForUpdates
/// before replying Skip; merely clearing our flag would leave it armed.
@MainActor final class UpdateUserDriver: SPUStandardUserDriver {
    var deferring = false
    var resumingInstallation = false
    var cancellation: (() -> Void)?
    var didCancel: (() -> Void)?
    var prepareInstallation: ((@escaping (Error?) -> Void) -> Void)?
    var stateChanged: ((HeaderUpdateState) -> Void)?
    private(set) var headerState = HeaderUpdateState() {
        didSet { if headerState != oldValue { stateChanged?(headerState) } }
    }
    private var headerInstall: (() -> Void)?
    private var installInOneClick = false
    private var expectedLength: UInt64 = 0
    private var receivedLength: UInt64 = 0
    private var generation = UUID()

    private func choice(installing: Bool, offerInHeader: Bool = false, reply: @escaping (SPUUserUpdateChoice) -> Void,
                        present: (@escaping (SPUUserUpdateChoice) -> Void) -> Void) {
        let token = UUID()
        generation = token
        let answer: (SPUUserUpdateChoice) -> Void = { [weak self] choice in
            guard let self, self.generation == token else { return }
            self.generation = UUID()
            self.cancellation = nil
            self.headerInstall = nil
            if choice != .install { self.clearHeaderUpdate() }
            else { self.headerState.phase = installing ? "installing" : "downloading" }
            if choice == .skip { self.didCancel?() }
            reply(choice)
        }
        cancellation = { answer(installing ? .skip : .dismiss) }
        if deferring { cancellation?(); return }
        let respond: (SPUUserUpdateChoice) -> Void = { [weak self] choice in
            guard let self, self.generation == token else { return }
            if choice == .install {
                self.headerInstall = nil
                self.headerState.phase = installing ? "installing" : "downloading"
            }
            if choice == .install, installing, let prepare = self.prepareInstallation {
                // Do not ask Sparkle to terminate until replacement is safe.
                prepare { error in
                    if let error { NSLog("Pharos update deferred: %@", error.localizedDescription) }
                    answer(error == nil ? .install : .skip)
                }
            } else { answer(choice) }
        }
        if offerInHeader {
            headerInstall = { [weak self] in
                guard let self, self.generation == token else { return }
                self.headerInstall = nil
                self.installInOneClick = true
                self.headerState.phase = installing ? "installing" : "downloading"
                respond(.install)
            }
            headerState.phase = "available"
        }
        if installing && installInOneClick { respond(.install) }
        else { present(respond) }
    }

    @discardableResult func installFromHeader() -> Bool {
        guard !deferring, let install = headerInstall else { return false }
        // The header answers Sparkle directly, bypassing the native alert's
        // button. Close that alert before download progress replaces it.
        // Calling the superclass clears only UI, preserving our guarded reply.
        super.dismissUpdateInstallation()
        install()
        return true
    }

    func clearHeaderUpdate() {
        generation = UUID()
        cancellation = nil
        headerInstall = nil
        installInOneClick = false
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
    var prepareInstallation: ((@escaping (Error?) -> Void) -> Void)?
    var cancelInstallationPreparation: (() -> Void)?
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
        driver.stateChanged = { [weak self] in self?.headerState = $0 }
        driver.didCancel = { [weak self] in self?.cancellationSent = true }
        driver.prepareInstallation = { [weak self] completion in
            guard let self, let prepare = self.prepareInstallation else {
                completion(UpdateFailure(message: "Pharos cannot prepare the update right now.")); return
            }
            self.preparingInstallation = true
            prepare { error in
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
            self.finishDeferral(UpdateFailure(message: "Sparkle could not cancel its installation. Let the updater finish preparing, then try again."))
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
            throw UpdateFailure(message: "Copy Pharos.app out of its disk image into Applications or a folder on a local APFS drive, then check for updates from that copy.")
        }
        var info = statfs()
        guard statfs(app.path, &info) == 0 else {
            throw UpdateFailure(message: "Pharos's installation drive is unavailable. Reconnect it before updating.")
        }
        let filesystem = withUnsafePointer(to: &info.f_fstypename) {
            $0.withMemoryRebound(to: CChar.self, capacity: Int(MFSTYPENAMELEN)) { String(cString: $0) }
        }
        guard filesystem == "apfs" || filesystem == "hfs" else {
            throw UpdateFailure(message: "Install Pharos.app on a local APFS or Mac OS Extended volume before updating. This drive cannot safely preserve the app's frameworks and permissions.")
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
        updates.prepareInstallation = { [weak service] completion in
            guard let service else { completion(UpdateFailure(message: "Pharos is closing.")); return }
            service.prepareToTerminate(installing: true, completion: completion)
        }
        updates.cancelInstallationPreparation = { [weak service] in service?.cancelUpdatePreparation() }
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

    private func showFailure(_ error: Error) {
        let alert = NSAlert()
        alert.messageText = "Pharos is still in use"
        alert.informativeText = error.localizedDescription
        alert.addButton(withTitle: "OK")
        alert.runModal()
    }
}
