import AppKit
import SwiftUI

enum UpdateNoticeStatus {
    case checking
    case available(AvailableUpdate)
    case message(title: String, detail: String)
}

@MainActor final class UpdateNotice: ObservableObject {
    @Published var status: UpdateNoticeStatus?
    @Published private(set) var isChecking = false
    private var checkedOnLaunch = false

    func checkOnLaunchIfEnabled(_ enabled: Bool) {
        guard enabled, !checkedOnLaunch else { return }
        checkedOnLaunch = true
        check(showAllResults: false)
    }

    func check(showAllResults: Bool = true) {
        guard !isChecking else { return }
        isChecking = true
        if showAllResults { status = .checking }
        let installedVersion = Bundle.main.object(forInfoDictionaryKey: "CFBundleVersion") as? String ?? ""
        Task {
            defer { isChecking = false }
            do {
                switch try await UpdateChecker.check(installedVersion: installedVersion) {
                case .available(let update): status = .available(update)
                case .current:
                    if showAllResults { status = .message(title: "Pharos is up to date", detail: "You have the latest published version (\(installedVersion)).") }
                case .noPublishedRelease:
                    if showAllResults { status = .message(title: "No release available", detail: "Pharos has no published release yet. Check again later.") }
                }
            } catch {
                if showAllResults { status = .message(title: "Could not check for updates", detail: error.localizedDescription) }
            }
        }
    }
}

struct UpdateNoticeSheet: View {
    @ObservedObject var notice: UpdateNotice

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            switch notice.status {
            case .checking:
                Text("Checking for updates…").font(.title2.weight(.semibold))
                ProgressView()
            case .available(let update):
                Text("Pharos \(update.version) is available").font(.title2.weight(.semibold))
                Text("Download the disk image, quit Pharos, and drag its app to the location of your original installation. The disk image shows the steps.")
                if !update.notes.isEmpty {
                    Text("Release notes").font(.headline)
                    ScrollView {
                        Text(update.notes).textSelection(.enabled)
                            .frame(maxWidth: .infinity, alignment: .leading)
                    }
                    .frame(maxHeight: 180)
                }
                HStack {
                    Spacer()
                    Button("Later") { notice.status = nil }
                    Button("Download Disk Image") {
                        if NSWorkspace.shared.open(update.downloadURL) {
                            notice.status = nil
                        } else {
                            notice.status = .message(title: "Could not open the download", detail: "Open the Pharos GitHub Releases page in your browser and download version \(update.version).")
                        }
                    }
                    .buttonStyle(.borderedProminent)
                }
            case .message(let title, let detail):
                Text(title).font(.title2.weight(.semibold))
                Text(detail).textSelection(.enabled)
                HStack {
                    Spacer()
                    Button("OK") { notice.status = nil }
                        .keyboardShortcut(.defaultAction)
                }
            case nil:
                EmptyView()
            }
        }
        .padding(24)
        .frame(width: 520)
    }
}
