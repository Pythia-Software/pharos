import AVFoundation
import Foundation

// Manual native dictation checks, built by macos/test/dictation.sh with
// macos/Dictation.swift. Session lifecycle uses fake recorders; the
// transcription check feeds a spoken file (from `say`) through the same
// conversion and SpeechAnalyzer path the microphone uses. No microphone.

@MainActor func expect(_ condition: Bool, _ message: String) {
    if !condition {
        FileHandle.standardError.write(Data("FAIL: \(message)\n".utf8))
        exit(1)
    }
}

@MainActor func until(_ message: String, timeout: TimeInterval = 10, _ condition: () -> Bool) async {
    let deadline = Date().addingTimeInterval(timeout)
    while !condition() {
        expect(Date() < deadline, message)
        try? await Task.sleep(for: .milliseconds(10))
    }
}

@MainActor final class FakeRecorder: DictationRecording {
    var gate: CheckedContinuation<Void, Error>?
    var failure: Error?
    var holdStart = false
    var stops = 0
    var cancels = 0
    var text: (@Sendable (String, Bool) async -> Void)?
    var finals: [String] = []

    func start(preparing: @escaping @Sendable () async -> Void, text: @escaping @Sendable (String, Bool) async -> Void) async throws {
        self.text = text
        await preparing()
        if let failure { throw failure }
        if holdStart { try await withCheckedThrowingContinuation { gate = $0 } }
    }

    func stop() async throws {
        stops += 1
        for final in finals { await text?(final, true) }
    }

    func cancel() async { cancels += 1 }
}

@MainActor final class Recorded {
    var events: [[String: Any]] = []
    var replies: [(Any?, String?)] = []
    func reply(_ value: Any?, _ error: String?) { replies.append((value, error)) }
    func events(_ session: String, _ type: String) -> [[String: Any]] {
        events.filter { $0["session"] as? String == session && $0["type"] as? String == type }
    }
}

@main enum DictationHarness {
    @MainActor static func main() async {
        await sessionTests()
        if CommandLine.arguments.count > 1 { await transcriptionTest(file: URL(fileURLWithPath: CommandLine.arguments[1])) }
        print("Dictation checks passed")
    }

    @MainActor static func sessionTests() async {
        let recorded = Recorded()
        var recorders: [FakeRecorder] = []
        var allowed = true
        var nextRecorder: () -> FakeRecorder = { FakeRecorder() }
        let sessions = DictationSessions(
            emit: { recorded.events.append($0) },
            authorize: { allowed },
            status: { ["available": true] },
            makeRecorder: { let recorder = nextRecorder(); recorders.append(recorder); return recorder }
        )

        sessions.handle(["action": "dance"], reply: recorded.reply)
        sessions.handle(["action": "start"], reply: recorded.reply)
        expect(recorded.replies.map(\.1) == ["Unknown dictation action.", "Dictation requests need a session."], "invalid requests are rejected with a message")
        recorded.replies.removeAll()

        // A full session: preparing, listening, text, stop with finals, end.
        sessions.handle(["action": "start", "session": "a"], reply: recorded.reply)
        await until("start replies") { recorded.replies.count == 1 }
        expect((recorded.replies[0].0 as? [String: Any])?["started"] as? Bool == true, "start replies once audio flows")
        expect(recorded.events("a", "state").compactMap { $0["state"] as? String } == ["preparing", "listening"], "states arrive in order")
        await recorders[0].text?("hello wor", false)
        recorders[0].finals = ["Hello world."]
        sessions.handle(["action": "stop", "session": "a"], reply: recorded.reply)
        await until("stop ends the session") { !recorded.events("a", "end").isEmpty }
        let texts = recorded.events("a", "text").map { "\($0["text"]!)|\($0["final"]!)" }
        expect(texts == ["hello wor|false", "Hello world.|true"], "text arrives in order with finals before end: \(texts)")
        expect(recorded.events("a", "end").count == 1 && recorded.events("a", "end")[0]["error"] is NSNull, "a stopped session ends once without an error")
        expect(recorders[0].stops == 1 && !sessions.isRecording, "stop releases the recorder")
        await recorders[0].text?("late", true)
        expect(recorded.events("a", "text").count == 2, "results after the end are dropped")

        // Starting another session cancels the first; cancel ends at once.
        recorded.replies.removeAll()
        sessions.handle(["action": "start", "session": "b"], reply: recorded.reply)
        await until("b starts") { recorded.replies.count == 1 }
        sessions.handle(["action": "start", "session": "c"], reply: recorded.reply)
        expect(recorded.events("b", "end").count == 1, "a new session ends the previous one")
        await until("c starts") { recorded.replies.count == 2 }
        await until("b's recorder is cancelled") { recorders[1].cancels == 1 }
        sessions.handle(["action": "cancel", "session": "c"], reply: recorded.reply)
        expect(recorded.events("c", "end").count == 1 && !sessions.isRecording, "cancel ends the session at once")
        sessions.handle(["action": "stop", "session": "c"], reply: recorded.reply)
        sessions.handle(["action": "cancel", "session": "unknown"], reply: recorded.reply)
        expect(recorded.replies.suffix(3).allSatisfy { $0.0 as? Bool == true }, "stopping or cancelling an ended session is harmless")

        // Cancelled while the model loads: the microphone is released and start fails.
        recorded.replies.removeAll()
        nextRecorder = { let recorder = FakeRecorder(); recorder.holdStart = true; return recorder }
        sessions.handle(["action": "start", "session": "d"], reply: recorded.reply)
        await until("d is loading") { recorders.count == 4 && recorders[3].gate != nil }
        sessions.cancelAll()
        recorders[3].gate?.resume()
        await until("d's start finishes") { recorded.replies.count == 1 }
        expect(recorded.replies[0].1 == "Dictation was cancelled." && recorders[3].cancels == 1, "a session cancelled while loading releases its recorder")
        expect(recorded.events("d", "end").count == 1, "the loading session still ends once")

        // Denied microphone and recorder failures end with the message.
        recorded.replies.removeAll()
        nextRecorder = { FakeRecorder() }
        allowed = false
        sessions.handle(["action": "start", "session": "e"], reply: recorded.reply)
        await until("e is refused") { recorded.replies.count == 1 }
        expect(recorded.replies[0].1 == DictationSupport.microphoneDenied && recorded.events("e", "end").first?["error"] as? String == DictationSupport.microphoneDenied, "a denied microphone explains how to allow it")
        allowed = true
        nextRecorder = { let recorder = FakeRecorder(); recorder.failure = DictationFailure("No model."); return recorder }
        sessions.handle(["action": "start", "session": "f"], reply: recorded.reply)
        await until("f fails") { recorded.replies.count == 2 }
        expect(recorded.replies[1].1 == "No model." && recorded.events("f", "end").first?["error"] as? String == "No model.", "a recorder failure is reported")

        // The time limit stops recording as if the user had.
        nextRecorder = { FakeRecorder() }
        sessions.timeLimit = .milliseconds(50)
        sessions.handle(["action": "start", "session": "g"], reply: recorded.reply)
        await until("the time limit stops g") { !recorded.events("g", "end").isEmpty }
        expect(recorders.last!.stops == 1, "the time limit stops rather than cancels")
        print("Dictation session checks passed")
    }

    @MainActor static func transcriptionTest(file: URL) async {
        guard #available(macOS 26.0, *) else { print("Skipping transcription: needs macOS 26"); return }
        let status = await DictationSupport.status()
        guard status["available"] as? Bool == true else { print("Skipping transcription: \(status["reason"] ?? "")"); return }
        final class Results: @unchecked Sendable { var volatile = 0; var finals: [String] = []; var preparing = false }
        let results = Results()
        do {
            let transcription = try await DictationTranscription.start(
                preparing: { results.preparing = true },
                text: { text, final in if final { results.finals.append(text) } else { results.volatile += 1 } }
            )
            let audio = try AVAudioFile(forReading: file)
            // Feed in microphone-sized chunks at the file's own format.
            while audio.framePosition < audio.length {
                guard let buffer = AVAudioPCMBuffer(pcmFormat: audio.processingFormat, frameCapacity: 4096) else { break }
                try audio.read(into: buffer, frameCount: 4096)
                transcription.feed(buffer)
            }
            try await transcription.finish()
        } catch {
            expect(false, "transcription failed: \(DictationSupport.message(error))")
        }
        let text = results.finals.joined(separator: " ")
        print("Transcribed (\(results.volatile) volatile, \(results.finals.count) final\(results.preparing ? ", downloaded the model" : "")): \(text)")
        let words = text.lowercased()
        expect(words.contains("lighthouse") && words.contains("harbor"), "the spoken file is transcribed on this Mac")
    }
}
