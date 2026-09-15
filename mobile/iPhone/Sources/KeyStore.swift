import Foundation
import Security
import SQLite3

/// The device's identity: one RSA key pair, generated the first time the app
/// runs and kept in a local SQLite database from then on.
///
/// Both halves live in the `settings` table; only the public half is ever put on
/// the wire. To prove it owns the private half without revealing it, the client
/// signs a canonical summary of each request (see `signature(for:)`), which the
/// Go server verifies with the public key the same request carried.
actor KeyStore {
    static let shared = KeyStore()

    enum Failure: LocalizedError {
        case database(String)
        case keyGeneration(String)
        case signing(String)

        var errorDescription: String? {
            switch self {
            case .database(let detail): return "Key database error: \(detail)"
            case .keyGeneration(let detail): return "Could not create this device's key: \(detail)"
            case .signing(let detail): return "Could not sign the request: \(detail)"
            }
        }
    }

    /// Row keys in the `settings` table.
    private enum Setting {
        static let publicKey = "publicKey"
        static let privateKey = "privateKey"
    }

    private static let keySizeInBits = 2048

    /// sqlite3_bind_text needs to know whether it may keep the caller's buffer.
    /// SQLITE_TRANSIENT ("copy it") is a macro the Swift overlay does not import.
    private static let transient = unsafeBitCast(-1, to: sqlite3_destructor_type.self)

    private var db: OpaquePointer?
    private var privateKey: SecKey?

    /// Base64 of the DER SubjectPublicKeyInfo — the exact string the server
    /// stores in `authorizedUsers.publicKey`, so it is also what a user pastes
    /// into `messageServer -authorize`.
    private(set) var publicKeyBase64: String?

    private init() {}

    // MARK: - Setup

    /// Opens the database and loads the key pair, creating either if this is the
    /// first launch. Safe to call repeatedly; the work happens once.
    func prepare() throws {
        if privateKey != nil { return }
        if db == nil { try openDatabase() }

        if let stored = try readSetting(Setting.privateKey),
           let publicKey = try readSetting(Setting.publicKey),
           let key = Self.importPrivateKey(stored) {
            privateKey = key
            publicKeyBase64 = publicKey
            return
        }

        // Either nothing is stored yet or what is stored no longer imports.
        // Generating again is the only way forward; the new key simply has to
        // be authorized on the server.
        let (key, publicKey) = try Self.generateKeyPair()
        try writeSetting(Setting.privateKey, value: try Self.exportPrivateKey(key))
        try writeSetting(Setting.publicKey, value: publicKey)
        privateKey = key
        publicKeyBase64 = publicKey
    }

    /// The public key, preparing the store if it has not been opened yet.
    func publicKey() throws -> String {
        try prepare()
        guard let publicKeyBase64 else {
            throw Failure.keyGeneration("no public key after setup")
        }
        return publicKeyBase64
    }

    /// Throws away the stored pair and makes a new one. The old key keeps
    /// whatever standing it had on the server, so the new one has to be
    /// authorized before the app can talk to it again.
    func regenerate() throws {
        try prepare()
        let (key, publicKey) = try Self.generateKeyPair()
        try writeSetting(Setting.privateKey, value: try Self.exportPrivateKey(key))
        try writeSetting(Setting.publicKey, value: publicKey)
        privateKey = key
        publicKeyBase64 = publicKey
    }

    // MARK: - Signing

    /// Signs `message` with the device's private key using RSA PKCS#1 v1.5 over
    /// SHA-256, the algorithm `rsa.VerifyPKCS1v15` expects on the Go side.
    func signature(for message: Data) throws -> String {
        try prepare()
        guard let privateKey else {
            throw Failure.signing("no private key")
        }

        var error: Unmanaged<CFError>?
        guard let signature = SecKeyCreateSignature(
            privateKey, .rsaSignatureMessagePKCS1v15SHA256, message as CFData, &error
        ) as Data? else {
            throw Failure.signing(Self.describe(error))
        }
        return signature.base64EncodedString()
    }

    // MARK: - SQLite

    /// The database lives in Application Support, which is backed up and not
    /// purged the way Caches can be — losing it would mean losing the identity
    /// the server has authorized.
    private static func databaseURL() throws -> URL {
        let fm = FileManager.default
        let dir = try fm.url(for: .applicationSupportDirectory, in: .userDomainMask,
                             appropriateFor: nil, create: true)
        return dir.appendingPathComponent("messages.sqlite")
    }

    private func openDatabase() throws {
        let url = try Self.databaseURL()
        var handle: OpaquePointer?
        guard sqlite3_open(url.path, &handle) == SQLITE_OK, let handle else {
            let detail = handle.map { String(cString: sqlite3_errmsg($0)) } ?? "could not open \(url.path)"
            sqlite3_close(handle)
            throw Failure.database(detail)
        }
        db = handle
        try execute("CREATE TABLE IF NOT EXISTS settings (key TEXT PRIMARY KEY, value TEXT NOT NULL)")
    }

    private func execute(_ sql: String) throws {
        guard let db else { throw Failure.database("database is not open") }
        var message: UnsafeMutablePointer<CChar>?
        guard sqlite3_exec(db, sql, nil, nil, &message) == SQLITE_OK else {
            let detail = message.map { String(cString: $0) } ?? "exec failed"
            sqlite3_free(message)
            throw Failure.database(detail)
        }
    }

    private func readSetting(_ key: String) throws -> String? {
        guard let db else { throw Failure.database("database is not open") }
        var statement: OpaquePointer?
        guard sqlite3_prepare_v2(db, "SELECT value FROM settings WHERE key = ?", -1, &statement, nil) == SQLITE_OK else {
            throw Failure.database(String(cString: sqlite3_errmsg(db)))
        }
        defer { sqlite3_finalize(statement) }

        sqlite3_bind_text(statement, 1, key, -1, Self.transient)
        guard sqlite3_step(statement) == SQLITE_ROW, let text = sqlite3_column_text(statement, 0) else {
            return nil
        }
        return String(cString: text)
    }

    private func writeSetting(_ key: String, value: String) throws {
        guard let db else { throw Failure.database("database is not open") }
        var statement: OpaquePointer?
        let sql = "INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value"
        guard sqlite3_prepare_v2(db, sql, -1, &statement, nil) == SQLITE_OK else {
            throw Failure.database(String(cString: sqlite3_errmsg(db)))
        }
        defer { sqlite3_finalize(statement) }

        sqlite3_bind_text(statement, 1, key, -1, Self.transient)
        sqlite3_bind_text(statement, 2, value, -1, Self.transient)
        guard sqlite3_step(statement) == SQLITE_DONE else {
            throw Failure.database(String(cString: sqlite3_errmsg(db)))
        }
    }

    // MARK: - Key material

    /// Creates a key pair outside the keychain, because this store keeps both
    /// halves itself — the requirement is that they live in the `settings` table.
    private static func generateKeyPair() throws -> (SecKey, String) {
        let attributes: [String: Any] = [
            kSecAttrKeyType as String: kSecAttrKeyTypeRSA,
            kSecAttrKeySizeInBits as String: keySizeInBits,
        ]
        var error: Unmanaged<CFError>?
        guard let privateKey = SecKeyCreateRandomKey(attributes as CFDictionary, &error) else {
            throw Failure.keyGeneration(describe(error))
        }
        guard let publicKey = SecKeyCopyPublicKey(privateKey) else {
            throw Failure.keyGeneration("no public half")
        }
        guard let pkcs1 = SecKeyCopyExternalRepresentation(publicKey, &error) as Data? else {
            throw Failure.keyGeneration(describe(error))
        }
        return (privateKey, subjectPublicKeyInfo(fromPKCS1: pkcs1).base64EncodedString())
    }

    private static func exportPrivateKey(_ key: SecKey) throws -> String {
        var error: Unmanaged<CFError>?
        guard let data = SecKeyCopyExternalRepresentation(key, &error) as Data? else {
            throw Failure.keyGeneration(describe(error))
        }
        return data.base64EncodedString()
    }

    private static func importPrivateKey(_ base64: String) -> SecKey? {
        guard let data = Data(base64Encoded: base64) else { return nil }
        let attributes: [String: Any] = [
            kSecAttrKeyType as String: kSecAttrKeyTypeRSA,
            kSecAttrKeyClass as String: kSecAttrKeyClassPrivate,
            kSecAttrKeySizeInBits as String: keySizeInBits,
        ]
        return SecKeyCreateWithData(data as CFData, attributes as CFDictionary, nil)
    }

    /// Security framework hands back a bare PKCS#1 `RSAPublicKey`, while Go's
    /// `x509.ParsePKIXPublicKey` wants a `SubjectPublicKeyInfo`. Wrapping is
    /// just prefixing the RSA algorithm identifier and a BIT STRING header.
    private static func subjectPublicKeyInfo(fromPKCS1 pkcs1: Data) -> Data {
        // SEQUENCE { OBJECT IDENTIFIER 1.2.840.113549.1.1.1 (rsaEncryption), NULL }
        let algorithm = Data([0x30, 0x0d, 0x06, 0x09, 0x2a, 0x86, 0x48, 0x86,
                              0xf7, 0x0d, 0x01, 0x01, 0x01, 0x05, 0x00])

        var bitString = Data([0x03])
        bitString.append(derLength(pkcs1.count + 1))
        bitString.append(0x00)  // no unused trailing bits
        bitString.append(pkcs1)

        var spki = Data([0x30])
        spki.append(derLength(algorithm.count + bitString.count))
        spki.append(algorithm)
        spki.append(bitString)
        return spki
    }

    /// DER length: short form below 128, otherwise a byte count followed by the
    /// length's own big-endian bytes.
    private static func derLength(_ length: Int) -> Data {
        if length < 0x80 { return Data([UInt8(length)]) }

        var bytes: [UInt8] = []
        var remaining = length
        while remaining > 0 {
            bytes.insert(UInt8(remaining & 0xff), at: 0)
            remaining >>= 8
        }
        return Data([0x80 | UInt8(bytes.count)] + bytes)
    }

    private static func describe(_ error: Unmanaged<CFError>?) -> String {
        guard let error = error?.takeRetainedValue() else { return "unknown error" }
        return CFErrorCopyDescription(error) as String? ?? "unknown error"
    }
}
