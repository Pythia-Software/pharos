import Foundation

/// Release versions use the same three numeric components as the app bundle.
struct UpdateVersion: Comparable, Equatable {
    let parts: [Int]

    init?(_ text: String) {
        let components = text.split(separator: ".", omittingEmptySubsequences: false)
        guard components.count == 3 else { return nil }
        let numbers = components.compactMap { component -> Int? in
            guard !component.isEmpty, component.allSatisfy(\.isNumber),
                  component.allSatisfy({ $0.isASCII }), let number = Int(component) else { return nil }
            return number
        }
        guard numbers.count == 3 else { return nil }
        parts = numbers
    }

    static func < (lhs: Self, rhs: Self) -> Bool {
        lhs.parts.lexicographicallyPrecedes(rhs.parts)
    }
}

struct AvailableUpdate: Equatable {
    let version: String
    let notes: String
    let downloadURL: URL
}

enum UpdateCheckResult: Equatable {
    case noPublishedRelease
    case current
    case available(AvailableUpdate)
}

enum UpdateCheckError: LocalizedError {
    case invalidInstalledVersion
    case invalidRelease
    case missingDiskImage(String)
    case server(Int)

    var errorDescription: String? {
        switch self {
        case .invalidInstalledVersion: return "This build has no valid release version."
        case .invalidRelease: return "GitHub returned release information Pharos could not read."
        case .missingDiskImage(let version): return "Release \(version) has no Pharos disk image. Check its GitHub release page or try again later."
        case .server(let status): return "GitHub returned HTTP \(status). Try again later."
        }
    }
}

enum UpdateChecker {
    static let latestURL = URL(string: "https://api.github.com/repos/gbdubs/pharos/releases/latest")!

    private struct Release: Decodable {
        let tagName: String
        let body: String?
        let draft: Bool
        let prerelease: Bool
        let assets: [Asset]

        enum CodingKeys: String, CodingKey {
            case tagName = "tag_name", body, draft, prerelease, assets
        }
    }

    private struct Asset: Decodable {
        let name: String
        let browserDownloadURL: URL

        enum CodingKeys: String, CodingKey {
            case name, browserDownloadURL = "browser_download_url"
        }
    }

    static func parse(_ data: Data, installedVersion: String) throws -> UpdateCheckResult {
        guard let installed = UpdateVersion(installedVersion) else { throw UpdateCheckError.invalidInstalledVersion }
        guard let release = try? JSONDecoder().decode(Release.self, from: data),
              release.tagName.hasPrefix("v"),
              let version = UpdateVersion(String(release.tagName.dropFirst())),
              !release.draft, !release.prerelease else { throw UpdateCheckError.invalidRelease }
        guard version > installed else { return .current }

        let name = "Pharos-\(release.tagName)-macos-universal.dmg"
        guard let asset = release.assets.first(where: { $0.name == name }),
              asset.browserDownloadURL.scheme == "https",
              asset.browserDownloadURL.host == "github.com",
              asset.browserDownloadURL.path == "/gbdubs/pharos/releases/download/\(release.tagName)/\(name)" else {
            throw UpdateCheckError.missingDiskImage(release.tagName)
        }
        return .available(AvailableUpdate(version: String(release.tagName.dropFirst()),
                                          notes: release.body ?? "", downloadURL: asset.browserDownloadURL))
    }

    static func check(installedVersion: String, session: URLSession = .shared) async throws -> UpdateCheckResult {
        var request = URLRequest(url: latestURL)
        request.setValue("application/vnd.github+json", forHTTPHeaderField: "Accept")
        request.setValue("Pharos/\(installedVersion)", forHTTPHeaderField: "User-Agent")
        request.timeoutInterval = 15
        let (data, response) = try await session.data(for: request)
        guard let response = response as? HTTPURLResponse else { throw UpdateCheckError.invalidRelease }
        if response.statusCode == 404 { return .noPublishedRelease }
        guard response.statusCode == 200 else { throw UpdateCheckError.server(response.statusCode) }
        return try parse(data, installedVersion: installedVersion)
    }
}
