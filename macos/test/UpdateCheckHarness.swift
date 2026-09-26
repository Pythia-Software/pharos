// swiftc -parse-as-library macos/UpdateChecker.swift macos/test/UpdateCheckHarness.swift -o /tmp/pharos-update-check && /tmp/pharos-update-check
import Foundation

final class StubReleaseResponse: URLProtocol {
    static var status = 200
    static var body = Data()

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        let response = HTTPURLResponse(url: request.url!, statusCode: Self.status, httpVersion: "HTTP/1.1", headerFields: nil)!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: Self.body)
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() {}
}

func release(_ tag: String, asset: String? = nil, downloadURL: String? = nil,
             draft: Bool = false, prerelease: Bool = false) -> Data {
    let name = asset ?? "Pharos-\(tag)-macos-universal.dmg"
    let url = downloadURL ?? "https://github.com/Pythia-Software/pharos/releases/download/\(tag)/\(name)"
    let object: [String: Any] = [
        "tag_name": tag,
        "body": "Release notes",
        "draft": draft,
        "prerelease": prerelease,
        "assets": [["name": name, "browser_download_url": url]],
    ]
    return try! JSONSerialization.data(withJSONObject: object)
}

func expect(_ condition: Bool, _ message: String) {
    if !condition { fatalError(message) }
}

@main struct UpdateCheckHarness {
    static func main() async throws {
        expect(UpdateVersion("0.2.10")! > UpdateVersion("0.2.9")!, "numeric comparison")
        expect(UpdateVersion("v0.3.0") == nil, "bundle versions have no v prefix")
        expect(UpdateVersion("0.3") == nil, "version must have three components")

        let available = try UpdateChecker.parse(release("v0.3.0"), installedVersion: "0.2.9")
        guard case .available(let update) = available else { fatalError("new version was not offered") }
        expect(update.version == "0.3.0" && update.notes == "Release notes", "release details")
        expect(try UpdateChecker.parse(release("v0.2.9"), installedVersion: "0.2.9") == .current,
               "equal version is current")
        expect(try UpdateChecker.parse(release("v0.2.8"), installedVersion: "0.2.9") == .current,
               "older version is current")

        for bad in [
            release("v0.3.0", asset: "Pharos-v0.3.0-macos-universal.zip"),
            release("v0.3.0", downloadURL: "https://example.com/Pharos-v0.3.0-macos-universal.dmg"),
            release("v0.3.0", draft: true),
            release("v0.3.0", prerelease: true),
            release("latest"),
        ] {
            do {
                _ = try UpdateChecker.parse(bad, installedVersion: "0.2.9")
                fatalError("invalid release was offered")
            } catch is UpdateCheckError { }
        }

        let configuration = URLSessionConfiguration.ephemeral
        configuration.protocolClasses = [StubReleaseResponse.self]
        let session = URLSession(configuration: configuration)
        StubReleaseResponse.status = 404
        expect(try await UpdateChecker.check(installedVersion: "0.2.9", session: session) == .noPublishedRelease,
               "no published release")
        StubReleaseResponse.status = 200
        StubReleaseResponse.body = release("v0.3.0")
        expect(try await UpdateChecker.check(installedVersion: "0.2.9", session: session) == available,
               "release response")
        print("Update check harness passed")
    }
}
