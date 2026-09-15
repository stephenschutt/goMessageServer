import Foundation

/// One chat message, mirroring the JSON shape the Go server returns from
/// GET /api/messages: {"id","sender","text","timestamp"}.
struct Message: Identifiable, Decodable, Equatable {
    let id: Int
    let sender: String
    let text: String
    let timestamp: Date

    enum CodingKeys: String, CodingKey {
        case id, sender, text, timestamp
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id = try c.decode(Int.self, forKey: .id)
        sender = try c.decode(String.self, forKey: .sender)
        text = try c.decode(String.self, forKey: .text)

        let raw = try c.decode(String.self, forKey: .timestamp)
        guard let parsed = Message.parseTimestamp(raw) else {
            throw DecodingError.dataCorruptedError(
                forKey: .timestamp, in: c,
                debugDescription: "not an RFC 3339 timestamp: \(raw)")
        }
        timestamp = parsed
    }

    init(id: Int, sender: String, text: String, timestamp: Date) {
        self.id = id
        self.sender = sender
        self.text = text
        self.timestamp = timestamp
    }

    private static let fractional: ISO8601DateFormatter = {
        let f = ISO8601DateFormatter()
        f.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        return f
    }()

    private static let whole: ISO8601DateFormatter = {
        let f = ISO8601DateFormatter()
        f.formatOptions = [.withInternetDateTime]
        return f
    }()

    /// Go's time.Time marshals as RFC 3339 with up to nine fractional digits,
    /// which ISO8601DateFormatter rejects — it only accepts three. Try both
    /// formatters first, then retry with the fraction truncated to milliseconds.
    static func parseTimestamp(_ raw: String) -> Date? {
        if let d = fractional.date(from: raw) { return d }
        if let d = whole.date(from: raw) { return d }

        guard let dot = raw.firstIndex(of: ".") else { return nil }
        let afterDot = raw.index(after: dot)
        guard let end = raw[afterDot...].firstIndex(where: { !$0.isNumber }) else { return nil }
        guard raw.distance(from: afterDot, to: end) > 3 else { return nil }

        let truncated = String(raw[..<raw.index(afterDot, offsetBy: 3)]) + String(raw[end...])
        return fractional.date(from: truncated)
    }
}
