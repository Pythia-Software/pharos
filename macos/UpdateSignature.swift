import CryptoKit
import Foundation

/// Public-key verification for release tooling. Never accesses the Keychain.
/// Sparkle itself verifies downloads in the app; this checks publishing inputs.
enum UpdateSignature {
    static func verify(_ data: Data, publicKey: Data, signature: Data? = nil) throws {
        let key = try Curve25519.Signing.PublicKey(rawRepresentation: publicKey)
        let message: Data
        let actualSignature: Data
        if let signature {
            message = data
            actualSignature = signature
        } else {
            let marker = Data("<!-- sparkle-signatures:".utf8)
            guard let range = data.range(of: marker, options: .backwards),
                  let footer = String(data: data[range.lowerBound...], encoding: .utf8) else {
                throw SignatureError.invalid
            }
            let pattern = #"\A<!-- sparkle-signatures:\s*edSignature: ([A-Za-z0-9+/=]+)\s*length: ([0-9]+)\s*-->\s*\z"#
            let expression = try NSRegularExpression(pattern: pattern)
            guard let match = expression.firstMatch(in: footer, range: NSRange(footer.startIndex..., in: footer)),
                  let signatureRange = Range(match.range(at: 1), in: footer),
                  let lengthRange = Range(match.range(at: 2), in: footer),
                  let decoded = Data(base64Encoded: String(footer[signatureRange])),
                  let length = Int(footer[lengthRange]), length > 0, length <= range.lowerBound,
                  data[length..<range.lowerBound].allSatisfy({ [9, 10, 13, 32].contains($0) }) else {
                throw SignatureError.invalid
            }
            message = data.prefix(length)
            actualSignature = decoded
        }
        guard key.isValidSignature(actualSignature, for: message) else { throw SignatureError.invalid }
    }

    enum SignatureError: Error { case invalid }
}
