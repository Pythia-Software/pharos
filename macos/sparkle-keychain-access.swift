// Inspect Sparkle ACLs, authorize trusted paths, or plan security(1)'s partition edit.
// Never reads, exports, creates or imports key material.
import Foundation
import Security

func check(_ status: OSStatus) throws {
    if status != errSecSuccess {
        throw NSError(domain: NSOSStatusErrorDomain, code: Int(status))
    }
}

func trustedPath(_ application: SecTrustedApplication) throws -> String {
    var data: CFData?
    try check(SecTrustedApplicationCopyData(application, &data))
    return String(decoding: (data! as Data).prefix { $0 != 0 }, as: UTF8.self)
}

func codePartition(_ path: String) throws -> String {
    var code: SecStaticCode?
    try check(SecStaticCodeCreateWithPath(URL(fileURLWithPath: path) as CFURL, [], &code))
    var information: CFDictionary?
    try check(SecCodeCopySigningInformation(code!, SecCSFlags(rawValue: kSecCSSigningInformation), &information))
    guard let hash = (information! as NSDictionary)[kSecCodeInfoUnique] as? Data else {
        throw NSError(domain: "Pinned Sparkle tool has no code-directory hash", code: 2)
    }
    return "cdhash:" + hash.map { String(format: "%02x", $0) }.joined()
}

// macOS stores this ACL's plist as hex in its description (as security(1) does).
func partitionProperties(_ description: CFString?) throws -> [String: Any] {
    let hex = description.map { $0 as String } ?? ""
    let bytes = Array(hex.utf8)
    guard !bytes.isEmpty, bytes.count % 2 == 0 else {
        throw NSError(domain: "Invalid Sparkle partition ACL", code: 2)
    }
    var data = Data()
    for offset in stride(from: 0, to: bytes.count, by: 2) {
        guard let byte = UInt8(String(decoding: bytes[offset...offset + 1], as: UTF8.self), radix: 16) else {
            throw NSError(domain: "Invalid Sparkle partition ACL encoding", code: 2)
        }
        data.append(byte)
    }
    guard let properties = try PropertyListSerialization.propertyList(from: data, format: nil) as? [String: Any],
          properties["Partitions"] is [String] else {
        throw NSError(domain: "Invalid Sparkle partition list", code: 2)
    }
    return properties
}

func partitionDescription(_ properties: [String: Any]) throws -> CFString {
    let data = try PropertyListSerialization.data(fromPropertyList: properties, format: .xml, options: 0)
    return data.map { String(format: "%02x", $0) }.joined() as CFString
}

func main() throws {
    let args = Array(CommandLine.arguments.dropFirst())
    guard args.count == 5, ["check", "authorize", "partitions"].contains(args[0]) else {
        throw NSError(domain: "Usage: sparkle-keychain-access check|authorize|partitions KEYCHAIN ACCOUNT GENERATE_KEYS SIGN_UPDATE", code: 2)
    }
    let authorize = args[0] == "authorize"
    // Checks must fail without opening another password dialog.
    try check(SecKeychainSetUserInteractionAllowed(authorize))
    var keychain: SecKeychain?
    try check(SecKeychainOpen(args[1], &keychain))
    let query: [String: Any] = [
        kSecClass as String: kSecClassGenericPassword,
        kSecAttrService as String: "https://sparkle-project.org",
        kSecAttrAccount as String: args[2],
        kSecMatchSearchList as String: [keychain!],
        kSecReturnRef as String: true,
        kSecMatchLimit as String: kSecMatchLimitOne,
    ]
    var result: CFTypeRef?
    try check(SecItemCopyMatching(query as CFDictionary, &result))
    let item = result as! SecKeychainItem
    var access: SecAccess?
    try check(SecKeychainItemCopyAccess(item, &access))
    var entries: CFArray?
    try check(SecAccessCopyACLList(access!, &entries))
    let paths = Array(args[3...4])
    let requiredPartitions = Set(try paths.map { try codePartition($0) })
    var mergedPartitions = requiredPartitions
    var changedAccess = false
    var found = false
    var foundPartitions = false
    for entry in entries! as! [SecACL] {
        let operations = SecACLCopyAuthorizations(entry) as! [String]
        var applications: CFArray?
        var description: CFString?
        var selector = SecKeychainPromptSelector()
        try check(SecACLCopyContents(entry, &applications, &description, &selector))
        if operations.contains(kSecACLAuthorizationPartitionID as String) {
            let properties = try partitionProperties(description)
            let existing = Set(properties["Partitions"] as! [String])
            mergedPartitions.formUnion(existing)
            if args[0] == "check" && !requiredPartitions.isSubset(of: existing) {
                throw NSError(domain: "Sparkle code hashes need one-time partition authorization: python3 macos/setup-release-keychain.py --authorize-sparkle", code: 2)
            }
            foundPartitions = true
        }
        guard operations.contains(kSecACLAuthorizationDecrypt as String) else { continue }
        // A nil list permits every application. Never silently keep that policy.
        guard let applications else {
            throw NSError(domain: "Sparkle key trusts every application; restrict it in Keychain Access first", code: 2)
        }
        var trusted = applications as! [SecTrustedApplication]
        let existing = try trusted.map { try trustedPath($0) }
        if authorize && !paths.allSatisfy({ existing.contains($0) }) {
            // Add these exact tools, preserving unrelated authorizations.
            trusted = try trusted.filter { !paths.contains(try trustedPath($0)) }
            for path in paths {
                var application: SecTrustedApplication?
                try check(SecTrustedApplicationCreateFromPath(path, &application))
                trusted.append(application!)
            }
            try check(SecACLSetContents(entry, trusted as CFArray, description!, selector))
            changedAccess = true
        } else if !paths.allSatisfy({ existing.contains($0) }) {
            throw NSError(domain: "Sparkle tools need one-time authorization: python3 macos/setup-release-keychain.py --authorize-sparkle", code: 2)
        }
        found = true
    }
    guard found else { throw NSError(domain: "Sparkle key has no decrypt ACL; inspect it in Keychain Access", code: 2) }
    guard foundPartitions else { throw NSError(domain: "Sparkle key has no partition ACL; inspect it in Keychain Access", code: 2) }
    // Partition updates require password credentials. The explicit setup command
    // delegates that to security(1)'s secure Terminal prompt, never to this helper.
    if changedAccess { try check(SecKeychainItemSetAccess(item, access!)) }
    if args[0] == "partitions" {
        print(mergedPartitions.sorted().joined(separator: ","))
        return
    }
    print(authorize ? "Sparkle trusted-application paths ready." : "Sparkle tool authorization verified.")
}

do { try main() } catch {
    fputs("Sparkle Keychain access: \(error)\n", stderr)
    exit(1)
}
