import AVFoundation
import Foundation
import Speech

// Dictation for the page's text boxes: the page's microphone button asks for a
// session, this file records from the default input and transcribes on this
// Mac with SpeechAnalyzer, and results flow back to the page as events. It
// does not depend on the system Dictation setting or its keyboard shortcut,
// and audio is never sent anywhere or kept.

struct DictationFailure: LocalizedError {
    let message: String
    init(_ message: String) { self.message = message }
    var errorDescription: String? { message }
}

enum DictationSupport {
    static let unsupportedSystem = "Dictation in Pharos needs macOS 26 or later."
    static let microphoneDenied = "Pharos doesn't have microphone access. Turn on Pharos in System Settings › Privacy & Security › Microphone, then try again."

    /// Whether this Mac can offer the microphone button at all. Permission is
    /// not part of this: a denied microphone is reported when dictation starts,
    /// so the button can say how to fix it.
    static func status(locale: Locale = .current) async -> [String: Any] {
        guard #available(macOS 26.0, *) else { return ["available": false, "reason": unsupportedSystem] }
        guard await DictationTranscriber.supportedLocale(equivalentTo: locale) != nil else {
            return ["available": false, "reason": "Dictation doesn't support \(languageName(locale)) on this Mac."]
        }
        return ["available": true]
    }

    static func microphoneAccess() async -> Bool {
        switch AVCaptureDevice.authorizationStatus(for: .audio) {
        case .authorized: return true
        case .notDetermined: return await AVCaptureDevice.requestAccess(for: .audio)
        default: return false
        }
    }

    static func languageName(_ locale: Locale) -> String {
        Locale.current.localizedString(forIdentifier: locale.identifier) ?? locale.identifier
    }

    static func message(_ error: Error) -> String {
        (error as? LocalizedError)?.errorDescription ?? error.localizedDescription
    }
}

/// One recording, from start until it is stopped or cancelled.
@MainActor
protocol DictationRecording: AnyObject {
    /// Returns once audio is flowing. `preparing` runs first if the speech
    /// model must be downloaded; `text` receives results in order.
    func start(
        preparing: @escaping @Sendable () async -> Void,
        text: @escaping @Sendable (_ text: String, _ final: Bool) async -> Void
    ) async throws
    /// Stops recording and returns once every final result has been delivered.
    func stop() async throws
    /// Stops recording and drops results that have not been delivered.
    func cancel() async
}

/// Converts any PCM audio to the analyzer's format and transcribes it. Feed
/// it from one thread at a time (the microphone tap, or a test's file reader).
@available(macOS 26.0, *)
final class DictationTranscription: @unchecked Sendable {
    let format: AVAudioFormat
    private let analyzer: SpeechAnalyzer
    private let input: AsyncStream<AnalyzerInput>.Continuation
    private let results: Task<Void, Error>
    private var converter: AVAudioConverter?

    private init(format: AVAudioFormat, analyzer: SpeechAnalyzer, input: AsyncStream<AnalyzerInput>.Continuation, results: Task<Void, Error>) {
        self.format = format
        self.analyzer = analyzer
        self.input = input
        self.results = results
    }

    static func start(
        locale: Locale = .current,
        preparing: @escaping @Sendable () async -> Void,
        text: @escaping @Sendable (String, Bool) async -> Void
    ) async throws -> DictationTranscription {
        guard let supported = await DictationTranscriber.supportedLocale(equivalentTo: locale) else {
            throw DictationFailure("Dictation doesn't support \(DictationSupport.languageName(locale)) on this Mac.")
        }
        let transcriber = DictationTranscriber(locale: supported, preset: .progressiveLongDictation)
        if let installation = try await AssetInventory.assetInstallationRequest(supporting: [transcriber]) {
            await preparing()
            try await installation.downloadAndInstall()
        }
        guard let format = await SpeechAnalyzer.bestAvailableAudioFormat(compatibleWith: [transcriber]) else {
            throw DictationFailure("The speech model for \(DictationSupport.languageName(supported)) is unavailable.")
        }
        let analyzer = SpeechAnalyzer(modules: [transcriber])
        try await analyzer.prepareToAnalyze(in: format)
        let (stream, input) = AsyncStream.makeStream(of: AnalyzerInput.self)
        let results = Task {
            for try await result in transcriber.results {
                await text(String(result.text.characters), result.isFinal)
            }
        }
        do {
            try await analyzer.start(inputSequence: stream)
        } catch {
            input.finish()
            results.cancel()
            throw error
        }
        return DictationTranscription(format: format, analyzer: analyzer, input: input, results: results)
    }

    func feed(_ buffer: AVAudioPCMBuffer) {
        if converter?.inputFormat != buffer.format { converter = AVAudioConverter(from: buffer.format, to: format) }
        guard let converter, let converted = Self.convert(buffer, with: converter) else { return }
        input.yield(AnalyzerInput(buffer: converted))
    }

    func finish() async throws {
        input.finish()
        try await analyzer.finalizeAndFinishThroughEndOfInput()
        try await results.value
    }

    func cancel() async {
        input.finish()
        results.cancel()
        await analyzer.cancelAndFinishNow()
    }

    static func convert(_ buffer: AVAudioPCMBuffer, with converter: AVAudioConverter) -> AVAudioPCMBuffer? {
        let ratio = converter.outputFormat.sampleRate / buffer.format.sampleRate
        let capacity = AVAudioFrameCount((Double(buffer.frameLength) * ratio).rounded(.up)) + 1
        guard let output = AVAudioPCMBuffer(pcmFormat: converter.outputFormat, frameCapacity: capacity) else { return nil }
        var consumed = false
        var error: NSError?
        let status = converter.convert(to: output, error: &error) { _, inputStatus in
            if consumed {
                inputStatus.pointee = .noDataNow
                return nil
            }
            consumed = true
            inputStatus.pointee = .haveData
            return buffer
        }
        return status == .error || output.frameLength == 0 ? nil : output
    }
}

/// Records from the default input device.
@available(macOS 26.0, *)
@MainActor
final class MicrophoneDictation: DictationRecording {
    private let engine = AVAudioEngine()
    private var transcription: DictationTranscription?
    private var tapped = false
    private var configurationObserver: NSObjectProtocol?


    func start(
        preparing: @escaping @Sendable () async -> Void,
        text: @escaping @Sendable (String, Bool) async -> Void
    ) async throws {
        let transcription = try await DictationTranscription.start(preparing: preparing, text: text)
        self.transcription = transcription
        do {
            try startMicrophone(transcription)
        } catch {
            stopMicrophone()
            await transcription.cancel()
            throw error
        }
        // A new default input (headphones connecting, say) stops the engine;
        // follow it, since the transcription converts any input format.
        configurationObserver = NotificationCenter.default.addObserver(
            forName: .AVAudioEngineConfigurationChange, object: engine, queue: .main
        ) { [weak self] _ in
            MainActor.assumeIsolated {
                guard let self, self.tapped else { return }
                self.stopMicrophone()
                try? self.startMicrophone(transcription)
            }
        }
    }

    private func startMicrophone(_ transcription: DictationTranscription) throws {
        let node = engine.inputNode
        let format = node.outputFormat(forBus: 0)
        guard format.sampleRate > 0, format.channelCount > 0 else {
            throw DictationFailure("No microphone is available. Connect one or choose an input in System Settings › Sound.")
        }
        node.installTap(onBus: 0, bufferSize: 4096, format: format) { buffer, _ in transcription.feed(buffer) }
        tapped = true
        engine.prepare()
        do {
            try engine.start()
        } catch {
            throw DictationFailure("Pharos couldn't start the microphone: \(error.localizedDescription)")
        }
    }

    func stop() async throws {
        endMicrophone()
        try await transcription?.finish()
    }

    func cancel() async {
        endMicrophone()
        await transcription?.cancel()
    }

    private func endMicrophone() {
        configurationObserver.map(NotificationCenter.default.removeObserver)
        configurationObserver = nil
        stopMicrophone()
    }

    private func stopMicrophone() {
        if engine.isRunning { engine.stop() }
        if tapped { engine.inputNode.removeTap(onBus: 0); tapped = false }
    }
}

/// The page's dictation sessions: at most one records at a time, each ends
/// exactly once with an `end` event, and nothing outlives the page.
///
/// Requests (`pharosDictation`, with a reply):
///   {action: "status"} → {available, reason?}
///   {action: "start", session} → {started: true} once audio is flowing
///   {action: "stop", session} → true; final results then `end` follow
///   {action: "cancel", session} → true; `end` follows at once
/// Events (`pharos:dictation` on window): {session, type: "state", state:
/// "preparing" | "listening" | "stopping"}, {session, type: "text", text,
/// final}, and {session, type: "end", error}. A text event replaces the
/// previous non-final text; a final one is kept.
@MainActor
final class DictationSessions {
    typealias Reply = (Any?, String?) -> Void

    private final class Session {
        let id: String
        var recorder: (any DictationRecording)?
        var started = false
        var stopping = false
        var ended = false
        var limit: Task<Void, Never>?
        init(id: String) { self.id = id }
    }

    /// Recording stops on its own after this long, as if the user had stopped it.
    var timeLimit: Duration = .seconds(600)
    private var current: Session?
    private let emit: ([String: Any]) -> Void
    private let authorize: () async -> Bool
    private let makeRecorder: @MainActor () -> (any DictationRecording)?
    private let status: () async -> [String: Any]

    init(
        emit: @escaping ([String: Any]) -> Void,
        authorize: @escaping () async -> Bool = DictationSupport.microphoneAccess,
        status: @escaping () async -> [String: Any] = { await DictationSupport.status() },
        makeRecorder: @escaping @MainActor () -> (any DictationRecording)? = {
            if #available(macOS 26.0, *) { return MicrophoneDictation() }
            return nil
        }
    ) {
        self.emit = emit
        self.authorize = authorize
        self.status = status
        self.makeRecorder = makeRecorder
    }

    var isRecording: Bool { current != nil }

    func handle(_ body: Any, reply: @escaping Reply) {
        guard let request = body as? [String: Any], let action = request["action"] as? String,
              ["status", "start", "stop", "cancel"].contains(action) else {
            reply(nil, "Unknown dictation action.")
            return
        }
        if action == "status" {
            Task { reply(await status(), nil) }
            return
        }
        guard let id = request["session"] as? String, !id.isEmpty, id.count <= 128 else {
            reply(nil, "Dictation requests need a session.")
            return
        }
        switch action {
        case "start": start(id, reply)
        case "stop":
            if let session = current, session.id == id {
                if session.started { stop(session) } else { cancel(session) }
            }
            reply(true, nil)
        default:
            if let session = current, session.id == id { cancel(session) }
            reply(true, nil)
        }
    }

    /// Ends any session at once; for navigation, closing the window or quitting.
    func cancelAll() {
        if let current { cancel(current) }
    }

    private func start(_ id: String, _ reply: @escaping Reply) {
        cancelAll()
        guard let recorder = makeRecorder() else {
            reply(nil, DictationSupport.unsupportedSystem)
            return
        }
        let session = Session(id: id)
        current = session
        Task {
            guard await authorize() else {
                end(session, error: DictationSupport.microphoneDenied)
                reply(nil, DictationSupport.microphoneDenied)
                return
            }
            guard !session.ended else { reply(nil, "Dictation was cancelled."); return }
            session.recorder = recorder
            do {
                try await recorder.start(
                    preparing: { [weak self] in await self?.send(session, ["type": "state", "state": "preparing"]) },
                    text: { [weak self] text, final in await self?.send(session, ["type": "text", "text": text, "final": final]) }
                )
            } catch {
                guard !session.ended else { reply(nil, "Dictation was cancelled."); return }
                let message = DictationSupport.message(error)
                end(session, error: message)
                reply(nil, message)
                return
            }
            // Cancelled while the model loaded: release the microphone now.
            guard !session.ended else {
                await recorder.cancel()
                reply(nil, "Dictation was cancelled.")
                return
            }
            session.started = true
            reply(["started": true], nil)
            send(session, ["type": "state", "state": "listening"])
            let limit = timeLimit
            session.limit = Task { [weak self] in
                guard (try? await Task.sleep(for: limit)) != nil else { return }
                self?.stop(session)
            }
        }
    }

    private func stop(_ session: Session) {
        guard !session.ended, !session.stopping, let recorder = session.recorder else { return }
        session.stopping = true
        session.limit?.cancel()
        send(session, ["type": "state", "state": "stopping"])
        Task {
            do {
                try await recorder.stop()
                end(session, error: nil)
            } catch {
                end(session, error: DictationSupport.message(error))
            }
        }
    }

    private func cancel(_ session: Session) {
        guard !session.ended else { return }
        if session.started, let recorder = session.recorder { Task { await recorder.cancel() } }
        end(session, error: nil)
    }

    private func send(_ session: Session, _ detail: [String: Any]) {
        guard !session.ended, current === session else { return }
        emit(detail.merging(["session": session.id]) { value, _ in value })
    }

    private func end(_ session: Session, error: String?) {
        guard !session.ended else { return }
        session.ended = true
        session.limit?.cancel()
        if current === session { current = nil }
        emit(["session": session.id, "type": "end", "error": error.map { $0 as Any } ?? NSNull()])
    }
}
