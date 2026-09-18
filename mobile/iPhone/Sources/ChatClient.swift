import Combine
import CryptoKit
import Foundation
import Security

/// ChatClient owns every conversation with the Go message server: it loads this
/// device's profile and chat roster, long-polls GET /api/messages for anything
/// newer than the highest id it has seen, and POSTs outgoing messages.
///
/// Identity comes from the server, not from this device: the username belongs to
/// the key, so the same key is the same person wherever it is used, and a
/// message is labelled with the name the server knows rather than one the client
/// asks for. Who can read a message is decided the same way — the server
/// delivers it to whoever is in the sender's chat.
///
/// Every request is signed with this device's RSA key (see `KeyStore`). The
/// public half travels in `X-Public-Key`; the private half never leaves the
/// phone. A server that does not recognise the key answers 403, which surfaces
/// as `.unauthorized` so Settings can show the key that needs approving.
@MainActor
final class ChatClient: ObservableObject {
    enum Status: Equatable {
        case connecting
        case connected
        /// Signed correctly, but this device's key is not in authorizedUsers.
        case unauthorized
        case failed(String)
    }

    /// Poll cadence, matched to the browser client in templates/index.html so
    /// both feel equally live.
    private static let pollInterval: Duration = .milliseconds(1500)

    /// How many polls apart the roster is re-read. Somebody else adding you
    /// changes your chat without you touching anything, but it is not worth a
    /// query every 1.5 seconds.
    private static let chatRefreshEvery = 4

    /// The deployment on EKS, behind the ALB in infra/terraform. HTTPS with a
    /// certificate ACM issued for the name, so App Transport Security is
    /// satisfied without an exception.
    private static let defaultServer = "https://messages.schuttsm.com"

    /// What `defaultServer` used to be. A device that ran an earlier build has
    /// this saved in UserDefaults, where it would quietly outrank the new
    /// default forever — so it is treated as "never chosen" rather than as a
    /// preference worth keeping.
    private static let legacyDefaultServer = "http://localhost:8080"

    private static let serverKey = "serverURL"

    @Published private(set) var messages: [Message] = []
    @Published private(set) var status: Status = .connecting

    /// The name this device posts under, empty until the user chooses one.
    @Published private(set) var username = ""

    /// Everyone in this device's chat, which is also everyone a message sent
    /// from here goes to.
    @Published private(set) var chatMembers: [String] = []

    /// This device's public key, base64 DER SPKI — the string to hand to
    /// `messageServer -authorize`. Nil until the key store has been opened.
    @Published private(set) var publicKey: String?

    /// Base URL of the Go server. It defaults to the deployed one and stays
    /// editable at runtime, because pointing at a server running on the Mac is
    /// how this gets developed — the simulator can use localhost, a physical
    /// iPhone needs the Mac's address on the same Wi-Fi.
    @Published var serverURL: String {
        didSet {
            guard serverURL != oldValue else { return }
            UserDefaults.standard.set(serverURL, forKey: Self.serverKey)
            restart()
        }
    }

    private var lastID = 0
    private var pollTask: Task<Void, Never>?
    private let session: URLSession
    private let keys = KeyStore.shared

    init() {
        let saved = UserDefaults.standard.string(forKey: Self.serverKey)
        serverURL = (saved == nil || saved == Self.legacyDefaultServer) ? Self.defaultServer : saved!

        let config = URLSessionConfiguration.ephemeral
        config.timeoutIntervalForRequest = 10
        config.requestCachePolicy = .reloadIgnoringLocalCacheData
        session = URLSession(configuration: config)
    }

    /// True once the device may use the API but has not said who it is. It is
    /// the one thing the user has to do before anything else works, so the
    /// conversation screen points at it.
    var needsUsername: Bool {
        username.isEmpty && status == .connected
    }

    /// A one-line description of the chat for the header.
    var chatTitle: String {
        chatMembers.isEmpty ? "" : chatMembers.joined(separator: ", ")
    }

    // MARK: - Lifecycle

    func start() {
        guard pollTask == nil else { return }
        pollTask = Task { [weak self] in
            await self?.run()
        }
    }

    func stop() {
        pollTask?.cancel()
        pollTask = nil
    }

    /// Drops the transcript and reconnects — used when the server address changes,
    /// since ids and usernames are only meaningful relative to one server.
    func restart() {
        stop()
        messages = []
        lastID = 0
        username = ""
        chatMembers = []
        status = .connecting
        start()
    }

    private func run() async {
        // Generating a 2048-bit key takes a moment on first launch, so it
        // happens here rather than in init where it would block the first frame.
        do {
            publicKey = try await keys.publicKey()
        } catch {
            status = .failed(Self.describe(error))
            // Drop the handle to this finished task so start() will try again
            // rather than seeing a poll already "in flight".
            pollTask = nil
            return
        }

        var tick = 0
        while !Task.isCancelled {
            if tick % Self.chatRefreshEvery == 0 {
                await refreshChat()
            }
            await poll()
            tick += 1
            try? await Task.sleep(for: Self.pollInterval)
        }
    }

    /// Discards this device's identity and makes a new key pair. The new key
    /// starts out unauthorized and unnamed, so the server has to approve it
    /// again and the user has to choose a username again.
    func regenerateKey() async {
        do {
            try await keys.regenerate()
            publicKey = try await keys.publicKey()
            restart()
        } catch {
            status = .failed(Self.describe(error))
        }
    }

    // MARK: - Profile and people

    /// Re-reads who this device is and who is in its chat.
    func refreshChat() async {
        do {
            let chat: Chat = try await perform("GET", "/api/chat")
            username = chat.username
            chatMembers = chat.members
            status = .connected
        } catch {
            fail(error)
        }
    }

    /// Claims `name` for this device. It throws rather than parking the problem
    /// in `status` because the caller is a form the user is looking at: a name
    /// already taken is something to answer there and then.
    func setUsername(_ name: String) async throws {
        let profile: Profile = try await perform(
            "PUT", "/api/username", body: ["username": name])
        username = profile.username
        await refreshChat()
    }

    /// Searches the directory of named users. An empty query lists everyone,
    /// which is what the search section shows before anything is typed.
    func searchUsers(_ query: String) async throws -> [DirectoryEntry] {
        try await perform("GET", "/api/users", query: [URLQueryItem(name: "q", value: query)])
    }

    /// Adds someone to this device's chat. The server makes the membership
    /// mutual, so the person added can answer without adding back.
    func addToChat(_ member: String) async throws {
        let chat: Chat = try await perform("POST", "/api/chat", body: ["username": member])
        apply(chat)
    }

    func removeFromChat(_ member: String) async throws {
        let chat: Chat = try await perform(
            "DELETE", "/api/chat", query: [URLQueryItem(name: "username", value: member)])
        apply(chat)
    }

    private func apply(_ chat: Chat) {
        username = chat.username
        chatMembers = chat.members
    }

    // MARK: - Messages

    private func poll() async {
        do {
            let fresh: [Message] = try await perform(
                "GET", "/api/messages", query: [URLQueryItem(name: "since", value: String(lastID))])
            if !fresh.isEmpty {
                messages.append(contentsOf: fresh)
                lastID = max(lastID, fresh.map(\.id).max() ?? lastID)
            }
            status = .connected
        } catch {
            fail(error)
        }
    }

    /// Posts `text` to everyone in this device's chat, then polls immediately so
    /// the sender sees their own bubble without waiting out the poll interval.
    func send(_ text: String) async {
        let trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty, !username.isEmpty else { return }
        do {
            let _: Message = try await perform("POST", "/api/messages", body: ["text": trimmed])
            await poll()
        } catch {
            fail(error)
        }
    }

    // MARK: - Requests

    private func url(_ path: String, query: [URLQueryItem]) -> URL? {
        guard var components = URLComponents(string: serverURL.trimmingCharacters(in: .whitespaces)) else {
            return nil
        }
        components.path = path
        components.queryItems = query.isEmpty ? nil : query
        return components.url
    }

    /// Signs, sends and decodes one API call. Everything the server answers with
    /// is JSON, so there is one shape of request here and no special cases.
    private func perform<T: Decodable>(
        _ method: String, _ path: String,
        query: [URLQueryItem] = [], body: [String: String]? = nil
    ) async throws -> T {
        guard let url = url(path, query: query) else {
            throw ClientError.badServerURL
        }

        var request = URLRequest(url: url)
        request.httpMethod = method
        if let body {
            request.httpBody = try JSONEncoder().encode(body)
            request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        }

        let (data, response) = try await session.data(for: try await signed(request))
        if let http = response as? HTTPURLResponse, !(200..<300).contains(http.statusCode) {
            let error = ServerError(status: http.statusCode, detail: Self.detail(data))
            // A refused key is a standing condition rather than a failed call,
            // so it is recorded here however the call was made.
            if http.statusCode == 403 {
                status = .unauthorized
            }
            throw error
        }
        return try JSONDecoder().decode(T.self, from: data)
    }

    // MARK: - Signing

    /// Adds the credential headers the Go server checks. The signature covers
    /// the method, path, query, timestamp, nonce and a hash of the body, so a
    /// captured request can be neither edited nor pointed at a different
    /// endpoint, and the nonce and timestamp keep it from being replayed.
    private func signed(_ request: URLRequest) async throws -> URLRequest {
        var request = request
        guard let url = request.url,
              let components = URLComponents(url: url, resolvingAgainstBaseURL: false) else {
            throw KeyStore.Failure.signing("request has no URL")
        }

        let method = request.httpMethod ?? "GET"
        let body = request.httpBody ?? Data()
        let timestamp = String(Int(Date().timeIntervalSince1970))
        let nonce = Self.nonce()
        let bodyHash = SHA256.hash(data: body)
            .map { String(format: "%02x", $0) }
            .joined()

        let canonical = [method, components.path, components.percentEncodedQuery ?? "",
                         timestamp, nonce, bodyHash]
            .map { $0 + "\n" }
            .joined()

        request.setValue(try await keys.publicKey(), forHTTPHeaderField: "X-Public-Key")
        request.setValue(timestamp, forHTTPHeaderField: "X-Timestamp")
        request.setValue(nonce, forHTTPHeaderField: "X-Nonce")
        request.setValue(try await keys.signature(for: Data(canonical.utf8)),
                         forHTTPHeaderField: "X-Signature")
        return request
    }

    /// A value used once, so two otherwise identical requests never carry the
    /// same signature.
    private static func nonce() -> String {
        var bytes = [UInt8](repeating: 0, count: 16)
        if SecRandomCopyBytes(kSecRandomDefault, bytes.count, &bytes) != errSecSuccess {
            // Falling back to a UUID still gives a value that will not repeat.
            return UUID().uuidString
        }
        return Data(bytes).base64EncodedString()
    }

    // MARK: - Errors

    enum ClientError: LocalizedError {
        case badServerURL

        var errorDescription: String? {
            switch self {
            case .badServerURL: return "Not a valid server URL"
            }
        }
    }

    /// A refusal from the server. The server answers with a sentence saying what
    /// was wrong — "that username is already taken" — so that sentence is the
    /// error, and only a response without one falls back to the status code.
    struct ServerError: LocalizedError, Equatable {
        let status: Int
        let detail: String

        var errorDescription: String? {
            if !detail.isEmpty { return detail }
            switch status {
            case 401: return "The server rejected this device's signature"
            case 403: return "This device isn't authorized yet"
            default: return "Server returned HTTP \(status)"
            }
        }
    }

    /// The server's explanation, if the body is one. Anything long or otherwise
    /// unlike a sentence is dropped rather than shown to the user.
    private static func detail(_ data: Data) -> String {
        let text = String(decoding: data, as: UTF8.self)
            .trimmingCharacters(in: .whitespacesAndNewlines)
        guard !text.isEmpty, text.count <= 160, !text.hasPrefix("<") else { return "" }
        return text
    }

    /// Records a failed background request. A 403 is singled out because it is
    /// not a fault to fix on this device — the key is simply waiting to be
    /// authorized.
    private func fail(_ error: Error) {
        if let server = error as? ServerError, server.status == 403 {
            status = .unauthorized
            return
        }
        status = .failed(Self.describe(error))
    }

    private static func describe(_ error: Error) -> String {
        if let localized = error as? LocalizedError, let message = localized.errorDescription,
           !(error is URLError) {
            return message
        }
        if let urlError = error as? URLError {
            switch urlError.code {
            case .cannotConnectToHost, .cannotFindHost:
                return "Can't reach the server"
            case .timedOut:
                return "Server timed out"
            case .notConnectedToInternet, .networkConnectionLost:
                return "No network connection"
            default:
                return urlError.localizedDescription
            }
        }
        return error.localizedDescription
    }
}
