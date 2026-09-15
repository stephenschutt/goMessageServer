import Foundation

/// Who this device is, as the server sees it. `username` is empty until the
/// user has chosen one: a device is authorized first and named second.
struct Profile: Decodable, Equatable {
    let publicKey: String
    let fingerprint: String
    let username: String
}

/// One result from the user directory, mirroring GET /api/users.
///
/// `inChat` comes from the server rather than being worked out here, so the
/// button next to a name is right even when the chat was changed on another
/// device.
struct DirectoryEntry: Decodable, Identifiable, Equatable {
    let username: String
    let inChat: Bool

    var id: String { username }
}

/// This device's chat: the name it posts under and everyone it is talking to,
/// mirroring GET /api/chat.
struct Chat: Decodable, Equatable {
    let username: String
    let members: [String]
}
