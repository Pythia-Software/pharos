import Foundation

@main struct VerifyUpdate {
    static func main() {
        do {
            let args = Array(CommandLine.arguments.dropFirst())
            guard (2...3).contains(args.count), let key = Data(base64Encoded: args[0]) else {
                throw UpdateSignature.SignatureError.invalid
            }
            let signature: Data?
            if args.count == 3 {
                guard let decoded = Data(base64Encoded: args[2]) else { throw UpdateSignature.SignatureError.invalid }
                signature = decoded
            } else { signature = nil }
            try UpdateSignature.verify(Data(contentsOf: URL(fileURLWithPath: args[1])), publicKey: key, signature: signature)
        } catch {
            FileHandle.standardError.write(Data("Update signature verification failed: \(error)\n".utf8))
            exit(1)
        }
    }
}
